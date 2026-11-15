package edit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"hetman/internal/i18n"
	"hetman/internal/language"
	"hetman/internal/runinput"
)

// Operation names.
const (
	OpNew        = "new"          // create the orders file with its first node
	OpAdd        = "add"          // add a node after or before another one
	OpRemove     = "remove"       // remove a node and connect around it
	OpRename     = "rename"       // rename a node everywhere it is referenced
	OpLink       = "link"         // set or change the transition of an outcome
	OpUnlink     = "unlink"       // remove the transition of an outcome
	OpSet        = "set"          // set a field of a node
	OpUnset      = "unset"        // remove a field of a node
	OpCheck      = "check"        // declare a check, or change one
	OpExecutor   = "executor"     // declare an executor, or change one
	OpGateAdd    = "gate-add"     // add a declared check to a gate
	OpGateRemove = "gate-remove"  // remove a check from a gate
	OpAsk        = "ask"          // add a question to a gate or a human node
	OpAssistant  = "assistant"    // name the executor and model Scribe may ask
	OpRole       = "role"         // declare a role, or change its selection mode
	OpRoleAdd    = "role-add"     // add a client to a role
	OpRoleRemove = "role-remove"  // remove a client from a role
	OpLanguage   = "language"     // set the language for the person
	OpUnlanguage = "unlanguage"   // remove the language for the person
	OpWorkdir    = "workdir"      // set the folder agent steps work in unless a node names its own
	OpShowAdd    = "show-add"     // add a file to what a gate or human node shows the person
	OpShowRemove = "show-remove"  // remove a file from what a gate or human node shows
	OpInputAdd   = "input-add"    // add a field to the description of the run input
	OpInputRm    = "input-remove" // remove a field of the run input
	OpInputSet   = "input-set"    // set the label, hint, required, default or ask of a run input field
	OpInputUnset = "input-unset"  // remove the label, hint or default of a run input field
)

