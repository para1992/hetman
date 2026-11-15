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
		return errors.New("use ID=TEXT")
	}
	if _, dup := p[k]; dup {
		return fmt.Errorf("%s is given twice", k)
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

func addAnswerFlags(fs *flag.FlagSet) *answerFlags {
	f := &answerFlags{values: pairs{}, comments: pairs{}}
	f.ask = fs.Int64("ask", 0, "answer only if this question (the number shown with it) is still open")
	fs.Var(f.values, "a", "ID=ANSWER for one question; repeat for each")
	fs.Var(f.comments, "comment", "ID=TEXT, a comment on one answer; repeat as needed")
	f.outcome = fs.String("outcome", "", "the outcome chosen at a human node")
	f.by = fs.String("by", "", "who answers (default: the user name of this computer)")
	return f
}

func (c *command) answer(id int64, f *answerFlags) int {
	k, err := c.campaign(id)
	if err != nil {
		return c.fail(err)
	}
	// A child campaign is answered through the campaign a person started.
	if k.Parent != 0 {
		top, err := engine.Top(c.chron, k.ID)
		if err != nil {
			return c.fail(err)
		}
		if k, err = c.chron.Campaign(top); err != nil {
			return c.fail(err)
		}
	}
	asked, st, err := engine.Pending(c.chron, k.ID)
	if err != nil {
		return c.fail(err)
	}
	if asked == nil {
		switch {
		case st.End != "":
			return c.fail(fmt.Errorf("campaign %d has ended (%s); it asks nothing", k.ID, st.End))
		case st.Ask != nil:
			return c.fail(fmt.Errorf("campaign %d: the questions of %s are already answered", k.ID, st.Ask.Node))
		}
		return c.fail(fmt.Errorf("campaign %d asks nothing now (it is at %s)", k.ID, st.Node))
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
		ao, _ := engine.OrdersOf(c.o, c.chron, asked.Campaign)
		printAsked(c.stdout, c.o, ao, k.ID, asked, false)
		r.Ask = asked.Seq
		if r.Given, err = c.interview(asked); err != nil {
			return c.fail(err)
		}
	}
	if _, err := engine.Answer(c.chron, k.ID, r); err != nil {
		fmt.Fprintln(c.stderr, "hetman answer:", err)
		if errors.Is(err, ask.ErrInvalid) {
			fmt.Fprintln(c.stderr, "nothing was recorded")
			return exitUsage
		}
		return exitFailed
	}
	fmt.Fprintf(c.stdout, "answers recorded for campaign %d at %s\n", k.ID, asked.Node)
	if alive, _ := engine.Driven(c.o, k.ID); alive {
		fmt.Fprintf(c.stdout, "the hetman process that drives campaign %d continues it\n", k.ID)
	} else {
		fmt.Fprintf(c.stdout, "no hetman process drives campaign %d; continue it with: %s\n", k.ID, engine.ResumeCommand(c.o, k.ID))
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
			return "", errors.New("no answer given; nothing was recorded")
		}
		return strings.TrimSpace(in.Text()), nil
	}
	for i, q := range asked.Questions {
		fmt.Fprintf(c.stdout, "\n%d/%d %s\n", i+1, len(asked.Questions), q.Question)
		for {
			v, err := read(fmt.Sprintf("    [%s] > ", q.Expect()))
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
				fmt.Fprintf(c.stdout, "    %s\n", strings.TrimPrefix(err.Error(), ask.ErrInvalid.Error()+": "))
				continue
			}
			g.Values[q.ID] = v
			break
		}
		if q.Kind != ask.Text {
			v, err := read("    comment (Enter for none) > ")
			if err != nil {
				return g, err
			}
			if v != "" {
				g.Comments[q.ID] = v
			}
		}
	}
	if len(asked.Outcomes) > 1 {
		fmt.Fprintf(c.stdout, "\nChoose the outcome: %s\n", strings.Join(asked.Outcomes, ", "))
		for {
			v, err := read("    [outcome] > ")
			if err != nil {
				return g, err
			}
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(asked.Outcomes) {
				v = asked.Outcomes[n-1]
			}
			if _, _, err := ask.Check(nil, asked.Outcomes, ask.Given{Outcome: v}); err != nil {
				fmt.Fprintf(c.stdout, "    %s\n", strings.TrimPrefix(err.Error(), ask.ErrInvalid.Error()+": "))
				continue
			}
			g.Outcome = v
			break
		}
	}
	return g, nil
}

// printAsked shows an open question: what is asked, what to look at, and
// how to answer it.
//
// o is the orders of the campaign a person started and id its number, which
// the commands name; no is the orders of the campaign that asks, which may be
// a child running a segment.
func printAsked(w io.Writer, o, no *orders.Orders, id int64, asked *chronicle.Event, commands bool) {
	if no == nil {
		no = o
	}
	what := map[string]string{chronicle.ReasonGate: "gate", chronicle.ReasonHuman: "human node",
		chronicle.ReasonDialogue: fmt.Sprintf("agent step (round %d)", asked.Round)}[asked.Reason]
	where := ""
	if asked.Campaign != id {
		where = fmt.Sprintf(" in campaign %d", asked.Campaign)
	}
	fmt.Fprintf(w, "%s %s%s asks (question #%d, %s):\n", what, asked.Node, where, asked.Seq, asked.At.Local().Format("2006-01-02 15:04:05"))
	for _, s := range asked.Show {
		p := filepath.Join(no.Dir, filepath.FromSlash(s))
		if _, err := os.Stat(p); err != nil {
			p += " (not found)"
		}
		fmt.Fprintf(w, "  look at: %s\n", p)
	}
	for i, q := range asked.Questions {
		fmt.Fprintf(w, "  %d. %s  [%s: %s]\n", i+1, q.Question, q.ID, q.Expect())
	}
	if len(asked.Outcomes) > 0 {
		var opts []string
		node := no.Nodes[asked.Node]
		for _, out := range asked.Outcomes {
			if node != nil && node.On[out] != "" {
				out += " (-> " + node.On[out] + ")"
			}
			opts = append(opts, out)
		}
		fmt.Fprintf(w, "  outcome, one of: %s\n", strings.Join(opts, ", "))
	}
	if !commands {
		return
	}
	cmd := engine.AnswerCommand(o, id)
	fmt.Fprintf(w, "  answer: %s\n", cmd)
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
	fmt.Fprintf(w, "      or: %s -ask %d %s\n", cmd, asked.Seq, strings.Join(flags, " "))
}

// printAnswered shows one recorded answer.
func printAnswered(w io.Writer, asked, answer *chronicle.Event) {
	by := ""
	if answer.By != "" {
		by = " by " + answer.By
	}
	via := ""
	if answer.Via != "" {
		via = " (" + answer.Via + ")"
	}
	fmt.Fprintf(w, "answers at %s%s%s, %s:\n", answer.Node, by, via, answer.At.Local().Format(time.DateTime))
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
		fmt.Fprintf(w, "  outcome: %s\n", answer.Outcome)
	}
}
