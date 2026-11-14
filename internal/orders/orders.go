// Package orders reads and checks a flow config ("orders").
//
// The config is parsed into a YAML node tree and walked by hand so that every
// problem is reported with the line and column it comes from.
package orders

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"hetman/internal/ask"
	"hetman/internal/language"
	"hetman/internal/prompt"
	"hetman/internal/schema"
)

// DefaultFile is the config file name used when none is given.
const DefaultFile = "orders.yaml"

// Node types.
const (
	Script = "script"
	Gate   = "gate"
	Agent  = "agent"
	Human  = "human"
)

// DefaultNotifyTimeout limits the notification hook when it sets no timeout.
const DefaultNotifyTimeout = 30 * time.Second

// DefaultMaxAttempts is the attempt budget of an agent node that does not
// set max_attempts.
const DefaultMaxAttempts = 3

// Reserved transition targets that end a campaign.
const (
	Done   = "done"
	Failed = "failed"
)

// Outcomes produced by the engine itself.
const (
	OK      = "ok"
	Fail    = "fail"
	Timeout = "timeout"
	Pass    = "pass"
	Blocked = "blocked"
)

// Orders is a checked flow config.
type Orders struct {
	Path      string // absolute path of the config file
	Dir       string // folder holding the config; relative paths resolve here
	Version   int
	Flow      string
	Start     string
	Checks    map[string]*Check
	Executors map[string]*Executor
	Nodes     map[string]*Node
	Order     []string // node ids in file order
	Notify    *Notify  // the notification hook, or nil
	Scribe    *Scribe  // the model the setup assistant may ask, or nil
	Language  string   // code of the language text for a person is written in; empty for none

	programs []program // the program of every run list, in file order
}

// program is the first item of a run list and where it is written.
type program struct {
	name      string
	line, col int
}

// Check is a gate check declared once at the top level.
type Check struct {
	Name    string
	Run     []string
	Timeout time.Duration
	Soft    bool
}

// Notify is the command run whenever a campaign starts waiting for a person.
type Notify struct {
	Run     []string
	Timeout time.Duration
}

// Scribe names the executor and model the setup assistant may ask to map a
// sentence to one operation. There is no default: without it the assistant
// works without a model.
type Scribe struct {
	Executor string
	Model    string
	Effort   string
	Timeout  time.Duration // zero for the assistant's own limit
}

// Executor is an adapter program declared once at the top level.
type Executor struct {
	Name string
	Run  []string
}

// Node is one step of the flow.
type Node struct {
	ID      string
	Type    string
	Run     []string
	Timeout time.Duration
	Checks  []string
	On      map[string]string

	// Gates and human nodes.
	Ask  []ask.Question // questions for a person
	Show []string       // files the person looks at, relative to the orders folder
	// Human nodes: the outcomes a person chooses from, in file order.
	Decisions []string

	// Agent nodes only.
	Executor    string
	Model       string
	Effort      string
	Tools       []string
	Prompt      string           // absolute path of the template file
	Template    *prompt.Template // the parsed template
	SchemaPath  string           // absolute path of the schema file
	Schema      *schema.Schema   // the parsed schema
	SchemaJSON  []byte           // the schema file, compacted, as sent to the adapter
	IdleTimeout time.Duration
	MaxAttempts int
	Dialogue    int // how many rounds of questions the step may ask a person; 0 for none
}

// Outcomes lists the outcomes the engine can produce for the node without a
// result file. Each of them must have a transition. For an agent node these
// are the values its schema allows for "outcome", and fail.
func (n *Node) Outcomes() []string {
	var out []string
	switch n.Type {
	case Gate:
		out = []string{Pass, Blocked}
	case Human:
		return n.Decisions
	case Agent:
		out = n.ModelOutcomes()
		if !contains(out, Fail) {
			out = append(out, Fail)
		}
		return out
	default:
		out = []string{OK, Fail}
	}
	if len(n.Run) > 0 && n.Timeout > 0 {
		out = append(out, Timeout)
	}
	return out
}

