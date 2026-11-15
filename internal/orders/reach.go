package orders

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// workdirKey names the folder agent steps work in: at the top level the
// default of every agent node, on an agent node its own.
const workdirKey = "workdir"

// FailOnDeniedKey makes an agent node fail an attempt in which the client
// denied a tool call, whatever the agent answered.
const FailOnDeniedKey = "fail_on_denied"

// StateDirName is the folder next to the orders that holds hetman's own
// files: the chronicle and the campaign folders.
const StateDirName = ".hetman"

// Unable is the reserved outcome an agent step reports when it cannot do
// its work at all, with the reason in UnableField. Without a transition for
// it the campaign stops and waits for a person; with one the flow follows it.
const (
	Unable      = "cannot_proceed"
	UnableField = "cannot_proceed_reason"
)

// WorkPrefix starts a file of show that lies in the campaign's work folder
// instead of the orders folder, as in "{{work}}/plan.md". In a prompt
// template {{work}} is the path of that folder.
const WorkPrefix = "{{" + RefWork + "}}/"

// MissingNode is the error of a placeholder that names a node the orders
// do not have, perhaps one still to be added.
type MissingNode struct {
	Name, Head, Node string
}

func (m *MissingNode) Error() string {
	if m.Node == "" {
		return fmt.Sprintf("{{%s}}: %s must be followed by a node id, as in {{%s.NODE}}", m.Name, m.Head, m.Head)
	}
	return fmt.Sprintf("{{%s}}: there is no node %q in the orders (yet)", m.Name, m.Node)
}

// noNode is the error of a placeholder whose node is not in the orders;
// rest is what follows the source, of which the node id is a prefix.
func noNode(name, head, rest string) error {
	node, _, _ := strings.Cut(rest, ".")
	return &MissingNode{Name: name, Head: head, Node: node}
}

// ParseBuilding is ParseOverlay for a flow being built: a placeholder that
// names a node the orders do not have yet is not a problem but is listed in
// Later, for the editor to show as a warning. Load and Parse still reject it.
func ParseBuilding(path string, data []byte, files map[string][]byte) (*Orders, error) {
	return parseWith(path, data, files, []string{filepath.Clean(path)}, true, nil)
}

// Limit caps how many times a transition may be taken in one campaign.
type Limit struct {
	Max  int    // how many times the transition may be taken
	Then string // the outcome taken instead once it has been taken Max times
}

// MaxLimit is the highest limit a transition may have.
const MaxLimit = 100

// workdir reads a folder relative to the orders folder. It must not lie in
// the state folder, which agents may not write.
func (p *parser) workdir(n *yaml.Node, what string) string {
	s, ok := p.scalar(n, what)
	if !ok {
		return ""
	}
	if strings.TrimSpace(s) == "" {
		p.errorf(n, "%s must name a folder, such as . for the orders folder or .. for the one above it", what)
		return ""
	}
	path := filepath.FromSlash(s)
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(p.file), path)
	}
	path = filepath.Clean(path)
	state := filepath.Join(filepath.Dir(p.file), StateDirName)
	if path == state || Within(state, path) {
		p.errorf(n, "%s %q lies in %s, which holds hetman's own files and is closed to agents", what, s, StateDirName)
		return ""
	}
	return path
}

// Within reports whether path lies inside dir (not dir itself).
func Within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && filepath.IsLocal(rel)
}

// agentReach reads what an agent node may reach: its working folder, which
// must be named on the node or as the default at the top level, the form of
// its tools, and that its schema leaves the reserved outcome alone.
func (p *parser) agentReach(node *Node, keyN *yaml.Node, fields map[string]entry, o *Orders, ownWorkdir bool) {
	what := fmt.Sprintf("node %q", node.ID)
	if node.Workdir == "" {
		node.Workdir = o.Workdir
	}
	if node.Workdir == "" && !ownWorkdir && !p.topWorkdir {
		p.errorf(keyN, "%s: missing key %q: the folder the agent works in, relative to the orders folder, such as . or ..; set it on the node or once at the top level", what, workdirKey)
	}
	if f, ok := fields["tools"]; ok && f.value.Kind == yaml.SequenceNode {
		for _, item := range f.value.Content {
			item = resolve(item)
			if item.Kind == yaml.ScalarNode {
				if msg := ToolProblem(item.Value); msg != "" {
					p.errorf(item, "%s: tools item %q: %s", what, item.Value, msg)
				}
			}
		}
	}
	if f, ok := fields[FailOnDeniedKey]; ok {
		switch v := f.value; {
		case v.Kind == yaml.ScalarNode && v.Tag == "!!bool":
			node.FailOnDenied = v.Value == "true"
		case v.Kind == yaml.SequenceNode && len(v.Content) > 0:
			for _, item := range v.Content {
				if s, ok := p.scalar(resolve(item), what+": "+FailOnDeniedKey+" item"); ok {
					if strings.TrimSpace(s) == "" || strings.ContainsAny(s, "() ") {
						p.errorf(item, "%s: %s lists tool names, such as Bash, not %q", what, FailOnDeniedKey, s)
						continue
					}
					node.FailOnDeniedTools = append(node.FailOnDeniedTools, s)
				}
			}
		default:
			p.errorf(v, "%s: %s is true (any denied call), false, or a list of the tools whose denied calls fail the attempt, such as [Bash]", what, FailOnDeniedKey)
		}
	}
	if contains(node.ModelOutcomes(), Unable) {
		p.errorIn(node.SchemaPath, node.Schema.Line, node.Schema.Col, "outcome %q is reserved: an agent step reports it when it cannot do its work; leave it out of the enum", Unable)
	}
	if node.Schema != nil && node.Schema.Properties[UnableField] != nil {
		p.errorIn(node.SchemaPath, node.Schema.Line, node.Schema.Col, "property %q is reserved for the reason an agent step cannot do its work", UnableField)
	}
}

