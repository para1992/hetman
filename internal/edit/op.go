package edit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Operation names.
const (
	OpNew        = "new"         // create the orders file with its first node
	OpAdd        = "add"         // add a node after or before another one
	OpRemove     = "remove"      // remove a node and connect around it
	OpRename     = "rename"      // rename a node everywhere it is referenced
	OpLink       = "link"        // set or change the transition of an outcome
	OpUnlink     = "unlink"      // remove the transition of an outcome
	OpSet        = "set"         // set a field of a node
	OpUnset      = "unset"       // remove a field of a node
	OpCheck      = "check"       // declare a check, or change one
	OpExecutor   = "executor"    // declare an executor, or change one
	OpGateAdd    = "gate-add"    // add a declared check to a gate
	OpGateRemove = "gate-remove" // remove a check from a gate
	OpAsk        = "ask"         // add a question to a gate or a human node
	OpAssistant  = "assistant"   // name the executor and model Scribe may ask
)

// Op is one operation on an orders file. Only the fields its operation
// takes may be set; Validate says which.
type Op struct {
	Op string `json:"op"`

	Node     string            `json:"node,omitempty"`     // the node acted on, or the name of a new node
	Type     string            `json:"type,omitempty"`     // new, add: script, gate, agent or human
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
var nodeFields = []string{"run", "timeout", "checks", "executor", "model", "effort", "tools", "attempts", "prompt", "schema", "outcomes", "on", "starters"}

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
	{OpLink, []string{"node", "outcome", "target"}, []string{"node", "outcome", "target"}, nil,
		"make an outcome of a node lead to a node, done or failed"},
	{OpUnlink, []string{"node", "outcome"}, []string{"node", "outcome"}, nil,
		"remove the transition of an outcome of a node"},
	{OpSet, []string{"node", "field", "value"}, []string{"node", "field", "value"}, nil,
		"set a field of a node: " + strings.Join(setFields, ", ")},
	{OpUnset, []string{"node", "field"}, []string{"node", "field"}, nil,
		"remove a field of a node: " + strings.Join(unsetFields, ", ")},
	{OpCheck, []string{"name"}, []string{"name"}, []string{"run", "timeout", "soft"},
		"declare a check, or change the command, timeout or soft flag of a declared one (soft only warns; required blocks the gate)"},
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
	setFields   = []string{"run", "timeout", "idle_timeout", "executor", "model", "effort", "tools", "attempts", "dialogue", "prompt", "schema"}
	unsetFields = []string{"timeout", "idle_timeout", "model", "effort", "tools", "attempts", "dialogue"}
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
	if (op.Op == OpSet || op.Op == OpUnset) && op.Field != "" {
		list := setFields
		if op.Op == OpUnset {
			list = unsetFields
		}
		if !contains(list, op.Field) {
			return fmt.Errorf("%s: unknown field %q (known: %s)", op.Op, op.Field, strings.Join(list, ", "))
		}
	}
	for _, s := range append(append(append([]string{op.Value, op.Question, op.Model, op.Effort}, op.Run...), op.Options...), op.Tools...) {
		if strings.ContainsAny(s, "\r\n") {
			return fmt.Errorf("%s: values must be one line", op.Op)
		}
	}
	return nil
}

// Words describes the operation in plain words.
func (op Op) Words() string {
	switch op.Op {
	case OpNew:
		return fmt.Sprintf("create a new flow%s whose first step is the %s node %s%s", optName(" named ", op.Flow), op.Type, op.Node, nodeWords(op))
	case OpAdd:
		where := "before " + op.Before
		if op.After != "" {
			where = "after " + op.After
			if op.Via != "" {
				where += " (on its outcome " + op.Via + ")"
			}
		}
		return fmt.Sprintf("add the %s node %s %s%s", op.Type, op.Node, where, nodeWords(op))
	case OpRemove:
		return fmt.Sprintf("remove the node %s and lead whatever led to it where its main outcome led", op.Node)
	case OpRename:
		return fmt.Sprintf("rename the node %s to %s everywhere it is referenced", op.Node, op.Name)
	case OpLink:
		return fmt.Sprintf("make the outcome %s of %s lead to %s", op.Outcome, op.Node, op.Target)
	case OpUnlink:
		return fmt.Sprintf("remove the transition of %s on the outcome %s", op.Node, op.Outcome)
	case OpSet:
		return fmt.Sprintf("set %s of %s to %s", op.Field, op.Node, op.Value)
	case OpUnset:
		return fmt.Sprintf("remove %s from %s", op.Field, op.Node)
	case OpCheck:
		var parts []string
		if len(op.Run) > 0 {
			parts = append(parts, "running "+strings.Join(op.Run, " "))
		}
		if op.Timeout != "" {
			parts = append(parts, "with the timeout "+op.Timeout)
		}
		if op.Soft != nil {
			parts = append(parts, map[bool]string{true: "soft (it only warns)", false: "required (it blocks the gate)"}[*op.Soft])
		}
		return fmt.Sprintf("declare or change the check %s%s", op.Name, optList(", ", parts))
	case OpExecutor:
		return fmt.Sprintf("declare or change the executor %s, running %s", op.Name, strings.Join(op.Run, " "))
	case OpGateAdd:
		at := "at the end"
		if op.At > 0 {
			at = fmt.Sprintf("at position %d", op.At)
		}
		return fmt.Sprintf("add the check %s to the gate %s %s", op.Check, op.Node, at)
	case OpGateRemove:
		return fmt.Sprintf("remove the check %s from the gate %s", op.Check, op.Node)
	case OpAsk:
		kind := op.Kind
		if kind == "" {
			kind = "yesno"
		}
		s := fmt.Sprintf("add the %s question %s to %s: %q", kind, op.ID, op.Node, op.Question)
		if len(op.Options) > 0 {
			s += " (options " + strings.Join(op.Options, ", ") + ")"
		}
		return s
	case OpAssistant:
		s := fmt.Sprintf("let Scribe ask the model %s through the executor %s", op.Model, op.Executor)
		if op.Effort != "" {
			s += " with the effort " + op.Effort
		}
		return s
	}
	return op.Op
}

func optName(prefix, s string) string {
	if s == "" {
		return ""
	}
	return prefix + s
}

func optList(prefix string, parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return prefix + strings.Join(parts, ", ")
}

func nodeWords(op Op) string {
	var parts []string
	if len(op.Run) > 0 {
		parts = append(parts, "running "+strings.Join(op.Run, " "))
	}
	if len(op.Checks) > 0 {
		parts = append(parts, "with the checks "+strings.Join(op.Checks, ", "))
	}
	if op.Executor != "" {
		parts = append(parts, "through the executor "+op.Executor)
	}
	if op.Model != "" {
		parts = append(parts, "model "+op.Model)
	}
	if op.Effort != "" {
		parts = append(parts, "effort "+op.Effort)
	}
	if len(op.Outcomes) > 0 {
		parts = append(parts, "outcomes "+strings.Join(op.Outcomes, ", "))
	}
	if op.Timeout != "" {
		parts = append(parts, "timeout "+op.Timeout)
	}
	var on []string
	for k, v := range op.On {
		on = append(on, k+" -> "+v)
	}
	sort.Strings(on)
	if len(on) > 0 {
		parts = append(parts, "on "+strings.Join(on, ", "))
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
