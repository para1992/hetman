package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/edit"
	"hetman/internal/engine"
	"hetman/internal/explain"
	"hetman/internal/i18n"
	"hetman/internal/language"
	"hetman/internal/orders"
	"hetman/internal/scribe"
	"hetman/internal/wizard"
)

// newModel makes the model Scribe asks for a sentence; tests replace it.
var newModel = func(o *orders.Orders) scribe.Model { return scribe.AdapterModel{Orders: o} }

// opUsage is the command form of every operation, with an example.
var opUsage = []struct{ op, form, example string }{
	{edit.OpNew, "new TYPE NODE [-flow NAME] [node flags]", "hetman scribe new script prepare -flow toy -run 'sh steps/prepare.sh'"},
	{edit.OpAdd, "add TYPE NODE (-after NODE [-via OUTCOME] | -before NODE) [node flags]", "hetman scribe add gate ready -after test -checks lint,tests"},
	{edit.OpRemove, "remove NODE", "hetman scribe remove prepare"},
	{edit.OpRename, "rename NODE NEW", "hetman scribe rename build compile"},
	{edit.OpLink, "link NODE OUTCOME TARGET", "hetman scribe link ready blocked build"},
	{edit.OpUnlink, "unlink NODE OUTCOME", "hetman scribe unlink release rework"},
	{edit.OpSet, "set NODE FIELD VALUE", "hetman scribe set build timeout 5m"},
	{edit.OpUnset, "unset NODE FIELD", "hetman scribe unset build timeout"},
	{edit.OpCheck, "check NAME [-run CMD] [-timeout D] [-soft | -required] [-locks A,B]", "hetman scribe check lint -run 'sh checks/lint.sh' -soft"},
	{edit.OpExecutor, "executor NAME -run CMD", "hetman scribe executor stub -run './bin/stub-adapter plan.json'"},
	{edit.OpGateAdd, "gate-add GATE CHECK [-at N]", "hetman scribe gate-add ready style -at 1"},
	{edit.OpGateRemove, "gate-remove GATE CHECK", "hetman scribe gate-remove ready lint"},
	{edit.OpAsk, "ask NODE ID QUESTION [-kind yesno|choice|text] [-options A,B]", "hetman scribe ask ready docs 'Are the docs updated?'"},
	{edit.OpRole, "role NAME [-select fixed|round_robin|quota] [-executor X [-model M] [-effort E]]", "hetman scribe role coder -select round_robin -executor first -model m1"},
	{edit.OpRoleAdd, "role-add ROLE EXECUTOR [-model M] [-effort E] [-at N]", "hetman scribe role-add coder second -model m2"},
	{edit.OpRoleRemove, "role-remove ROLE N", "hetman scribe role-remove coder 2"},
	{edit.OpAssistant, "assistant EXECUTOR MODEL [-effort E]", "hetman scribe assistant stub any-model"},
	{edit.OpLanguage, "language CODE", "hetman scribe language pt"},
	{edit.OpUnlanguage, "unlanguage", "hetman scribe unlanguage"},
}

func scribeUsage(p *i18n.Printer) string {
	var b strings.Builder
	b.WriteString(p.T("scribe.usage", "kinds", strings.Join(explain.Kinds, ", ")))
	for _, u := range opUsage {
		spec, _ := edit.SpecOf(u.op)
		fmt.Fprintf(&b, "  %s\n      %s\n", u.form, spec.SummaryIn(p))
	}
	b.WriteString(p.T("scribe.usage_flags", "codes", strings.Join(language.Codes(), ", ")))
	return b.String()
}

// printerFor speaks the person's language for a command on an orders file
// that may not exist or may not pass the check.
func printerFor(file string) *i18n.Printer {
	o, _ := orders.Load(file)
	return personPrinter(o)
}