// Op is one operation on an orders file. Only the fields its operation
// takes may be set; Validate says which.
type Op struct {
	Op string `json:"op"`

	Node     string            `json:"node,omitempty"`     // the node acted on, or the name of a new node
	Type     string            `json:"type,omitempty"`     // new, add: script, gate, agent, human, segment or foreach
	After    string            `json:"after,omitempty"`    // add: the node the new one follows
	Via      string            `json:"via,omitempty"`      // add with after: the outcome of After that leads to the new node
	Before   string            `json:"before,omitempty"`   // add: the node the new one comes before
	Name     string            `json:"name,omitempty"`     // rename: the new name; check, executor: what is declared
	Outcome  string            `json:"outcome,omitempty"`  // link, unlink
	Target   string            `json:"target,omitempty"`   // link: a node, done or failed
	Field    string            `json:"field,omitempty"`    // set, unset
	Value    string            `json:"value,omitempty"`    // set
	Check    string            `json:"check,omitempty"`    // gate-add, gate-remove
	At       int               `json:"at,omitempty"`       // gate-add: position from 1; 0 for the end
	ID       string            `json:"id,omitempty"`       // ask: question id
	Question string            `json:"question,omitempty"` // ask
	Kind     string            `json:"kind,omitempty"`     // ask: yesno (default), choice or text
	Options  []string          `json:"options,omitempty"`  // ask: the options of a choice
	Flow     string            `json:"flow,omitempty"`     // new: the flow name
	Run      []string          `json:"run,omitempty"`      // a command: program and arguments
	Timeout  string            `json:"timeout,omitempty"`  // a duration such as 5m
	Checks   []string          `json:"checks,omitempty"`   // a gate's checks, in order
	Executor string            `json:"executor,omitempty"` // agent node, assistant
	Model    string            `json:"model,omitempty"`    // agent node, assistant
	Effort   string            `json:"effort,omitempty"`   // agent node, assistant
	Tools    []string          `json:"tools,omitempty"`    // agent node
	Attempts int               `json:"attempts,omitempty"` // agent node: max_attempts
	Prompt   string            `json:"prompt,omitempty"`   // agent node: template file
	Schema   string            `json:"schema,omitempty"`   // agent node: schema file
	Outcomes []string          `json:"outcomes,omitempty"` // agent or human node: its outcomes
	On       map[string]string `json:"on,omitempty"`       // new node: transitions other than the defaults
	Soft     *bool             `json:"soft,omitempty"`     // check: soft (true) or required (false)
	Starters bool              `json:"starters,omitempty"` // agent node: create missing prompt and schema files
	Language string            `json:"language,omitempty"` // language: a code from the built-in list
	Segment  string            `json:"segment,omitempty"`  // segment or foreach node: the segment's orders file
	Input    string            `json:"input,omitempty"`    // segment node: what its child gets as input, such as output.plan.task
	Items    string            `json:"items,omitempty"`    // foreach node: the list, such as output.plan.items
	Parallel int               `json:"parallel,omitempty"` // foreach node: how many items run at the same time
	Locks    []string          `json:"locks,omitempty"`    // a new node or a check: resources it holds exclusively
	Workdir  string            `json:"workdir,omitempty"`  // workdir: the default folder of agent steps; add: an agent node's own; new: the default
	Max      int               `json:"max,omitempty"`      // link: how many times the transition may be taken
	Then     string            `json:"then,omitempty"`     // link with max: the outcome taken instead after that
	File     string            `json:"file,omitempty"`     // show-add, show-remove: a file relative to the orders folder, or {{work}}/FILE
	Role     string            `json:"role,omitempty"`     // a new agent node: the role whose clients run it
	Select   string            `json:"select,omitempty"`   // role: fixed, round_robin or quota
	Of       string            `json:"of,omitempty"`       // input-*: the list of objects the field belongs to; empty for a top-level field
	Label    string            `json:"label,omitempty"`    // input-add: the question or label shown to the person
	Hint     string            `json:"hint,omitempty"`     // input-add: a hint shown with it
	Required *bool             `json:"required,omitempty"` // input-add: whether an answer is needed
	Ask      *bool             `json:"ask,omitempty"`      // input-add: false for a fixed value that is never asked
	Default  string            `json:"default,omitempty"`  // input-add: the default, as text
}

// Spec describes one operation for people, the command line and a model.
type Spec struct {
	Op       string
	Args     []string // the fields given in order on the command line
	Required []string // fields that must be set
	Optional []string // fields that may be set
	Summary  string
}

// nodeFields are the fields that describe a new node.
var nodeFields = []string{"run", "timeout", "checks", "executor", "role", "model", "effort", "tools", "attempts", "prompt", "schema", "outcomes", "on", "starters",
	"segment", "input", "items", "parallel", "locks", "workdir"}

