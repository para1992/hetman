package watchtower

import (
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
