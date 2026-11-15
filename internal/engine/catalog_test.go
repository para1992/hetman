package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/adapter"
	"hetman/internal/orders"
	"hetman/internal/stubadapter"
	"hetman/internal/teststep"
)

// catalogOrders writes orders whose executor "cat" has the stub's fixed
// catalogue and "bare" has none, with the agent nodes and roles given.
func catalogOrders(t *testing.T, nodes, roles string) *orders.Orders {
	t.Helper()
	dir := tempDir(t)
	files := map[string]string{
		"p.md":      "Do it.",
		"s.json":    okSchema,
		"cat.json":  `{"catalog": true}`,
		"bare.json": `{}`,
		orders.DefaultFile: `version: 0
workdir: .
start: a
executors:
  cat: {run: ` + teststep.Adapter(filepath.Join(dir, "cat.json")) + `}
  bare: {run: ` + teststep.Adapter(filepath.Join(dir, "bare.json")) + `}
` + roles + `nodes:
` + nodes,
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	o, err := orders.Load(filepath.Join(dir, orders.DefaultFile))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestExecutorCatalog(t *testing.T) {
	o := catalogOrders(t, "  a: {type: agent, executor: cat, prompt: p.md, schema: s.json, on: {ok: done, fail: failed}}\n", "")
	ctx := context.Background()
	r := ExecutorCatalog(ctx, o, "cat", false)
	if r.Catalog == nil || len(r.Catalog.Models) != 4 || !r.Catalog.Complete {
		t.Fatalf("catalogue: %+v", r)
	}
	if m := r.Catalog.Model("alpha/small"); m == nil || !m.EffortsKnown() || len(m.Efforts) != 0 {
		t.Fatalf("a model without effort must say so: %+v", m)
	}
	if m := r.Catalog.Model("beta/plain"); m == nil || m.EffortsKnown() {
		t.Fatalf("a model whose efforts are unknown must say so: %+v", m)
	}
	// Kept in memory: a changed plan is seen only on refresh.
	os.WriteFile(filepath.Join(o.Dir, "cat.json"), []byte(`{"catalog": {"complete": false, "models": [{"id": "only"}]}}`), 0o644)
	if r := ExecutorCatalog(ctx, o, "cat", false); len(r.Catalog.Models) != 4 {
		t.Fatalf("the kept catalogue must be used: %+v", r.Catalog)
	}
	if r := ExecutorCatalog(ctx, o, "cat", true); r.Catalog == nil || len(r.Catalog.Models) != 1 || r.Catalog.Complete {
		t.Fatalf("refresh must ask again: %+v", r)
	}
	if r := ExecutorCatalog(ctx, o, "bare", false); r.Catalog != nil || r.Reason != CatalogUnsupported {
		t.Fatalf("an adapter without the feature: %+v", r)
	}
	if r := ExecutorCatalog(ctx, o, "nobody", false); r.Reason != CatalogNoExecutor {
		t.Fatalf("an unknown executor: %+v", r)
	}
	os.WriteFile(filepath.Join(o.Dir, "cat.json"), []byte(`{"catalog": {"models": [{"id": "x"}, {"id": "x"}]}}`), 0o644)
	if r := ExecutorCatalog(ctx, o, "cat", true); r.Reason != CatalogFailed || !strings.Contains(r.Detail, "twice") {
		t.Fatalf("a bad catalogue: %+v", r)
	}
	o.Executors["missing"] = &orders.Executor{Name: "missing", Run: []string{filepath.Join(o.Dir, "no-such-program")}}
	if r := ExecutorCatalog(ctx, o, "missing", false); r.Reason != CatalogFailed || r.Detail == "" {
		t.Fatalf("an adapter that cannot start: %+v", r)
	}
}

// hetman check reports at the node a model the catalogue does not list and
// an effort the model does not offer, with the nearest valid names, and
// checks the clients of a role the same way.
func TestCheckUsesTheCatalogue(t *testing.T) {
	o := catalogOrders(t, `  a: {type: agent, executor: cat, model: alpha/smal, prompt: p.md, schema: s.json, on: {ok: b, fail: failed}}
  b: {type: agent, executor: cat, model: alpha/small, effort: high, prompt: p.md, schema: s.json, on: {ok: c, fail: failed}}
  c: {type: agent, executor: cat, model: alpha/large, effort: hihg, prompt: p.md, schema: s.json, on: {ok: d, fail: failed}}
  d: {type: agent, executor: cat, model: beta/plain, effort: anything, prompt: p.md, schema: s.json, on: {ok: e, fail: failed}}
  e: {type: agent, executor: bare, model: whatever, effort: any, prompt: p.md, schema: s.json, on: {ok: f, fail: failed}}
  f: {type: agent, role: r, prompt: p.md, schema: s.json, on: {ok: done, fail: failed}}
`, `roles:
  r:
    select: fixed
    clients:
      - {executor: cat, model: beta/deep, effort: low}
`)
	errs, warnings := CheckAdapters(context.Background(), o)
	var lines []string
	for _, p := range errs {
		lines = append(lines, p.String())
	}
	text := strings.Join(lines, "\n")
	for _, want := range []string{
		`node "a": model: model "alpha/smal" is not in the catalogue of the executor; nearest: alpha/small`,
		`node "b": effort: model "alpha/small" has no effort setting; remove effort "high" (catalogue of executor cat)`,
		`node "c": effort: effort "hihg" is not offered by model "alpha/large", which offers low, medium, high; nearest: high`,
		`node "f": effort: effort "low" is not offered by model "beta/deep", which offers high, max`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if len(errs) != 4 || len(warnings) != 0 {
		t.Fatalf("errors:\n%s\nwarnings: %v", text, warnings)
	}
	if !strings.Contains(text, "orders.yaml:13:") {
		t.Errorf("a problem must be at its node's line:\n%s", text)
	}
}

// Where the adapter's own check already reports the field, its message
// stays and only the catalogue's suggestions are added.
func TestCatalogueAddsToTheAdapterCheck(t *testing.T) {
	list := withCatalog([]found{{node: "a", field: "model", msg: "not known to the client", by: "adapter"}},
		&stubadapter.FixedCatalog, []adapter.Step{{NodeID: "a", Model: "alpha/larg"}})
	if len(list) != 1 || list[0].msg != "not known to the client" || len(list[0].suggest) == 0 || list[0].suggest[0] != "alpha/large" {
		t.Fatalf("%+v", list)
	}
}
