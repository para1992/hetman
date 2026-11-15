// Package wizard builds and changes an orders file by asking fixed
// questions in a terminal. It uses no model: every answer is typed or
// picked from suggestions taken from the current file, and every change is
// made by the operations of package edit, checked, shown and confirmed
// before it is written.
package wizard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"hetman/internal/edit"
	"hetman/internal/engine"
	"hetman/internal/i18n"
	"hetman/internal/language"
	"hetman/internal/orders"
)

// errQuit ends the wizard: the person chose to quit, or the input ended.
var errQuit = errors.New("quit")

type wizard struct {
	path string
	in   *bufio.Reader
	out  io.Writer
	echo bool           // write each answer after its question, for input that is not typed
	o    *orders.Orders // the orders as they are now, nil before the first step
	p    *i18n.Printer  // the language of the questions
}

// labels are the message keys of the fixed choices the wizard offers. A
// choice is kept by its English text; the person sees it in their language
// and may answer in either.
var labels = map[string]string{
	"add a step":              "wizard.action.add_step",
	"add a gate":              "wizard.action.add_gate",
	"remove a step":           "wizard.action.remove_step",
	"rename a step":           "wizard.action.rename_step",
	"change a transition":     "wizard.action.change_transition",
	"set or remove a timeout": "wizard.action.timeout",
	"change an agent step (executor, model, effort, tools, attempts)": "wizard.action.change_agent",
	"declare or change a check":                                       "wizard.action.declare_check",
	"declare an executor":                                             "wizard.action.declare_executor",
	"add a question to a gate or a human step":                        "wizard.action.add_question",
	"quit":                          "wizard.action.quit",
	"after a step":                  "wizard.place.after",
	"before a step":                 "wizard.place.before",
	"script (runs a command)":       "wizard.type.script",
	"agent (a model does the work)": "wizard.type.agent",
	"human (a person decides)":      "wizard.type.human",
	"required (blocks the gate)":    "wizard.kind.required",
	"soft (only warns)":             "wizard.kind.soft",
	"another outcome":               "wizard.another_outcome",
}

// label is how a choice is shown.
func (w *wizard) label(option string) string {
	if key, ok := labels[option]; ok {
		return w.p.T(key)
	}
	return option
}

// Run asks questions on in and out until the person quits or the input
// ends. Each change is written once confirmed. echo writes every answer
// read, so that a transcript of scripted input reads like a typed one.
func Run(path string, in io.Reader, out io.Writer, echo bool) error {
	return RunIn(nil, path, in, out, echo)
}

// RunIn runs the wizard as Run does, asking in the language of p.
func RunIn(p *i18n.Printer, path string, in io.Reader, out io.Writer, echo bool) error {
	w := &wizard{path: path, in: bufio.NewReader(in), out: out, echo: echo, p: p}
	fmt.Fprintln(out, p.T("wizard.title", "path", path))
	fmt.Fprintln(out, p.T("wizard.hint"))
	doc, err := edit.Open(path)
	if err != nil {
		return err
	}
	if !doc.Exists() {
		fmt.Fprintf(out, "\n%s\n", p.T("wizard.new_file", "file", filepath.Base(path)))
		for {
			ops, err := w.newFlow()
			if err == errQuit {
				return nil
			}
			if err == nil && w.commit(ops) {
				break
			}
		}
	}
	actions := []struct {
		name string
		do   func() ([]edit.Op, error)
	}{
		{"add a step", w.addStep},
		{"add a gate", w.addGate},
		{"remove a step", w.removeStep},
		{"rename a step", w.renameStep},
		{"change a transition", w.changeTransition},
		{"set or remove a timeout", w.setTimeout},
		{"change an agent step (executor, model, effort, tools, attempts)", w.changeAgent},
		{"declare or change a check", w.declareCheck},
		{"declare an executor", w.declareExecutor},
		{"add a question to a gate or a human step", w.addQuestion},
		{"quit", nil},
	}
	for {
		if err := w.load(); err != nil {
			return err
		}
		var names []string
		for _, a := range actions {
			names = append(names, a.name)
		}
		fmt.Fprintln(out)
		pick, err := w.choose(p.T("wizard.what_next"), names, "quit")
		if err != nil || pick == "quit" {
			return nil
		}
		for _, a := range actions {
			if a.name != pick {
				continue
			}
			ops, err := a.do()
			switch {
			case err == errQuit:
				return nil
			case err != nil:
				fmt.Fprintf(out, "%v\n", err)
			default:
				w.commit(ops)
			}
		}
	}
}

