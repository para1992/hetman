package watchtower_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hetman/internal/teststep"
	"hetman/internal/watchtower"
)

// waitStopped waits until campaign id stops in the failed or the error state.
func (w *world) waitStopped(id int) watchtower.Campaign {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		c := w.campaign(id)
		if c.State == watchtower.StateFailed || c.State == watchtower.StateError {
			return c
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("campaign %d is %s\n%s", id, c.State, w.dump())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tracedPrompts lists the prompts of the start messages the stub adapter
// was sent, in order.
func tracedPrompts(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var step struct {
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(sc.Bytes(), &step); err != nil {
			t.Fatalf("trace line %q: %v", sc.Text(), err)
		}
		out = append(out, step.Prompt)
	}
	return out
}

// A prompt template of a segment edited on disk while the page server runs,
// with no change to any orders file, is what a retry from the page renders.
func TestRetryFromThePageRendersAnEditedSegmentPrompt(t *testing.T) {
	segDir := t.TempDir()
	plan := filepath.Join(segDir, "plan.json")
	trace := filepath.Join(segDir, "trace.jsonl")
	planText, _ := json.Marshal(map[string]any{
		"trace": trace,
		"when":  []any{map[string]any{"prompt": "OLD WORDING", "moves": [][]any{{map[string]any{"error": "permanent", "message": "old prompt"}}}}},
		"nodes": map[string]any{"*": [][]any{{map[string]any{"result": map[string]any{"outcome": "ok"}}}}},
	})
	os.WriteFile(plan, planText, 0o644)
	os.WriteFile(filepath.Join(segDir, "p.md"), []byte("OLD WORDING for {{input.id}}."), 0o644)
	os.WriteFile(filepath.Join(segDir, "s.json"), []byte(`{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}, "required": ["outcome"]}`), 0o644)
	seg := filepath.Join(segDir, "seg.yaml")
	os.WriteFile(seg, []byte(`version: 0
workdir: .
flow: item
start: work
executors: {stub: {run: `+teststep.Adapter(plan)+`}}
nodes:
  work: {type: agent, executor: stub, prompt: p.md, schema: s.json, max_attempts: 1, on: {ok: done, fail: failed}}
`), 0o644)
	items := `[{"id":"x"}]`
	w := newWorld(t, `version: 0
workdir: .
start: plan
nodes:
  plan: {type: script, run: `+arg("write", "items.json", items)+`, on: {ok: each, fail: failed}}
  each: {type: foreach, segment: '`+strings.ReplaceAll(seg, "'", "''")+`', items: artifact.plan.items.json, parallel: 1, on: {done: done, failed: failed}}
`)
	if code, res := w.post("/api/flows/orders.yaml/start", map[string]any{}, pageHeaders); code != http.StatusOK {
		t.Fatalf("start: %d %v", code, res)
	}
	c := w.waitStopped(1)
	if c.Retry == "" {
		t.Fatalf("no retry offered: %+v", c.Summary)
	}
	if p := tracedPrompts(t, trace); len(p) != 1 || !strings.Contains(p[0], "OLD WORDING") {
		t.Fatalf("first prompts %q", p)
	}
	// The page reads the orders again (as every view does) before the
	// template is edited, and the orders files are left alone.
	w.campaign(1)
	os.WriteFile(filepath.Join(segDir, "p.md"), []byte("NEW WORDING, longer than before, for {{input.id}}."), 0o644)
	if code, res := w.post("/api/campaigns/1/retry", map[string]any{}, pageHeaders); code != http.StatusOK {
		t.Fatalf("retry: %d %v", code, res)
	}
	w.waitState(1, watchtower.StateDone)
	p := tracedPrompts(t, trace)
	if len(p) != 2 || !strings.Contains(p[1], "NEW WORDING") || strings.Contains(p[1], "OLD WORDING") {
		t.Fatalf("prompts %q", p)
	}
}
