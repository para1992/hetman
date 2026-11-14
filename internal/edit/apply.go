package edit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"hetman/internal/ask"
	"hetman/internal/language"
	"hetman/internal/orders"
	"hetman/internal/schema"
)

// The order keys are written in, used to place a key that is added.
var (
	topOrder      = []string{"version", "flow", "start", "language", "scribe", "notify", "executors", "checks", "nodes"}
	nodeOrder     = []string{"type", "run", "executor", "prompt", "schema", "model", "effort", "tools", "checks", "ask", "show", "timeout", "idle_timeout", "max_attempts", "dialogue", "on"}
	checkOrder    = []string{"run", "timeout", "soft"}
	questionOrder = []string{"id", "question", "kind", "options"}
	scribeOrder   = []string{"executor", "model", "effort", "timeout"}
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ValidName reports whether s can name a node, check, executor or flow.
func ValidName(s string) bool { return namePattern.MatchString(s) && !orders.IsEnd(s) }

func str(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }

func num(n int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(n)}
}

func flowSeq(list []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle, Tag: "!!seq"}
	for _, s := range list {
		n.Content = append(n.Content, str(s))
	}
	return n
}

func mapping(flow bool, kv ...*yaml.Node) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: kv}
	if flow {
		n.Style = yaml.FlowStyle
	}
	return n
}

// lookup finds a key of a mapping: its index and value, or -1.
func lookup(m *yaml.Node, key string) (int, *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return -1, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i / 2, m.Content[i+1]
		}
	}
	return -1, nil
}

func keys(m *yaml.Node) []string {
	var out []string
	for i := 0; i+1 < len(m.Content); i += 2 {
		out = append(out, m.Content[i].Value)
	}
	return out
}

// place is the index a new key goes to: before the first key that comes
// after it in order, else at the end.
func place(m *yaml.Node, key string, order []string) int {
	rank := indexOf(order, key)
	if rank < 0 {
		return len(m.Content) / 2
	}
	for i, k := range keys(m) {
		if r := indexOf(order, k); r > rank {
			return i
		}
	}
	return len(m.Content) / 2
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

// editing is one operation being applied.
type editing struct {
	d  *Doc
	t  *tree
	op Op
}

// set gives a key of a mapping a value, replacing it or adding the key.
func (e *editing) set(m *yaml.Node, key string, v *yaml.Node, order []string) {
	if i, old := lookup(m, key); i >= 0 {
		if old.Kind == yaml.ScalarNode && v.Kind == yaml.ScalarNode && old.Value == v.Value && old.ShortTag() == v.ShortTag() {
			return
		}
		e.t.replace(m, i, v)
		return
	}
	e.t.insert(m, place(m, key, order), str(key), v)
}

// unset removes a key and reports whether it was there.
func (e *editing) unset(m *yaml.Node, key string) bool {
	i, _ := lookup(m, key)
	if i < 0 {
		return false
	}
	e.t.remove(m, i)
	return true
}

func (e *editing) root() *yaml.Node { return e.t.root() }

func (e *editing) nodes() (*yaml.Node, error) {
	_, n := lookup(e.root(), "nodes")
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the orders have no nodes mapping")
	}
	return n, nil
}

func (e *editing) ids() []string {
	if e.t == nil {
		return nil
	}
	n, err := e.nodes()
	if err != nil {
		return nil
	}
	return keys(n)
}

// node returns the mapping of a node.
func (e *editing) node(id string) (*yaml.Node, error) {
	nodes, err := e.nodes()
	if err != nil {
		return nil, err
	}
	_, n := lookup(nodes, id)
	if n == nil {
		return nil, fmt.Errorf("there is no node %q (the nodes are: %s)", id, strings.Join(keys(nodes), ", "))
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("node %q is not a mapping", id)
	}
	return n, nil
}

func scalarOf(m *yaml.Node, key string) string {
	_, v := lookup(m, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return ""
	}
	return v.Value
}

func onOf(n *yaml.Node) *yaml.Node {
	_, on := lookup(n, "on")
	if on == nil || on.Kind != yaml.MappingNode {
		return nil
	}
	return on
}

// Main returns the main outcome of a node's transitions: ok, else pass,
// else the first outcome that is not fail, timeout or blocked.
func Main(outcomes []string) string {
	for _, o := range []string{orders.OK, orders.Pass} {
		if contains(outcomes, o) {
			return o
		}
	}
	for _, o := range outcomes {
		if o != orders.Fail && o != orders.Timeout && o != orders.Blocked {
			return o
		}
	}
	return ""
}

