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
	{edit.OpAssistant, "assistant EXECUTOR MODEL [-effort E]", "hetman scribe assistant stub any-model"},
	{edit.OpLanguage, "language CODE", "hetman scribe language pt"},
	{edit.OpUnlanguage, "unlanguage", "hetman scribe unlanguage"},
}

func scribeUsage() string {
	var b strings.Builder
	b.WriteString(`usage:
  hetman scribe [-f FILE]                      the wizard: build or change the flow by answering questions
  hetman scribe [-f FILE] [-yes] "SENTENCE"    one sentence to one operation or question, through the model the orders name
  hetman scribe OPERATION ARGS [flags]         one operation on the orders file
  hetman scribe explain [-f FILE] [-c ID] [-kind KIND] [-node NODE]
                                               explain a campaign in plain words (kinds: ` + strings.Join(explain.Kinds, ", ") + `)
  hetman scribe skill [-dir DIR] [-force]      write the setup assistant skill for a coding agent

operations:
`)
	for _, u := range opUsage {
		spec, _ := edit.SpecOf(u.op)
		fmt.Fprintf(&b, "  %s\n      %s\n", u.form, spec.Summary)
	}
	b.WriteString(`
node flags (new, add): -run CMD, -timeout D, -checks A,B, -executor NAME, -model M, -effort E,
  -tools A,B, -attempts N, -prompt FILE, -schema FILE, -outcomes A,B, -on OUTCOME=TARGET (repeat),
  -starters (create missing prompt and schema files), -locks A,B (resources held exclusively),
  -segment FILE (segment, foreach), -input REF (segment), -items REF and -parallel N (foreach)
set fields: run, timeout, idle_timeout, executor, model, effort, tools, attempts, dialogue, prompt, schema,
  locks, segment, input, items, parallel
unset fields: timeout, idle_timeout, model, effort, tools, attempts, dialogue, locks, input, parallel
language codes: ` + strings.Join(language.Codes(), ", ") + `
flags of every operation:
  -f FILE    orders file (default orders.yaml)
  -dry-run   show the change and the check, write nothing
  -force     write even if the result does not pass the check (for a state meant to be finished later)
`)
	return b.String()
}

// scribeCmd is the entry to the setup assistant.
func scribeCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "skill":
			return writeScribe(args[1:], stdout, stderr)
		case "explain":
			return explainCmd(args[1:], stdout, stderr)
		case "help", "-h", "--help":
			fmt.Fprint(stdout, scribeUsage())
			return exitDone
		}
		if _, ok := edit.SpecOf(args[0]); ok {
			return opCmd(args[0], args[1:], stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("hetman scribe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, "orders file")
	yes := fs.Bool("yes", false, "apply a clear operation without asking")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitUsage
	}
	switch len(pos) {
	case 0:
		if *yes {
			fmt.Fprintln(stderr, "hetman scribe: -yes is for a sentence; the wizard always asks")
			return exitUsage
		}
		if err := wizard.Run(*file, Stdin, stdout, !isTerminal(Stdin)); err != nil {
			fmt.Fprintln(stderr, "hetman scribe:", err)
			return exitFailed
		}
		return exitDone
	case 1:
		return sentenceCmd(*file, pos[0], *yes, stdout, stderr)
	}
	fmt.Fprintf(stderr, "hetman scribe: %q is not an operation; put a sentence in quotes, or see hetman scribe help\n", pos[0])
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
		return errors.New("use OUTCOME=TARGET")
	}
	f[k] = v
	return nil
}

