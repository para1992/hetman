package orders

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"hetman/internal/ask"
	"hetman/internal/prompt"
	"hetman/internal/schema"
)

// agentKeys are the keys only an agent node may have.
var agentKeys = []string{"executor", "role", "prompt", "model", "effort", "tools", "schema", "idle_timeout", "max_attempts", "dialogue"}

// OutcomeField is the field of an agent's output that names its outcome.
const OutcomeField = "outcome"

// ModelOutcomes lists the outcomes the schema of an agent node allows the
// model to report, in the order of the schema.
func (n *Node) ModelOutcomes() []string {
	if n.Schema == nil {
		return nil
	}
	out, _ := n.Schema.Properties[OutcomeField].Strings()
	return out
}

func (p *parser) executors(n *yaml.Node, o *Orders) {
	entries, ok := p.mapping(n, "executors")
	if !ok {
		return
	}
	for _, e := range entries {
		name, ok := p.name(e.keyN, "executor name")
		if !ok {
			continue
		}
		what := fmt.Sprintf("executor %q", name)
		fields, ok := p.mapping(e.value, what, "run")
		if !ok {
			continue
		}
		x := &Executor{Name: name}
		for _, f := range fields {
			x.Run = p.argv(f.value, what+": run")
		}
		if len(fields) == 0 {
			p.errorf(e.keyN, "%s: missing key \"run\" (the adapter program and its arguments)", what)
		}
		o.Executors[name] = x
	}
}

// scribe reads the model the setup assistant may ask.
func (p *parser) scribe(n *yaml.Node, o *Orders) {
	fields, ok := p.mapping(n, "scribe", "executor", "model", "effort", "timeout")
	if !ok {
		return
	}
	x := &Scribe{}
	for _, f := range fields {
		switch f.key {
		case "executor":
			if name, ok := p.name(f.value, "scribe: executor"); ok {
				if o.Executors[name] == nil {
					p.errorf(f.value, "scribe: executor %q is not declared under the top-level executors", name)
				}
				x.Executor = name
			}
		case "model":
			x.Model, _ = p.scalar(f.value, "scribe: model")
		case "effort":
			x.Effort, _ = p.scalar(f.value, "scribe: effort")
		case "timeout":
			x.Timeout = p.duration(f.value, "scribe: timeout")
		}
	}
	for _, k := range []string{"executor", "model"} {
		if !hasKey(n, k) {
			p.errorf(n, "scribe: missing key %q (the setup assistant has no default model)", k)
		}
	}
	o.Scribe = x
}

// agentNode reads the keys of an agent node.
func (p *parser) agentNode(node *Node, keyN *yaml.Node, fields map[string]entry, o *Orders) {
	what := fmt.Sprintf("node %q", node.ID)
	node.MaxAttempts = DefaultMaxAttempts
	for _, k := range []string{"prompt", "schema"} {
		if _, ok := fields[k]; !ok {
			p.errorf(keyN, "%s: missing key %q", what, k)
		}
	}
	if f, ok := fields["role"]; ok {
		// A role names the executor, model and effort of each of its
		// clients, so the node names none of them.
		clash := false
		for _, k := range []string{"executor", "model", "effort"} {
			if g, ok := fields[k]; ok {
				p.errorf(g.keyN, "%s: %s is given by each client of role %q; a node names either a role or an executor, not both", what, k, f.value.Value)
				clash = true
			}
		}
		if !clash {
			p.roleKey(node, f, o)
		}
		p.agentRest(node, fields)
		return
	}
	if _, ok := fields["executor"]; !ok {
		p.errorf(keyN, "%s: missing key \"executor\" (or \"role\", a pool of clients declared under the top-level roles)", what)
	}
	if f, ok := fields["executor"]; ok {
		if name, ok := p.name(f.value, what+": executor"); ok {
			if o.Executors[name] == nil {
				p.errorf(f.value, "%s: executor %q is not declared under the top-level executors", what, name)
			} else {
				node.Executor = name
			}
		}
	}
	for _, k := range []string{"model", "effort"} {
		f, ok := fields[k]
		if !ok {
			continue
		}
		s, ok := p.scalar(f.value, what+": "+k)
		if !ok {
			continue
		}
		if k == "model" {
			node.Model = s
		} else {
			node.Effort = s
		}
	}
	p.agentRest(node, fields)
}