// target checks a transition target: a node, done or failed.
func (e *editing) target(name string, extra ...string) error {
	if orders.IsEnd(name) || contains(e.ids(), name) || contains(extra, name) {
		return nil
	}
	return fmt.Errorf("there is no node %q to lead to (use a node, done or failed; the nodes are: %s)", name, strings.Join(e.ids(), ", "))
}

func newName(what, name string, taken []string) error {
	switch {
	case !namePattern.MatchString(name):
		return fmt.Errorf("%s %q: use letters, digits, '_', '-' and '.' only, starting with a letter or digit", what, name)
	case orders.IsEnd(name):
		return fmt.Errorf("%s %q is reserved for the end of a flow", what, name)
	case contains(taken, name):
		return fmt.Errorf("%s %q already exists", what, name)
	}
	return nil
}

func duration(what, s string) error {
	if d, err := time.ParseDuration(s); err != nil || d <= 0 {
		return fmt.Errorf("%s must be a positive duration such as 30s, 5m or 1h30m, not %q", what, s)
	}
	return nil
}

func (e *editing) apply() error {
	switch e.op.Op {
	case OpAdd:
		return e.add()
	case OpRemove:
		return e.removeNode()
	case OpRename:
		return e.rename()
	case OpLink:
		return e.link()
	case OpUnlink:
		return e.unlink()
	case OpSet:
		return e.setField()
	case OpUnset:
		return e.unsetField()
	case OpCheck:
		return e.declareCheck()
	case OpExecutor:
		return e.declareExecutor()
	case OpGateAdd:
		return e.gateAdd()
	case OpGateRemove:
		return e.gateRemove()
	case OpAsk:
		return e.addQuestion()
	case OpAssistant:
		return e.assistant()
	case OpLanguage:
		if _, ok := language.Lookup(e.op.Language); !ok {
			return fmt.Errorf("%s", language.Unknown(e.op.Language))
		}
		e.set(e.root(), "language", str(e.op.Language), topOrder)
		return nil
	case OpUnlanguage:
		e.unset(e.root(), "language")
		return nil
	}
	return fmt.Errorf("unknown operation %q", e.op.Op)
}

// flowOn reports whether transitions are written in braces in this file,
// judged by the first node that has them.
func (e *editing) flowOn() bool {
	if e.t == nil {
		return true
	}
	nodes, err := e.nodes()
	if err != nil {
		return true
	}
	for _, it := range items(nodes) {
		if on := onOf(it[1]); on != nil {
			return isFlow(on)
		}
	}
	return true
}