// ToolProblem says what is wrong with the form of a tools entry, or "" when
// nothing is: the entry must not be empty and its parentheses must pair up.
// Which entries a client honours is for its adapter to say.
func ToolProblem(t string) string {
	if strings.TrimSpace(t) == "" {
		return "empty entry"
	}
	depth := 0
	for _, r := range t {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return "a closing parenthesis without an opening one"
			}
		}
	}
	if depth > 0 {
		return "an opening parenthesis without a closing one; a list flag may have split the entry at a space"
	}
	return ""
}

// limitThens lists the outcomes the limits of a node's transitions name to
// take instead: they may appear in on although the node never produces them.
func limitThens(entries []entry) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		if e.value.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(e.value.Content); i += 2 {
			if k, v := resolve(e.value.Content[i]), resolve(e.value.Content[i+1]); k.Value == "then" && v.Kind == yaml.ScalarNode {
				out[v.Value] = true
			}
		}
	}
	return out
}

// limit reads a transition written as {to: TARGET, max: N, then: OUTCOME}
// and returns the node of its target, or nil when it is rejected.
func (p *parser) limit(node *Node, outcome string, n *yaml.Node, what string) *yaml.Node {
	at := fmt.Sprintf("%s: transition %s", what, outcome)
	fields, ok := p.mapping(n, at, "to", "max", "then")
	if !ok {
		return nil
	}
	var to *yaml.Node
	var lim Limit
	for _, f := range fields {
		switch f.key {
		case "to":
			to = f.value
		case "max":
			k, err := strconv.Atoi(f.value.Value)
			if f.value.Kind != yaml.ScalarNode || f.value.Tag != "!!int" || err != nil || k < 1 || k > MaxLimit {
				p.errorf(f.value, "%s: max must be a whole number from 1 to %d, got %q", at, MaxLimit, f.value.Value)
				return nil
			}
			lim.Max = k
		case "then":
			if lim.Then, ok = p.name(f.value, at+": then"); !ok {
				return nil
			}
		}
	}
	for _, k := range []string{"to", "max", "then"} {
		if !hasKey(n, k) {
			p.errorf(n, "%s: missing key %q (a limited transition is {to: TARGET, max: N, then: OUTCOME})", at, k)
			return nil
		}
	}
	if lim.Then == outcome {
		p.errorf(n, "%s: then must name another outcome than %s", at, outcome)
		return nil
	}
	if node.Limits == nil {
		node.Limits = map[string]Limit{}
	}
	node.Limits[outcome] = lim
	return to
}

// checkLimits makes sure the outcome a limit names has a transition of its
// own and that limits do not name each other in a circle.
func (p *parser) checkLimits(node *Node, entries []entry, what string) {
	for _, e := range entries {
		lim, ok := node.Limits[e.key]
		if !ok {
			continue
		}
		if _, ok := node.On[lim.Then]; !ok {
			p.errorf(e.value, "%s: transition %s: then names %q, which has no transition in on", what, e.key, lim.Then)
			continue
		}
		seen := map[string]bool{e.key: true}
		for at := lim.Then; ; {
			next, ok := node.Limits[at]
			if !ok {
				break
			}
			if seen[next.Then] {
				p.errorf(e.value, "%s: transition %s: the limits of its outcomes name each other in a circle", what, e.key)
				break
			}
			seen[at] = true
			at = next.Then
		}
	}
}

// Files lists the files the orders consist of: the orders file itself, the
// prompt templates and schemas of its agent nodes, the programs its run
// lists name with a path relative to the orders folder, and the same for
// every segment, each once.
func (o *Orders) Files() []string {
	seen := map[string]bool{}
	var out []string
	var walk func(*Orders, int)
	walk = func(o *Orders, depth int) {
		add := func(path string) {
			if path != "" && !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
		}
		add(o.Path)
		for _, id := range o.Order {
			n := o.Nodes[id]
			add(n.Prompt)
			add(n.SchemaPath)
			if n.Segment != nil && depth < MaxDepth {
				walk(n.Segment, depth+1)
			}
		}
		for _, pr := range o.programs {
			if strings.ContainsAny(pr.name, `/\`) && !filepath.IsAbs(pr.name) {
				add(o.Program(pr.name))
			}
		}
	}
	walk(o, 0)
	return out
}
