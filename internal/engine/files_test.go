package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"hetman/internal/chronicle"
)

// sameJSON fails the test unless the file at path holds JSON equal to want.
func sameJSON(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got, exp any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("%s: %v: %s", path, err, data)
	}
	json.Unmarshal([]byte(want), &exp)
	if !reflect.DeepEqual(got, exp) {
		t.Fatalf("%s holds %s, want %s", path, data, want)
	}
}

const runInput = `{"ticket": {"id": "T-1", "title": "Fix it"}, "n": 2}`

func TestInputFileReachesScriptsChecksAndHook(t *testing.T) {
	f := setupAgent(t, `version: 0
start: a
notify: {run: `+arg("input")+`}
checks:
  first:  {run: `+arg("input", "delete")+`}
  second: {run: `+arg("input")+`}
nodes:
  a: {type: script, run: `+arg("input", "tamper")+`, on: {ok: g, fail: failed}}
  g:
    type: gate
    checks: [first, second]
    ask: [{id: ok, question: "Fine?"}]
    on: {pass: done, blocked: failed}
`, nil, runInput)
	done := f.driveAsync(context.Background())
	f.waitAsked(0)
	f.answer(map[string]string{"ok": "yes"}, nil, "")
	f.wait(done, Done)
	f.e.WaitHooks()
	// a overwrites the file and first deletes it; each later command still
	// finds the recorded input.
	for _, seen := range []string{"seen.a.1.json", "seen.g.first.1.json", "seen.g.second.1.json", "seen.g.1.json"} {
		sameJSON(t, filepath.Join(f.work, seen), runInput)
	}
	sameJSON(t, filepath.Join(f.work, InputFile), runInput)
}

func TestInputFileWithoutInputIsAnEmptyObject(t *testing.T) {
	f := setup(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("input")+`, on: {ok: done, fail: failed}}
`)
	f.drive(Done)
	data, err := os.ReadFile(filepath.Join(f.work, "seen.a.1.json"))
	if err != nil || string(data) != "{}\n" {
		t.Fatalf("input file %q (%v), want an empty object", data, err)
	}
}

func TestInputFileIsRestoredOnResume(t *testing.T) {
	f := setupAgent(t, twoStepOrders(), nil, runInput)
	f.drive(Done)
	// The engine died during a, and the file was edited meanwhile.
	g := setupAgent(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("input")+`, on: {ok: done, fail: failed}}
`, nil, runInput)
	g.record(chronicle.Event{Kind: chronicle.StepStarted, Node: "a", Attempt: 1})
	path := filepath.Join(g.work, InputFile)
	if err := os.MkdirAll(g.work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"edited": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g.drive(Done)
	sameJSON(t, filepath.Join(g.work, "seen.a.2.json"), runInput)
	// A file deleted after the campaign ended is back on the next look.
	if err := os.Remove(filepath.Join(f.work, InputFile)); err != nil {
		t.Fatal(err)
	}
	f.drive(Done)
	sameJSON(t, filepath.Join(f.work, InputFile), runInput)
}

func TestAcceptedAgentOutputIsWrittenForScripts(t *testing.T) {
	f := setupAgent(t, `version: 0
start: review
executors: {stub: {run: PLAN}}
nodes:
  review: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: done, needs_rework: failed, fail: failed}}
`, map[string]string{"p.md": "Review.", "s.json": reviewSchema, "plan.json": `{"nodes": {"review": [
		[{"result": {"outcome": "maybe"}}],
		[{"result": {"outcome": "ok", "notes": "fine"}}]]}}`}, "")
	f.drive(Done)
	path := filepath.Join(f.work, filepath.FromSlash(OutputFile("review")))
	sameJSON(t, path, `{"outcome": "ok", "notes": "fine"}`)
	if err := os.WriteFile(path, []byte(`{"outcome": "maybe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f.drive(Done)
	sameJSON(t, path, `{"outcome": "ok", "notes": "fine"}`)
}
