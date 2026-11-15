package wizard

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"hetman/internal/adapter"
	"hetman/internal/edit"
	"hetman/internal/engine"
	"hetman/internal/i18n"
)

// shown is how many models a numbered list shows at once; a longer
// catalogue is narrowed by typing part of a name.
const shown = 20

// EffortsIn says the efforts of a model in a few words.
func EffortsIn(p *i18n.Printer, m adapter.CatalogModel) string {
	switch {
	case !m.EffortsKnown():
		return p.T("scribe.models.efforts_unknown")
	case len(m.Efforts) == 0:
		return p.T("scribe.models.no_effort")
	}
	return p.T("scribe.models.efforts", "efforts", strings.Join(m.Efforts, ", "))
}

// catalog returns the catalogue of an executor of the orders or, for one
// declared in the same change, of the adapter command x; nil with a line
// that says why when there is none.
func (w *wizard) catalog(executor string, x *edit.Op) *adapter.Catalog {
	var r engine.CatalogResult
	switch {
	case x != nil && x.Name == executor && len(x.Run) > 0:
		dir := filepath.Dir(w.path)
		argv := append([]string{}, x.Run...)
		if !filepath.IsAbs(argv[0]) && strings.ContainsAny(argv[0], `/\`) {
			argv[0] = filepath.Join(dir, filepath.FromSlash(argv[0]))
		}
		r = engine.CatalogOf(context.Background(), dir, argv, false)
		r.Executor = executor
	case w.o != nil:
		r = engine.ExecutorCatalog(context.Background(), w.o, executor, false)
	default:
		return nil
	}
	if r.Catalog == nil {
		fmt.Fprintln(w.out, w.p.T("wizard.models.none", "reason", r.ReasonIn(w.p)))
	}
	return r.Catalog
}

// pickModel asks for the model of an agent step: from the catalogue when
// there is one, by number or by a part of its name, or typed by hand. An
// empty answer keeps cur; "none" is the client's default model. It returns
// the model and its catalogue entry, nil when the catalogue does not list it.
func (w *wizard) pickModel(c *adapter.Catalog, cur string) (string, *adapter.CatalogModel, error) {
	if c == nil {
		v, err := w.ask(w.p.T("wizard.model"), cur)
		return none(v), nil, err
	}
	list := c.Models
	fmt.Fprintln(w.out, w.p.T("wizard.models.title", "n", len(c.Models)))
	for {
		if len(list) > shown {
			fmt.Fprintln(w.out, w.p.T("wizard.models.many", "n", len(list)))
		} else {
			for i, m := range list {
				fmt.Fprintf(w.out, "  %d) %s  (%s)\n", i+1, m.ID, EffortsIn(w.p, m))
			}
		}
		s, err := w.ask(w.p.T("wizard.models.prompt", "other", w.p.T("wizard.models.other")), cur)
		if err != nil {
			return "", nil, err
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(list) && len(list) <= shown {
			return list[n-1].ID, &list[n-1], nil
		}
		switch {
		case s == "" || s == "none":
			return "", nil, nil
		case c.Model(s) != nil:
			return s, c.Model(s), nil
		case s == cur:
			return cur, nil, nil
		case s == "other" || s == w.p.T("wizard.models.other"):
			v, err := w.need(w.p.T("wizard.models.by_hand"), "")
			if err == nil && c.Model(v) == nil && c.Complete {
				fmt.Fprintln(w.out, w.p.T("wizard.models.unlisted", "model", v))
			}
			return v, c.Model(v), err
		}
		found := c.Filter(s)
		switch len(found) {
		case 0:
			fmt.Fprintln(w.out, w.p.T("wizard.models.no_match", "filter", s))
			if near := adapter.Suggest(s, c.IDs(), 3); len(near) > 0 {
				fmt.Fprintln(w.out, w.p.T("wizard.models.nearest", "names", strings.Join(near, ", ")))
			}
		case 1:
			fmt.Fprintln(w.out, w.p.T("wizard.models.picked", "model", found[0].ID))
			return found[0].ID, &found[0], nil
		default:
			list = found
		}
	}
}

// pickEffort asks for the effort of a step with model m: one of the
// model's efforts when the catalogue knows them, nothing when the model has
// no effort setting, typed by hand otherwise. An empty answer keeps cur
// when the model takes it; "none" is the client's default.
func (w *wizard) pickEffort(m *adapter.CatalogModel, cur string) (string, error) {
	if m == nil || !m.EffortsKnown() {
		v, err := w.ask(w.p.T("wizard.effort"), cur)
		return none(v), err
	}
	if len(m.Efforts) == 0 {
		fmt.Fprintln(w.out, w.p.T("wizard.models.no_effort", "model", m.ID))
		return "", nil
	}
	if !contains(m.Efforts, cur) {
		cur = ""
	}
	fmt.Fprintln(w.out, w.p.T("wizard.models.efforts", "model", m.ID))
	for i, e := range m.Efforts {
		fmt.Fprintf(w.out, "  %d) %s\n", i+1, e)
	}
	for {
		s, err := w.ask(w.p.T("wizard.models.effort_prompt"), cur)
		if err != nil {
			return "", err
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(m.Efforts) {
			return m.Efforts[n-1], nil
		}
		if s == "" || s == "none" {
			return "", nil
		}
		if contains(m.Efforts, s) {
			return s, nil
		}
		fmt.Fprintln(w.out, w.p.T("wizard.pick_number", "from", 1, "to", len(m.Efforts)))
	}
}

// none reads "none" as no value.
func none(v string) string {
	if v == "none" {
		return ""
	}
	return v
}