// agentRest reads the keys of an agent node that do not name who runs it.
func (p *parser) agentRest(node *Node, fields map[string]entry) {
	what := fmt.Sprintf("node %q", node.ID)
	if f, ok := fields["tools"]; ok {
		if f.value.Kind != yaml.SequenceNode {
			p.errorf(f.value, "%s: tools must be a list", what)
		} else {
			node.Tools = []string{}
			for _, item := range f.value.Content {
				if s, ok := p.scalar(resolve(item), what+": tools item"); ok {
					node.Tools = append(node.Tools, s)
				}
			}
		}
	}
	if f, ok := fields["idle_timeout"]; ok {
		node.IdleTimeout = p.duration(f.value, what+": idle_timeout")
	}
	if f, ok := fields["max_attempts"]; ok {
		v := f.value
		n, err := strconv.Atoi(v.Value)
		if v.Kind != yaml.ScalarNode || v.Tag != "!!int" || err != nil || n < 1 || n > 100 {
			p.errorf(v, "%s: max_attempts must be a whole number from 1 to 100, got %q", what, v.Value)
		} else {
			node.MaxAttempts = n
		}
	}
	if f, ok := fields["dialogue"]; ok {
		v := f.value
		n, err := strconv.Atoi(v.Value)
		if v.Kind != yaml.ScalarNode || v.Tag != "!!int" || err != nil || n < 1 || n > 100 {
			p.errorf(v, "%s: dialogue must be a whole number of question rounds from 1 to 100, got %q", what, v.Value)
		} else {
			node.Dialogue = n
		}
	}
	if f, ok := fields["prompt"]; ok {
		if path, data, ok := p.readFile(f.value, what+": prompt"); ok {
			node.Prompt = path
			node.Template = prompt.Parse(string(data))
		}
	}
	if f, ok := fields["schema"]; ok {
		if path, data, ok := p.readFile(f.value, what+": schema"); ok {
			p.agentSchema(node, path, data)
		}
	}
}

// file reads a file named by a scalar, relative to the orders folder.
func (p *parser) readFile(n *yaml.Node, what string) (string, []byte, bool) {
	rel, ok := p.scalar(n, what)
	if !ok {
		return "", nil, false
	}
	path := rel
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(p.file), filepath.FromSlash(rel))
	}
	data, ok := p.overlay[path]
	var err error
	if !ok {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		if os.IsNotExist(err) {
			p.errorf(n, "%s: file %q does not exist (looked for %s)", what, rel, path)
		} else {
			p.errorf(n, "%s: cannot read %q: %v", what, rel, err)
		}
		return "", nil, false
	}
	return path, data, true
}

// agentSchema reads the schema of an agent node and checks that it makes
// the output an object with a required string field "outcome" limited to a
// list of names.
func (p *parser) agentSchema(node *Node, path string, data []byte) {
	s, err := schema.Parse(data)
	if err != nil {
		for _, sp := range err.(schema.Problems) {
			p.errorIn(path, sp.Line, sp.Col, "%s", sp.Msg)
		}
		return
	}
	at := func(sc *schema.Schema, format string, args ...any) {
		p.errorIn(path, sc.Line, sc.Col, format, args...)
	}
	if s.Type != "object" {
		at(s, "the output of an agent step must be an object: set \"type\": \"object\"")
		return
	}
	out := s.Properties[OutcomeField]
	switch {
	case out == nil:
		at(s, "the schema must have a property %q listing the outcomes the step may report", OutcomeField)
		return
	case out.Type != "string":
		at(out, "property %q must have \"type\": \"string\"", OutcomeField)
		return
	case len(out.Enum) == 0:
		at(out, "property %q must have an enum listing the outcomes the step may report", OutcomeField)
		return
	case !s.IsRequired(OutcomeField):
		at(s, "property %q must be listed in required", OutcomeField)
		return
	}
	names, _ := out.Strings()
	for _, name := range names {
		if !namePattern.MatchString(name) {
			at(out, "outcome %q: use letters, digits, '_', '-' and '.' only", name)
			return
		}
		if name == ask.Outcome && node.Dialogue > 0 {
			at(out, "outcome %q is reserved: a dialogue step reports it when it asks the person questions; leave it out of the enum", name)
			return
		}
	}
	if q := s.Properties[ask.Field]; q != nil && node.Dialogue > 0 {
		at(q, "property %q is reserved for the questions of a dialogue step", ask.Field)
		return
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		at(s, "%v", err)
		return
	}
	node.SchemaPath, node.Schema, node.SchemaJSON = path, s, compact.Bytes()
}

// Placeholder sources.
const (
	RefInput    = "input"    // {{input}} or {{input.field...}}: the run input
	RefOutput   = "output"   // {{output.NODE}} or {{output.NODE.field...}}: an agent node's output
	RefArtifact = "artifact" // {{artifact.NODE.FILE}}: a file a script or gate listed as an artifact
	RefRework   = "rework"   // {{rework}}: why the flow came back to this node
	RefAnswers  = "answers"  // {{answers.NODE}}, {{answers.NODE.ID}}, {{answers.NODE.ID.comment}}: what a person answered
	RefLanguage = "language" // {{language}}, {{language.native}}, {{language.code}}: the language for the person
)