// scribeCmd is the entry to the setup assistant.
func scribeCmd(args []string, stdout, stderr io.Writer) int {
	p := personPrinter(nil)
	if len(args) > 0 {
		switch args[0] {
		case "skill":
			return writeScribe(p, args[1:], stdout, stderr)
		case "explain":
			return explainCmd(args[1:], stdout, stderr)
		case "help", "-h", "--help":
			fmt.Fprint(stdout, scribeUsage(p))
			return exitDone
		}
		if _, ok := edit.SpecOf(args[0]); ok {
			return opCmd(args[0], args[1:], stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("hetman scribe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, p.T("flag.f"))
	yes := fs.Bool("yes", false, p.T("flag.yes"))
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitUsage
	}
	p = printerFor(*file)
	switch len(pos) {
	case 0:
		if *yes {
			fmt.Fprintln(stderr, p.T("scribe.yes_for_sentence"))
			return exitUsage
		}
		if err := wizard.RunIn(p, *file, Stdin, stdout, !isTerminal(Stdin)); err != nil {
			fmt.Fprintln(stderr, "hetman scribe:", err)
			return exitFailed
		}
		return exitDone
	case 1:
		return sentenceCmd(p, *file, pos[0], *yes, stdout, stderr)
	}
	fmt.Fprintln(stderr, p.T("scribe.not_an_operation", "arg", strconv.Quote(pos[0])))
	return exitUsage
}

// parseInterleaved parses flags that may come before, between and after
// the positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

type onFlag map[string]string

func (f onFlag) String() string { return "" }

func (f onFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" || v == "" {
		return errors.New(personPrinter(nil).T("flag.err.on"))
	}
	f[k] = v
	return nil
}

func listOf(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

// opCmd runs one operation given on the command line.
func opCmd(name string, args []string, stdout, stderr io.Writer) int {
	p := personPrinter(nil)
	spec, _ := edit.SpecOf(name)
	fs := flag.NewFlagSet("hetman scribe "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, p.T("flag.f"))
	dry := fs.Bool("dry-run", false, p.T("flag.dry_run"))
	force := fs.Bool("force", false, p.T("flag.op_force"))
	strs := map[string]*string{}
	lists := map[string]*string{}
	ints := map[string]*int{}
	var run *string
	var starters, soft, required *bool
	on := onFlag{}
	for _, f := range append(append([]string{}, spec.Required...), spec.Optional...) {
		if contains(spec.Args, f) {
			continue
		}
		switch f {
		case "run":
			run = fs.String("run", "", p.T("flag.run"))
		case "checks", "tools", "outcomes", "options", "locks":
			lists[f] = fs.String(f, "", p.T("flag.list"))
		case "at", "attempts", "parallel":
			ints[f] = fs.Int(f, 0, p.T("flag.number"))
		case "starters":
			starters = fs.Bool("starters", false, p.T("flag.starters"))
		case "soft":
			soft = fs.Bool("soft", false, p.T("flag.soft"))
			required = fs.Bool("required", false, p.T("flag.required"))
		case "on":
			fs.Var(on, "on", "OUTCOME=TARGET")
		default:
			strs[f] = fs.String(f, "", f)
		}
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitUsage
	}
	p = printerFor(*file)
	if len(pos) != len(spec.Args) {
		for _, u := range opUsage {
			if u.op == name {
				fmt.Fprintln(stderr, p.T("scribe.op_usage", "form", u.form, "example", u.example))
			}
		}
		return exitUsage
	}
	op := edit.Op{Op: name}
	set := func(field, value string) error {
		switch field {
		case "node":
			op.Node = value
		case "type":
			op.Type = value
		case "after":
			op.After = value
		case "via":
			op.Via = value
		case "before":
			op.Before = value
		case "name":
			op.Name = value
		case "outcome":
			op.Outcome = value
		case "target":
			op.Target = value
		case "field":
			op.Field = value
		case "value":
			op.Value = value
		case "check":
			op.Check = value
		case "id":
			op.ID = value
		case "question":
			op.Question = value
		case "kind":
			op.Kind = value
		case "flow":
			op.Flow = value
		case "timeout":
			op.Timeout = value
		case "executor":
			op.Executor = value
		case "role":
			op.Role = value
		case "select":
			op.Select = value
		case "model":
			op.Model = value
		case "effort":
			op.Effort = value
		case "prompt":
			op.Prompt = value
		case "schema":
			op.Schema = value
		case "language":
			op.Language = value
		case "segment":
			op.Segment = value
		case "input":
			op.Input = value
		case "items":
			op.Items = value
		case "locks":
			op.Locks = listOf(value)
		case "checks":
			op.Checks = listOf(value)
		case "tools":
			op.Tools = listOf(value)
		case "outcomes":
			op.Outcomes = listOf(value)
		case "options":
			op.Options = listOf(value)
		case "at", "attempts", "parallel":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("-%s must be a number", field) // i18n: keep (flag parsing has checked it already)
			}
			switch field {
			case "at":
				op.At = n
			case "attempts":
				op.Attempts = n
			default:
				op.Parallel = n
			}
		}
		return nil
	}
	for i, f := range spec.Args {
		set(f, pos[i])
	}
	for f, p := range strs {
		if *p != "" {
			set(f, *p)
		}
	}
	for f, p := range lists {
		if *p != "" {
			set(f, *p)
		}
	}
	for f, p := range ints {
		if *p != 0 {
			set(f, strconv.Itoa(*p))
		}
	}
	if run != nil && *run != "" {
		if op.Run, err = edit.SplitCommand(*run); err != nil {
			fmt.Fprintln(stderr, "hetman scribe:", err)
			return exitUsage
		}
	}
	if starters != nil {
		op.Starters = *starters
	}
	if soft != nil {
		switch {
		case *soft && *required:
			fmt.Fprintln(stderr, p.T("scribe.soft_or_required"))
			return exitUsage
		case *soft:
			op.Soft = soft
		case *required:
			f := false
			op.Soft = &f
		}
	}
	if len(on) > 0 {
		op.On = on
	}
	doc, err := edit.Open(*file)
	if err != nil {
		fmt.Fprintln(stderr, "hetman scribe:", err)
		return exitFailed
	}
	if err := doc.Apply(op); err != nil {
		fmt.Fprintf(stderr, "hetman scribe %s: %v\n%s\n", name, err, p.T("scribe.nothing_written"))
		return exitUsage
	}
	fmt.Fprintln(stdout, i18n.Upper(op.WordsIn(p))+".")
	if !doc.Changed() {
		fmt.Fprintln(stdout, p.T("scribe.already_says"))
		return exitDone
	}
	fmt.Fprint(stdout, doc.Diff())
	for _, n := range doc.NotesIn(p) {
		fmt.Fprintln(stdout, p.T("op.note", "note", n))
	}
	o, checkErr := doc.Check()
	if checkErr != nil {
		fmt.Fprintf(stderr, "%v\n%s\n", checkErr, p.T("scribe.does_not_pass"))
	} else {
		for _, s := range edit.Warnings(o) {
			fmt.Fprintln(stderr, "warning:", s)
		}
	}
	switch {
	case *dry:
		fmt.Fprintln(stdout, p.T("scribe.dry_run"))
		if checkErr != nil {
			return exitUsage
		}
		return exitDone
	case checkErr != nil && !*force:
		fmt.Fprintln(stderr, p.T("scribe.nothing_written_force"))
		return exitUsage
	}
	if err := doc.Save(*force); err != nil {
		fmt.Fprintln(stderr, "hetman scribe:", err)
		return exitFailed
	}
	if checkErr != nil {
		fmt.Fprintln(stdout, p.T("scribe.wrote_forced", "path", doc.Path))
	} else {
		fmt.Fprintln(stdout, p.T("op.wrote", "path", doc.Path))
	}
	for _, f := range doc.Files() {
		if f.Old == nil {
			fmt.Fprintln(stdout, p.T("op.created", "path", f.Path))
		}
	}
	return exitDone
}

// readChronicle opens the chronicle of an orders file only if it exists, so
// that asking about campaigns never creates one.
func readChronicle(o *orders.Orders) (*chronicle.Chronicle, error) {
	path := engine.ChroniclePath(o)
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	return chronicle.Open(path)
}

// explainer answers queries from the chronicle of an orders file.
func explainer(p *i18n.Printer, o *orders.Orders, chron *chronicle.Chronicle) func(scribe.Query) (string, error) {
	return func(q scribe.Query) (string, error) {
		if chron == nil {
			return "", errors.New(p.T("scribe.no_campaigns"))
		}
		var k chronicle.Campaign
		var err error
		if q.Campaign == 0 {
			k, err = chron.Latest()
		} else {
			k, err = chron.Campaign(q.Campaign)
		}
		if errors.Is(err, chronicle.ErrNoCampaign) {
			if q.Campaign == 0 {
				return "", errors.New(p.T("scribe.no_campaigns"))
			}
			return "", errors.New(p.T("scribe.no_campaign", "id", q.Campaign))
		}
		if err != nil {
			return "", err
		}
		events, err := chron.Events(k.ID)
		if err != nil {
			return "", err
		}
		if q.Node != "" && (o == nil || o.Nodes[q.Node] == nil) {
			found := false
			for _, e := range events {
				found = found || e.Node == q.Node
			}
			if !found {
				return "", errors.New(p.T("scribe.no_node", "id", k.ID, "node", q.Node))
			}
		}
		return explain.Text(explain.In(p, k, events, q.Kind, q.Node, time.Local)), nil
	}
}

func explainCmd(args []string, stdout, stderr io.Writer) int {
	p := personPrinter(nil)
	fs := flag.NewFlagSet("hetman scribe explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, p.T("flag.f"))
	id := fs.Int64("c", 0, p.T("flag.explain_c"))
	kind := fs.String("kind", explain.Summary, p.T("flag.kind"))
	node := fs.String("node", "", p.T("flag.node"))
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, p.T("cli.unexpected_argument", "command", "scribe explain", "arg", strconv.Quote(fs.Arg(0))))
		return exitUsage
	}
	if !contains(explain.Kinds, *kind) {
		fmt.Fprintln(stderr, p.T("scribe.bad_kind", "kinds", strings.Join(explain.Kinds, ", ")))
		return exitUsage
	}
	// The explanation comes from the chronicle alone, so orders that do not
	// pass the check now still tell where the chronicle is.
	abs, err := filepath.Abs(*file)
	if err != nil {
		fmt.Fprintln(stderr, "hetman scribe explain:", err)
		return exitUsage
	}
	o, _ := orders.Load(abs)
	p = personPrinter(o)
	if o == nil {
		o = &orders.Orders{Path: abs, Dir: filepath.Dir(abs)}
	}
	chron, err := readChronicle(o)
	if err != nil {
		fmt.Fprintln(stderr, "hetman scribe explain:", err)
		return exitFailed
	}
	if chron != nil {
		defer chron.Close()
	}
	text, err := explainer(p, o, chron)(scribe.Query{Kind: *kind, Campaign: *id, Node: *node})
	if err != nil {
		fmt.Fprintln(stderr, "hetman scribe explain:", err)
		return exitFailed
	}
	fmt.Fprint(stdout, text)
	return exitDone
}

func sentenceCmd(p *i18n.Printer, file, sentence string, yes bool, stdout, stderr io.Writer) int {
	o, err := orders.Load(file)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n%s\n", err, p.T("scribe.sentence_invalid"))
		return exitUsage
	}
	if o.Scribe == nil {
		fmt.Fprintln(stderr, p.T("scribe.no_model", "path", o.Path))
		return exitUsage
	}
	chron, err := readChronicle(o)
	if err != nil {
		fmt.Fprintln(stderr, "hetman scribe:", err)
		return exitFailed
	}
	var campaigns []int64
	var explainFn func(scribe.Query) (string, error)
	if chron != nil {
		defer chron.Close()
		list, err := chron.Campaigns(1000)
		if err != nil {
			fmt.Fprintln(stderr, "hetman scribe:", err)
			return exitFailed
		}
		for i := len(list) - 1; i >= 0; i-- {
			campaigns = append(campaigns, list[i].ID)
		}
		explainFn = explainer(p, o, chron)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a := &scribe.Assistant{Orders: o, Model: newModel(o), In: bufio.NewReader(Stdin), Out: stdout, Yes: yes, Echo: !isTerminal(Stdin),
		Campaigns: campaigns, Explain: explainFn, Text: p}
	switch err := a.Handle(ctx, sentence); {
	case err == nil:
		return exitDone
	case errors.Is(err, scribe.ErrDeclined), errors.Is(err, scribe.ErrUnclear), errors.Is(err, scribe.ErrRejected), errors.Is(err, scribe.ErrInvalid):
		return exitFailed
	default:
		fmt.Fprintln(stderr, "hetman scribe:", err)
		return exitFailed
	}
}

// isTerminal reports whether r is a terminal a person types into.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
