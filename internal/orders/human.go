package orders

import (
	"fmt"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"hetman/internal/ask"
)

// questions reads the ask list of a gate or a human node. A gate asks only
// yes/no questions.
func (p *parser) questions(n *yaml.Node, what string, yesNoOnly bool) []ask.Question {
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		p.errorf(n, "%s: ask must be a non-empty list of questions, each with id and question", what)
		return nil
	}
	seen := map[string]bool{}
	var out []ask.Question
	for _, item := range n.Content {
		item = resolve(item)
		fields, ok := p.mapping(item, what+": question", "id", "question", "kind", "options")
		if !ok {
			continue
		}
		q := ask.Question{Kind: ask.YesNo}
		var idN, kindN, optionsN *yaml.Node
		for _, f := range fields {
			switch f.key {
			case "id":
				idN = f.value
				q.ID, _ = p.name(f.value, what+": question id")
			case "question":
				if s, ok := p.scalar(f.value, what+": question"); ok {
					q.Question = strings.TrimSpace(s)
					if q.Question == "" {
						p.errorf(f.value, "%s: question must not be empty", what)
					}
				}
			case "kind":
				kindN = f.value
				if s, ok := p.scalar(f.value, what+": kind"); ok {
					q.Kind = s
				}
			case "options":
				optionsN = f.value
				if f.value.Kind != yaml.SequenceNode {
					p.errorf(f.value, "%s: options must be a list", what)
					continue
				}
				for _, o := range f.value.Content {
					if s, ok := p.scalar(resolve(o), what+": option"); ok {
						q.Options = append(q.Options, s)
					}
				}
			}
		}
		if idN == nil {
			p.errorf(item, "%s: a question needs an id", what)
		}
		if q.Question == "" && !hasKey(item, "question") {
			p.errorf(item, "%s: a question needs the text of the question under question", what)
		}
		at := kindN
		if at == nil {
			at = item
		}
		switch {
		case q.Kind != ask.YesNo && q.Kind != ask.Choice && q.Kind != ask.Text:
			p.errorf(at, "%s: kind must be one of %s, not %q", what, strings.Join(ask.Kinds, ", "), q.Kind)
		case yesNoOnly && q.Kind != ask.YesNo:
			p.errorf(at, "%s: a gate asks yes/no questions only; use a human node for kind %s", what, q.Kind)
		case q.Kind == ask.Choice && optionsN == nil:
			p.errorf(at, "%s: a choice needs options", what)
		case q.Kind == ask.Choice:
			if msg := ask.CheckOptions(q.Options); msg != "" {
				p.errorf(optionsN, "%s: %s", what, msg)
			}
		case optionsN != nil:
			p.errorf(optionsN, "%s: options are allowed only with kind choice", what)
		}
		if q.ID != "" && seen[q.ID] {
			p.errorf(idN, "%s: question id %q is used twice", what, q.ID)
			continue
		}
		seen[q.ID] = true
		out = append(out, q)
	}
	return out
}

// show reads the files a person looks at before answering: paths inside the
// orders folder.
func (p *parser) show(n *yaml.Node, what string) []string {
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		p.errorf(n, "%s: show must be a non-empty list of files", what)
		return nil
	}
	var out []string
	for _, item := range n.Content {
		item = resolve(item)
		s, ok := p.scalar(item, what+": show item")
		if !ok {
			continue
		}
		if s == "" || !filepath.IsLocal(filepath.FromSlash(s)) {
			p.errorf(item, "%s: show %q must be a relative path inside the orders folder", what, s)
			continue
		}
		out = append(out, s)
	}
	return out
}

func (p *parser) notify(n *yaml.Node, o *Orders) {
	fields, ok := p.mapping(n, "notify", "run", "timeout")
	if !ok {
		return
	}
	x := &Notify{Timeout: DefaultNotifyTimeout}
	for _, f := range fields {
		switch f.key {
		case "run":
			x.Run = p.argv(f.value, "notify: run")
		case "timeout":
			x.Timeout = p.duration(f.value, "notify: timeout")
		}
	}
	if !hasKey(n, "run") {
		p.errorf(n, "notify: missing key \"run\" (the command to run when a campaign waits for a person)")
		return
	}
	o.Notify = x
}

// answersRef resolves {{answers.NODE...}}.
func (o *Orders) answersRef(name, rest string) (Ref, error) {
	node, tail := o.longestNode(rest)
	if node == "" {
		return Ref{}, fmt.Errorf("{{%s}}: answers must be followed by a node id, as in {{answers.NODE}}", name)
	}
	def := o.Nodes[node]
	r := Ref{Kind: RefAnswers, Node: node}
	if def.Type != Human && !(def.Type == Gate && len(def.Ask) > 0) {
		return r, fmt.Errorf("{{%s}}: node %q asks a person nothing; answers come from a human node or a gate with ask", name, node)
	}
	if tail == "" {
		return r, nil
	}
	r.Path = strings.Split(tail, ".")
	if len(r.Path) > 2 || (len(r.Path) == 2 && r.Path[1] != "comment") {
		return r, fmt.Errorf("{{%s}}: use {{answers.%s}}, {{answers.%s.ID}} or {{answers.%s.ID.comment}}", name, node, node, node)
	}
	for _, q := range def.Ask {
		if q.ID == r.Path[0] {
			return r, nil
		}
	}
	return r, fmt.Errorf("{{%s}}: node %q has no question %q", name, node, r.Path[0])
}