// Specs lists every operation.
var Specs = []Spec{
	{OpNew, []string{"type", "node"}, []string{"type", "node"}, append([]string{"flow"}, nodeFields...),
		"create the orders file with its first node, whose main outcome leads to done"},
	{OpAdd, []string{"type", "node"}, []string{"type", "node"}, append([]string{"after", "via", "before"}, nodeFields...),
		"add a node after a node (on its main outcome, or the outcome via) or before a node; the flow is rewired through it"},
	{OpRemove, []string{"node"}, []string{"node"}, nil,
		"remove a node; whatever led to it then leads where its main outcome led"},
	{OpRename, []string{"node", "name"}, []string{"node", "name"}, nil,
		"rename a node in the start, every transition and every prompt template placeholder"},
	{OpLink, []string{"node", "outcome", "target"}, []string{"node", "outcome", "target"}, []string{"max", "then"},
		"make an outcome of a node lead to a node, done or failed; with max and then, at most max times per campaign, after which the outcome then is taken instead"},
	{OpUnlink, []string{"node", "outcome"}, []string{"node", "outcome"}, nil,
		"remove the transition of an outcome of a node"},
	{OpSet, []string{"node", "field", "value"}, []string{"node", "field", "value"}, nil,
		"set a field of a node: " + strings.Join(setFields, ", ")},
	{OpUnset, []string{"node", "field"}, []string{"node", "field"}, nil,
		"remove a field of a node: " + strings.Join(unsetFields, ", ")},
	{OpCheck, []string{"name"}, []string{"name"}, []string{"run", "timeout", "soft", "locks"},
		"declare a check, or change the command, timeout, soft flag or locks of a declared one (soft only warns; required blocks the gate)"},
	{OpExecutor, []string{"name"}, []string{"name", "run"}, nil,
		"declare an executor (an adapter program and its arguments), or change its command"},
	{OpGateAdd, []string{"node", "check"}, []string{"node", "check"}, []string{"at"},
		"add a declared check to a gate, at a position from 1 (the end when not given)"},
	{OpGateRemove, []string{"node", "check"}, []string{"node", "check"}, nil,
		"remove a check from a gate"},
	{OpAsk, []string{"node", "id", "question"}, []string{"node", "id", "question"}, []string{"kind", "options"},
		"add a question to a gate (yes or no) or a human node (yesno, choice with options, or text)"},
	{OpAssistant, []string{"executor", "model"}, []string{"executor", "model"}, []string{"effort"},
		"name the executor and model Scribe may ask to turn a sentence into one operation"},
	{OpRole, []string{"name"}, []string{"name"}, []string{"select", "executor", "model", "effort"},
		"declare a role (its selection mode, fixed, round_robin or quota, and its first client: executor, model, effort), or change the mode of a declared one"},
	{OpRoleAdd, []string{"name", "executor"}, []string{"name", "executor"}, []string{"model", "effort", "at"},
		"add a client (an executor with its own model and effort) to a role, at a position from 1 (the end when not given)"},
	{OpRoleRemove, []string{"name", "at"}, []string{"name", "at"}, nil,
		"remove the client at a position from 1 from a role; a role keeps at least one client"},
	{OpLanguage, []string{"language"}, []string{"language"}, nil,
		"write what the flow addresses to a person (questions, summaries, plans) in this language, given by its code; code and the repository stay English"},
	{OpUnlanguage, nil, nil, nil,
		"remove the language for the person: the flow uses English and adds no language instruction"},
	{OpShowAdd, []string{"node", "file"}, []string{"node", "file"}, nil,
		"add a file to what a gate or human node shows the person before answering: relative to the orders folder, or {{work}}/FILE for one in the campaign's work folder"},
	{OpShowRemove, []string{"node", "file"}, []string{"node", "file"}, nil,
		"remove a file from what a gate or human node shows the person"},
	{OpWorkdir, []string{"workdir"}, []string{"workdir"}, nil,
		"set the folder agent steps work in, relative to the orders folder, for every agent node that names none of its own"},
	{OpInputAdd, []string{"name", "kind"}, []string{"name", "kind"}, []string{"of", "label", "hint", "language", "required", "ask", "default", "options"},
		"add a field to the run input a person gives when starting a campaign: kind " + strings.Join(InputKinds, ", ") + "; of names the list of objects it belongs to, which is created by its first field; ask false with a default for a fixed value"},
	{OpInputRm, []string{"name"}, []string{"name"}, []string{"of"},
		"remove a field of the run input, or of a list of objects named by of; the input section goes with its last field"},
	{OpInputSet, []string{"name", "field", "value"}, []string{"name", "field", "value"}, []string{"of", "language"},
		"set the label, hint, required (true or false), default or ask (true or false) of a run input field; a label or hint with language is written per language"},
	{OpInputUnset, []string{"name", "field"}, []string{"name", "field"}, []string{"of", "language"},
		"remove the label, hint or default of a run input field; with language only that language's text"},
}

// InputKinds are the kinds of a run input field.
var InputKinds = runinput.Kinds

// The properties of a run input field input-set and input-unset change.
var (
	inputSetFields   = []string{"label", "hint", "required", "default", "ask"}
	inputUnsetFields = []string{"label", "hint", "default"}
)

