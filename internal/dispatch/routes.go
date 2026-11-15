// Package dispatch keeps Hetman's own queue of small internal tasks and runs
// each one as an ordinary campaign of the flow its kind is routed to.
//
// The routing file maps a task kind to an orders file. The queue lives in
// .hetman/tasks.db next to the routing file, beside the chronicle that the
// routed flows share. A task only records that it was started and which
// campaign handles it; where it stands after that is read from the
// chronicle, like everything else about a campaign.
package dispatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"hetman/internal/orders"
)

// DefaultRoutesFile is the routing file used when none is given.
const DefaultRoutesFile = "routes.yaml"

// DefaultLimit is how many campaigns run at once when neither the routing
// file nor the dispatcher command sets a limit.
const DefaultLimit = 1

// Route sends the tasks of one kind to one flow.
type Route struct {
	Kind     string
	Orders   string // absolute path of the orders file
	Priority int    // higher first; 0 by default
	Limit    int    // campaigns of this route running at once; 0 for no limit of its own
}

// Routes is a checked routing file.
type Routes struct {
	Path   string // absolute path of the routing file
	Dir    string // its folder, which holds the routed orders files and .hetman
	Limit  int    // campaigns running at once over all routes
	Routes map[string]*Route
	Kinds  []string // kinds in file order
}

// Known reports whether a kind has a route, and says which kinds do when it
// has none.
func (r *Routes) Known(kind string) error {
	if r.Routes[kind] != nil {
		return nil
	}
	return newError(nil, "cli.task.unknown_kind", "kind", strconv.Quote(kind), "path", r.Path, "kinds", strings.Join(r.Kinds, ", "))
}

var kindPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

var yamlLine = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)

// LoadRoutes reads and checks the routing file at path. Every routed orders
// file is checked too, as hetman check checks it, including its programs;
// warnings about programs not on the system path are returned apart.
func LoadRoutes(path string) (*Routes, orders.Problems, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, err
	}
	return ParseRoutes(abs, data)
}

// ParseRoutes checks routing file data; path is its absolute path.
func ParseRoutes(path string, data []byte) (*Routes, orders.Problems, error) {
	p := &routeParser{file: path}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		msg := err.Error()
		if m := yamlLine.FindStringSubmatch(msg); m != nil {
			line, _ := strconv.Atoi(m[1])
			return nil, nil, orders.Problems{{File: path, Line: line, Col: 1, Msg: m[2]}}
		}
		return nil, nil, orders.Problems{{File: path, Msg: msg}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, nil, orders.Problems{{File: path, Msg: "empty routing file"}}
	}
	r := &Routes{Path: path, Dir: filepath.Dir(path), Limit: DefaultLimit, Routes: map[string]*Route{}}
	p.top(resolve(doc.Content[0]), r)
	if len(p.problems) > 0 {
		return nil, p.warnings, p.problems
	}
	return r, p.warnings, nil
}

type routeParser struct {
	file     string
	problems orders.Problems
	warnings orders.Problems
	checked  map[string]bool // orders files already checked
}