// Fields of the language placeholder after "language.".
const (
	LanguageNative = "native" // its own name
	LanguageCode   = "code"   // its BCP 47 code
)

// Ref is what a placeholder of a prompt template refers to.
type Ref struct {
	Kind string
	Node string   // output and artifact
	Path []string // fields inside the input or the output; the question id (and "comment") of answers; the language field
	File string   // artifact
}

// Ref resolves a placeholder name. A node id may contain dots; the longest
// node id that fits is taken.
func (o *Orders) Ref(name string) (Ref, error) {
	head, rest, _ := strings.Cut(name, ".")
	switch head {
	case RefRework:
		if rest != "" || strings.Contains(name, "/") {
			return Ref{}, fmt.Errorf("{{%s}}: rework takes no fields", name)
		}
		return Ref{Kind: RefRework}, nil
	case RefInput:
		r := Ref{Kind: RefInput}
		if strings.HasSuffix(name, ".") {
			return r, fmt.Errorf("{{%s}}: empty field name", name)
		}
		if rest != "" {
			r.Path = strings.Split(rest, ".")
		}
		for _, f := range r.Path {
			if f == "" {
				return r, fmt.Errorf("{{%s}}: empty field name", name)
			}
		}
		return r, nil
	case RefAnswers:
		return o.answersRef(name, rest)
	case RefLanguage:
		r := Ref{Kind: RefLanguage}
		switch {
		case name == RefLanguage:
			return r, nil
		case rest == LanguageNative || rest == LanguageCode:
			r.Path = []string{rest}
			return r, nil
		}
		return r, fmt.Errorf("{{%s}}: use {{language}} (the English name), {{language.native}} (its own name) or {{language.code}}", name)
	case RefOutput, RefArtifact:
		node, tail := o.longestNode(rest)
		if node == "" {
			return Ref{}, fmt.Errorf("{{%s}}: %s must be followed by a node id, as in {{%s.NODE}}", name, head, head)
		}
		def := o.Nodes[node]
		r := Ref{Kind: head, Node: node}
		if head == RefOutput {
			if def.Type != Agent {
				return r, fmt.Errorf("{{%s}}: node %q is a %s; only agent nodes have an output (use artifact for files of scripts)", name, node, def.Type)
			}
			if tail != "" {
				r.Path = strings.Split(tail, ".")
			}
			for _, f := range r.Path {
				if f == "" {
					return r, fmt.Errorf("{{%s}}: empty field name", name)
				}
			}
			return r, checkOutputPath(name, def, r.Path)
		}
		if def.Type == Agent {
			return r, fmt.Errorf("{{%s}}: node %q is an agent node; use {{output.%s}}", name, node, node)
		}
		if tail == "" {
			return r, fmt.Errorf("{{%s}}: name the file, as in {{artifact.%s.report.txt}}", name, node)
		}
		r.File = tail
		if !filepath.IsLocal(filepath.FromSlash(tail)) {
			return r, fmt.Errorf("{{%s}}: the file must be a path inside the campaign's work folder", name)
		}
		return r, nil
	}
	return Ref{}, fmt.Errorf("{{%s}} is not a known placeholder (use input, output.NODE, artifact.NODE.FILE, answers.NODE, language or rework)", name)
}

// longestNode splits "a.b.c" into the longest node id prefix and the rest.
func (o *Orders) longestNode(s string) (string, string) {
	best := ""
	for id := range o.Nodes {
		if (s == id || strings.HasPrefix(s, id+".")) && len(id) > len(best) {
			best = id
		}
	}
	if best == "" {
		return "", ""
	}
	return best, strings.TrimPrefix(strings.TrimPrefix(s, best), ".")
}

// checkOutputPath rejects a field the node's schema rules out.
func checkOutputPath(name string, def *Node, path []string) error {
	s := def.Schema
	for _, f := range path {
		if s == nil || s.Type != "object" {
			return nil
		}
		next := s.Properties[f]
		if next == nil {
			if s.NoAdditional {
				return fmt.Errorf("{{%s}}: the schema of node %q has no field %q", name, def.ID, f)
			}
			return nil
		}
		s = next
	}
	return nil
}

// template checks every placeholder of an agent node's prompt template.
func (p *parser) template(node *Node, o *Orders) {
	for _, ph := range node.Template.Placeholders() {
		if _, err := o.Ref(ph.Name); err != nil {
			p.errorIn(node.Prompt, ph.Line, ph.Col, "%v", err)
		}
	}
}
