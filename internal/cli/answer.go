package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/i18n"
	"hetman/internal/orders"
)

// Stdin is where the interactive answer command reads from.
var Stdin io.Reader = os.Stdin

// pairs collects repeated ID=TEXT flags.
type pairs map[string]string

func (p pairs) String() string { return "" }

func (p pairs) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return errors.New(personPrinter(nil).T("flag.err.pair"))
	}
	if _, dup := p[k]; dup {
		return errors.New(personPrinter(nil).T("flag.err.twice", "id", k))
	}
	p[k] = v
	return nil
}

// answerFlags are the flags of the answer command.
type answerFlags struct {
	ask      *int64
	values   pairs
	comments pairs
	outcome  *string
	by       *string
}

func addAnswerFlags(p *i18n.Printer, fs *flag.FlagSet) *answerFlags {
	f := &answerFlags{values: pairs{}, comments: pairs{}}
	f.ask = fs.Int64("ask", 0, p.T("flag.ask"))
	fs.Var(f.values, "a", p.T("flag.a"))
	fs.Var(f.comments, "comment", p.T("flag.comment"))
	f.outcome = fs.String("outcome", "", p.T("flag.outcome"))
	f.by = fs.String("by", "", p.T("flag.by"))
	return f
}

func (c *command) answer(id int64, f *answerFlags) int {
	k, err := c.campaign(id)
	if err != nil {
		return c.fail(err)
	}
	asked, st, err := engine.Pending(c.chron, k.ID)
	if err != nil {
		return c.fail(err)
	}
	if asked == nil {
		switch {
		case st.End != "":
			return c.fail(errors.New(c.p.T("answer.ended", "id", k.ID, "end", st.End)))
		case st.Ask != nil:
			return c.fail(errors.New(c.p.T("answer.already", "id", k.ID, "node", st.Ask.Node)))
		}
		return c.fail(errors.New(c.p.T("answer.nothing_asked", "id", k.ID, "node", st.Node)))
	}
	r := engine.Reply{Ask: *f.ask, By: *f.by, Via: engine.ViaCommand}
	if r.By == "" {
		if u, err := user.Current(); err == nil {
			r.By = filepath.Base(u.Username) // a Windows name comes as DOMAIN\name
			if i := strings.LastIndex(r.By, `\`); i >= 0 {
				r.By = r.By[i+1:]
			}
		}
	}
	if len(f.values) > 0 || len(f.comments) > 0 || *f.outcome != "" {
		r.Given = ask.Given{Values: f.values, Comments: f.comments, Outcome: *f.outcome}
	} else {
		printAsked(c.p, c.stdout, c.o, k.ID, asked, false)
		r.Ask = asked.Seq
		if r.Given, err = c.interview(asked); err != nil {
			return c.fail(err)
		}
	}
	if _, err := engine.Answer(c.chron, k.ID, r); err != nil {
		fmt.Fprintln(c.stderr, "hetman answer:", c.p.Error(err))
		if errors.Is(err, ask.ErrInvalid) {
			fmt.Fprintln(c.stderr, c.p.T("answer.nothing_recorded"))
			return exitUsage
		}
		return exitFailed
	}
	fmt.Fprintln(c.stdout, c.p.T("answer.recorded", "id", k.ID, "node", asked.Node))
	if alive, _ := engine.Driven(c.o, k.ID); alive {
		fmt.Fprintln(c.stdout, c.p.T("answer.driven", "id", k.ID))
	} else {
		fmt.Fprintln(c.stdout, c.p.T("answer.not_driven", "id", k.ID, "command", engine.ResumeCommand(c.o, k.ID)))
	}
	return exitDone
}

// interview asks every question in the terminal, one line per answer.
func (c *command) interview(asked *chronicle.Event) (ask.Given, error) {
	g := ask.Given{Values: map[string]string{}, Comments: map[string]string{}}
	in := bufio.NewScanner(Stdin)
	in.Buffer(nil, 1<<20)
	read := func(prompt string) (string, error) {
		fmt.Fprint(c.stdout, prompt)
		if !in.Scan() {
			fmt.Fprintln(c.stdout)
			return "", errors.New(c.p.T("answer.no_answer"))
		}
		return strings.TrimSpace(in.Text()), nil
	}
	for i, q := range asked.Questions {
		fmt.Fprintf(c.stdout, "\n%d/%d %s\n", i+1, len(asked.Questions), q.Question)
		for {
			v, err := read(fmt.Sprintf("    [%s] > ", expect(c.p, q)))
			if err != nil {
				return g, err
			}
			if q.Kind == ask.Choice {
				if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(q.Options) {
					v = q.Options[n-1]
				}
			}
			one := []ask.Question{q}
			if _, _, err := ask.Check(one, nil, ask.Given{Values: map[string]string{q.ID: v}}); err != nil {
				fmt.Fprintf(c.stdout, "    %s\n", problem(c.p, err))
				continue
			}
			g.Values[q.ID] = v
			break
		}
		if q.Kind != ask.Text {
			v, err := read("    " + c.p.T("answer.comment_prompt") + " > ")
			if err != nil {
				return g, err
			}
			if v != "" {
				g.Comments[q.ID] = v
			}
		}
	}
	if len(asked.Outcomes) > 1 {
		fmt.Fprintf(c.stdout, "\n%s\n", c.p.T("answer.choose_outcome", "outcomes", strings.Join(asked.Outcomes, ", ")))
		for {
			v, err := read("    [" + c.p.T("answer.outcome_prompt") + "] > ")
			if err != nil {
				return g, err
			}
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(asked.Outcomes) {
				v = asked.Outcomes[n-1]
			}
			if _, _, err := ask.Check(nil, asked.Outcomes, ask.Given{Outcome: v}); err != nil {
				fmt.Fprintf(c.stdout, "    %s\n", problem(c.p, err))
				continue
			}
			g.Outcome = v
			break
		}
	}
	return g, nil
}

// expect says what kind of answer a question takes.
func expect(p *i18n.Printer, q ask.Question) string {
	if q.Kind == ask.Text {
		return p.T("answer.expect_text")
	}
	return q.Expect()
}

// problem says what is wrong with an answer, without the lead.
func problem(p *i18n.Printer, err error) string {
	var inv *ask.Invalid
	if errors.As(err, &inv) {
		return inv.Problem(p)
	}
	return err.Error()
}

// printAsked shows an open question: what is asked, what to look at, and
// how to answer it.
func printAsked(p *i18n.Printer, w io.Writer, o *orders.Orders, id int64, asked *chronicle.Event, commands bool) {
	at := asked.At.Local().Format("2006-01-02 15:04:05")
	fmt.Fprintln(w, p.T("answer.asks."+asked.Reason, "node", asked.Node, "seq", asked.Seq, "at", at, "round", asked.Round))
	for _, s := range asked.Show {
		path := filepath.Join(o.Dir, filepath.FromSlash(s))
		if _, err := os.Stat(path); err != nil {
			path = p.T("answer.not_found", "path", path)
		}
		fmt.Fprintln(w, p.T("answer.look_at", "path", path))
	}
	for i, q := range asked.Questions {
		fmt.Fprintf(w, "  %d. %s  [%s: %s]\n", i+1, q.Question, q.ID, expect(p, q))
	}
	if len(asked.Outcomes) > 0 {
		var opts []string
		node := o.Nodes[asked.Node]
		for _, out := range asked.Outcomes {
			if node != nil && node.On[out] != "" {
				out += " (-> " + node.On[out] + ")"
			}
			opts = append(opts, out)
		}
		fmt.Fprintln(w, p.T("answer.outcome_one_of", "outcomes", strings.Join(opts, ", ")))
	}
	if !commands {
		return
	}
	cmd := engine.AnswerCommand(o, id)
	fmt.Fprintln(w, p.T("answer.answer_with", "command", cmd))
	var flags []string
	for _, q := range asked.Questions {
		v := "TEXT"
		switch q.Kind {
		case ask.YesNo:
			v = "yes"
		case ask.Choice:
			v = q.Options[0]
		}
		flags = append(flags, "-a "+engine.ShellQuote(q.ID+"="+v))
	}
	if len(asked.Outcomes) > 1 {
		flags = append(flags, "-outcome "+asked.Outcomes[0])
	}
	fmt.Fprintln(w, p.T("answer.or", "command", fmt.Sprintf("%s -ask %d %s", cmd, asked.Seq, strings.Join(flags, " "))))
}

// printAnswered shows one recorded answer.
func printAnswered(p *i18n.Printer, w io.Writer, asked, answer *chronicle.Event) {
	by := ""
	if answer.By != "" {
		by = p.T("answer.by", "by", answer.By)
	}
	via := ""
	if answer.Via != "" {
		via = " (" + p.T("answer.via."+answer.Via) + ")"
	}
	fmt.Fprintln(w, p.T("answer.answers_at", "node", answer.Node, "by", by, "via", via, "at", answer.At.Local().Format(time.DateTime)))
	byID := map[string]ask.Answer{}
	for _, a := range answer.Answers {
		byID[a.ID] = a
	}
	for _, q := range asked.Questions {
		a := byID[q.ID]
		line := fmt.Sprintf("  %s: %s", q.ID, a.Value)
		if a.Comment != "" {
			line += " (" + a.Comment + ")"
		}
		fmt.Fprintln(w, line)
	}
	if answer.Outcome != "" && asked.Reason == chronicle.ReasonHuman {
		fmt.Fprintln(w, p.T("answer.outcome", "outcome", answer.Outcome))
	}
}
