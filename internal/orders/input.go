package orders

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"hetman/internal/ask"
	"hetman/internal/language"
	"hetman/internal/runinput"
)

// inputKey is the top-level key that describes the run input.
const inputKey = "input"

// inputFieldKeys are the keys of one field of the input section.
var inputFieldKeys = []string{"kind", "label", "hint", "required", "default", "ask", "options", "fields"}

// input reads the input section: the fields of the run input by name.
func (p *parser) input(n *yaml.Node, o *Orders) {
	fields := p.inputFields(n, "input", false)
	if fields != nil {
		o.Input = &runinput.Spec{Fields: fields}
	}
}

// inputFields reads a mapping of fields; nested is true inside a list of
// objects, where another list of objects is not allowed.
func (p *parser) inputFields(n *yaml.Node, what string, nested bool) []*runinput.Field {
	entries, ok := p.mapping(n, what)
	if !ok {
		return nil
	}
	if len(entries) == 0 {
		p.errorf(n, "%s must declare at least one field, such as task: {kind: text, required: true}", what)
		return nil
	}
	var out []*runinput.Field
	good := true
	for _, e := range entries {
		f := p.inputField(e, what, nested)
		if f == nil {
			good = false
			continue
		}
		out = append(out, f)
	}
	if !good {
		return nil
	}
	return out
}

func (p *parser) inputField(e entry, what string, nested bool) *runinput.Field {
	name := e.key
	at := fmt.Sprintf("%s: field %q", what, name)
	switch {
	case !runinput.NamePattern.MatchString(name):
		p.errorf(e.keyN, "%s: field %q: use letters, digits, '_' and '-' only, starting with a letter or digit (no dots: {{input.%s}} splits at them)", what, name, name)
		return nil
	case strings.HasPrefix(name, runinput.ReservedPrefix):
		p.errorf(e.keyN, "%s: names starting with %s are kept for what hetman adds to the run input", at, runinput.ReservedPrefix)
		return nil
	}
	keys, ok := p.mapping(e.value, at, inputFieldKeys...)
	if !ok {
		return nil
	}
	f := &runinput.Field{Name: name, Ask: true}
	byKey := map[string]entry{}
	for _, k := range keys {
		byKey[k.key] = k
	}
	before := len(p.problems)
	kindE, hasKind := byKey["kind"]
	if !hasKind {
		p.errorf(e.keyN, "%s: missing key \"kind\" (%s)", at, strings.Join(runinput.Kinds, ", "))
		return nil
	}
	kind, ok := p.scalar(kindE.value, at+": kind")
	if !ok {
		return nil
	}
	if !contains(runinput.Kinds, kind) {
		p.errorf(kindE.value, "%s: unknown kind %q (known: %s)", at, kind, strings.Join(runinput.Kinds, ", "))
		return nil
	}
	f.Kind = kind
	if k, ok := byKey["label"]; ok {
		f.Label = p.inputWords(k.value, at+": label")
	}
	if k, ok := byKey["hint"]; ok {
		f.Hint = p.inputWords(k.value, at+": hint")
	}
	if k, ok := byKey["required"]; ok {
		f.Required = p.inputBool(k.value, at+": required")
	}
	if k, ok := byKey["ask"]; ok {
		f.Ask = p.inputBool(k.value, at+": ask")
	}
	optE, hasOptions := byKey["options"]
	switch {
	case kind == runinput.Choice && !hasOptions:
		p.errorf(e.keyN, "%s: a choice needs options, such as options: [small, large]", at)
	case kind == runinput.Choice:
		f.Options = p.inputOptions(optE.value, at)
	case hasOptions:
		p.errorf(optE.keyN, "%s: options are allowed only on a field of kind choice", at)
	}
	fieldsE, hasFields := byKey["fields"]
	switch {
	case kind == runinput.Objects && nested:
		p.errorf(kindE.value, "%s: a list of objects cannot hold another list of objects (one level of nesting is supported); describe its parts as fields of their own, or as a list of texts", at)
	case kind == runinput.Objects && !hasFields:
		p.errorf(e.keyN, "%s: a list of objects needs fields: the fields of each object", at)
	case kind == runinput.Objects:
		f.Fields = p.inputFields(fieldsE.value, at+": fields", true)
	case hasFields:
		p.errorf(fieldsE.keyN, "%s: fields are allowed only on a field of kind objects", at)
	}
	defE, hasDefault := byKey["default"]
	if hasDefault {
		if kind == runinput.Objects {
			p.errorf(defE.keyN, "%s: a list of objects takes no default", at)
		} else if len(p.problems) == before {
			f.Default = p.inputDefault(f, defE.value, at)
		}
	}
	if !f.Ask && f.Default == nil && !hasDefault {
		p.errorf(e.keyN, "%s: a field with ask: false is never asked, so it needs a default: the fixed value it always gets", at)
	}
	if len(p.problems) > before {
		return nil
	}
	return f
}