// build makes the mapping of a new node whose main outcome leads to next.
func (e *editing) build(op Op, next string, flowNode, flowOn bool) (*yaml.Node, error) {
	allowed := map[string][]string{
		orders.Script: {"run", "timeout", "on"},
		orders.Gate:   {"run", "timeout", "checks", "on"},
		orders.Agent:  {"executor", "model", "effort", "tools", "attempts", "prompt", "schema", "outcomes", "timeout", "starters", "on"},
		orders.Human:  {"outcomes", "on"},
	}
	list, ok := allowed[op.Type]
	if !ok {
		return nil, fmt.Errorf("unknown node type %q (script, gate, agent or human)", op.Type)
	}
	for _, f := range op.Fields() {
		if contains(nodeFields, f) && !contains(list, f) {
			return nil, fmt.Errorf("a %s node does not take %s", op.Type, f)
		}
	}
	if op.Timeout != "" {
		if err := duration("timeout", op.Timeout); err != nil {
			return nil, err
		}
	}
	n := mapping(flowNode, str("type"), str(op.Type))
	add := func(k string, v *yaml.Node) { n.Content = append(n.Content, str(k), v) }
	var outcomes []string
	switch op.Type {
	case orders.Script:
		if len(op.Run) == 0 {
			return nil, fmt.Errorf("a script node needs run: the program and its arguments")
		}
		add("run", flowSeq(op.Run))
		outcomes = []string{orders.OK, orders.Fail}
		if op.Timeout != "" {
			add("timeout", str(op.Timeout))
			outcomes = append(outcomes, orders.Timeout)
		}
	case orders.Gate:
		switch {
		case len(op.Run) > 0 && len(op.Checks) > 0:
			return nil, fmt.Errorf("a gate has either run or checks, not both")
		case len(op.Run) > 0:
			add("run", flowSeq(op.Run))
		case len(op.Checks) > 0:
			if op.Timeout != "" {
				return nil, fmt.Errorf("a gate with checks has no timeout of its own; give each check one")
			}
			for _, c := range op.Checks {
				if err := e.declared("checks", c); err != nil {
					return nil, err
				}
			}
			add("checks", flowSeq(op.Checks))
		default:
			return nil, fmt.Errorf("a gate needs checks or run; for a gate that only asks a person, add it with a check or a command and use ask, or add it with force and then ask")
		}
		outcomes = []string{orders.Pass, orders.Blocked}
		if op.Timeout != "" {
			add("timeout", str(op.Timeout))
			outcomes = append(outcomes, orders.Timeout)
		}
	case orders.Agent:
		var err error
		if outcomes, err = e.agent(op, add); err != nil {
			return nil, err
		}
	case orders.Human:
		if len(op.Outcomes) == 0 {
			return nil, fmt.Errorf("a human node needs outcomes: the choices the person has")
		}
		for _, o := range op.Outcomes {
			if !namePattern.MatchString(o) {
				return nil, fmt.Errorf("outcome %q: use letters, digits, '_', '-' and '.' only", o)
			}
		}
		outcomes = op.Outcomes
	}
	on := mapping(flowOn)
	main := Main(outcomes)
	for _, o := range outcomes {
		to := orders.Failed
		if o == main {
			to = next
		}
		if v, ok := op.On[o]; ok {
			to = v
		}
		on.Content = append(on.Content, str(o), str(to))
	}
	for o, to := range op.On {
		if !contains(outcomes, o) {
			if op.Type != orders.Script {
				return nil, fmt.Errorf("a %s node %s has no outcome %q (its outcomes: %s)", op.Type, op.Node, o, strings.Join(outcomes, ", "))
			}
			on.Content = append(on.Content, str(o), str(to))
		}
		if !namePattern.MatchString(o) {
			return nil, fmt.Errorf("outcome %q: use letters, digits, '_', '-' and '.' only", o)
		}
		if err := e.target(to, op.Node); err != nil {
			return nil, err
		}
	}
	add("on", on)
	return n, nil
}

// declared checks that a check or an executor is declared.
func (e *editing) declared(section, name string) error {
	if e.t == nil {
		return fmt.Errorf("%s %q is not declared", strings.TrimSuffix(section, "s"), name)
	}
	_, sec := lookup(e.root(), section)
	if i, _ := lookup(sec, name); i < 0 {
		what := strings.TrimSuffix(section, "s")
		var have []string
		if sec != nil && sec.Kind == yaml.MappingNode {
			have = keys(sec)
		}
		return fmt.Errorf("%s %q is not declared (declared: %s); declare it first with the %s operation", what, name, orNone(have), what)
	}
	return nil
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

// agent adds the keys of a new agent node and returns its outcomes. A
// prompt or schema file that does not exist is created as a starter when
// asked for.
func (e *editing) agent(op Op, add func(string, *yaml.Node)) ([]string, error) {
	if op.Executor == "" {
		return nil, fmt.Errorf("an agent node needs an executor (Hetman has no default)")
	}
	if e.t != nil {
		if err := e.declared("executors", op.Executor); err != nil {
			return nil, err
		}
	}
	promptRel, schemaRel := op.Prompt, op.Schema
	if promptRel == "" {
		promptRel = "prompts/" + op.Node + ".md"
	}
	if schemaRel == "" {
		schemaRel = "schemas/" + op.Node + ".json"
	}
	for _, o := range op.Outcomes {
		if !namePattern.MatchString(o) || o == orders.Fail || o == ask.Outcome {
			return nil, fmt.Errorf("outcome %q cannot be reported by a model: use letters, digits, '_', '-' and '.', and not fail or questions", o)
		}
	}
	var outcomes []string
	if data, ok := e.d.read(schemaRel); ok {
		s, err := schema.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("schema %s: %v", schemaRel, err)
		}
		outcomes, _ = s.Properties[orders.OutcomeField].Strings()
		if len(op.Outcomes) > 0 && strings.Join(op.Outcomes, ",") != strings.Join(outcomes, ",") {
			return nil, fmt.Errorf("the schema %s allows the outcomes %s; leave outcomes out or change the schema", schemaRel, orNone(outcomes))
		}
	} else {
		outcomes = op.Outcomes
		if len(outcomes) == 0 {
			outcomes = []string{orders.OK}
		}
		if op.Starters {
			e.d.create(schemaRel, StarterSchema(outcomes))
		}
	}
	if _, ok := e.d.read(promptRel); !ok && op.Starters {
		e.d.create(promptRel, StarterPrompt(op.Node, outcomes))
	}
	add("executor", str(op.Executor))
	add("prompt", str(promptRel))
	add("schema", str(schemaRel))
	if op.Model != "" {
		add("model", str(op.Model))
	}
	if op.Effort != "" {
		add("effort", str(op.Effort))
	}
	if len(op.Tools) > 0 {
		add("tools", flowSeq(op.Tools))
	}
	if op.Timeout != "" {
		add("timeout", str(op.Timeout))
	}
	if op.Attempts != 0 {
		if op.Attempts < 1 || op.Attempts > 100 {
			return nil, fmt.Errorf("attempts must be from 1 to 100")
		}
		add("max_attempts", num(op.Attempts))
	}
	return append(append([]string{}, outcomes...), orders.Fail), nil
}

