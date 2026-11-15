// Package runinput describes the run input a flow expects, as its orders
// declare it in the top-level input section, and checks a given input
// against that description.
//
// The description is what a form is made from: the terminal form of
// hetman run -ask and hetman task new, and the form of the web page. All of
// them, and hetman run -input and hetman task add, check an input with the
// same code: Check.
package runinput

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"hetman/internal/i18n"
	"hetman/internal/language"
)

// Field kinds.
const (
	Text     = "text"     // one line of text
	LongText = "longtext" // text of several lines
	Number   = "number"   // a number
	YesNo    = "yesno"    // true or false
	Choice   = "choice"   // one of Options
	List     = "list"     // a list of texts
	Objects  = "objects"  // a list of objects, each with Fields
)

// Kinds lists the field kinds.
var Kinds = []string{Text, LongText, Number, YesNo, Choice, List, Objects}

// ReservedPrefix starts the names of fields the engine adds to a run input
// itself; a declared field may not use it.
const ReservedPrefix = "hetman_"

// NamePattern is what a field name looks like. A name has no dots, since
// a placeholder such as {{input.tasks}} splits at them.
var NamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Words is text for a person: one text for every language, or one per
// language code.
type Words struct {
	Plain  string            // the text when it is not given per language
	ByLang map[string]string // language code to text; nil when Plain is used
	Order  []string          // the codes of ByLang in the order written
}

// In picks the text for a language: its own, else English, else the first
// one written.
func (w Words) In(code string) string {
	if w.ByLang == nil {
		return w.Plain
	}
	if s, ok := w.ByLang[code]; ok {
		return s
	}
	if s, ok := w.ByLang[language.Default]; ok {
		return s
	}
	if len(w.Order) > 0 {
		return w.ByLang[w.Order[0]]
	}
	return ""
}

// Empty reports whether no text is given.
func (w Words) Empty() bool { return w.Plain == "" && len(w.ByLang) == 0 }

// Field is one field of the run input.
type Field struct {
	Name     string
	Kind     string
	Label    Words // the question or label shown; the name when empty
	Hint     Words
	Required bool
	// Default is the value an absent field gets, as JSON reads it: a
	// string, a json.Number, a bool or a list of strings; nil for none.
	Default any
	Ask     bool     // false: the field is never asked; it gets Default
	Options []string // a choice
	Fields  []*Field // a list of objects: the fields of each object
}

// LabelIn is the label in a language, or the field name.
func (f *Field) LabelIn(code string) string {
	if s := f.Label.In(code); s != "" {
		return s
	}
	return f.Name
}

// Spec is the run input a flow describes: its fields in the order written.
type Spec struct {
	Fields []*Field
}

// Field finds a top-level field by name.
func (s *Spec) Field(name string) *Field {
	for _, f := range s.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// Invalid is the error of an input that does not fit its description.
type Invalid struct {
	Problems []i18n.Message // in field order
}

func (e *Invalid) Error() string { return e.Localize(nil) }

// Localize says the problems in the language of p.
func (e *Invalid) Localize(p *i18n.Printer) string {
	return p.T("form.invalid", "problems", strings.Join(e.Lines(p), "; "))
}

// Lines says each problem on its own.
func (e *Invalid) Lines(p *i18n.Printer) []string {
	out := make([]string, len(e.Problems))
	for i, m := range e.Problems {
		out[i] = m.In(p)
	}
	return out
}

// ErrInvalid is matched by errors.Is for every *Invalid.
var ErrInvalid = errors.New("invalid run input")

func (e *Invalid) Is(target error) bool { return target == ErrInvalid }

// Check checks a run input against the description and returns it with the
// default of every absent field filled in, its fields in the order the
// description gives them, as compact JSON. An empty raw is an empty
// object. Every problem names the field, such as tasks[2].title.
func Check(s *Spec, raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, &Invalid{Problems: []i18n.Message{i18n.M("form.not_json", "error", err.Error())}}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, &Invalid{Problems: []i18n.Message{i18n.M("form.not_object")}}
	}
	c := &checker{}
	out := c.object(s.Fields, m, "")
	if len(c.problems) > 0 {
		return nil, &Invalid{Problems: c.problems}
	}
	return json.Marshal(out)
}

type checker struct {
	problems []i18n.Message
}

func (c *checker) add(key string, args ...any) {
	c.problems = append(c.problems, i18n.M(key, args...))
}

