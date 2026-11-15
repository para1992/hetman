package main

import (
	"bytes"
	"strings"
	"testing"

	"hetman/internal/adapter"
)

// --catalog gives the aliases the adapter accepts without warning, each
// with every effort level it passes on, and says the list is not complete.
func TestCatalog(t *testing.T) {
	var out, diag bytes.Buffer
	if code := run([]string{adapter.CatalogFlag}, strings.NewReader(""), &out, &diag); code != 0 {
		t.Fatalf("exit %d: %s", code, diag.String())
	}
	c, err := adapter.ParseCatalog(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if c.Complete || len(c.Models) != len(modelAliases) {
		t.Fatalf("catalogue %+v", c)
	}
	for _, m := range c.Models {
		if !modelAliases[m.ID] || strings.Join(m.Efforts, ",") != strings.Join(efforts, ",") {
			t.Errorf("model %+v", m)
		}
		// What the catalogue lists, the check accepts.
		for _, e := range m.Efforts {
			if p := stepProblems(options{}, adapter.Step{Model: m.ID, Effort: e}, nil); len(p) != 0 {
				t.Errorf("%s %s: %+v", m.ID, e, p)
			}
		}
	}
	out.Reset()
	run([]string{adapter.DescribeFlag}, strings.NewReader(""), &out, &diag)
	if d, err := adapter.ParseDescription(out.Bytes()); err != nil || !d.Has(adapter.FeatureCatalog) {
		t.Fatalf("%v %+v", err, d)
	}
}
