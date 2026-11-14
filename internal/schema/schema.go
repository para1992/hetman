// Package schema reads and applies a small subset of JSON Schema, enough to
// describe the output of an agent step.
//
// Supported keywords: type (one of object, array, string, number, integer,
// boolean, null), properties, required, enum, items and
// additionalProperties (true, false or a schema). The annotations title,
// description and $schema are accepted and ignored. Any other keyword is
// rejected, so a schema never silently means less than it says.
package schema

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Types a schema may name.
var types = []string{"object", "array", "string", "number", "integer", "boolean", "null"}

// Schema is a parsed schema. The zero value accepts anything.
type Schema struct {
	Type       string
	Properties map[string]*Schema
	Order      []string // property names in file order
	Required   []string
	Enum       []any // values decoded as encoding/json does
	Items      *Schema
	// NoAdditional is set by "additionalProperties": false. Additional,
	// when not nil, is the schema of properties not listed in Properties.
	NoAdditional bool
	Additional   *Schema

	Line, Col int // where the schema object starts in its file
}

// Problem is one reason a schema file is not accepted, with its position
// in the file (zero when unknown).
type Problem struct {
	Line, Col int
	Msg       string
}

// Problems is the error returned by Parse.
type Problems []Problem

func (ps Problems) Error() string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteByte('\n')
		}
		if p.Line > 0 {
			fmt.Fprintf(&b, "%d:%d: ", p.Line, p.Col)
		}
		b.WriteString(p.Msg)
	}
	return b.String()
}

// Parse reads a schema from JSON text.
func Parse(data []byte) (*Schema, error) {
	if err := json.Unmarshal(data, new(any)); err != nil {
		p := Problem{Msg: "not valid JSON: " + err.Error()}
		var se *json.SyntaxError
		if errors.As(err, &se) {
			p.Line, p.Col = position(data, se.Offset-1)
		}
		return nil, Problems{p}
	}
	// The text is valid JSON, which is also YAML; the YAML parser keeps the
	// line and column of every value for the messages below.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return nil, Problems{{Msg: "cannot read the schema"}}
	}
	var ps Problems
	s := parseNode(doc.Content[0], "schema", &ps)
	if len(ps) > 0 {
		return nil, ps
	}
	return s, nil
}

// position turns a byte offset into a line and column, both from 1.
func position(data []byte, offset int64) (int, int) {
	line, col := 1, 1
	for i := int64(0); i < offset && i < int64(len(data)); i++ {
		if data[i] == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return line, col
}

func errorf(ps *Problems, n *yaml.Node, format string, args ...any) {
	*ps = append(*ps, Problem{Line: n.Line, Col: n.Column, Msg: fmt.Sprintf(format, args...)})
}

func parseNode(n *yaml.Node, where string, ps *Problems) *Schema {
	s := &Schema{Line: n.Line, Col: n.Column}
	if n.Kind != yaml.MappingNode {
		errorf(ps, n, "%s must be an object", where)
		return s
	}
	seen := map[string]bool{}
	var requiredN *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if seen[k.Value] {
			errorf(ps, k, "%s: duplicate key %q", where, k.Value)
			continue
		}
		seen[k.Value] = true
		switch k.Value {
		case "type":
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" || !contains(types, v.Value) {
				errorf(ps, v, "%s: type must be one of %s", where, strings.Join(types, ", "))
				continue
			}
			s.Type = v.Value
		case "properties":
			if v.Kind != yaml.MappingNode {
				errorf(ps, v, "%s: properties must be an object", where)
				continue
			}
			s.Properties = map[string]*Schema{}
			for j := 0; j+1 < len(v.Content); j += 2 {
				name := v.Content[j].Value
				if _, dup := s.Properties[name]; dup {
					errorf(ps, v.Content[j], "%s: duplicate property %q", where, name)
					continue
				}
				s.Properties[name] = parseNode(v.Content[j+1], fmt.Sprintf("property %q", name), ps)
				s.Order = append(s.Order, name)
			}
		case "required":
			requiredN = v
			if v.Kind != yaml.SequenceNode {
				errorf(ps, v, "%s: required must be a list of property names", where)
				continue
			}
			for _, item := range v.Content {
				if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
					errorf(ps, item, "%s: required must list property names", where)
					continue
				}
				s.Required = append(s.Required, item.Value)
			}
		case "enum":
			if v.Kind != yaml.SequenceNode || len(v.Content) == 0 {
				errorf(ps, v, "%s: enum must be a non-empty list", where)
				continue
			}
			for _, item := range v.Content {
				s.Enum = append(s.Enum, value(item))
			}
		case "items":
			s.Items = parseNode(v, where+" items", ps)
		case "additionalProperties":
			switch {
			case v.Kind == yaml.ScalarNode && v.Tag == "!!bool":
				s.NoAdditional = v.Value == "false"
			case v.Kind == yaml.MappingNode:
				s.Additional = parseNode(v, where+" additionalProperties", ps)
			default:
				errorf(ps, v, "%s: additionalProperties must be true, false or a schema", where)
			}
		case "title", "description", "$schema":
			// Annotations: shown to people and models, never checked.
		default:
			errorf(ps, k, "%s: keyword %q is not supported (supported: type, properties, required, enum, items, additionalProperties, title, description)", where, k.Value)
		}
	}
	if s.Type != "" && s.Type != "object" {
		for _, kw := range []string{"properties", "required", "additionalProperties"} {
			if seen[kw] {
				errorf(ps, n, "%s: %s applies only to type object", where, kw)
			}
		}
	}
	if s.Type != "" && s.Type != "array" && seen["items"] {
		errorf(ps, n, "%s: items applies only to type array", where)
	}
	for _, name := range s.Required {
		if s.Properties != nil && s.Properties[name] == nil && s.NoAdditional {
			errorf(ps, requiredN, "%s: required property %q is not listed in properties and additional properties are not allowed", where, name)
		}
	}
	for _, e := range s.Enum {
		if s.Type != "" && typeOf(e) != s.Type && !(s.Type == "number" && typeOf(e) == "integer") {
			errorf(ps, n, "%s: enum value %s is not of type %s", where, show(e), s.Type)
		}
	}
	return s
}