// StarterSchema is a minimal schema for an agent step with these outcomes.
func StarterSchema(outcomes []string) []byte {
	enum, _ := json.Marshal(outcomes)
	return []byte(`{
  "type": "object",
  "properties": {
    "outcome": {"type": "string", "enum": ` + strings.ReplaceAll(string(enum), `","`, `", "`) + `},
    "summary": {"type": "string"}
  },
  "required": ["outcome"],
  "additionalProperties": false
}
`)
}

// StarterPrompt is a minimal prompt template for an agent step.
func StarterPrompt(node string, outcomes []string) []byte {
	return []byte(fmt.Sprintf(`Step %s. Replace this text with what the step must do.

{{rework}}

Reply with a single JSON object: "outcome" is one of %s, and "summary" says in a sentence what you did.
`, node, strings.Join(outcomes, ", ")))
}

// create starts a new orders file.
func (e *editing) create() error {
	op := e.op
	if err := newName("node", op.Node, nil); err != nil {
		return err
	}
	flow := op.Flow
	if flow == "" {
		flow = filepath.Base(e.d.Dir)
		if !namePattern.MatchString(flow) {
			flow = "flow"
		}
	}
	if !namePattern.MatchString(flow) {
		return fmt.Errorf("flow name %q: use letters, digits, '_', '-' and '.' only", flow)
	}
	n, err := e.build(op, orders.Done, false, true)
	if err != nil {
		return err
	}
	root := mapping(false,
		str("version"), num(0),
		str("flow"), str(flow),
		str("start"), str(op.Node),
		str("nodes"), mapping(false, str(op.Node), n))
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return err
	}
	e.d.text = bytes.Replace(b.Bytes(), []byte("\nnodes:"), []byte("\n\nnodes:"), 1)
	return nil
}

// nodeIndex is the index of a node in the nodes mapping.
func (e *editing) nodeIndex(id string) int {
	nodes, _ := e.nodes()
	i, _ := lookup(nodes, id)
	return i
}

func (e *editing) add() error {
	op := e.op
	nodes, err := e.nodes()
	if err != nil {
		return err
	}
	if err := newName("node", op.Node, keys(nodes)); err != nil {
		return err
	}
	var next string
	var rewire []func()
	var anchor *yaml.Node
	index := 0
	if op.After != "" {
		x, err := e.node(op.After)
		if err != nil {
			return err
		}
		anchor = x
		on := onOf(x)
		if on == nil {
			return fmt.Errorf("%s has no transitions to lead to the new node", op.After)
		}
		via := op.Via
		if via == "" {
			via = Main(keys(on))
		}
		i, to := lookup(on, via)
		if i < 0 {
			return fmt.Errorf("%s has no outcome %q (its outcomes: %s)", op.After, via, strings.Join(keys(on), ", "))
		}
		next = to.Value
		rewire = append(rewire, func() { e.t.replace(on, i, str(op.Node)) })
		index = e.nodeIndex(op.After) + 1
	} else {
		y, err := e.node(op.Before)
		if err != nil {
			return err
		}
		anchor = y
		next = op.Before
		index = e.nodeIndex(op.Before)
		for _, it := range items(nodes) {
			on := onOf(it[1])
			if on == nil {
				continue
			}
			main := Main(keys(on))
			if i, to := lookup(on, main); i >= 0 && to.Value == op.Before {
				rewire = append(rewire, func() { e.t.replace(on, i, str(op.Node)) })
			}
		}
		if i, start := lookup(e.root(), "start"); i >= 0 && start.Value == op.Before {
			rewire = append(rewire, func() { e.t.replace(e.root(), i, str(op.Node)) })
		}
		if len(rewire) == 0 {
			return fmt.Errorf("no node leads to %s on its main outcome and it is not the start, so a node before it would never run; add it after a node instead", op.Before)
		}
	}
	n, err := e.build(op, next, isFlow(anchor), e.flowOn())
	if err != nil {
		return err
	}
	e.t.insert(nodes, index, str(op.Node), n)
	for _, f := range rewire {
		f()
	}
	return nil
}