// IsEnd reports whether a transition target ends the campaign.
func IsEnd(target string) bool { return target == Done || target == Failed }

// Problem is one config error with its position.
type Problem struct {
	File string
	Line int
	Col  int
	Msg  string
}

// Program returns the path a run list's program is started with: a program
// written with a path separator is relative to the orders folder; a bare name
// is returned as is and looked up on the system path when started.
func (o *Orders) Program(name string) string {
	if filepath.IsAbs(name) || !strings.ContainsAny(name, `/\`) {
		return name
	}
	return filepath.Join(o.Dir, filepath.FromSlash(name))
}

// CheckPrograms looks for the program of every run list on this computer.
// A program written with a path must exist relative to the orders folder and
// be an executable file; otherwise it is an error. A bare name that is not
// on the system path is only a warning, because the orders may be checked on
// one computer and run on another, or the program may be installed by an
// earlier step.
func (o *Orders) CheckPrograms() (errs, warnings Problems) {
	for _, pr := range o.programs {
		at := Problem{File: o.Path, Line: pr.line, Col: pr.col}
		if !strings.ContainsAny(pr.name, `/\`) {
			if _, err := exec.LookPath(pr.name); err != nil {
				at.Msg = fmt.Sprintf("warning: program %q is not on the system path of this computer", pr.name)
				warnings = append(warnings, at)
			}
			continue
		}
		path := o.Program(pr.name)
		// LookPath checks what starting the program needs: a regular file
		// with the executable bit, or on Windows one with a known extension.
		if _, err := exec.LookPath(path); err == nil {
			continue
		}
		fi, err := os.Stat(path)
		switch {
		case err != nil:
			at.Msg = fmt.Sprintf("program %q does not exist (looked for %s)", pr.name, path)
		case !fi.Mode().IsRegular():
			at.Msg = fmt.Sprintf("program %q is not a regular file", pr.name)
		default:
			at.Msg = fmt.Sprintf("program %q is not executable", pr.name)
		}
		errs = append(errs, at)
	}
	return errs, warnings
}

func (p Problem) String() string {
	if p.Line == 0 {
		return fmt.Sprintf("%s: %s", p.File, p.Msg)
	}
	return fmt.Sprintf("%s:%d:%d: %s", p.File, p.Line, p.Col, p.Msg)
}

// Problems is the error returned for a rejected config.
type Problems []Problem

func (ps Problems) Error() string {
	lines := make([]string, len(ps))
	for i, p := range ps {
		lines[i] = p.String()
	}
	return strings.Join(lines, "\n")
}

// Load reads and checks the config at path.
func Load(path string) (*Orders, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return Parse(abs, data)
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

var yamlLine = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

// Parse checks config data. path is used for positions and as the base of
// relative paths.
func Parse(path string, data []byte) (*Orders, error) {
	return ParseOverlay(path, data, nil)
}

// ParseOverlay is Parse with files that are not written yet: a prompt
// template or schema whose absolute path is a key of files is read from
// there instead of the disk.
func ParseOverlay(path string, data []byte, files map[string][]byte) (*Orders, error) {
	p := &parser{file: path, overlay: files}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		msg := err.Error()
		if m := yamlLine.FindStringSubmatch(msg); m != nil {
			line, _ := strconv.Atoi(m[1])
			return nil, Problems{{File: path, Line: line, Col: 1, Msg: m[2]}}
		}
		return nil, Problems{{File: path, Msg: msg}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, Problems{{File: path, Msg: "empty config"}}
	}
	o := &Orders{
		Path:      path,
		Dir:       filepath.Dir(path),
		Checks:    map[string]*Check{},
		Executors: map[string]*Executor{},
		Nodes:     map[string]*Node{},
	}
	p.top(resolve(doc.Content[0]), o)
	if len(p.problems) > 0 {
		sort.SliceStable(p.problems, func(i, j int) bool {
			a, b := p.problems[i], p.problems[j]
			if a.File != b.File {
				return a.File == path // problems in the orders come first
			}
			return a.Line < b.Line
		})
		return nil, p.problems
	}
	sort.SliceStable(p.programs, func(i, j int) bool { return p.programs[i].line < p.programs[j].line })
	o.programs = p.programs
	return o, nil
}

type parser struct {
	file     string
	problems Problems
	programs []program
	overlay  map[string][]byte // files read instead of the disk
}

func (p *parser) errorf(n *yaml.Node, format string, args ...any) {
	p.problems = append(p.problems, Problem{File: p.file, Line: n.Line, Col: n.Column, Msg: fmt.Sprintf(format, args...)})
}

// errorIn reports a problem in another file than the orders, such as a
// prompt template or a schema.
func (p *parser) errorIn(file string, line, col int, format string, args ...any) {
	p.problems = append(p.problems, Problem{File: file, Line: line, Col: col, Msg: fmt.Sprintf(format, args...)})
}

func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

type entry struct {
	key   string
	keyN  *yaml.Node
	value *yaml.Node
}

// mapping returns the entries of a mapping node, reporting non-mappings,
// non-scalar keys, duplicate keys and keys outside allowed.
func (p *parser) mapping(n *yaml.Node, what string, allowed ...string) ([]entry, bool) {
	if n.Kind != yaml.MappingNode {
		p.errorf(n, "%s must be a mapping", what)
		return nil, false
	}
	seen := map[string]bool{}
	var out []entry
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := resolve(n.Content[i]), resolve(n.Content[i+1])
		if k.Kind != yaml.ScalarNode {
			p.errorf(k, "%s: keys must be plain names", what)
			continue
		}
		if seen[k.Value] {
			p.errorf(k, "%s: duplicate key %q", what, k.Value)
			continue
		}
		seen[k.Value] = true
		if allowed != nil && !contains(allowed, k.Value) {
			p.errorf(k, "%s: unknown key %q (allowed: %s)", what, k.Value, strings.Join(allowed, ", "))
			continue
		}
		out = append(out, entry{key: k.Value, keyN: k, value: v})
	}
	return out, true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (p *parser) scalar(n *yaml.Node, what string) (string, bool) {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		p.errorf(n, "%s must be a single value", what)
		return "", false
	}
	return n.Value, true
}

func (p *parser) name(n *yaml.Node, what string) (string, bool) {
	s, ok := p.scalar(n, what)
	if !ok {
		return "", false
	}
	if !namePattern.MatchString(s) {
		p.errorf(n, "%s %q: use letters, digits, '_', '-' and '.' only", what, s)
		return "", false
	}
	return s, true
}

func (p *parser) argv(n *yaml.Node, what string) []string {
	if n.Kind != yaml.SequenceNode {
		p.errorf(n, "%s must be a list: the program followed by its arguments, for example [./build.sh, --fast]", what)
		return nil
	}
	if len(n.Content) == 0 {
		p.errorf(n, "%s must not be empty", what)
		return nil
	}
	var out []string
	for _, item := range n.Content {
		s, ok := p.scalar(resolve(item), what+" item")
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	if out[0] == "" {
		p.errorf(n, "%s: program must not be empty", what)
		return nil
	}
	first := resolve(n.Content[0])
	p.programs = append(p.programs, program{name: out[0], line: first.Line, col: first.Column})
	return out
}

func (p *parser) duration(n *yaml.Node, what string) time.Duration {
	s, ok := p.scalar(n, what)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 || n.Tag != "!!str" {
		p.errorf(n, "%s must be a positive duration such as 30s, 5m or 1h30m, got %q", what, s)
		return 0
	}
	return d
}

func (p *parser) top(root *yaml.Node, o *Orders) {
	entries, ok := p.mapping(root, "orders", "version", "flow", "start", "language", "scribe", "checks", "executors", "notify", "nodes")
	if !ok {
		return
	}
	var startN, nodesN, scribeN *yaml.Node
	hasVersion := false
	for _, e := range entries {
		switch e.key {
		case "version":
			hasVersion = true
			if e.value.Kind != yaml.ScalarNode || e.value.Tag != "!!int" || e.value.Value != "0" {
				p.errorf(e.value, "unsupported version %q: this hetman reads version 0", e.value.Value)
			}
		case "flow":
			o.Flow, _ = p.name(e.value, "flow")
		case "language":
			if code, ok := p.scalar(e.value, "language"); ok {
				if _, known := language.Lookup(code); known {
					o.Language = code
				} else {
					p.errorf(e.value, "language: %s", language.Unknown(code))
				}
			}
		case "start":
			startN = e.value
			o.Start, _ = p.name(e.value, "start")
		case "checks":
			p.checks(e.value, o)
		case "executors":
			p.executors(e.value, o)
		case "notify":
			p.notify(e.value, o)
		case "scribe":
			scribeN = e.value
		case "nodes":
			nodesN = e.value
		}
	}
	// Nodes are read after checks and executors so that nodes can refer to
	// them wherever they are declared in the file.
	if nodesN != nil {
		p.nodes(nodesN, o)
	}
	if scribeN != nil {
		p.scribe(scribeN, o)
	}
	if !hasVersion {
		p.errorf(root, "missing key \"version\" (use version: 0)")
	}
	if o.Flow == "" {
		o.Flow = strings.TrimSuffix(filepath.Base(o.Path), filepath.Ext(o.Path))
	}
	if nodesN == nil {
		p.errorf(root, "missing key \"nodes\"")
	} else if nodesN.Kind == yaml.MappingNode && len(nodesN.Content) == 0 {
		p.errorf(nodesN, "nodes must not be empty")
	}
	switch {
	case startN == nil:
		p.errorf(root, "missing key \"start\": the node the flow begins with")
	case o.Start != "" && nodesN != nil && o.Nodes[o.Start] == nil && !p.nodeDeclared(nodesN, o.Start):
		p.errorf(startN, "start node %q does not exist", o.Start)
	}
}

// nodeDeclared reports whether id is a key under nodes even if the node
// itself was rejected, so one mistake is not reported twice.
func (p *parser) nodeDeclared(nodes *yaml.Node, id string) bool {
	if nodes.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(nodes.Content); i += 2 {
		if resolve(nodes.Content[i]).Value == id {
			return true
		}
	}
	return false
}

func (p *parser) checks(n *yaml.Node, o *Orders) {
	entries, ok := p.mapping(n, "checks")
	if !ok {
		return
	}
	for _, e := range entries {
		name, ok := p.name(e.keyN, "check name")
		if !ok {
			continue
		}
		c := &Check{Name: name}
		what := fmt.Sprintf("check %q", name)
		fields, ok := p.mapping(e.value, what, "run", "timeout", "soft")
		if !ok {
			continue
		}
		hasRun := false
		for _, f := range fields {
			switch f.key {
			case "run":
				hasRun = true
				c.Run = p.argv(f.value, what+": run")
			case "timeout":
				c.Timeout = p.duration(f.value, what+": timeout")
			case "soft":
				if f.value.Kind != yaml.ScalarNode || f.value.Tag != "!!bool" {
					p.errorf(f.value, "%s: soft must be true or false", what)
				} else {
					c.Soft = f.value.Value == "true"
				}
			}
		}
		if !hasRun {
			p.errorf(e.keyN, "%s: missing key \"run\"", what)
		}
		o.Checks[name] = c
	}
}

func (p *parser) nodes(n *yaml.Node, o *Orders) {
	entries, ok := p.mapping(n, "nodes")
	if !ok {
		return
	}
	type pending struct {
		node *Node
		onN  *yaml.Node
		keyN *yaml.Node
	}
	var all []pending
	for _, e := range entries {
		id, ok := p.name(e.keyN, "node id")
		if !ok {
			continue
		}
		if IsEnd(id) {
			p.errorf(e.keyN, "node id %q is reserved for the end of a flow", id)
			continue
		}
		node, onN := p.node(id, e.keyN, e.value, o)
		if node == nil {
			continue
		}
		o.Nodes[id] = node
		o.Order = append(o.Order, id)
		all = append(all, pending{node, onN, e.keyN})
	}
	// Transitions are checked once every node id is known, and prompt
	// templates once every node and its schema are.
	for _, pn := range all {
		p.transitions(pn.node, pn.onN, pn.keyN, n, o)
	}
	for _, id := range o.Order {
		if node := o.Nodes[id]; node.Template != nil {
			p.template(node, o)
		}
	}
}

func (p *parser) node(id string, keyN, n *yaml.Node, o *Orders) (*Node, *yaml.Node) {
	what := fmt.Sprintf("node %q", id)
	fields, ok := p.mapping(n, what, append([]string{"type", "run", "timeout", "checks", "ask", "show", "on"}, agentKeys...)...)
	if !ok {
		return nil, nil
	}
	node := &Node{ID: id, On: map[string]string{}}
	var typeN, runN, timeoutN, checksN, askN, showN, onN *yaml.Node
	agent := map[string]entry{}
	for _, f := range fields {
		switch f.key {
		case "type":
			typeN = f.value
		case "run":
			runN = f.value
		case "timeout":
			timeoutN = f.value
		case "checks":
			checksN = f.value
		case "ask":
			askN = f.value
		case "show":
			showN = f.value
		case "on":
			onN = f.value
		default:
			agent[f.key] = f
		}
	}
	if typeN == nil {
		p.errorf(keyN, "%s: missing key \"type\" (script, gate, agent or human)", what)
		return nil, nil
	}
	t, ok := p.scalar(typeN, what+": type")
	if !ok {
		return nil, nil
	}
	if t != Script && t != Gate && t != Agent && t != Human {
		p.errorf(typeN, "%s: unknown node type %q (known: script, gate, agent, human)", what, t)
		return nil, nil
	}
	node.Type = t
	if t != Gate && t != Human {
		if askN != nil {
			p.errorf(askN, "%s: ask is allowed only on a gate or a human node", what)
			askN = nil
		}
		if showN != nil {
			p.errorf(showN, "%s: show is allowed only on a gate or a human node", what)
			showN = nil
		}
	}
	if askN != nil {
		node.Ask = p.questions(askN, what, t == Gate)
	}
	if showN != nil {
		node.Show = p.show(showN, what)
		if askN == nil && t == Gate {
			p.errorf(showN, "%s: show needs ask: the files are shown to the person who answers", what)
		}
	}
	if t == Human {
		for _, k := range []struct {
			n    *yaml.Node
			name string
		}{{runN, "run"}, {checksN, "checks"}, {timeoutN, "timeout"}} {
			if k.n != nil {
				p.errorf(k.n, "%s: a human node has no %s; it waits for a person", what, k.name)
			}
		}
		runN, checksN, timeoutN = nil, nil, nil
	}
	if t != Agent {
		for _, k := range agentKeys {
			if f, ok := agent[k]; ok {
				p.errorf(f.keyN, "%s: %s is allowed only on an agent node", what, k)
			}
		}
	} else {
		if runN != nil {
			p.errorf(runN, "%s: an agent node has no run; its executor is declared under the top-level executors", what)
			runN = nil
		}
		if checksN != nil {
			p.errorf(checksN, "%s: checks are allowed only on a gate", what)
			checksN = nil
		}
		p.agentNode(node, keyN, agent, o)
	}
	if runN != nil {
		node.Run = p.argv(runN, what+": run")
	}
	if timeoutN != nil {
		node.Timeout = p.duration(timeoutN, what+": timeout")
	}
	switch t {
	case Script:
		if checksN != nil {
			p.errorf(checksN, "%s: checks are allowed only on a gate", what)
		}
		if runN == nil {
			p.errorf(keyN, "%s: missing key \"run\"", what)
		}
	case Gate:
		switch {
		case runN != nil && checksN != nil:
			p.errorf(checksN, "%s: a gate has either run or checks, not both", what)
		case runN != nil && askN != nil:
			p.errorf(askN, "%s: a gate with ask takes checks, not run; declare the command as a check", what)
		case runN == nil && checksN == nil && askN == nil:
			p.errorf(keyN, "%s: a gate needs checks, ask, or a single run command", what)
		case checksN != nil || askN != nil:
			if timeoutN != nil {
				p.errorf(timeoutN, "%s: a gate with checks or ask has no timeout of its own; set timeout on each check", what)
			}
			if checksN != nil {
				node.Checks = p.gateChecks(checksN, what, o)
			}
		}
	}
	return node, onN
}

func (p *parser) gateChecks(n *yaml.Node, what string, o *Orders) []string {
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		p.errorf(n, "%s: checks must be a non-empty list of check names", what)
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, item := range n.Content {
		item = resolve(item)
		name, ok := p.name(item, what+": check")
		if !ok {
			continue
		}
		if o.Checks[name] == nil {
			p.errorf(item, "%s: check %q is not declared under the top-level checks", what, name)
			continue
		}
		if seen[name] {
			p.errorf(item, "%s: check %q is listed twice", what, name)
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func (p *parser) transitions(node *Node, onN, keyN, nodesN *yaml.Node, o *Orders) {
	what := fmt.Sprintf("node %q", node.ID)
	if onN == nil {
		p.errorf(keyN, "%s: missing key \"on\" (transitions by outcome)", what)
		return
	}
	entries, ok := p.mapping(onN, what+": on")
	if !ok {
		return
	}
	for _, e := range entries {
		outcome, ok := p.name(e.keyN, what+": outcome")
		if !ok {
			continue
		}
		if node.Type == Gate && (len(node.Checks) > 0 || len(node.Ask) > 0) && outcome != Pass && outcome != Blocked {
			p.errorf(e.keyN, "%s: a gate with checks or ask only produces pass or blocked, not %q", what, outcome)
			continue
		}
		if node.Type == Human {
			node.Decisions = append(node.Decisions, outcome)
		}
		target, ok := p.name(e.value, what+": transition target")
		if !ok {
			continue
		}
		if !IsEnd(target) && o.Nodes[target] == nil {
			if !p.nodeDeclared(nodesN, target) {
				p.errorf(e.value, "%s: transition %s -> %q: no such node (use a node id, done or failed)", what, outcome, target)
			}
			continue
		}
		node.On[outcome] = target
	}
	if node.Type == Human && len(entries) == 0 {
		p.errorf(onN, "%s: a human node needs at least one outcome in on: the choices the person has", what)
	}
	for _, outcome := range node.Outcomes() {
		if _, ok := node.On[outcome]; !ok && !hasKey(onN, outcome) {
			if node.Type == Agent && outcome != Fail {
				p.errorf(onN, "%s: outcome %q has no transition, but the schema allows the model to report it", what, outcome)
			} else {
				p.errorf(onN, "%s: outcome %q has no transition", what, outcome)
			}
		}
	}
	if node.Type == Agent && node.Schema != nil {
		for _, e := range entries {
			if e.key != Fail && !contains(node.Outcomes(), e.key) {
				p.errorf(e.keyN, "%s: outcome %q can never happen: the schema does not allow it for \"outcome\" (allowed: %s)", what, e.key, strings.Join(node.ModelOutcomes(), ", "))
			}
		}
	}
}

func hasKey(n *yaml.Node, key string) bool {
	if n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i < len(n.Content); i += 2 {
		if resolve(n.Content[i]).Value == key {
			return true
		}
	}
	return false
}