// value converts a YAML node read from JSON text into the value
// encoding/json would produce: numbers are float64.
func value(n *yaml.Node) any {
	switch n.Kind {
	case yaml.SequenceNode:
		out := make([]any, len(n.Content))
		for i, c := range n.Content {
			out[i] = value(c)
		}
		return out
	case yaml.MappingNode:
		out := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			out[n.Content[i].Value] = value(n.Content[i+1])
		}
		return out
	}
	switch n.Tag {
	case "!!null":
		return nil
	case "!!bool":
		return n.Value == "true"
	case "!!int", "!!float":
		f, _ := strconv.ParseFloat(n.Value, 64)
		return f
	}
	return n.Value
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// typeOf names the JSON type of a value decoded by encoding/json; a whole
// number is "integer".
func typeOf(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if x == math.Trunc(x) && !math.IsInf(x, 0) {
			return "integer"
		}
		return "number"
	case json.Number:
		if _, err := x.Int64(); err == nil {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func show(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// maxProblems limits how many problems Validate reports.
const maxProblems = 10

// Validate checks a value decoded by encoding/json against the schema and
// returns what is wrong, one problem per entry, each starting with the path
// of the value ("$" is the whole value). Nil means the value is valid.
func (s *Schema) Validate(v any) []string {
	var out []string
	s.validate(v, "$", &out)
	if len(out) > maxProblems {
		out = append(out[:maxProblems], fmt.Sprintf("and %d more problems", len(out)-maxProblems))
	}
	return out
}

func (s *Schema) validate(v any, path string, out *[]string) {
	if s == nil {
		return
	}
	if s.Type != "" {
		t := typeOf(v)
		if t != s.Type && !(s.Type == "number" && t == "integer") {
			*out = append(*out, fmt.Sprintf("%s: must be of type %s, got %s", path, s.Type, t))
			return
		}
	}
	if len(s.Enum) > 0 {
		found := false
		for _, e := range s.Enum {
			if reflect.DeepEqual(e, v) {
				found = true
				break
			}
		}
		if !found {
			allowed := make([]string, len(s.Enum))
			for i, e := range s.Enum {
				allowed[i] = show(e)
			}
			*out = append(*out, fmt.Sprintf("%s: must be one of %s, got %s", path, strings.Join(allowed, ", "), show(v)))
		}
	}
	switch x := v.(type) {
	case map[string]any:
		for _, name := range s.Required {
			if _, ok := x[name]; !ok {
				*out = append(*out, fmt.Sprintf("%s: missing required property %q", path, name))
			}
		}
		names := make([]string, 0, len(x))
		for name := range x {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			sub := path + "." + name
			if p := s.Properties[name]; p != nil {
				p.validate(x[name], sub, out)
			} else if s.NoAdditional {
				*out = append(*out, fmt.Sprintf("%s: property %q is not allowed", path, name))
			} else {
				s.Additional.validate(x[name], sub, out)
			}
		}
	case []any:
		for i, item := range x {
			s.Items.validate(item, fmt.Sprintf("%s[%d]", path, i), out)
		}
	}
}

// Strings returns the enum values of a schema when all of them are strings.
func (s *Schema) Strings() ([]string, bool) {
	if s == nil || len(s.Enum) == 0 {
		return nil, false
	}
	out := make([]string, len(s.Enum))
	for i, e := range s.Enum {
		str, ok := e.(string)
		if !ok {
			return nil, false
		}
		out[i] = str
	}
	return out, true
}

// IsRequired reports whether the schema requires the named property.
func (s *Schema) IsRequired(name string) bool {
	return s != nil && contains(s.Required, name)
}
