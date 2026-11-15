package watchtower_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/engine"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

func TestRolesViewAndClientWords(t *testing.T) {
	files := t.TempDir()
	for name, text := range map[string]string{"p.md": "Do it.",
		"s.json": `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}, "required": ["outcome"]}`} {
		if err := os.WriteFile(filepath.Join(files, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, sc := teststep.Quote(filepath.Join(files, "p.md")), teststep.Quote(filepath.Join(files, "s.json"))
	w := newWorld(t, `version: 0
workdir: .
flow: rotate
start: a
executors:
  first: {run: `+teststep.Adapter("SYNC1/first.json")+`}
  second: {run: `+teststep.Adapter("SYNC1/second.json")+`}
roles:
  r:
    select: quota
    clients:
      - {executor: first, model: m1, allowance: {five_hour: 4}}
      - {executor: second, model: m2, effort: high}
nodes:
  a: {type: agent, role: r, prompt: `+p+`, schema: `+sc+`, on: {ok: b, fail: failed}}
  b: {type: agent, role: r, prompt: `+p+`, schema: `+sc+`, on: {ok: done, fail: failed}}
`)
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(w.dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("sync1", "first.json"), `{"nodes": {"b": [[{"error": "limit", "message": "spent", "retry_in": "2h"}]]}}`)
	write(filepath.Join("sync1", "second.json"), `{"usage": {"five_hour": {"used": 0.5}, "weekly": {"used": 0.25}}}`)

	var none []engine.RoleView
	if code := w.get("/api/roles", &none); code != http.StatusOK || len(none) != 1 || len(none[0].Clients) != 2 {
		t.Fatalf("roles before any campaign: %d %+v", code, none)
	}
	list := w.stream("/api/stream")
	list.until("roles", func(d string) bool { return strings.Contains(d, `"select":"quota"`) })

	o, err := orders.Load(w.orders)
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.chron.NewCampaign(o.Flow, o.Path, o.Start, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	e := &engine.Engine{Orders: o, Chron: w.chron, Campaign: id}
	if res, err := e.Drive(context.Background()); err != nil || res != engine.Done {
		t.Fatalf("drive %v %v", res, err)
	}

	// a: first has no measured share and an estimate of 0 of 4, second a
	// measured 50%: first runs. b: first at 1 of 4 (25%) runs, hits its
	// limit, and second continues.
	c := w.campaign(1)
	a, b := c.Nodes["a"], c.Nodes["b"]
	if len(a) != 1 || a[0].Choice == nil || a[0].Executor != "first" || !strings.Contains(a[0].Client, "1 (first, m1) of role r (quota)") ||
		!strings.Contains(a[0].Compared, "five_hour 0% estimated") || !strings.Contains(a[0].Compared, "five_hour 50% measured") {
		t.Fatalf("a %+v", a)
	}
	if len(b) != 2 || b[1].Executor != "second" || b[1].Switched != "client 1 (first, m1) had hit its limit" {
		t.Fatalf("b %+v", b)
	}

	var roles []engine.RoleView
	w.get("/api/roles", &roles)
	first, second := roles[0].Clients[0], roles[0].Clients[1]
	if !first.Exhausted || first.Why != "limit" || first.Until == nil || first.Allowance["five_hour"] != "4" || first.Windows[0].Source != "limit" {
		t.Fatalf("first %+v", first)
	}
	if second.Exhausted || second.Windows[0].Source != "measured" || second.Windows[0].At == nil || second.Effort != "high" || second.Windows[1].Attempts != 1 {
		data, _ := json.Marshal(second)
		t.Fatalf("second %s", data)
	}
	list.until("roles", func(d string) bool { return strings.Contains(d, `"exhausted":true`) })
}
