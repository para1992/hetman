package orders

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The stamp of loaded orders covers the orders file, its segments, and the
// prompt templates and schemas of both, as they were read.
func TestStampCoversEveryFileAsRead(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema := `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}, "required": ["outcome"]}`
	write("p.md", "Do it.")
	write("s.json", schema)
	write("seg.yaml", "version: 0\nworkdir: .\nstart: w\nexecutors: {x: {run: [x]}}\nnodes:\n  w: {type: agent, executor: x, prompt: p.md, schema: s.json, on: {ok: done, fail: failed}}\n")
	write("orders.yaml", "version: 0\nstart: s\nnodes:\n  s: {type: segment, segment: seg.yaml, on: {done: done, failed: failed}}\n")
	o, err := Load(filepath.Join(dir, "orders.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if o.Stamp() == "" || o.Stamp() != o.StampNow() {
		t.Fatalf("stamp %q, now %q", o.Stamp(), o.StampNow())
	}
	for i, name := range []string{"p.md", "s.json", "seg.yaml", "orders.yaml"} {
		before := o.StampNow()
		later := time.Now().Add(time.Duration(i+1) * time.Minute)
		if err := os.Chtimes(filepath.Join(dir, name), later, later); err != nil {
			t.Fatal(err)
		}
		if o.StampNow() == before || o.StampNow() == o.Stamp() {
			t.Fatalf("a change to %s is not seen", name)
		}
	}
	data, _ := os.ReadFile(o.Path)
	if p, err := Parse(o.Path, data); err != nil || p.Stamp() != "" {
		t.Fatalf("parsed orders keep a stamp (%v)", err)
	}
}
