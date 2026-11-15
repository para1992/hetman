package watchtower_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/teststep"
	"hetman/internal/watchtower"
)

func TestCatalogEndpoint(t *testing.T) {
	files := t.TempDir()
	for name, text := range map[string]string{"p.md": "Do it.",
		"s.json": `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}, "required": ["outcome"]}`} {
		if err := os.WriteFile(filepath.Join(files, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w := newWorld(t, `version: 0
workdir: .
start: a
executors:
  cat: {run: `+teststep.Adapter("SYNC1/cat.json")+`}
  bare: {run: `+teststep.Adapter("SYNC1/bare.json")+`}
nodes:
  a: {type: agent, executor: cat, prompt: `+teststep.Quote(filepath.Join(files, "p.md"))+`, schema: `+teststep.Quote(filepath.Join(files, "s.json"))+`, on: {ok: done, fail: failed}}
`)
	os.WriteFile(filepath.Join(w.dir, "sync1", "cat.json"), []byte(`{"catalog": true}`), 0o644)
	os.WriteFile(filepath.Join(w.dir, "sync1", "bare.json"), []byte(`{}`), 0o644)

	var v watchtower.CatalogView
	if code := w.get("/api/flows/orders.yaml/catalog?executor=cat", &v); code != http.StatusOK || !v.Available || !v.Complete || len(v.Models) != 4 {
		t.Fatalf("%d %+v", code, v)
	}
	if m := v.Models[0]; m.ID != "alpha/small" || m.Efforts == nil || len(m.Efforts) != 0 || v.Models[3].Efforts != nil {
		t.Fatalf("efforts must tell none from unknown: %+v", v.Models)
	}
	v = watchtower.CatalogView{}
	if w.get("/api/flows/orders.yaml/catalog?executor=bare&lang=uk", &v); v.Available || !strings.Contains(v.Reason, "не повідомляє") || v.Models == nil {
		t.Fatalf("no catalogue: %+v", v)
	}
	v = watchtower.CatalogView{}
	if w.get("/api/flows/orders.yaml/catalog?executor=nobody", &v); v.Available || !strings.Contains(v.Reason, "no executor nobody") {
		t.Fatalf("unknown executor: %+v", v)
	}
	if code := w.get("/api/flows/other.yaml/catalog?executor=cat", nil); code != http.StatusNotFound {
		t.Fatalf("unknown file: %d", code)
	}
}
