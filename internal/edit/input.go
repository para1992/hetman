package edit

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"hetman/internal/ask"
	"hetman/internal/i18n"
	"hetman/internal/language"
	"hetman/internal/runinput"
)

// inputOrder is the order the keys of a run input field are written in.
var inputOrder = []string{"kind", "label", "hint", "required", "ask", "default", "options", "fields"}

func boolNode(b bool) *yaml.Node {
	v := "false"
	if b {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

// valueNode writes a default as JSON reads it.
func valueNode(v any) *yaml.Node {
	switch x := v.(type) {
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(string(x), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: string(x)}
	case bool:
		return boolNode(x)
	case []any:
		var list []string
		for _, s := range x {
			list = append(list, fmt.Sprint(s))
		}
		return flowSeq(list)
	}
	return str(fmt.Sprint(v))
}

// words writes a label or hint: plain, or for one language.
func words(text, lang string) *yaml.Node {
	if lang == "" {
		return str(text)
	}
	return mapping(true, str(lang), str(text))
}

func checkLanguage(code string) error {
	if code == "" {
		return nil
	}
	if _, ok := language.Lookup(code); !ok {
		return fmt.Errorf("%s", language.Unknown(code))
	}
	return nil
}

// inputSection is the input mapping of the orders, or nil.
func (e *editing) inputSection() *yaml.Node {
	_, sec := lookup(e.root(), "input")
	if sec == nil || sec.Kind != yaml.MappingNode {
		return nil
	}
	return sec
}

// inputObjects returns the fields mapping of a list of objects.
func (e *editing) inputObjects(of string) (*yaml.Node, error) {
	sec := e.inputSection()
	_, parent := lookup(sec, of)
	if parent == nil {
		return nil, fmt.Errorf("the run input has no field %q (its fields are: %s)", of, orNone(keysOf(sec)))
	}
	if parent.Kind != yaml.MappingNode || scalarOf(parent, "kind") != runinput.Objects {
		return nil, fmt.Errorf("the run input field %q is not a list of objects", of)
	}
	_, fields := lookup(parent, "fields")
	if fields == nil || fields.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the list of objects %q has no fields mapping", of)
	}
	return fields, nil
}

func keysOf(m *yaml.Node) []string {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	return keys(m)
}

// inputField finds the mapping of the field an operation names, and the
// mapping that holds it.
func (e *editing) inputField() (field, parent *yaml.Node, err error) {
	op := e.op
	parent = e.inputSection()
	if parent == nil {
		return nil, nil, fmt.Errorf("the orders describe no run input yet; add a field with input-add")
	}
	if op.Of != "" {
		if parent, err = e.inputObjects(op.Of); err != nil {
			return nil, nil, err
		}
	}
	_, field = lookup(parent, op.Name)
	if field == nil {
		where := "the run input"
		if op.Of != "" {
			where = "the list of objects " + op.Of
		}
		return nil, nil, fmt.Errorf("%s has no field %q (its fields are: %s)", where, op.Name, orNone(keys(parent)))
	}
	if field.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("the run input field %q is not a mapping", op.Name)
	}
	return field, parent, nil
}

// optionsOf reads the options of a field as written.
func optionsOf(field *yaml.Node) []string {
	_, n := lookup(field, "options")
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var out []string
	for _, it := range n.Content {
		out = append(out, it.Value)
	}
	return out
}