func (e *editing) removeNode() error {
	op := e.op
	nodes, err := e.nodes()
	if err != nil {
		return err
	}
	n, err := e.node(op.Node)
	if err != nil {
		return err
	}
	if len(nodes.Content) == 2 {
		return fmt.Errorf("%s is the only node; a flow needs at least one", op.Node)
	}
	on := onOf(n)
	next := ""
	if on != nil {
		_, to := lookup(on, Main(keys(on)))
		if to != nil {
			next = to.Value
		}
	}
	if next == "" || next == op.Node {
		return fmt.Errorf("%s has no main outcome leading elsewhere, so what leads to it cannot be connected around it", op.Node)
	}
	for _, it := range items(nodes) {
		if it[0].Value == op.Node {
			continue
		}
		if zon := onOf(it[1]); zon != nil {
			for i, it2 := range items(zon) {
				if it2[1].Value == op.Node {
					e.t.replace(zon, i, str(next))
				}
			}
		}
	}
	if i, start := lookup(e.root(), "start"); i >= 0 && start.Value == op.Node {
		if orders.IsEnd(next) {
			return fmt.Errorf("%s is the start and its main outcome ends the flow; there is nothing to start with instead", op.Node)
		}
		e.t.replace(e.root(), i, str(next))
	}
	e.t.remove(nodes, e.nodeIndex(op.Node))
	return nil
}

// rekey renames the key at index i of a mapping.
func (t *tree) rekey(m *yaml.Node, i int, name string) {
	old := m.Content[2*i]
	k := str(name)
	m.Content[2*i] = k
	t.was[k] = old
	if t.failed != nil {
		return
	}
	end, err := t.scalarEnd(old)
	if err != nil {
		t.fail(err)
		return
	}
	text, err := inline(k, t.inFlow[old])
	if err != nil {
		t.fail(err)
		return
	}
	t.edit(t.start(old), end, text)
}

var placeholder = regexp.MustCompile(`\{\{ *([A-Za-z0-9_][A-Za-z0-9_./-]*) *\}\}`)

func (e *editing) rename() error {
	op := e.op
	nodes, err := e.nodes()
	if err != nil {
		return err
	}
	if _, err := e.node(op.Node); err != nil {
		return err
	}
	if op.Name == op.Node {
		return fmt.Errorf("%s already has that name", op.Node)
	}
	if err := newName("node", op.Name, keys(nodes)); err != nil {
		return err
	}
	ids := keys(nodes)
	// Prompt templates first, while the node ids are the old ones.
	for _, it := range items(nodes) {
		if scalarOf(it[1], "type") != orders.Agent {
			continue
		}
		rel := scalarOf(it[1], "prompt")
		if rel == "" {
			continue
		}
		data, ok := e.d.read(rel)
		if !ok {
			continue
		}
		changed := renamePlaceholders(data, ids, op.Node, op.Name)
		if !bytes.Equal(changed, data) {
			e.d.change(e.d.abs(rel), data, changed)
		}
	}
	for _, it := range items(nodes) {
		if on := onOf(it[1]); on != nil {
			for i, it2 := range items(on) {
				if it2[1].Value == op.Node {
					e.t.replace(on, i, str(op.Name))
				}
			}
		}
	}
	if i, start := lookup(e.root(), "start"); i >= 0 && start.Value == op.Node {
		e.t.replace(e.root(), i, str(op.Name))
	}
	e.t.rekey(nodes, e.nodeIndex(op.Node), op.Name)
	return nil
}

// renamePlaceholders rewrites the placeholders of a prompt template that
// name a node: {{output.NODE...}}, {{artifact.NODE...}}, {{answers.NODE...}}.
func renamePlaceholders(text []byte, ids []string, from, to string) []byte {
	return placeholder.ReplaceAllFunc(text, func(m []byte) []byte {
		sub := placeholder.FindSubmatchIndex(m)
		name := string(m[sub[2]:sub[3]])
		head, rest, ok := strings.Cut(name, ".")
		if !ok || (head != orders.RefOutput && head != orders.RefArtifact && head != orders.RefAnswers) {
			return m
		}
		best := ""
		for _, id := range ids {
			if (rest == id || strings.HasPrefix(rest, id+".")) && len(id) > len(best) {
				best = id
			}
		}
		if best != from {
			return m
		}
		renamed := head + "." + to + rest[len(from):]
		return append(append(append([]byte{}, m[:sub[2]]...), renamed...), m[sub[3]:]...)
	})
}

