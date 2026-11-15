package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"hetman/internal/adapter"
	"hetman/internal/engine"
	"hetman/internal/i18n"
	"hetman/internal/orders"
	"hetman/internal/wizard"
)

// modelsCmd prints what the adapters of the executors of the orders say
// their clients offer: "hetman scribe models [EXECUTOR] [WORDS...]". The
// first word names an executor when the orders declare one by that name;
// the other words filter the models by id, label and group.
func modelsCmd(args []string, stdout, stderr io.Writer) int {
	p := personPrinter(nil)
	fs := flag.NewFlagSet("hetman scribe models", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, p.T("flag.f"))
	refresh := fs.Bool("refresh", false, p.T("flag.models_refresh"))
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return exitUsage
	}
	o, err := orders.Load(*file)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n%s\n", err, p.T("scribe.models.invalid"))
		return exitUsage
	}
	var names []string
	if len(pos) > 0 && o.Executors[pos[0]] != nil {
		names, pos = []string{pos[0]}, pos[1:]
	} else {
		for name := range o.Executors {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	if len(names) == 0 {
		fmt.Fprintln(stderr, p.T("scribe.models.no_executors", "path", o.Path))
		return exitUsage
	}
	filter := strings.Join(pos, " ")
	code := exitDone
	for i, name := range names {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		r := engine.ExecutorCatalog(context.Background(), o, name, *refresh)
		if r.Catalog == nil {
			fmt.Fprintln(stdout, p.T("scribe.models.none", "executor", name, "reason", r.ReasonIn(p)))
			if len(names) == 1 {
				code = exitFailed
			}
			continue
		}
		writeCatalog(p, stdout, name, r.Catalog, filter)
	}
	return code
}

// writeCatalog prints a catalogue grouped, with the efforts beside each
// model.
func writeCatalog(p *i18n.Printer, w io.Writer, executor string, c *adapter.Catalog, filter string) {
	models := c.Filter(filter)
	if filter == "" {
		fmt.Fprintln(w, p.T("scribe.models.title", "executor", executor, "n", len(c.Models)))
	} else {
		fmt.Fprintln(w, p.T("scribe.models.title_filtered", "executor", executor, "n", len(models), "total", len(c.Models), "filter", filter))
	}
	if !c.Complete {
		fmt.Fprintln(w, p.T("scribe.models.incomplete"))
	}
	if len(models) == 0 {
		fmt.Fprintln(w, p.T("scribe.models.no_match"))
		return
	}
	width := 0
	for _, m := range models {
		width = max(width, len([]rune(m.ID)))
	}
	width = min(width, 48)
	labels := 0
	for _, m := range models {
		labels = max(labels, len([]rune(label(m))))
	}
	labels = min(labels, 32)
	grouped := false
	for _, m := range models {
		grouped = grouped || m.Group != ""
	}
	group := "\x00"
	for _, m := range models {
		indent := "  "
		if grouped {
			if m.Group != group {
				group = m.Group
				name := group
				if name == "" {
					name = p.T("scribe.models.no_group")
				}
				fmt.Fprintf(w, "  %s\n", name)
			}
			indent = "    "
		}
		fmt.Fprintf(w, "%s%-*s  %-*s  %s\n", indent, width, m.ID, labels, label(m), wizard.EffortsIn(p, m))
	}
}

// label is a model's name for people when it says more than its id.
func label(m adapter.CatalogModel) string {
	if m.Label == m.ID {
		return ""
	}
	return m.Label
}

// catalogRefusal lists what the catalogues say against the orders after an
// operation and not already before it, other than warnings. An operation
// that names a model or effort the catalogue does not offer is refused
// unless the person asks for it with -unlisted.
func catalogRefusal(before, after *orders.Orders) []string {
	ctx := context.Background()
	old := map[string]bool{}
	if before != nil {
		for _, f := range engine.CatalogFindings(ctx, before) {
			old[f.Text()] = true
		}
	}
	var out []string
	for _, f := range engine.CatalogFindings(ctx, after) {
		if !f.Warning && !old[f.Text()] {
			out = append(out, f.Text())
		}
	}
	return out
}