// inputWords reads a label or hint: one text, or a mapping from language
// code to text.
func (p *parser) inputWords(n *yaml.Node, what string) runinput.Words {
	if n.Kind == yaml.ScalarNode && n.Tag != "!!null" {
		if strings.TrimSpace(n.Value) == "" {
			p.errorf(n, "%s must not be empty", what)
		}
		return runinput.Words{Plain: n.Value}
	}
	if n.Kind != yaml.MappingNode {
		p.errorf(n, "%s must be a text, or a text per language such as {en: Title, uk: Назва}", what)
		return runinput.Words{}
	}
	entries, _ := p.mapping(n, what)
	w := runinput.Words{ByLang: map[string]string{}}
	for _, e := range entries {
		if _, known := language.Lookup(e.key); !known {
			p.errorf(e.keyN, "%s: %s", what, language.Unknown(e.key))
			continue
		}
		s, ok := p.scalar(e.value, what+": "+e.key)
		if !ok {
			continue
		}
		if strings.TrimSpace(s) == "" {
			p.errorf(e.value, "%s: %s must not be empty", what, e.key)
			continue
		}
		w.ByLang[e.key] = s
		w.Order = append(w.Order, e.key)
	}
	if len(entries) == 0 {
		p.errorf(n, "%s must give at least one language", what)
	}
	return w
}

func (p *parser) inputBool(n *yaml.Node, what string) bool {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
		p.errorf(n, "%s must be true or false", what)
		return false
	}
	return n.Value == "true"
}

func (p *parser) inputOptions(n *yaml.Node, what string) []string {
	if n.Kind != yaml.SequenceNode {
		p.errorf(n, "%s: options must be a list, such as [small, large]", what)
		return nil
	}
	var out []string
	for _, item := range n.Content {
		s, ok := p.scalar(resolve(item), what+": option")
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	if msg := ask.CheckOptions(out); msg != "" {
		p.errorf(n, "%s: %s", what, msg)
		return nil
	}
	return out
}

// inputDefault reads a default in the shape its kind needs.
func (p *parser) inputDefault(f *runinput.Field, n *yaml.Node, what string) any {
	what += ": default"
	if f.Kind == runinput.List {
		if n.Kind != yaml.SequenceNode {
			p.errorf(n, "%s of a list must be a list of texts, such as [one, two]", what)
			return nil
		}
		out := []any{}
		for _, item := range n.Content {
			s, ok := p.scalar(resolve(item), what+" item")
			if !ok {
				return nil
			}
			out = append(out, s)
		}
		return out
	}
	s, ok := p.scalar(n, what)
	if !ok {
		return nil
	}
	switch f.Kind {
	case runinput.Number:
		if n.Tag != "!!int" && n.Tag != "!!float" {
			p.errorf(n, "%s of a number must be a number, not %q", what, s)
			return nil
		}
		var num json.Number
		if json.Unmarshal([]byte(s), &num) != nil {
			p.errorf(n, "%s %q: write the number in decimal digits, such as 3 or 2.5", what, s)
			return nil
		}
		return num
	case runinput.YesNo:
		if n.Tag != "!!bool" {
			p.errorf(n, "%s of a yes/no field must be true or false, not %q", what, s)
			return nil
		}
		return s == "true"
	case runinput.Text:
		if strings.ContainsAny(s, "\r\n") {
			p.errorf(n, "%s of a text field must be one line", what)
			return nil
		}
	}
	v, err := runinput.ParseDefault(f, s)
	if err != nil {
		p.errorf(n, "%s: %v", what, err)
		return nil
	}
	return v
}