// load reads the orders as they are now.
func (w *wizard) load() error {
	o, err := orders.Load(w.path)
	if err != nil {
		return fmt.Errorf("%s\n%v", w.p.T("wizard.orders_invalid"), err)
	}
	w.o = o
	return nil
}

// commit applies operations together, checks the result, shows it and
// writes it once confirmed. It reports whether it wrote.
func (w *wizard) commit(ops []edit.Op) bool {
	doc, err := edit.Open(w.path)
	if err != nil {
		fmt.Fprintln(w.out, err)
		return false
	}
	for _, op := range ops {
		if err := doc.Apply(op); err != nil {
			fmt.Fprintln(w.out, w.p.T("wizard.cannot", "words", op.WordsIn(w.p), "error", err))
			return false
		}
	}
	o, err := doc.Check()
	if err != nil {
		fmt.Fprintln(w.out, w.p.T("assist.check_fails", "problems", err))
		return false
	}
	fmt.Fprintf(w.out, "\n%s\n", w.p.T("wizard.the_change"))
	for _, op := range ops {
		fmt.Fprintf(w.out, "  - %s\n", op.WordsIn(w.p))
	}
	fmt.Fprint(w.out, doc.Diff())
	for _, n := range doc.NotesIn(w.p) {
		fmt.Fprintln(w.out, w.p.T("op.note", "note", n))
	}
	for _, s := range append(edit.Warnings(o), engine.AdapterWarnings(context.Background(), o)...) {
		fmt.Fprintln(w.out, "warning:", s) // i18n: keep; the orders check problems stay as tools read them
	}
	ok, err := w.yesno(w.p.T("wizard.write_it"), true)
	if err != nil || !ok {
		fmt.Fprintln(w.out, w.p.T("assist.nothing_changed"))
		return false
	}
	if err := doc.Save(false); err != nil {
		fmt.Fprintln(w.out, err)
		return false
	}
	fmt.Fprintln(w.out, w.p.T("op.wrote", "path", filepath.Base(doc.Path)))
	return true
}

// ask reads one answer; an empty answer gives def.
func (w *wizard) ask(prompt, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(w.out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(w.out, "%s: ", prompt)
	}
	line, err := w.in.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(w.out)
		return "", errQuit
	}
	if w.echo {
		fmt.Fprintln(w.out, strings.TrimRight(line, "\r\n"))
	}
	if s := strings.TrimSpace(line); s != "" {
		return s, nil
	}
	return def, nil
}

// need reads an answer that must not be empty.
func (w *wizard) need(prompt, def string) (string, error) {
	for {
		s, err := w.ask(prompt, def)
		if err != nil || s != "" {
			return s, err
		}
		fmt.Fprintln(w.out, w.p.T("wizard.need_answer"))
	}
}

// name reads the name of a new node: valid and not taken.
func (w *wizard) name(prompt, def string) (string, error) {
	for {
		s, err := w.need(prompt, def)
		switch {
		case err != nil:
			return "", err
		case !edit.ValidName(s):
			fmt.Fprintln(w.out, w.p.T("wizard.bad_name"))
		case w.o != nil && w.o.Nodes[s] != nil:
			fmt.Fprintln(w.out, w.p.T("wizard.name_taken", "name", s))
		default:
			return s, nil
		}
	}
}

