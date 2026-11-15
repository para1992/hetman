package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// An adapter that declares FeatureCatalog tells what its client offers:
// the models a step may name and the efforts each of them takes. The
// engine starts it with the single extra argument CatalogFlag; the adapter
// prints one Catalog line, makes no model request, and exits with code 0.
// It answers from the client's own listing when the client has one, else
// from what the adapter itself knows the client accepts.

// FeatureCatalog: the adapter answers CatalogFlag with the models and
// efforts its client offers, without a model request.
const FeatureCatalog = "catalog"

// CatalogFlag is the argument that asks an adapter for its client's
// catalogue.
const CatalogFlag = "--catalog"

// Catalog is what an adapter prints for CatalogFlag.
type Catalog struct {
	// Complete says the list holds every model the client accepts, so a
	// step naming another one fails. When false the client may accept
	// other names too, such as the full name of a model the list gives by
	// an alias.
	Complete bool           `json:"complete"`
	Models   []CatalogModel `json:"models"`
}

// CatalogModel is one model a step may name.
type CatalogModel struct {
	ID    string `json:"id"`              // exactly what goes into a step's model
	Label string `json:"label,omitempty"` // a name for people
	Group string `json:"group,omitempty"` // such as the provider
	// Efforts are the effort values the model takes. An empty list means
	// the model has no effort setting; null or absent means the adapter
	// cannot tell.
	Efforts []string `json:"efforts"`
}

// EffortsKnown reports whether the adapter said which efforts the model
// takes.
func (m CatalogModel) EffortsKnown() bool { return m.Efforts != nil }

// ParseCatalog reads the output of CatalogFlag.
func ParseCatalog(out []byte) (Catalog, error) {
	var c Catalog
	text := strings.TrimSpace(string(out))
	if text == "" {
		return c, errors.New("printed nothing for " + CatalogFlag)
	}
	if strings.Contains(text, "\n") {
		return c, errors.New("printed more than one line for " + CatalogFlag)
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("catalogue is not valid: %v", err)
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		switch {
		case strings.TrimSpace(m.ID) == "":
			return c, errors.New("catalogue has a model without an id")
		case seen[m.ID]:
			return c, fmt.Errorf("catalogue lists model %q twice", m.ID)
		}
		seen[m.ID] = true
		efforts := map[string]bool{}
		for _, e := range m.Efforts {
			if strings.TrimSpace(e) == "" || efforts[e] {
				return c, fmt.Errorf("catalogue: model %q has an empty or repeated effort", m.ID)
			}
			efforts[e] = true
		}
	}
	return c, nil
}

// Model returns the model with the id, or nil.
func (c *Catalog) Model(id string) *CatalogModel {
	if c == nil {
		return nil
	}
	for i := range c.Models {
		if c.Models[i].ID == id {
			return &c.Models[i]
		}
	}
	return nil
}

// IDs lists the ids of the models in catalogue order.
func (c *Catalog) IDs() []string {
	if c == nil {
		return nil
	}
	out := make([]string, len(c.Models))
	for i, m := range c.Models {
		out[i] = m.ID
	}
	return out
}

// Filter returns the models whose id, label or group holds every word of
// query, ignoring case; all of them for an empty query.
func (c *Catalog) Filter(query string) []CatalogModel {
	if c == nil {
		return nil
	}
	words := strings.Fields(strings.ToLower(query))
	var out []CatalogModel
	for _, m := range c.Models {
		hay := strings.ToLower(m.ID + " " + m.Label + " " + m.Group)
		ok := true
		for _, w := range words {
			ok = ok && strings.Contains(hay, w)
		}
		if ok {
			out = append(out, m)
		}
	}
	return out
}

// Finding is what a catalogue says about the model and effort of a step.
type Finding struct {
	Field   string   // FieldModel or FieldEffort
	Message string   // one line, without the suggestions
	Warning bool     // the catalogue may not know every model: not a rejection
	Suggest []string // the nearest valid names, best first
}

// Text is the message with the suggestions.
func (f Finding) Text() string {
	if len(f.Suggest) == 0 {
		return f.Message
	}
	return f.Message + "; " + SuggestText(f.Suggest)
}

// SuggestText says suggested names in a few words.
func SuggestText(names []string) string {
	return "nearest: " + strings.Join(names, ", ")
}

// Check compares a model and an effort with the catalogue. An empty model
// is the client's default, whose efforts the catalogue cannot tell.
func (c *Catalog) Check(model, effort string) []Finding {
	if c == nil || model == "" {
		return nil
	}
	m := c.Model(model)
	if m == nil {
		f := Finding{Field: FieldModel, Suggest: Suggest(model, c.IDs(), 3)}
		if c.Complete {
			f.Message = fmt.Sprintf("model %q is not in the catalogue of the executor", model)
		} else {
			f.Message = fmt.Sprintf("model %q is not in the catalogue of the executor, which may not list every name its client accepts", model)
			f.Warning = true
		}
		return []Finding{f}
	}
	if effort == "" || !m.EffortsKnown() {
		return nil
	}
	if len(m.Efforts) == 0 {
		return []Finding{{Field: FieldEffort, Message: fmt.Sprintf("model %q has no effort setting; remove effort %q", model, effort)}}
	}
	for _, e := range m.Efforts {
		if e == effort {
			return nil
		}
	}
	return []Finding{{Field: FieldEffort, Message: fmt.Sprintf("effort %q is not offered by model %q, which offers %s", effort, model, strings.Join(m.Efforts, ", ")),
		Suggest: Suggest(effort, m.Efforts, 2)}}
}

// Suggest returns up to n of the candidates nearest to want: those that
// hold it or are held by it, then those a few edits away, ignoring case.
// It returns none when nothing is near.
func Suggest(want string, candidates []string, n int) []string {
	w := strings.ToLower(want)
	type scored struct {
		name  string
		score int
	}
	var list []scored
	for _, c := range candidates {
		lc := strings.ToLower(c)
		// The part after the provider is compared too, so a model given
		// without its provider, or with another one, is still found.
		_, tail, _ := strings.Cut(lc, "/")
		_, wtail, _ := strings.Cut(w, "/")
		short := w
		if wtail != "" {
			short = wtail
		}
		d := distance(w, lc)
		dt := d
		if tail != "" {
			dt = distance(short, tail)
		}
		switch {
		case w != "" && (strings.Contains(lc, w) || strings.Contains(w, lc) || wtail != "" && strings.Contains(lc, wtail)):
			list = append(list, scored{c, min(d, dt) - 100})
		case d <= max(2, len([]rune(w))/3):
			list = append(list, scored{c, d})
		case dt <= max(1, len([]rune(short))/3):
			list = append(list, scored{c, dt})
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].score < list[j].score })
	var out []string
	for i := 0; i < len(list) && i < n; i++ {
		out = append(out, list[i].name)
	}
	return out
}

// distance is the edit distance of two strings.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
