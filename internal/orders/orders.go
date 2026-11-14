// Package orders reads and checks a flow config ("orders").
//
// The config is parsed into a YAML node tree and walked by hand so that every
// problem is reported with the line and column it comes from.
package orders

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultFile is the config file name used when none is given.
const DefaultFile = "orders.yaml"

// Node types.
const (
	Script = "script"
	Gate   = "gate"
)

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
	Path    string // absolute path of the config file
	Dir     string // folder holding the config; relative paths resolve here
	Version int
	Flow    string
	Start   string
	Checks  map[string]*Check
	Nodes   map[string]*Node
	Order   []string // node ids in file order
}

// Check is a gate check declared once at the top level.
type Check struct {
	Name    string
	Run     []string
	Timeout time.Duration
	Soft    bool
}

// Node is one step of the flow.
type Node struct {
	ID      string
	Type    string
	Run     []string
	Timeout time.Duration
	Checks  []string
	On      map[string]string
}

// Outcomes lists the outcomes the engine can produce for the node without a
// result file. Each of them must have a transition.
func (n *Node) Outcomes() []string {
	var out []string
	if n.Type == Gate {
		out = []string{Pass, Blocked}
	} else {
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
	p := &parser{file: path}
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
		Path:   path,
		Dir:    filepath.Dir(path),
		Checks: map[string]*Check{},
		Nodes:  map[string]*Node{},
	}
	p.top(resolve(doc.Content[0]), o)
	if len(p.problems) > 0 {
		sort.SliceStable(p.problems, func(i, j int) bool { return p.problems[i].Line < p.problems[j].Line })
		return nil, p.problems
	}
	return o, nil
}

type parser struct {
	file     string
	problems Problems
}

func (p *parser) errorf(n *yaml.Node, format string, args ...any) {
	p.problems = append(p.problems, Problem{File: p.file, Line: n.Line, Col: n.Column, Msg: fmt.Sprintf(format, args...)})
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
	entries, ok := p.mapping(root, "orders", "version", "flow", "start", "checks", "nodes")
	if !ok {
		return
	}
	var startN, nodesN *yaml.Node
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
		case "start":
			startN = e.value
			o.Start, _ = p.name(e.value, "start")
		case "checks":
			p.checks(e.value, o)
		case "nodes":
			nodesN = e.value
		}
	}
	// Nodes are read after checks so that gates can refer to checks declared
	// anywhere in the file.
	if nodesN != nil {
		p.nodes(nodesN, o)
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
	// Transitions are checked once every node id is known.
	for _, pn := range all {
		p.transitions(pn.node, pn.onN, pn.keyN, n, o)
	}
}

func (p *parser) node(id string, keyN, n *yaml.Node, o *Orders) (*Node, *yaml.Node) {
	what := fmt.Sprintf("node %q", id)
	fields, ok := p.mapping(n, what, "type", "run", "timeout", "checks", "on")
	if !ok {
		return nil, nil
	}
	node := &Node{ID: id, On: map[string]string{}}
	var typeN, runN, timeoutN, checksN, onN *yaml.Node
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
		case "on":
			onN = f.value
		}
	}
	if typeN == nil {
		p.errorf(keyN, "%s: missing key \"type\" (script or gate)", what)
		return nil, nil
	}
	t, ok := p.scalar(typeN, what+": type")
	if !ok {
		return nil, nil
	}
	if t != Script && t != Gate {
		p.errorf(typeN, "%s: unknown node type %q (known: script, gate)", what, t)
		return nil, nil
	}
	node.Type = t
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
		case runN == nil && checksN == nil:
			p.errorf(keyN, "%s: a gate needs checks (or a single run command)", what)
		case checksN != nil:
			if timeoutN != nil {
				p.errorf(timeoutN, "%s: a gate with checks has no timeout of its own; set timeout on each check", what)
			}
			node.Checks = p.gateChecks(checksN, what, o)
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
		if node.Type == Gate && len(node.Checks) > 0 && outcome != Pass && outcome != Blocked {
			p.errorf(e.keyN, "%s: a gate with checks only produces pass or blocked, not %q", what, outcome)
			continue
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
	for _, outcome := range node.Outcomes() {
		if _, ok := node.On[outcome]; !ok && !hasKey(onN, outcome) {
			p.errorf(onN, "%s: outcome %q has no transition", what, outcome)
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