func (e *editing) inputAdd() error {
	op := e.op
	switch {
	case !runinput.NamePattern.MatchString(op.Name):
		return fmt.Errorf("field name %q: use letters, digits, '_' and '-' only, starting with a letter or digit", op.Name)
	case strings.HasPrefix(op.Name, runinput.ReservedPrefix):
		return fmt.Errorf("field names starting with %s are kept for what hetman adds to the run input", runinput.ReservedPrefix)
	case !contains(runinput.Kinds, op.Kind):
		return fmt.Errorf("kind must be one of %s, not %q", strings.Join(runinput.Kinds, ", "), op.Kind)
	case op.Kind == runinput.Objects:
		return fmt.Errorf("a list of objects is made by adding its first field: input-add FIELD KIND with of %s", op.Name)
	case op.Kind == runinput.Choice:
		if msg := ask.CheckOptions(op.Options); msg != "" {
			return fmt.Errorf("%s", msg)
		}
	case len(op.Options) > 0:
		return fmt.Errorf("options are given only with the kind choice")
	}
	if err := checkLanguage(op.Language); err != nil {
		return err
	}
	if op.Of != "" && !runinput.NamePattern.MatchString(op.Of) {
		return fmt.Errorf("list name %q: use letters, digits, '_' and '-' only, starting with a letter or digit", op.Of)
	}
	v := mapping(true, str("kind"), str(op.Kind))
	if op.Label != "" {
		v.Content = append(v.Content, str("label"), words(op.Label, op.Language))
	}
	if op.Hint != "" {
		v.Content = append(v.Content, str("hint"), words(op.Hint, op.Language))
	}
	if op.Required != nil && *op.Required {
		v.Content = append(v.Content, str("required"), boolNode(true))
	}
	if op.Ask != nil && !*op.Ask {
		if op.Default == "" {
			return fmt.Errorf("a field that is not asked needs a default: the fixed value it always gets")
		}
		v.Content = append(v.Content, str("ask"), boolNode(false))
	}
	if op.Default != "" {
		d, err := runinput.ParseDefault(&runinput.Field{Kind: op.Kind, Options: op.Options}, op.Default)
		if err != nil {
			return err
		}
		v.Content = append(v.Content, str("default"), valueNode(d))
	}
	if len(op.Options) > 0 {
		v.Content = append(v.Content, str("options"), flowSeq(op.Options))
	}
	sec := e.inputSection()
	if op.Of == "" {
		if _, old := lookup(sec, op.Name); old != nil {
			return fmt.Errorf("the run input already has a field %q", op.Name)
		}
		if _, added := e.section("input", op.Name, v); !added {
			e.t.insert(sec, len(sec.Content)/2, str(op.Name), v)
		}
		return nil
	}
	if _, parent := lookup(sec, op.Of); parent == nil {
		list := mapping(false, str("kind"), str(runinput.Objects), str("fields"), mapping(false, str(op.Name), v))
		if _, added := e.section("input", op.Of, list); !added {
			e.t.insert(sec, len(sec.Content)/2, str(op.Of), list)
		}
		return nil
	}
	fields, err := e.inputObjects(op.Of)
	if err != nil {
		return err
	}
	if _, old := lookup(fields, op.Name); old != nil {
		return fmt.Errorf("the list of objects %q already has a field %q", op.Of, op.Name)
	}
	e.t.insert(fields, len(fields.Content)/2, str(op.Name), v)
	return nil
}

func (e *editing) inputRemove() error {
	op := e.op
	_, parent, err := e.inputField()
	if err != nil {
		return err
	}
	i, _ := lookup(parent, op.Name)
	if len(parent.Content) == 2 {
		if op.Of != "" {
			return fmt.Errorf("a list of objects keeps at least one field; remove the list %s instead", op.Of)
		}
		e.unset(e.root(), "input")
		return nil
	}
	e.t.remove(parent, i)
	return nil
}

// parseBool reads true or false as an operation gives it.
func parseBool(field, s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "y":
		return true, nil
	case "false", "no", "n":
		return false, nil
	}
	return false, fmt.Errorf("%s must be true or false, not %q", field, s)
}