func (e *editing) link() error {
	op := e.op
	n, err := e.node(op.Node)
	if err != nil {
		return err
	}
	if !namePattern.MatchString(op.Outcome) {
		return fmt.Errorf("outcome %q: use letters, digits, '_', '-' and '.' only", op.Outcome)
	}
	if err := e.target(op.Target); err != nil {
		return err
	}
	on := onOf(n)
	if on == nil {
		e.set(n, "on", mapping(e.flowOn(), str(op.Outcome), str(op.Target)), nodeOrder)
		return nil
	}
	if _, to := lookup(on, op.Outcome); to != nil && to.Value == op.Target {
		return fmt.Errorf("%s already leads to %s on %s", op.Node, op.Target, op.Outcome)
	}
	e.set(on, op.Outcome, str(op.Target), nil)
	return nil
}

func (e *editing) unlink() error {
	op := e.op
	n, err := e.node(op.Node)
	if err != nil {
		return err
	}
	on := onOf(n)
	if i, _ := lookup(on, op.Outcome); i < 0 {
		have := []string{}
		if on != nil {
			have = keys(on)
		}
		return fmt.Errorf("%s has no transition on %q (it has: %s)", op.Node, op.Outcome, orNone(have))
	}
	e.unset(on, op.Outcome)
	return nil
}

// fieldKey is the key a field of set and unset is written as.
func fieldKey(field string) string {
	if field == "attempts" {
		return "max_attempts"
	}
	return field
}

func (e *editing) setField() error {
	op := e.op
	n, err := e.node(op.Node)
	if err != nil {
		return err
	}
	typ := scalarOf(n, "type")
	agentOnly := map[string]bool{"idle_timeout": true, "executor": true, "model": true, "effort": true, "tools": true,
		"attempts": true, "dialogue": true, "prompt": true, "schema": true}
	if agentOnly[op.Field] && typ != orders.Agent {
		return fmt.Errorf("%s is a %s node; %s is set only on an agent node", op.Node, typ, op.Field)
	}
	var v *yaml.Node
	switch op.Field {
	case "run":
		if typ != orders.Script && typ != orders.Gate {
			return fmt.Errorf("%s is a %s node; run is set only on a script node or a gate", op.Node, typ)
		}
		if i, _ := lookup(n, "checks"); i >= 0 {
			return fmt.Errorf("%s runs checks; a gate has either run or checks", op.Node)
		}
		argv, err := SplitCommand(op.Value)
		if err != nil {
			return err
		}
		if len(argv) == 0 {
			return fmt.Errorf("run needs a program")
		}
		v = flowSeq(argv)
	case "timeout", "idle_timeout":
		if err := duration(op.Field, op.Value); err != nil {
			return err
		}
		if op.Field == "timeout" && typ == orders.Human {
			return fmt.Errorf("%s is a human node; it waits for a person and has no timeout", op.Node)
		}
		v = str(op.Value)
	case "attempts", "dialogue":
		k, err := strconv.Atoi(op.Value)
		if err != nil || k < 1 || k > 100 {
			return fmt.Errorf("%s must be a whole number from 1 to 100, not %q", op.Field, op.Value)
		}
		v = num(k)
	case "tools":
		v = flowSeq(splitList(op.Value))
	case "executor":
		if err := e.declared("executors", op.Value); err != nil {
			return err
		}
		v = str(op.Value)
	default:
		v = str(op.Value)
	}
	e.set(n, fieldKey(op.Field), v, nodeOrder)
	// A step with a timeout can end with the outcome timeout, which needs
	// a transition.
	if op.Field == "timeout" && (typ == orders.Script || typ == orders.Gate && hasKey(n, "run")) {
		if on := onOf(n); on != nil {
			if i, _ := lookup(on, orders.Timeout); i < 0 {
				e.t.insert(on, len(on.Content)/2, str(orders.Timeout), str(orders.Failed))
			}
		}
	}
	return nil
}

func hasKey(m *yaml.Node, key string) bool {
	i, _ := lookup(m, key)
	return i >= 0
}

func (e *editing) unsetField() error {
	op := e.op
	n, err := e.node(op.Node)
	if err != nil {
		return err
	}
	if !e.unset(n, fieldKey(op.Field)) {
		return fmt.Errorf("%s has no %s", op.Node, fieldKey(op.Field))
	}
	// Without a timeout the outcome timeout cannot happen any more.
	if op.Field == "timeout" {
		if on := onOf(n); on != nil && len(on.Content) > 2 {
			e.unset(on, orders.Timeout)
		}
	}
	return nil
}

