package wizard

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/adapter"
	"hetman/internal/orders"
	"hetman/internal/stubadapter"
	"hetman/internal/teststep"
)

func TestMain(m *testing.M) {
	if os.Getenv(teststep.EnvHelper) == "1" && len(os.Args) > 1 && os.Args[1] == "adapter" {
		os.Exit(stubadapter.Main(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Setenv(teststep.EnvHelper, "1")
	os.Exit(m.Run())
}

func scripted(answers ...string) (*wizard, *bytes.Buffer) {
	var out bytes.Buffer
	return &wizard{in: bufio.NewReader(input(answers...)), out: &out, echo: true}, &out
}

// A long catalogue is narrowed by typing part of a name, then picked by
// number; "other" types a name by hand.
func TestPickModelFromALongCatalogue(t *testing.T) {
	c := &adapter.Catalog{Complete: true}
	for i := 0; i < 30; i++ {
		c.Models = append(c.Models, adapter.CatalogModel{ID: fmt.Sprintf("p/m%d", i), Efforts: []string{"low", "high"}})
	}
	w, out := scripted("m1", "3")
	model, entry, err := w.pickModel(c, "")
	if err != nil || model != "p/m11" || entry == nil || entry.ID != "p/m11" {
		t.Fatalf("%q %+v %v\n%s", model, entry, err, out)
	}
	if !strings.Contains(out.String(), "30 models match; type part of a name") || !strings.Contains(out.String(), "  3) p/m11  (effort: low, high)") {
		t.Fatalf("transcript:\n%s", out)
	}
	w, out = scripted("zzz", "other", "p/m99")
	model, entry, err = w.pickModel(c, "")
	if err != nil || model != "p/m99" || entry != nil || !strings.Contains(out.String(), `No model matches "zzz"`) || !strings.Contains(out.String(), "does not list p/m99") {
		t.Fatalf("%q %+v %v\n%s", model, entry, err, out)
	}
	w, _ = scripted("")
	if model, _, _ := w.pickModel(c, "p/m4"); model != "p/m4" {
		t.Fatalf("Enter keeps the current model: %q", model)
	}
}

// The effort is asked from the model's efforts, not at all for a model
// without one, and by hand when the catalogue cannot tell.
func TestPickEffort(t *testing.T) {
	w, out := scripted("2")
	if e, err := w.pickEffort(&adapter.CatalogModel{ID: "m", Efforts: []string{"low", "high"}}, ""); err != nil || e != "high" {
		t.Fatalf("%q %v\n%s", e, err, out)
	}
	w, out = scripted()
	if e, _ := w.pickEffort(&adapter.CatalogModel{ID: "m", Efforts: []string{}}, "high"); e != "" || !strings.Contains(out.String(), "Model m has no effort setting") {
		t.Fatalf("%q\n%s", e, out)
	}
	w, _ = scripted("whatever")
	if e, _ := w.pickEffort(&adapter.CatalogModel{ID: "m"}, ""); e != "whatever" {
		t.Fatalf("unknown efforts are typed: %q", e)
	}
}

// Changing the model of an agent step offers the catalogue of its
// executor; a model without the step's effort removes the effort too.
func TestChangeModelFromTheCatalogue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, orders.DefaultFile)
	files := map[string]string{
		"p.md":      "Do it.",
		"s.json":    `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}, "required": ["outcome"]}`,
		"plan.json": `{"catalog": true}`,
		orders.DefaultFile: `version: 0
workdir: .
start: a
executors:
  stub: {run: ` + teststep.Adapter(filepath.Join(dir, "plan.json")) + `}
nodes:
  a: {type: agent, executor: stub, prompt: p.md, schema: s.json, model: alpha/large, effort: high, on: {ok: done, fail: failed}}
`,
	}
	for name, text := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644)
	}
	var out bytes.Buffer
	// change an agent step, a, model; "small" narrows to one model, which
	// has no effort setting; write it; quit.
	if err := Run(path, input("7", "", "", "small", "", "11"), &out, true); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "model: alpha/small") || strings.Contains(string(data), "effort") {
		t.Fatalf("orders:\n%s\ntranscript:\n%s", data, out.String())
	}
	for _, want := range []string{"The executor's client offers 4 models.", "  2) alpha/large  (effort: low, medium, high)", "Model: alpha/small",
		"Model alpha/small has no effort setting"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}