func (e *editing) inputSet() error {
	op := e.op
	field, _, err := e.inputField()
	if err != nil {
		return err
	}
	if err := checkLanguage(op.Language); err != nil {
		return err
	}
	if op.Language != "" && op.Field != "label" && op.Field != "hint" {
		return fmt.Errorf("language is given only with label or hint")
	}
	kind := scalarOf(field, "kind")
	switch op.Field {
	case "label", "hint":
		_, cur := lookup(field, op.Field)
		switch {
		case op.Language == "" && cur != nil && cur.Kind == yaml.MappingNode:
			return fmt.Errorf("the %s of %s is written per language (%s); name the language", op.Field, op.Name, strings.Join(keys(cur), ", "))
		case op.Language == "":
			e.set(field, op.Field, str(op.Value), inputOrder)
		case cur != nil && cur.Kind == yaml.MappingNode:
			e.set(cur, op.Language, str(op.Value), nil)
		case cur != nil && cur.Kind == yaml.ScalarNode && op.Language != language.Default:
			// The text written for every language is kept as the English one.
			e.set(field, op.Field, mapping(true, str(language.Default), str(cur.Value), str(op.Language), str(op.Value)), inputOrder)
		default:
			e.set(field, op.Field, words(op.Value, op.Language), inputOrder)
		}
	case "required":
		b, err := parseBool("required", op.Value)
		if err != nil {
			return err
		}
		if b {
			e.set(field, "required", boolNode(true), inputOrder)
		} else {
			e.unset(field, "required")
		}
	case "ask":
		b, err := parseBool("ask", op.Value)
		if err != nil {
			return err
		}
		if b {
			e.unset(field, "ask")
		} else {
			e.set(field, "ask", boolNode(false), inputOrder)
		}
	case "default":
		if kind == runinput.Objects {
			return fmt.Errorf("a list of objects takes no default")
		}
		d, err := runinput.ParseDefault(&runinput.Field{Kind: kind, Options: optionsOf(field)}, op.Value)
		if err != nil {
			return err
		}
		e.set(field, "default", valueNode(d), inputOrder)
	}
	return nil
}

func (e *editing) inputUnset() error {
	op := e.op
	field, _, err := e.inputField()
	if err != nil {
		return err
	}
	if err := checkLanguage(op.Language); err != nil {
		return err
	}
	if op.Language != "" && op.Field == "default" {
		return fmt.Errorf("language is given only with label or hint")
	}
	_, cur := lookup(field, op.Field)
	switch {
	case cur == nil:
		return nil
	case op.Language == "":
		e.unset(field, op.Field)
	case cur.Kind != yaml.MappingNode:
		return fmt.Errorf("the %s of %s is one text for every language; remove it without language", op.Field, op.Name)
	case len(cur.Content) == 2 && cur.Content[0].Value == op.Language:
		e.unset(field, op.Field)
	default:
		e.unset(cur, op.Language)
	}
	return nil
}

// inputWords describes an operation on the run input.
func inputWords(p *i18n.Printer, op Op) string {
	field := p.T("op.words.input_field", "name", op.Name)
	if op.Of != "" {
		field = p.T("op.words.input_field_of", "name", op.Name, "of", op.Of)
	}
	switch op.Op {
	case OpInputAdd:
		var parts []string
		if op.Label != "" {
			parts = append(parts, p.T("op.words.input_label", "label", fmt.Sprintf("%q", op.Label)))
		}
		if op.Hint != "" {
			parts = append(parts, p.T("op.words.input_hint", "hint", fmt.Sprintf("%q", op.Hint)))
		}
		if op.Language != "" {
			parts = append(parts, p.T("op.words.input_in", "language", op.Language))
		}
		if op.Required != nil && *op.Required {
			parts = append(parts, p.T("op.words.input_required"))
		}
		if op.Default != "" {
			parts = append(parts, p.T("op.words.input_default", "value", op.Default))
		}
		if op.Ask != nil && !*op.Ask {
			parts = append(parts, p.T("op.words.input_fixed"))
		}
		if len(op.Options) > 0 {
			parts = append(parts, p.T("op.words.input_options", "options", strings.Join(op.Options, ", ")))
		}
		return p.T("op.words.input_add", "field", field, "kind", op.Kind, "more", optList(", ", parts))
	case OpInputRm:
		return p.T("op.words.input_remove", "field", field)
	case OpInputSet:
		if op.Language != "" {
			return p.T("op.words.input_set_in", "what", op.Field, "language", op.Language, "field", field, "value", op.Value)
		}
		return p.T("op.words.input_set", "what", op.Field, "field", field, "value", op.Value)
	}
	if op.Language != "" {
		return p.T("op.words.input_unset_in", "what", op.Field, "language", op.Language, "field", field)
	}
	return p.T("op.words.input_unset", "what", op.Field, "field", field)
}
