package orders

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Node types that run another orders file, a segment, as a child campaign.
const (
	Segment = "segment" // runs the segment once
	ForEach = "foreach" // runs the segment once for every item of a list
)

// The outcomes of a segment and a for-each node: the end the child campaign
// reached, or for a for-each whether every item reached done.
var compositeOutcomes = []string{Done, Failed}

// Composite reports whether a node runs a segment as child campaigns.
func (n *Node) Composite() bool { return n.Type == Segment || n.Type == ForEach }

// Fields of an item of a for-each list.
const (
	ItemID    = "id"    // the item's id, unique in the list
	ItemNeeds = "needs" // ids of the items that must reach done before this one starts
)

// MaxParallel limits the parallel key of a for-each node.
const MaxParallel = 64

// compositeKeys are the keys only a segment or a for-each node may have.
var compositeKeys = []string{"segment", "input", "items", "parallel"}

// lockKey names the resources a node or a check needs exclusively.
const lockKey = "locks"

// locks reads a locks list: names of resources, each at most once.
func (p *parser) locks(n *yaml.Node, what string) []string {
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		p.errorf(n, "%s: locks must be a non-empty list of resource names, for example [test-stand]", what)
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, item := range n.Content {
		item = resolve(item)
		name, ok := p.name(item, what+": lock")
		if !ok {
			continue
		}
		if seen[name] {
			p.errorf(item, "%s: lock %q is listed twice", what, name)
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// compositeNode reads the keys of a segment or a for-each node.
func (p *parser) compositeNode(node *Node, keyN *yaml.Node, fields map[string]entry) {
	what := fmt.Sprintf("node %q", node.ID)
	node.Parallel = 1
	for _, k := range compositeKeys {
		f, ok := fields[k]
		if !ok {
			continue
		}
		switch {
		case k == "input" && node.Type != Segment:
			p.errorf(f.keyN, "%s: input is allowed only on a segment node; a for-each gives each child its item", what)
			delete(fields, k)
		case (k == "items" || k == "parallel") && node.Type != ForEach:
			p.errorf(f.keyN, "%s: %s is allowed only on a foreach node", what, k)
			delete(fields, k)
		}
	}
	f, ok := fields["segment"]
	if !ok {
		p.errorf(keyN, "%s: missing key \"segment\" (the orders file of the segment it runs)", what)
	} else {
		p.loadSegment(node, f.value, what)
	}
	if f, ok := fields["input"]; ok {
		node.Input, _ = p.scalar(f.value, what+": input")
		node.inputN = f.value
	}
	if f, ok := fields["items"]; ok {
		node.Items, _ = p.scalar(f.value, what+": items")
		node.itemsN = f.value
	} else if node.Type == ForEach {
		p.errorf(keyN, "%s: missing key \"items\" (the list to run the segment for, such as output.plan.items)", what)
	}
	if f, ok := fields["parallel"]; ok {
		v := f.value
		n, err := strconv.Atoi(v.Value)
		if v.Kind != yaml.ScalarNode || v.Tag != "!!int" || err != nil || n < 1 || n > MaxParallel {
			p.errorf(v, "%s: parallel must be a whole number from 1 to %d, got %q", what, MaxParallel, v.Value)
		} else {
			node.Parallel = n
		}
	}
}

// loadSegment reads and checks the orders file of a segment. A segment that
// runs itself, directly or through other segments, is rejected.
func (p *parser) loadSegment(node *Node, n *yaml.Node, what string) {
	rel, ok := p.scalar(n, what+": segment")
	if !ok {
		return
	}
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(p.file), filepath.FromSlash(rel))
	}
	path = filepath.Clean(path)
	for i, s := range p.stack {
		if sameFile(s, path) {
			chain := []string{}
			for _, x := range append(p.stack[i:], path) {
				chain = append(chain, filepath.Base(x))
			}
			p.errorf(n, "%s: segment %q runs itself: %s", what, rel, strings.Join(chain, " -> "))
			return
		}
	}
	if len(p.stack) >= MaxDepth {
		p.errorf(n, "%s: segment %q nests segments deeper than %d levels", what, rel, MaxDepth)
		return
	}
	data, ok := p.overlay[path]
	if !ok {
		var err error
		p.note(path)
		if data, err = os.ReadFile(path); err != nil {
			if os.IsNotExist(err) {
				p.errorf(n, "%s: segment file %q does not exist (looked for %s)", what, rel, path)
			} else {
				p.errorf(n, "%s: cannot read segment %q: %v", what, rel, err)
			}
			return
		}
	}
	seg, err := parse(path, data, p.overlay, append(append([]string{}, p.stack...), path), p.seen)
	if err != nil {
		ps, isProblems := err.(Problems)
		if !isProblems {
			p.errorf(n, "%s: segment %q: %v", what, rel, err)
			return
		}
		p.problems = append(p.problems, ps...)
		p.errorf(n, "%s: segment %q is rejected: %d problem(s), listed with its file", what, rel, len(ps))
		return
	}
	node.SegmentPath, node.Segment = path, seg
}

// MaxDepth limits how deeply segments may nest.
const MaxDepth = 8

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

// compositeRefs checks what the input of a segment node and the items of a
// for-each node refer to, once every node is known.
func (p *parser) compositeRefs(node *Node, o *Orders) {
	what := fmt.Sprintf("node %q", node.ID)
	if node.inputN != nil && node.Input != "" {
		r, err := o.Ref(node.Input)
		switch {
		case err != nil:
			p.errorf(node.inputN, "%s: input: %s", what, strings.Trim(err.Error(), "{}"))
		case r.Kind != RefInput && r.Kind != RefOutput && r.Kind != RefArtifact:
			p.errorf(node.inputN, "%s: input must be input[.FIELD], output.NODE[.FIELD] or artifact.NODE.FILE, got %q", what, node.Input)
		default:
			node.InputRef = r
		}
	}
	if node.itemsN != nil && node.Items != "" {
		r, err := o.Ref(node.Items)
		switch {
		case err != nil:
			p.errorf(node.itemsN, "%s: items: %s", what, strings.Trim(err.Error(), "{}"))
		case r.Kind != RefInput && r.Kind != RefOutput && r.Kind != RefArtifact:
			p.errorf(node.itemsN, "%s: items must be output.NODE.FIELD, artifact.NODE.FILE or input.FIELD, got %q", what, node.Items)
		default:
			node.ItemsRef = r
		}
	}
}

// checkLockOrder makes nested locks follow one order: a step inside a
// segment may only take locks whose names sort after every lock its segment
// node holds. Every step takes its own locks in name order too, so no two
// holders can ever wait for each other.
func (p *parser) checkLockOrder(node *Node, n *yaml.Node) {
	if len(node.Locks) == 0 || node.Segment == nil {
		return
	}
	held := node.Locks[0]
	for _, l := range node.Locks {
		if l > held {
			held = l
		}
	}
	for _, inner := range node.Segment.AllLocks() {
		if inner <= held {
			p.errorf(n, "node %q: its segment takes the lock %q while this node holds %q; locks taken inside a segment must sort after the locks of the node that runs it (rename them so they do), or no two campaigns could be kept from waiting for each other", node.ID, inner, held)
			return
		}
	}
}

// LocksOf lists every lock a step of the node holds while it runs, in the
// order they are taken: its own and those of the checks of a gate, sorted by
// name.
func (o *Orders) LocksOf(n *Node) []string {
	set := map[string]bool{}
	for _, l := range n.Locks {
		set[l] = true
	}
	for _, c := range n.Checks {
		if def := o.Checks[c]; def != nil {
			for _, l := range def.Locks {
				set[l] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// AllLocks lists every lock any node or check of the orders, or of a segment
// they run, may take, sorted.
func (o *Orders) AllLocks() []string {
	set := map[string]bool{}
	for _, c := range o.Checks {
		for _, l := range c.Locks {
			set[l] = true
		}
	}
	for _, n := range o.Nodes {
		for _, l := range n.Locks {
			set[l] = true
		}
		if n.Segment != nil {
			for _, l := range n.Segment.AllLocks() {
				set[l] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}