// object checks one object against fields; at is the path of the object,
// empty for the input itself.
func (c *checker) object(fields []*Field, m map[string]any, at string) Object {
	var out Object
	known := map[string]bool{}
	for _, f := range fields {
		known[f.Name] = true
		path := f.Name
		if at != "" {
			path = at + "." + f.Name
		}
		v, ok := m[f.Name]
		if !ok || v == nil {
			if f.Default != nil {
				out = append(out, Pair{f.Name, f.Default})
			} else if f.Required {
				c.add("form.missing", "field", path)
			}
			continue
		}
		if v, ok := c.value(f, v, path); ok {
			out = append(out, Pair{f.Name, v})
		}
	}
	var unknown []string
	for k := range m {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	var names []string
	for _, f := range fields {
		names = append(names, f.Name)
	}
	for _, k := range unknown {
		path := k
		if at != "" {
			path = at + "." + k
		}
		c.add("form.unknown", "field", path, "fields", strings.Join(names, ", "))
	}
	return out
}

// value checks the value of one field.
func (c *checker) value(f *Field, v any, path string) (any, bool) {
	switch f.Kind {
	case Text, LongText:
		s, ok := v.(string)
		switch {
		case !ok:
			c.add("form.want_text", "field", path)
			return nil, false
		case f.Kind == Text && strings.ContainsAny(s, "\r\n"):
			c.add("form.one_line", "field", path)
			return nil, false
		case f.Required && strings.TrimSpace(s) == "":
			c.add("form.empty", "field", path)
			return nil, false
		}
		return s, true
	case Number:
		n, ok := v.(json.Number)
		if !ok {
			c.add("form.want_number", "field", path)
			return nil, false
		}
		return n, true
	case YesNo:
		b, ok := v.(bool)
		if !ok {
			c.add("form.want_yesno", "field", path)
			return nil, false
		}
		return b, true
	case Choice:
		s, ok := v.(string)
		if !ok || !contains(f.Options, s) {
			c.add("form.want_option", "field", path, "options", strings.Join(f.Options, ", "))
			return nil, false
		}
		return s, true
	case List:
		list, ok := v.([]any)
		if !ok {
			c.add("form.want_list", "field", path)
			return nil, false
		}
		out := []any{}
		good := true
		for i, item := range list {
			s, isText := item.(string)
			if !isText || strings.ContainsAny(s, "\r\n") {
				c.add("form.want_item_text", "field", fmt.Sprintf("%s[%d]", path, i+1))
				good = false
				continue
			}
			out = append(out, s)
		}
		if good && f.Required && len(out) == 0 {
			c.add("form.empty_list", "field", path)
			return nil, false
		}
		return out, good
	case Objects:
		list, ok := v.([]any)
		if !ok {
			c.add("form.want_objects", "field", path)
			return nil, false
		}
		out := []any{}
		before := len(c.problems)
		for i, item := range list {
			at := fmt.Sprintf("%s[%d]", path, i+1)
			m, isObject := item.(map[string]any)
			if !isObject {
				c.add("form.want_item_object", "field", at)
				continue
			}
			out = append(out, c.object(f.Fields, m, at))
		}
		if len(c.problems) > before {
			return nil, false
		}
		if f.Required && len(out) == 0 {
			c.add("form.empty_list", "field", path)
			return nil, false
		}
		return out, true
	}
	return v, true
}

// Pair is one field of an Object.
type Pair struct {
	Key   string
	Value any
}

// Object is a JSON object that keeps the order of its fields.
type Object []Pair

// MarshalJSON writes the fields in order.
func (o Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(p.Key)
		b.Write(k)
		b.WriteByte(':')
		v, err := json.Marshal(p.Value)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Get returns the value of a field.
func (o Object) Get(key string) (any, bool) {
	for _, p := range o {
		if p.Key == key {
			return p.Value, true
		}
	}
	return nil, false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Write renders a checked input for a person: one line per field, the
// items of a list below it, in the language of p. Fields the description
// does not ask for are shown too, marked as fixed.
func Write(p *i18n.Printer, s *Spec, raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return string(raw)
	}
	var b strings.Builder
	writeObject(&b, p, s.Fields, m, "")
	return b.String()
}

func writeObject(b *strings.Builder, p *i18n.Printer, fields []*Field, m map[string]any, indent string) {
	code := p.Lang()
	for _, f := range fields {
		v, ok := m[f.Name]
		if !ok {
			continue
		}
		label := f.LabelIn(code)
		if !f.Ask {
			label += " " + p.T("form.fixed")
		}
		switch f.Kind {
		case LongText:
			s, _ := v.(string)
			if !strings.Contains(s, "\n") {
				fmt.Fprintf(b, "%s%s: %s\n", indent, label, s)
				continue
			}
			fmt.Fprintf(b, "%s%s:\n", indent, label)
			for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
				fmt.Fprintf(b, "%s    %s\n", indent, line)
			}
		case YesNo:
			word := p.T("form.no")
			if v == true {
				word = p.T("form.yes")
			}
			fmt.Fprintf(b, "%s%s: %s\n", indent, label, word)
		case List:
			list, _ := v.([]any)
			if len(list) == 0 {
				fmt.Fprintf(b, "%s%s: %s\n", indent, label, p.T("form.none"))
				continue
			}
			fmt.Fprintf(b, "%s%s:\n", indent, label)
			for _, item := range list {
				fmt.Fprintf(b, "%s  - %v\n", indent, item)
			}
		case Objects:
			list, _ := v.([]any)
			if len(list) == 0 {
				fmt.Fprintf(b, "%s%s: %s\n", indent, label, p.T("form.none"))
				continue
			}
			fmt.Fprintf(b, "%s%s:\n", indent, label)
			for i, item := range list {
				obj, _ := item.(map[string]any)
				fmt.Fprintf(b, "%s  %s\n", indent, p.T("form.item", "label", label, "n", i+1))
				writeObject(b, p, f.Fields, obj, indent+"    ")
			}
		default:
			fmt.Fprintf(b, "%s%s: %v\n", indent, label, v)
		}
	}
}

// ParseDefault reads a default written as text for a field of a kind, as
// an operation gives it: a number, yes or no (true or false), one of the
// options, or for a list its items separated by commas.
func ParseDefault(f *Field, s string) (any, error) {
	switch f.Kind {
	case Text, LongText:
		return s, nil
	case Number:
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return nil, fmt.Errorf("the default of a number must be a number, not %q", s)
		}
		return json.Number(s), nil
	case YesNo:
		switch strings.ToLower(s) {
		case "true", "yes", "y":
			return true, nil
		case "false", "no", "n":
			return false, nil
		}
		return nil, fmt.Errorf("the default of a yes/no field must be true or false, not %q", s)
	case Choice:
		if !contains(f.Options, s) {
			return nil, fmt.Errorf("the default %q is not one of the options %s", s, strings.Join(f.Options, ", "))
		}
		return s, nil
	case List:
		var out []any
		for _, item := range strings.Split(s, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("a %s field takes no default", f.Kind)
}