// choose offers a numbered list; the answer is a number, one of the
// options as written, or its first word.
func (w *wizard) choose(prompt string, options []string, def string) (string, error) {
	fmt.Fprintln(w.out, prompt)
	for i, o := range options {
		fmt.Fprintf(w.out, "  %d) %s\n", i+1, w.label(o))
	}
	if def != "" {
		def = w.label(def)
	}
	for {
		s, err := w.ask(w.p.T("wizard.choice"), def)
		if err != nil {
			return "", err
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(options) {
			return options[n-1], nil
		}
		for _, o := range options {
			if o == s || strings.Fields(o)[0] == s {
				return o, nil
			}
		}
		for _, o := range options {
			if l := w.label(o); l == s || strings.Fields(l)[0] == s {
				return o, nil
			}
		}
		fmt.Fprintln(w.out, w.p.T("wizard.pick_number", "from", 1, "to", len(options)))
	}
}

func (w *wizard) yesno(prompt string, def bool) (bool, error) {
	d := w.p.T("wizard.no_short")
	if def {
		d = w.p.T("wizard.yes_short")
	}
	for {
		s, err := w.ask(prompt+" "+w.p.T("wizard.yes_no"), d)
		if err != nil {
			return false, err
		}
		switch {
		case i18n.IsYes(w.p, s):
			return true, nil
		case i18n.IsNo(w.p, s):
			return false, nil
		}
		fmt.Fprintln(w.out, w.p.T("wizard.answer_yes_no"))
	}
}

// nodes lists the node ids, optionally only those of some types.
func (w *wizard) nodes(types ...string) []string {
	if w.o == nil {
		return nil
	}
	var out []string
	for _, id := range w.o.Order {
		if len(types) == 0 || contains(types, w.o.Nodes[id].Type) {
			out = append(out, id)
		}
	}
	return out
}

// lastOnLine is the last node reached from the start by main outcomes: the
// natural place to add a step after.
func (w *wizard) lastOnLine() string {
	seen := map[string]bool{}
	last := w.o.Start
	for n := w.o.Start; n != "" && !seen[n] && w.o.Nodes[n] != nil; {
		seen[n], last = true, n
		node := w.o.Nodes[n]
		n = node.On[edit.Main(keysOf(node.On))]
	}
	return last
}

// keysOf lists the outcomes of a node in a stable order: the engine's own
// first, then the rest by name.
func keysOf(on map[string]string) []string {
	var out []string
	for _, k := range []string{orders.OK, orders.Pass} {
		if _, ok := on[k]; ok {
			out = append(out, k)
		}
	}
	var rest []string
	for k := range on {
		if k != orders.OK && k != orders.Pass {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (w *wizard) newFlow() ([]edit.Op, error) {
	abs, _ := filepath.Abs(w.path)
	dir := filepath.Base(filepath.Dir(abs))
	if !edit.ValidName(dir) {
		dir = "flow"
	}
	flow, err := w.need(w.p.T("wizard.flow_name"), dir)
	if err != nil {
		return nil, err
	}
	lang, err := w.language()
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(w.out, w.p.T("wizard.first_step"))
	op, extra, err := w.step(edit.Op{Op: edit.OpNew, Flow: flow}, false)
	if err != nil {
		return nil, err
	}
	ops := append([]edit.Op{op}, extra...)
	if lang != "" {
		ops = append(ops, edit.Op{Op: edit.OpLanguage, Language: lang})
	}
	return ops, nil
}

// skipLanguage is the answer that keeps English.
const skipLanguage = "skip"

// language asks for the language of what the flow addresses to a person
// and returns its code, or nothing to keep English.
func (w *wizard) language() (string, error) {
	fmt.Fprintln(w.out, w.p.T("wizard.language.question"))
	fmt.Fprintln(w.out, w.p.T("wizard.language.note"))
	fmt.Fprintf(w.out, "  0) %s\n", w.p.T("wizard.language.skip", "skip", skipLanguage))
	for i, l := range language.List {
		fmt.Fprintf(w.out, "  %d) %s  %s\n", i+1, l.Code, l.Label())
	}
	for {
		s, err := w.ask(w.p.T("wizard.language.prompt"), skipLanguage)
		if err != nil {
			return "", err
		}
		if strings.EqualFold(s, skipLanguage) || s == "0" {
			return "", nil
		}
		if n, err := strconv.Atoi(s); err == nil {
			if n >= 1 && n <= len(language.List) {
				return language.List[n-1].Code, nil
			}
			fmt.Fprintln(w.out, w.p.T("wizard.pick_number", "from", 0, "to", len(language.List)))
			continue
		}
		switch found := language.Find(s); len(found) {
		case 1:
			return found[0].Code, nil
		case 0:
			fmt.Fprintln(w.out, w.p.T("wizard.language.unknown", "name", strconv.Quote(s)))
		default:
			var names []string
			for _, l := range found {
				names = append(names, l.Code+" "+l.English)
			}
			fmt.Fprintln(w.out, w.p.T("wizard.language.ambiguous", "names", strings.Join(names, ", ")))
		}
	}
}

// place asks where a new node goes.
func (w *wizard) place(op *edit.Op) error {
	where, err := w.choose(w.p.T("wizard.where"), []string{"after a step", "before a step"}, "after a step")
	if err != nil {
		return err
	}
	if where == "after a step" {
		op.After, err = w.choose(w.p.T("wizard.after_which"), w.nodes(), w.lastOnLine())
	} else {
		op.Before, err = w.choose(w.p.T("wizard.before_which"), w.nodes(), w.o.Start)
	}
	return err
}

// target asks where an outcome leads.
func (w *wizard) target(prompt, def string, self string) (string, error) {
	options := append(append([]string{}, w.nodes()...), orders.Done, orders.Failed)
	if self != "" && !contains(options, self) {
		options = append([]string{self}, options...)
	}
	return w.choose(prompt, options, def)
}

func (w *wizard) addStep() ([]edit.Op, error) {
	op, extra, err := w.step(edit.Op{Op: edit.OpAdd}, true)
	if err != nil {
		return nil, err
	}
	return append(extra, op), nil
}

// step asks for a script, agent or human node. Operations it needs first
// (declaring an executor) come back as extra; for a new flow they come
// after the new operation, which creates the file.
func (w *wizard) step(op edit.Op, placed bool) (edit.Op, []edit.Op, error) {
	var extra []edit.Op
	typ, err := w.choose(w.p.T("wizard.step_kind"), []string{"script (runs a command)", "agent (a model does the work)", "human (a person decides)"}, "script (runs a command)")
	if err != nil {
		return op, nil, err
	}
	op.Type = strings.Fields(typ)[0]
	if op.Node, err = w.name(w.p.T("wizard.step_name"), ""); err != nil {
		return op, nil, err
	}
	if placed {
		if err := w.place(&op); err != nil {
			return op, nil, err
		}
	}
	op.On = map[string]string{}
	switch op.Type {
	case orders.Script:
		cmd, err := w.need(w.p.T("wizard.command"), "sh steps/"+op.Node+".sh")
		if err != nil {
			return op, nil, err
		}
		if op.Run, err = edit.SplitCommand(cmd); err != nil {
			return op, nil, err
		}
		if op.Timeout, err = w.ask(w.p.T("wizard.time_limit", "example", "5m"), ""); err != nil {
			return op, nil, err
		}
		to, err := w.target(w.p.T("wizard.if_fails"), orders.Failed, op.Node)
		if err != nil {
			return op, nil, err
		}
		op.On[orders.Fail] = to
		if op.Timeout != "" {
			op.On[orders.Timeout] = to
		}
	case orders.Agent:
		var executors []string
		if w.o != nil {
			for name := range w.o.Executors {
				executors = append(executors, name)
			}
			sort.Strings(executors)
		}
		var declared *edit.Op
		if len(executors) > 0 {
			if op.Executor, err = w.choose(w.p.T("wizard.which_executor"), executors, executors[0]); err != nil {
				return op, nil, err
			}
		} else {
			fmt.Fprintln(w.out, w.p.T("wizard.no_executor"))
			x, err := w.executorOp()
			if err != nil {
				return op, nil, err
			}
			extra = append(extra, x)
			op.Executor, declared = x.Name, &x
		}
		model, entry, err := w.pickModel(w.catalog(op.Executor, declared), "")
		if err != nil {
			return op, nil, err
		}
		op.Model = model
		if op.Effort, err = w.pickEffort(entry, ""); err != nil {
			return op, nil, err
		}
		fmt.Fprintln(w.out, w.p.T("wizard.fail_on_denied_hint"))
		outs, err := w.need(w.p.T("wizard.outcomes"), "ok")
		if err != nil {
			return op, nil, err
		}
		op.Outcomes = list(outs)
		for _, o := range op.Outcomes[1:] {
			if op.On[o], err = w.target(w.p.T("wizard.if_outcome", "outcome", o), orders.Failed, op.Node); err != nil {
				return op, nil, err
			}
		}
		if op.On[orders.Fail], err = w.target(w.p.T("wizard.if_step_fails"), orders.Failed, op.Node); err != nil {
			return op, nil, err
		}
		if op.Timeout, err = w.ask(w.p.T("wizard.time_limit", "example", "10m"), "10m"); err != nil {
			return op, nil, err
		}
		if op.Starters, err = w.yesno(w.p.T("wizard.starters"), true); err != nil {
			return op, nil, err
		}
	case orders.Human:
		outs, err := w.need(w.p.T("wizard.human_choices"), "approve, reject")
		if err != nil {
			return op, nil, err
		}
		op.Outcomes = list(outs)
		for _, o := range op.Outcomes[1:] {
			if op.On[o], err = w.target(w.p.T("wizard.if_person_chooses", "outcome", o), orders.Failed, op.Node); err != nil {
				return op, nil, err
			}
		}
	}
	// Every outcome but the main one leads to failed unless told otherwise.
	for k, v := range op.On {
		if v == orders.Failed {
			delete(op.On, k)
		}
	}
	if len(op.On) == 0 {
		op.On = nil
	}
	return op, extra, nil
}

// list splits a list the way the list flags of hetman scribe do: an entry
// ends at a comma or space outside parentheses and quotes.
func list(s string) []string {
	out, _ := edit.SplitList(s)
	return out
}

func (w *wizard) addGate() ([]edit.Op, error) {
	op := edit.Op{Op: edit.OpAdd, Type: orders.Gate}
	var err error
	if op.Node, err = w.name(w.p.T("wizard.gate_name"), "gate"); err != nil {
		return nil, err
	}
	if err := w.place(&op); err != nil {
		return nil, err
	}
	var declared []string
	for name := range w.o.Checks {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	var ops []edit.Op
	declareNew := true
	if len(declared) > 0 {
		fmt.Fprintln(w.out, w.p.T("wizard.declared_checks", "checks", strings.Join(declared, ", ")))
		s, err := w.need(w.p.T("wizard.gate_checks"), strings.Join(declared, ", "))
		if err != nil {
			return nil, err
		}
		for _, c := range list(s) {
			if c != "new" {
				op.Checks = append(op.Checks, c)
			}
		}
		declareNew = contains(list(s), "new")
	}
	if declareNew {
		fmt.Fprintln(w.out, w.p.T("wizard.declare_gate_checks"))
	}
	for declareNew {
		c, err := w.checkOp(false)
		if err != nil {
			return nil, err
		}
		ops = append(ops, c)
		op.Checks = append(op.Checks, c.Name)
		if declareNew, err = w.yesno(w.p.T("wizard.another_check"), false); err != nil {
			return nil, err
		}
	}
	to, err := w.target(w.p.T("wizard.if_blocks"), orders.Failed, "")
	if err != nil {
		return nil, err
	}
	if to != orders.Failed {
		op.On = map[string]string{orders.Blocked: to}
	}
	return append(ops, op), nil
}

// checkOp asks for a check declaration.
func (w *wizard) checkOp(existing bool) (edit.Op, error) {
	op := edit.Op{Op: edit.OpCheck}
	var err error
	if op.Name, err = w.need(w.p.T("wizard.check_name"), ""); err != nil {
		return op, err
	}
	def := "sh checks/" + op.Name + ".sh"
	if c := w.o.Checks[op.Name]; existing && c != nil {
		def = strings.Join(c.Run, " ")
	}
	cmd, err := w.need(w.p.T("wizard.command"), def)
	if err != nil {
		return op, err
	}
	if op.Run, err = edit.SplitCommand(cmd); err != nil {
		return op, err
	}
	kind, err := w.choose(w.p.T("wizard.check_kind"), []string{"required (blocks the gate)", "soft (only warns)"}, "required (blocks the gate)")
	if err != nil {
		return op, err
	}
	soft := strings.HasPrefix(kind, "soft")
	op.Soft = &soft
	if op.Timeout, err = w.ask(w.p.T("wizard.time_limit", "example", "1m"), ""); err != nil {
		return op, err
	}
	return op, nil
}

func (w *wizard) executorOp() (edit.Op, error) {
	op := edit.Op{Op: edit.OpExecutor}
	var err error
	if op.Name, err = w.need(w.p.T("wizard.executor_name"), "agent"); err != nil {
		return op, err
	}
	cmd, err := w.need(w.p.T("wizard.adapter_command"), "")
	if err != nil {
		return op, err
	}
	op.Run, err = edit.SplitCommand(cmd)
	return op, err
}

func (w *wizard) removeStep() ([]edit.Op, error) {
	n, err := w.choose(w.p.T("wizard.which_step"), w.nodes(), "")
	if err != nil {
		return nil, err
	}
	return []edit.Op{{Op: edit.OpRemove, Node: n}}, nil
}

func (w *wizard) renameStep() ([]edit.Op, error) {
	n, err := w.choose(w.p.T("wizard.which_step"), w.nodes(), "")
	if err != nil {
		return nil, err
	}
	name, err := w.name(w.p.T("wizard.new_name"), "")
	if err != nil {
		return nil, err
	}
	return []edit.Op{{Op: edit.OpRename, Node: n, Name: name}}, nil
}

func (w *wizard) changeTransition() ([]edit.Op, error) {
	n, err := w.choose(w.p.T("wizard.which_step"), w.nodes(), "")
	if err != nil {
		return nil, err
	}
	node := w.o.Nodes[n]
	outs := keysOf(node.On)
	for _, o := range node.Outcomes() {
		if !contains(outs, o) {
			outs = append(outs, o)
		}
	}
	outs = append(outs, "another outcome")
	out, err := w.choose(w.p.T("wizard.which_outcome"), outs, "")
	if err != nil {
		return nil, err
	}
	if out == "another outcome" {
		if out, err = w.need(w.p.T("wizard.outcome_name"), ""); err != nil {
			return nil, err
		}
	}
	to, err := w.target(w.p.T("wizard.lead_where"), node.On[out], n)
	if err != nil {
		return nil, err
	}
	return []edit.Op{{Op: edit.OpLink, Node: n, Outcome: out, Target: to}}, nil
}

func (w *wizard) setTimeout() ([]edit.Op, error) {
	n, err := w.choose(w.p.T("wizard.which_step"), w.nodes(orders.Script, orders.Gate, orders.Agent), "")
	if err != nil {
		return nil, err
	}
	cur := ""
	if t := w.o.Nodes[n].Timeout; t > 0 {
		cur = t.String()
	}
	v, err := w.need(w.p.T("wizard.time_limit_none", "example", "5m"), cur)
	if err != nil {
		return nil, err
	}
	if v == "none" {
		return []edit.Op{{Op: edit.OpUnset, Node: n, Field: "timeout"}}, nil
	}
	return []edit.Op{{Op: edit.OpSet, Node: n, Field: "timeout", Value: v}}, nil
}

func (w *wizard) changeAgent() ([]edit.Op, error) {
	agents := w.nodes(orders.Agent)
	if len(agents) == 0 {
		return nil, errors.New(w.p.T("wizard.no_agent_step"))
	}
	n, err := w.choose(w.p.T("wizard.which_agent_step"), agents, agents[0])
	if err != nil {
		return nil, err
	}
	field, err := w.choose(w.p.T("wizard.what_to_change"), []string{"executor", "model", "effort", "tools", "attempts", "fail_on_denied"}, "model")
	if err != nil {
		return nil, err
	}
	node := w.o.Nodes[n]
	if (field == "model" || field == "effort") && node.Role == "" {
		return w.changeModel(n, field)
	}
	cur := map[string]string{"executor": node.Executor, "model": node.Model, "effort": node.Effort,
		"tools": strings.Join(node.Tools, ", "), "attempts": strconv.Itoa(node.MaxAttempts), "fail_on_denied": failOnDenied(node)}[field]
	if field == "fail_on_denied" {
		fmt.Fprintln(w.out, w.p.T("wizard.fail_on_denied_hint"))
	}
	v, err := w.ask(w.p.T("wizard.new_value"), cur)
	if err != nil {
		return nil, err
	}
	if v == "none" || v == "" {
		return []edit.Op{{Op: edit.OpUnset, Node: n, Field: field}}, nil
	}
	return []edit.Op{{Op: edit.OpSet, Node: n, Field: field, Value: v}}, nil
}

// changeModel changes the model or the effort of an agent node, offering
// what the catalogue of its executor lists. A new model that does not take
// the node's effort is followed by a question about the effort.
func (w *wizard) changeModel(n, field string) ([]edit.Op, error) {
	node := w.o.Nodes[n]
	c := w.catalog(node.Executor, nil)
	model, entry := node.Model, c.Model(node.Model)
	var err error
	if field == "model" {
		if model, entry, err = w.pickModel(c, node.Model); err != nil {
			return nil, err
		}
	}
	effort := node.Effort
	if field == "effort" || entry != nil && entry.EffortsKnown() && node.Effort != "" && !contains(entry.Efforts, node.Effort) {
		if effort, err = w.pickEffort(entry, node.Effort); err != nil {
			return nil, err
		}
	}
	var ops []edit.Op
	for _, f := range []struct{ field, old, new string }{{"model", node.Model, model}, {"effort", node.Effort, effort}} {
		switch {
		case f.new == f.old:
		case f.new == "":
			ops = append(ops, edit.Op{Op: edit.OpUnset, Node: n, Field: f.field})
		default:
			ops = append(ops, edit.Op{Op: edit.OpSet, Node: n, Field: f.field, Value: f.new})
		}
	}
	if len(ops) == 0 {
		return nil, errors.New(w.p.T("wizard.models.unchanged"))
	}
	return ops, nil
}

// failOnDenied writes a node's fail_on_denied as the set operation takes it.
func failOnDenied(n *orders.Node) string {
	if len(n.FailOnDeniedTools) > 0 {
		return strings.Join(n.FailOnDeniedTools, ", ")
	}
	return strconv.FormatBool(n.FailOnDenied)
}

func (w *wizard) declareCheck() ([]edit.Op, error) {
	var declared []string
	for name := range w.o.Checks {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	if len(declared) > 0 {
		fmt.Fprintln(w.out, w.p.T("wizard.declared_checks_change", "checks", strings.Join(declared, ", ")))
	}
	op, err := w.checkOp(true)
	if err != nil {
		return nil, err
	}
	return []edit.Op{op}, nil
}

func (w *wizard) declareExecutor() ([]edit.Op, error) {
	op, err := w.executorOp()
	if err != nil {
		return nil, err
	}
	return []edit.Op{op}, nil
}

func (w *wizard) addQuestion() ([]edit.Op, error) {
	targets := w.nodes(orders.Gate, orders.Human)
	if len(targets) == 0 {
		return nil, errors.New(w.p.T("wizard.no_ask_target"))
	}
	n, err := w.choose(w.p.T("wizard.which_ask_target"), targets, targets[0])
	if err != nil {
		return nil, err
	}
	op := edit.Op{Op: edit.OpAsk, Node: n}
	if op.ID, err = w.need(w.p.T("wizard.question_id"), ""); err != nil {
		return nil, err
	}
	if op.Question, err = w.need(w.p.T("wizard.question"), ""); err != nil {
		return nil, err
	}
	if w.o.Nodes[n].Type == orders.Human {
		if op.Kind, err = w.choose(w.p.T("wizard.answer_kind"), []string{"yesno", "choice", "text"}, "yesno"); err != nil {
			return nil, err
		}
		if op.Kind == "choice" {
			s, err := w.need(w.p.T("wizard.options"), "")
			if err != nil {
				return nil, err
			}
			op.Options = list(s)
		}
		if op.Kind == "yesno" {
			op.Kind = ""
		}
	}
	return []edit.Op{op}, nil
}