func (p *routeParser) errorf(n *yaml.Node, format string, args ...any) {
	p.problems = append(p.problems, orders.Problem{File: p.file, Line: n.Line, Col: n.Column, Msg: fmt.Sprintf(format, args...)})
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

func (p *routeParser) mapping(n *yaml.Node, what string, allowed ...string) []entry {
	if n.Kind != yaml.MappingNode {
		p.errorf(n, "%s must be a mapping", what)
		return nil
	}
	seen := map[string]bool{}
	var out []entry
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := resolve(n.Content[i]), resolve(n.Content[i+1])
		switch {
		case k.Kind != yaml.ScalarNode:
			p.errorf(k, "%s: keys must be plain names", what)
		case seen[k.Value]:
			p.errorf(k, "%s: duplicate key %q", what, k.Value)
		case allowed != nil && !contains(allowed, k.Value):
			p.errorf(k, "%s: unknown key %q (allowed: %s)", what, k.Value, strings.Join(allowed, ", "))
		default:
			seen[k.Value] = true
			out = append(out, entry{key: k.Value, keyN: k, value: v})
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// number reads a whole number of at least min.
func (p *routeParser) number(n *yaml.Node, what string, min int) int {
	v, err := strconv.Atoi(n.Value)
	if n.Kind != yaml.ScalarNode || n.Tag != "!!int" || err != nil || v < min {
		p.errorf(n, "%s must be a whole number of at least %d, got %q", what, min, n.Value)
		return 0
	}
	return v
}

func (p *routeParser) top(root *yaml.Node, r *Routes) {
	entries := p.mapping(root, "routing file", "version", "limit", "routes")
	if entries == nil && root.Kind != yaml.MappingNode {
		return
	}
	hasVersion, hasRoutes := false, false
	for _, e := range entries {
		switch e.key {
		case "version":
			hasVersion = true
			if e.value.Kind != yaml.ScalarNode || e.value.Tag != "!!int" || e.value.Value != "0" {
				p.errorf(e.value, "unsupported version %q: this hetman reads version 0", e.value.Value)
			}
		case "limit":
			r.Limit = p.number(e.value, "limit", 1)
		case "routes":
			hasRoutes = true
			p.routes(e.value, r)
		}
	}
	if !hasVersion {
		p.errorf(root, "missing required key \"version\" (use version: 0)")
	}
	if !hasRoutes {
		p.errorf(root, "missing required key \"routes\": a mapping from task kind to {orders: FILE}")
	}
}

func (p *routeParser) routes(n *yaml.Node, r *Routes) {
	entries := p.mapping(n, "routes")
	if n.Kind == yaml.MappingNode && len(entries) == 0 && len(n.Content) == 0 {
		p.errorf(n, "routes must route at least one task kind")
	}
	for _, e := range entries {
		if !kindPattern.MatchString(e.key) {
			p.errorf(e.keyN, "task kind %q: use letters, digits, '_', '-' and '.' only", e.key)
			continue
		}
		rt := &Route{Kind: e.key}
		hasOrders := false
		for _, f := range p.mapping(e.value, "route "+e.key, "orders", "priority", "limit") {
			switch f.key {
			case "orders":
				hasOrders = true
				rt.Orders = p.ordersFile(f.value, r, e.key)
			case "priority":
				v, err := strconv.Atoi(f.value.Value)
				if f.value.Kind != yaml.ScalarNode || f.value.Tag != "!!int" || err != nil {
					p.errorf(f.value, "route %s: priority must be a whole number, got %q", e.key, f.value.Value)
				}
				rt.Priority = v
			case "limit":
				rt.Limit = p.number(f.value, "route "+e.key+": limit", 1)
			}
		}
		if e.value.Kind == yaml.MappingNode && !hasOrders {
			p.errorf(e.value, "route %s: missing required key \"orders\": the orders file of the flow that handles this kind", e.key)
		}
		r.Routes[e.key] = rt
		r.Kinds = append(r.Kinds, e.key)
	}
}

// ordersFile checks one routed orders file and returns its absolute path.
func (p *routeParser) ordersFile(n *yaml.Node, r *Routes, kind string) string {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" || n.Value == "" {
		p.errorf(n, "route %s: orders must be a file name", kind)
		return ""
	}
	path := filepath.Join(r.Dir, filepath.FromSlash(n.Value))
	if filepath.IsAbs(n.Value) {
		path = filepath.Clean(n.Value)
	}
	if filepath.Dir(path) != r.Dir {
		p.errorf(n, "route %s: orders file %q must be in the folder of the routing file, which holds the chronicle and the queue the routed flows share", kind, n.Value)
		return ""
	}
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		p.errorf(n, "route %s: orders file %q does not exist (looked for %s)", kind, n.Value, path)
		return ""
	case err != nil:
		p.errorf(n, "route %s: orders file %q: %v", kind, n.Value, err)
		return ""
	case fi.IsDir():
		p.errorf(n, "route %s: orders file %q is a folder", kind, n.Value)
		return ""
	}
	if p.checked == nil {
		p.checked = map[string]bool{}
	}
	if p.checked[path] {
		return path
	}
	p.checked[path] = true
	o, err := orders.Load(path)
	if err != nil {
		var ps orders.Problems
		if errors.As(err, &ps) {
			p.errorf(n, "route %s: orders file %q is rejected (%d problem(s) below)", kind, n.Value, len(ps))
			p.problems = append(p.problems, ps...)
		} else {
			p.errorf(n, "route %s: orders file %q: %v", kind, n.Value, err)
		}
		return ""
	}
	errs, warnings := o.CheckPrograms()
	p.warnings = append(p.warnings, warnings...)
	if len(errs) > 0 {
		p.errorf(n, "route %s: orders file %q is rejected (%d problem(s) below)", kind, n.Value, len(errs))
		p.problems = append(p.problems, errs...)
	}
	return path
}