func listOf(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

// opCmd runs one operation given on the command line.
func opCmd(name string, args []string, stdout, stderr io.Writer) int {
	spec, _ := edit.SpecOf(name)
	fs := flag.NewFlagSet("hetman scribe "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, "orders file")
	dry := fs.Bool("dry-run", false, "show the change, write nothing")
	force := fs.Bool("force", false, "write even if the result does not pass the check")
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
			run = fs.String("run", "", "command: program and arguments")
		case "checks", "tools", "outcomes", "options", "locks":
			lists[f] = fs.String(f, "", "list, separated by commas")
		case "at", "attempts", "parallel":
			ints[f] = fs.Int(f, 0, "number")
		case "starters":
			starters = fs.Bool("starters", false, "create missing prompt and schema files")
		case "soft":
			soft = fs.Bool("soft", false, "the check only warns")
			required = fs.Bool("required", false, "the check blocks the gate")
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
	if len(pos) != len(spec.Args) {
		for _, u := range opUsage {
			if u.op == name {
				fmt.Fprintf(stderr, "usage: hetman scribe %s\nexample: %s\n", u.form, u.example)
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
				return fmt.Errorf("-%s must be a number", field)
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
			fmt.Fprintln(stderr, "hetman scribe: a check is either -soft or -required")
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
		fmt.Fprintf(stderr, "hetman scribe %s: %v\nnothing was written\n", name, err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "%s\n", strings.ToUpper(op.Words()[:1])+op.Words()[1:]+".")
	if !doc.Changed() {
		fmt.Fprintln(stdout, "The file already says that; nothing to write.")
		return exitDone
	}
	fmt.Fprint(stdout, doc.Diff())
	for _, n := range doc.Notes() {
		fmt.Fprintln(stdout, "note:", n)
	}
	o, checkErr := doc.Check()
	if checkErr != nil {
		fmt.Fprintf(stderr, "%v\nthe result does not pass the orders check\n", checkErr)
	} else {
		for _, s := range edit.Warnings(o) {
			fmt.Fprintln(stderr, "warning:", s)
		}
	}
	switch {
	case *dry:
		fmt.Fprintln(stdout, "Dry run: nothing was written.")
		if checkErr != nil {
			return exitUsage
		}
		return exitDone
	case checkErr != nil && !*force:
		fmt.Fprintln(stderr, "nothing was written (use -force to write it anyway)")
		return exitUsage
	}
	if err := doc.Save(*force); err != nil {
		fmt.Fprintln(stderr, "hetman scribe:", err)
		return exitFailed
	}
	if checkErr != nil {
		fmt.Fprintf(stdout, "Wrote %s although it does not pass the check (-force).\n", doc.Path)
	} else {
		fmt.Fprintf(stdout, "Wrote %s.\n", doc.Path)
	}
	for _, f := range doc.Files() {
		if f.Old == nil {
			fmt.Fprintf(stdout, "Created %s.\n", f.Path)
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
func explainer(o *orders.Orders, chron *chronicle.Chronicle) func(scribe.Query) (string, error) {
	return func(q scribe.Query) (string, error) {
		if chron == nil {
			return "", errors.New("there are no campaigns yet")
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
				return "", errors.New("there are no campaigns yet")
			}
			return "", fmt.Errorf("there is no campaign %d", q.Campaign)
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
				return "", fmt.Errorf("campaign %d has no node %s", k.ID, q.Node)
			}
		}
		return explain.Text(explain.Campaign(k, events, q.Kind, q.Node, time.Local)), nil
	}
}

func explainCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hetman scribe explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, "orders file")
	id := fs.Int64("c", 0, "campaign id (default: the latest)")
	kind := fs.String("kind", explain.Summary, "what to explain")
	node := fs.String("node", "", "only this node")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "hetman scribe explain: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	if !contains(explain.Kinds, *kind) {
		fmt.Fprintf(stderr, "hetman scribe explain: -kind must be one of %s\n", strings.Join(explain.Kinds, ", "))
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
	text, err := explainer(o, chron)(scribe.Query{Kind: *kind, Campaign: *id, Node: *node})
	if err != nil {
		fmt.Fprintln(stderr, "hetman scribe explain:", err)
		return exitFailed
	}
	fmt.Fprint(stdout, text)
	return exitDone
}

func sentenceCmd(file, sentence string, yes bool, stdout, stderr io.Writer) int {
	o, err := orders.Load(file)
	if err != nil {
		fmt.Fprintf(stderr, "%v\nhetman scribe: the orders do not pass the check; a sentence can only be mapped to an operation on a valid file\n", err)
		return exitUsage
	}
	if o.Scribe == nil {
		fmt.Fprintf(stderr, "hetman scribe: %s names no model for Scribe, and there is no default.\n"+
			"To use one, name a declared executor and a model, for example:\n"+
			"  hetman scribe assistant EXECUTOR MODEL\n"+
			"Without a model, the wizard asks fixed questions: hetman scribe\n"+
			"and every operation is a command: hetman scribe help\n", o.Path)
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
		explainFn = explainer(o, chron)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a := &scribe.Assistant{Orders: o, Model: newModel(o), In: bufio.NewReader(Stdin), Out: stdout, Yes: yes, Echo: !isTerminal(Stdin),
		Campaigns: campaigns, Explain: explainFn}
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
