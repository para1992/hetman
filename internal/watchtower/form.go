package watchtower

import (
	"strings"

	"hetman/internal/i18n"
	"hetman/internal/runinput"
)

// FormField is one field of the run input as the start form shows it, in
// the language of the page. Fields that are never asked are left out: the
// server gives them their default.
type FormField struct {
	Name     string      `json:"name"`
	Kind     string      `json:"kind"`
	Label    string      `json:"label"`
	Hint     string      `json:"hint,omitempty"`
	Required bool        `json:"required"`
	Options  []string    `json:"options,omitempty"`
	Default  any         `json:"default,omitempty"`
	Fields   []FormField `json:"fields,omitempty"`
}

// FieldProblem is one problem of a run input, placed at the field it names
// so that the page can show it there.
type FieldProblem struct {
	Path  string `json:"path"`  // the field as the check names it, such as tasks[2].title; empty for the whole input
	Text  string `json:"text"`  // the problem, the field named by its labels, such as Tasks 2 › Title is required
	Short string `json:"short"` // the problem, the field named by its own label only, for beside the field
}

// fieldProblems says the problems of a run input field by field, naming
// each field by the labels a person sees on the form.
func fieldProblems(p *i18n.Printer, s *runinput.Spec, inv *runinput.Invalid) []FieldProblem {
	out := []FieldProblem{}
	for _, m := range inv.Problems {
		fp := FieldProblem{Text: m.In(p), Short: m.In(p)}
		for i := 0; i+1 < len(m.Args); i += 2 {
			if m.Args[i] != "field" {
				continue
			}
			path, _ := m.Args[i+1].(string)
			fp.Path = path
			long, short := pathLabels(p, s.Fields, path)
			if long != "" {
				fp.Text = withField(p, m, long)
				fp.Short = withField(p, m, short)
			}
		}
		out = append(out, fp)
	}
	return out
}

// withField renders a message with its field named differently.
func withField(p *i18n.Printer, m i18n.Message, name string) string {
	args := append([]any{}, m.Args...)
	for i := 0; i+1 < len(args); i += 2 {
		if args[i] == "field" {
			args[i+1] = name
		}
	}
	return i18n.M(m.Key, args...).In(p)
}

// pathLabels names a field path such as tasks[2].title by its labels: the
// whole way (Tasks 2 › Title) and the last field alone (Title). Both are
// empty when the path names no field of the description.
func pathLabels(p *i18n.Printer, fields []*runinput.Field, path string) (string, string) {
	code := p.Lang()
	var parts []string
	short := ""
	for _, seg := range strings.Split(path, ".") {
		name, index, _ := strings.Cut(seg, "[")
		var f *runinput.Field
		for _, x := range fields {
			if x.Name == name {
				f = x
			}
		}
		if f == nil {
			return "", ""
		}
		label := f.LabelIn(code)
		short = label
		if index != "" {
			n := strings.TrimSuffix(index, "]")
			label = p.T("form.item", "label", label, "n", n)
			if f.Kind == runinput.Objects {
				short = label
			}
		}
		parts = append(parts, label)
		fields = f.Fields
	}
	return strings.Join(parts, " › "), short
}

// formFields describes the fields of a run input for the page.
func formFields(fields []*runinput.Field, code string) []FormField {
	out := []FormField{}
	for _, f := range fields {
		if !f.Ask {
			continue
		}
		ff := FormField{Name: f.Name, Kind: f.Kind, Label: f.LabelIn(code), Hint: f.Hint.In(code), Required: f.Required,
			Options: f.Options, Default: f.Default}
		if f.Kind == runinput.Objects {
			ff.Fields = formFields(f.Fields, code)
		}
		out = append(out, ff)
	}
	return out
}