// section returns a top-level mapping, adding it when it is missing. The
// first entry is given, since an empty block mapping has no text.
func (e *editing) section(name, key string, value *yaml.Node) (sec *yaml.Node, added bool) {
	_, sec = lookup(e.root(), name)
	if sec != nil && sec.Kind == yaml.MappingNode {
		return sec, false
	}
	sec = mapping(false, str(key), value)
	e.set(e.root(), name, sec, topOrder)
	return sec, true
}

func (e *editing) declareCheck() error {
	op := e.op
	if !namePattern.MatchString(op.Name) {
		return fmt.Errorf("check name %q: use letters, digits, '_', '-' and '.' only", op.Name)
	}
	if op.Timeout != "" {
		if err := duration("timeout", op.Timeout); err != nil {
			return err
		}
	}
	_, sec := lookup(e.root(), "checks")
	_, c := lookup(sec, op.Name)
	if c == nil {
		if len(op.Run) == 0 {
			return fmt.Errorf("check %q is not declared yet; give the command it runs", op.Name)
		}
		v := mapping(true, str("run"), flowSeq(op.Run))
		if op.Timeout != "" {
			v.Content = append(v.Content, str("timeout"), str(op.Timeout))
		}
		if op.Soft != nil && *op.Soft {
			v.Content = append(v.Content, str("soft"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		}
		if _, added := e.section("checks", op.Name, v); !added {
			e.t.insert(sec, len(sec.Content)/2, str(op.Name), v)
		}
		return nil
	}
	if c.Kind != yaml.MappingNode {
		return fmt.Errorf("check %q is not a mapping", op.Name)
	}
	if len(op.Run) > 0 {
		e.set(c, "run", flowSeq(op.Run), checkOrder)
	}
	if op.Timeout != "" {
		e.set(c, "timeout", str(op.Timeout), checkOrder)
	}
	if op.Soft != nil {
		if *op.Soft {
			e.set(c, "soft", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}, checkOrder)
		} else {
			e.unset(c, "soft")
		}
	}
	return nil
}

func (e *editing) declareExecutor() error {
	op := e.op
	if !namePattern.MatchString(op.Name) {
		return fmt.Errorf("executor name %q: use letters, digits, '_', '-' and '.' only", op.Name)
	}
	_, sec := lookup(e.root(), "executors")
	if _, x := lookup(sec, op.Name); x != nil {
		if x.Kind != yaml.MappingNode {
			return fmt.Errorf("executor %q is not a mapping", op.Name)
		}
		e.set(x, "run", flowSeq(op.Run), nil)
		return nil
	}
	v := mapping(true, str("run"), flowSeq(op.Run))
	if _, added := e.section("executors", op.Name, v); !added {
		e.t.insert(sec, len(sec.Content)/2, str(op.Name), v)
	}
	return nil
}

func (e *editing) gate(id string) (*yaml.Node, error) {
	n, err := e.node(id)
	if err != nil {
		return nil, err
	}
	if typ := scalarOf(n, "type"); typ != orders.Gate {
		return nil, fmt.Errorf("%s is a %s node, not a gate", id, typ)
	}
	return n, nil
}

func (e *editing) gateAdd() error {
	op := e.op
	n, err := e.gate(op.Node)
	if err != nil {
		return err
	}
	if err := e.declared("checks", op.Check); err != nil {
		return err
	}
	_, list := lookup(n, "checks")
	if list == nil {
		if hasKey(n, "run") {
			return fmt.Errorf("%s runs a single command; a gate has either run or checks", op.Node)
		}
		e.set(n, "checks", flowSeq([]string{op.Check}), nodeOrder)
		return nil
	}
	if list.Kind != yaml.SequenceNode {
		return fmt.Errorf("the checks of %s are not a list", op.Node)
	}
	for _, c := range list.Content {
		if c.Value == op.Check {
			return fmt.Errorf("%s already runs the check %s", op.Node, op.Check)
		}
	}
	at := len(list.Content)
	if op.At > 0 && op.At-1 < at {
		at = op.At - 1
	}
	if op.At < 0 {
		return fmt.Errorf("at counts from 1")
	}
	e.t.insert(list, at, nil, str(op.Check))
	return nil
}

func (e *editing) gateRemove() error {
	op := e.op
	n, err := e.gate(op.Node)
	if err != nil {
		return err
	}
	_, list := lookup(n, "checks")
	if list != nil && list.Kind == yaml.SequenceNode {
		for i, c := range list.Content {
			if c.Value == op.Check {
				if len(list.Content) == 1 {
					e.unset(n, "checks")
				} else {
					e.t.remove(list, i)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("%s does not run the check %s", op.Node, op.Check)
}

func (e *editing) addQuestion() error {
	op := e.op
	n, err := e.node(op.Node)
	if err != nil {
		return err
	}
	typ := scalarOf(n, "type")
	kind := op.Kind
	if kind == "" {
		kind = ask.YesNo
	}
	switch {
	case typ != orders.Gate && typ != orders.Human:
		return fmt.Errorf("%s is a %s node; only a gate or a human node asks a person", op.Node, typ)
	case !contains(ask.Kinds, kind):
		return fmt.Errorf("kind must be one of %s, not %q", strings.Join(ask.Kinds, ", "), kind)
	case typ == orders.Gate && kind != ask.YesNo:
		return fmt.Errorf("a gate asks yes/no questions only; use a human node for a %s question", kind)
	case !ask.IDPattern.MatchString(op.ID):
		return fmt.Errorf("question id %q: use letters, digits, '_', '-' and '.' only", op.ID)
	case strings.TrimSpace(op.Question) == "":
		return fmt.Errorf("the question must not be empty")
	case kind == ask.Choice:
		if msg := ask.CheckOptions(op.Options); msg != "" {
			return fmt.Errorf("%s", msg)
		}
	case len(op.Options) > 0:
		return fmt.Errorf("options are given only with the kind choice")
	}
	q := mapping(false, str("id"), str(op.ID), str("question"), str(strings.TrimSpace(op.Question)))
	if kind != ask.YesNo {
		q.Content = append(q.Content, str("kind"), str(kind))
	}
	if len(op.Options) > 0 {
		q.Content = append(q.Content, str("options"), flowSeq(op.Options))
	}
	_, list := lookup(n, "ask")
	if list == nil {
		if isFlow(n) {
			q.Style = yaml.FlowStyle
			e.set(n, "ask", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle, Content: []*yaml.Node{q}}, nodeOrder)
			return nil
		}
		e.set(n, "ask", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{q}}, nodeOrder)
		return nil
	}
	if list.Kind != yaml.SequenceNode {
		return fmt.Errorf("the questions of %s are not a list", op.Node)
	}
	for _, it := range list.Content {
		if scalarOf(it, "id") == op.ID {
			return fmt.Errorf("%s already asks a question with the id %s", op.Node, op.ID)
		}
	}
	if isFlow(list) {
		q.Style = yaml.FlowStyle
	}
	e.t.insert(list, len(list.Content), nil, q)
	return nil
}

func (e *editing) assistant() error {
	op := e.op
	if err := e.declared("executors", op.Executor); err != nil {
		return err
	}
	_, old := lookup(e.root(), "scribe")
	if old != nil && old.Kind == yaml.MappingNode && !isFlow(old) {
		e.set(old, "executor", str(op.Executor), scribeOrder)
		e.set(old, "model", str(op.Model), scribeOrder)
		if op.Effort != "" {
			e.set(old, "effort", str(op.Effort), scribeOrder)
		} else {
			e.unset(old, "effort")
		}
		return nil
	}
	v := mapping(true, str("executor"), str(op.Executor), str("model"), str(op.Model))
	if op.Effort != "" {
		v.Content = append(v.Content, str("effort"), str(op.Effort))
	}
	if old != nil {
		if _, t := lookup(old, "timeout"); t != nil {
			v.Content = append(v.Content, str("timeout"), str(t.Value))
		}
	}
	e.set(e.root(), "scribe", v, topOrder)
	return nil
}

// splitList splits a list written with commas or spaces.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
}

// SplitCommand splits a command line into the program and its arguments
// at spaces. Single or double quotes keep spaces inside an argument; a
// backslash inside double quotes keeps the next character as it is.
func SplitCommand(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in := false
	var quote rune
	for i := 0; i < len(s); i++ {
		c := rune(s[i])
		switch {
		case quote != 0:
			switch {
			case c == quote:
				quote = 0
			case c == '\\' && quote == '"' && i+1 < len(s):
				i++
				cur.WriteByte(s[i])
			default:
				cur.WriteByte(s[i])
			}
		case c == '"' || c == '\'':
			quote, in = c, true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(s[i])
			in = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote in %q", s)
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}