// SummaryIn is the summary in the language of p; in English it is Summary.
func (s Spec) SummaryIn(p *i18n.Printer) string {
	fields := ""
	switch s.Op {
	case OpSet:
		fields = strings.Join(setFields, ", ")
	case OpUnset:
		fields = strings.Join(unsetFields, ", ")
	case OpInputAdd:
		fields = strings.Join(InputKinds, ", ")
	}
	return p.T("op.summary."+s.Op, "fields", fields)
}

// SpecOf returns the spec of an operation.
func SpecOf(op string) (Spec, bool) {
	for _, s := range Specs {
		if s.Op == op {
			return s, true
		}
	}
	return Spec{}, false
}

// Node fields set and unset can change.
var (
	setFields = []string{"run", "timeout", "idle_timeout", "executor", "role", "model", "effort", "tools", "attempts", "dialogue", "prompt", "schema",
		"locks", "segment", "input", "items", "parallel", "workdir", "fail_on_denied"}
	unsetFields = []string{"timeout", "idle_timeout", "model", "effort", "tools", "attempts", "dialogue", "locks", "input", "parallel", "workdir", "fail_on_denied"}
)

// Fields lists the fields an Op has set, by their JSON names.
func (op Op) Fields() []string {
	data, _ := json.Marshal(op)
	var m map[string]json.RawMessage
	json.Unmarshal(data, &m)
	var out []string
	for k := range m {
		if k != "op" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Validate checks that an operation is known and has the fields it needs
// and no others. Whether the names it uses exist is checked when it is
// applied.
func (op Op) Validate() error {
	spec, ok := SpecOf(op.Op)
	if !ok {
		var names []string
		for _, s := range Specs {
			names = append(names, s.Op)
		}
		return fmt.Errorf("unknown operation %q (known: %s)", op.Op, strings.Join(names, ", "))
	}
	have := map[string]bool{}
	for _, f := range op.Fields() {
		have[f] = true
		if !contains(spec.Required, f) && !contains(spec.Optional, f) {
			return fmt.Errorf("%s does not take %s (it takes %s)", op.Op, f, strings.Join(append(append([]string{}, spec.Required...), spec.Optional...), ", "))
		}
	}
	for _, f := range spec.Required {
		if !have[f] {
			return fmt.Errorf("%s needs %s", op.Op, f)
		}
	}
	if op.Op == OpAdd && (op.After == "") == (op.Before == "") {
		return fmt.Errorf("add needs exactly one of after and before")
	}
	if op.Via != "" && op.After == "" {
		return fmt.Errorf("via is given only with after")
	}
	if op.Op == OpLink && (op.Max == 0) != (op.Then == "") {
		return fmt.Errorf("link takes max and then together: the transition may be taken max times, then the outcome then is taken instead")
	}
	if (op.Op == OpSet || op.Op == OpUnset) && op.Field != "" {
		list := setFields
		if op.Op == OpUnset {
			list = unsetFields
		}
		if !contains(list, op.Field) {
			return fmt.Errorf("%s: unknown field %q (known: %s)", op.Op, op.Field, strings.Join(list, ", "))
		}
	}
	if (op.Op == OpInputSet || op.Op == OpInputUnset) && op.Field != "" {
		list := inputSetFields
		if op.Op == OpInputUnset {
			list = inputUnsetFields
		}
		if !contains(list, op.Field) {
			return fmt.Errorf("%s: unknown field %q (known: %s)", op.Op, op.Field, strings.Join(list, ", "))
		}
	}
	if op.Op == OpInputAdd && op.Language != "" && op.Label == "" && op.Hint == "" {
		return fmt.Errorf("input-add: language says which language the label and hint are in; give one of them")
	}
	if op.Op == OpRole && op.Executor == "" && (op.Model != "" || op.Effort != "") {
		return fmt.Errorf("role: model and effort belong to the first client; give its executor too")
	}
	if op.Executor != "" && op.Role != "" {
		return fmt.Errorf("%s: an agent node names either a role or an executor, not both", op.Op)
	}
	for _, s := range append(append(append([]string{op.Value, op.Question, op.Model, op.Effort, op.Segment, op.Input, op.Items, op.Workdir, op.Then, op.File, op.Of, op.Label, op.Hint, op.Default}, op.Run...), op.Options...), op.Tools...) {
		if strings.ContainsAny(s, "\r\n") {
			return fmt.Errorf("%s: values must be one line", op.Op)
		}
	}
	return nil
}

// Words describes the operation in plain words, in English.
func (op Op) Words() string { return op.WordsIn(nil) }

// WordsIn describes the operation in plain words in the language of p.
func (op Op) WordsIn(p *i18n.Printer) string {
	switch op.Op {
	case OpNew:
		key := "op.words.new"
		if op.Flow != "" {
			key = "op.words.new_named"
		}
		return p.T(key, "flow", op.Flow, "type", op.Type, "node", op.Node, "more", nodeWords(p, op))
	case OpAdd:
		args := []any{"type", op.Type, "node", op.Node, "before", op.Before, "after", op.After, "via", op.Via, "more", nodeWords(p, op)}
		switch {
		case op.After != "" && op.Via != "":
			return p.T("op.words.add_after_via", args...)
		case op.After != "":
			return p.T("op.words.add_after", args...)
		}
		return p.T("op.words.add_before", args...)
	case OpRemove:
		return p.T("op.words.remove", "node", op.Node)
	case OpRename:
		return p.T("op.words.rename", "node", op.Node, "name", op.Name)
	case OpLink:
		if op.Max > 0 {
			return p.T("op.words.link_limited", "outcome", op.Outcome, "node", op.Node, "target", op.Target, "n", op.Max, "then", op.Then)
		}
		return p.T("op.words.link", "outcome", op.Outcome, "node", op.Node, "target", op.Target)
	case OpWorkdir:
		return p.T("op.words.workdir", "workdir", op.Workdir)
	case OpShowAdd:
		return p.T("op.words.show_add", "file", op.File, "node", op.Node)
	case OpShowRemove:
		return p.T("op.words.show_remove", "file", op.File, "node", op.Node)
	case OpUnlink:
		return p.T("op.words.unlink", "node", op.Node, "outcome", op.Outcome)
	case OpSet:
		return p.T("op.words.set", "field", op.Field, "node", op.Node, "value", op.Value)
	case OpUnset:
		return p.T("op.words.unset", "field", op.Field, "node", op.Node)
	case OpCheck:
		var parts []string
		if len(op.Run) > 0 {
			parts = append(parts, p.T("op.words.running", "run", strings.Join(op.Run, " ")))
		}
		if op.Timeout != "" {
			parts = append(parts, p.T("op.words.with_timeout", "timeout", op.Timeout))
		}
		if op.Soft != nil {
			parts = append(parts, p.T(map[bool]string{true: "op.words.soft", false: "op.words.required"}[*op.Soft]))
		}
		if len(op.Locks) > 0 {
			parts = append(parts, p.T("op.words.holding_locks", "locks", strings.Join(op.Locks, ", ")))
		}
		return p.T("op.words.check", "name", op.Name, "more", optList(", ", parts))
	case OpExecutor:
		return p.T("op.words.executor", "name", op.Name, "run", strings.Join(op.Run, " "))
	case OpGateAdd:
		if op.At > 0 {
			return p.T("op.words.gate_add_at", "check", op.Check, "node", op.Node, "at", op.At)
		}
		return p.T("op.words.gate_add", "check", op.Check, "node", op.Node)
	case OpGateRemove:
		return p.T("op.words.gate_remove", "check", op.Check, "node", op.Node)
	case OpAsk:
		kind := op.Kind
		if kind == "" {
			kind = "yesno"
		}
		args := []any{"kind", kind, "id", op.ID, "node", op.Node, "question", strconv.Quote(op.Question), "options", strings.Join(op.Options, ", ")}
		if len(op.Options) > 0 {
			return p.T("op.words.ask_options", args...)
		}
		return p.T("op.words.ask", args...)
	case OpRole:
		if op.Executor == "" {
			return p.T("op.words.role_select", "name", op.Name, "select", op.Select)
		}
		return p.T("op.words.role", "name", op.Name, "select", op.Select, "client", clientWords(p, op))
	case OpRoleAdd:
		if op.At > 0 {
			return p.T("op.words.role_add_at", "name", op.Name, "client", clientWords(p, op), "at", op.At)
		}
		return p.T("op.words.role_add", "name", op.Name, "client", clientWords(p, op))
	case OpRoleRemove:
		return p.T("op.words.role_remove", "name", op.Name, "at", op.At)
	case OpLanguage:
		if l, ok := language.Lookup(op.Language); ok {
			return p.T("op.words.language", "language", l.Label(), "code", l.Code)
		}
		return p.T("op.words.language_code", "code", op.Language)
	case OpUnlanguage:
		return p.T("op.words.unlanguage")
	case OpInputAdd, OpInputRm, OpInputSet, OpInputUnset:
		return inputWords(p, op)
	case OpAssistant:
		key := "op.words.assistant"
		if op.Effort != "" {
			key = "op.words.assistant_effort"
		}
		return p.T(key, "model", op.Model, "executor", op.Executor, "effort", op.Effort)
	}
	return op.Op
}

// clientWords describes the client of a role operation.
func clientWords(p *i18n.Printer, op Op) string {
	var parts []string
	if op.Model != "" {
		parts = append(parts, p.T("op.words.model", "model", op.Model))
	}
	if op.Effort != "" {
		parts = append(parts, p.T("op.words.effort", "effort", op.Effort))
	}
	return op.Executor + optList(", ", parts)
}

func optList(prefix string, parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return prefix + strings.Join(parts, ", ")
}

func nodeWords(p *i18n.Printer, op Op) string {
	var parts []string
	if len(op.Run) > 0 {
		parts = append(parts, p.T("op.words.running", "run", strings.Join(op.Run, " ")))
	}
	if len(op.Checks) > 0 {
		parts = append(parts, p.T("op.words.with_checks", "checks", strings.Join(op.Checks, ", ")))
	}
	if op.Executor != "" {
		parts = append(parts, p.T("op.words.through_executor", "executor", op.Executor))
	}
	if op.Workdir != "" {
		parts = append(parts, p.T("op.words.working_in", "workdir", op.Workdir))
	}
	if op.Role != "" {
		parts = append(parts, p.T("op.words.through_role", "role", op.Role))
	}
	if op.Segment != "" {
		parts = append(parts, p.T("op.words.segment", "segment", op.Segment))
	}
	if op.Items != "" {
		parts = append(parts, p.T("op.words.items", "items", op.Items))
	}
	if op.Parallel > 0 {
		parts = append(parts, p.T("op.words.parallel", "n", op.Parallel))
	}
	if op.Input != "" {
		parts = append(parts, p.T("op.words.input", "input", op.Input))
	}
	if len(op.Locks) > 0 {
		parts = append(parts, p.T("op.words.holding_locks", "locks", strings.Join(op.Locks, ", ")))
	}
	if op.Model != "" {
		parts = append(parts, p.T("op.words.model", "model", op.Model))
	}
	if op.Effort != "" {
		parts = append(parts, p.T("op.words.effort", "effort", op.Effort))
	}
	if len(op.Outcomes) > 0 {
		parts = append(parts, p.T("op.words.outcomes", "outcomes", strings.Join(op.Outcomes, ", ")))
	}
	if op.Timeout != "" {
		parts = append(parts, p.T("op.words.timeout", "timeout", op.Timeout))
	}
	var on []string
	for k, v := range op.On {
		on = append(on, k+" -> "+v)
	}
	sort.Strings(on)
	if len(on) > 0 {
		parts = append(parts, p.T("op.words.on", "on", strings.Join(on, ", ")))
	}
	return optList(", ", parts)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
