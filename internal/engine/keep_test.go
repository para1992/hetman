package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/adapter"
	"hetman/internal/chronicle"
)

const keepOrders = `version: 0
workdir: .
start: implement
executors: {stub: {run: PLAN}}
nodes:
  implement:
    type: agent
    executor: stub
    prompt: implement.md
    schema: ok.json
    on: {ok: review, fail: failed}
  review:
    type: agent
    executor: stub
    prompt: review.md
    schema: review.json
    model: m1
    max_attempts: 2
    keep_session: true
    on: {ok: done, needs_rework: implement, fail: failed}
`

// keepWorld is an implement and review loop whose reviewer keeps its
// session; review lists the stub's moves of the reviewer by attempt.
func keepWorld(t *testing.T, review string) *fixture {
	return setupAgent(t, keepOrders, map[string]string{
		"implement.md": "Implement.",
		"review.md":    "Review: {{output.implement.summary}}",
		"ok.json":      okSchema,
		"review.json":  reviewSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {
			"implement": [[{"result": {"outcome": "ok", "summary": "v1"}}], [{"result": {"outcome": "ok", "summary": "v2"}}]],
			"review": ` + review + `}}`,
	}, "")
}

// reviewSteps are the start messages the reviewer got.
func (f *fixture) reviewSteps() []adapter.Step {
	var out []adapter.Step
	for _, s := range f.steps() {
		if s.NodeID == "review" {
			out = append(out, s)
		}
	}
	return out
}

func session(s adapter.Step) string {
	if s.Session == nil {
		return ""
	}
	return *s.Session
}

// The second visit of the reviewer continues the conversation of the
// first with the newly rendered prompt; the first visit starts afresh.
func TestKeptSessionContinuesOnTheNextVisit(t *testing.T) {
	f := keepWorld(t, `[[{"session": "r-1"}, {"result": {"outcome": "needs_rework", "notes": "add tests"}}],
		[{"require_session": "r-1"}, {"session": "r-1"}, {"result": {"outcome": "ok"}}]]`)
	f.drive(Done)
	want(t, f.finished(), []string{"implement#1 ok/", "review#1 needs_rework/", "implement#2 ok/", "review#2 ok/"})
	rs := f.reviewSteps()
	if len(rs) != 2 || session(rs[0]) != "" || session(rs[1]) != "r-1" || rs[1].Prompt != "Review: v2" {
		t.Fatalf("review steps %+v", rs)
	}
	var started []chronicle.Event
	for _, ev := range f.events(chronicle.StepStarted) {
		if ev.Node == "review" {
			started = append(started, ev)
		}
	}
	if len(started) != 2 || started[0].Session != "" || started[1].Session != "r-1" {
		t.Fatalf("step_started %+v", started)
	}
	if log := f.file(LogFile("review", "", 2)); !strings.Contains(log, "hetman: continuing session r-1 of the previous visit of review (keep_session)") {
		t.Fatalf("log:\n%s", log)
	}
}

// A conversation belongs to one client: a visit on another model starts
// afresh.
func TestKeptSessionNotContinuedOnAnotherModel(t *testing.T) {
	f := keepWorld(t, `[[{"session": "r-1"}, {"result": {"outcome": "needs_rework"}}], [{"result": {"outcome": "ok"}}]]`)
	// The second implement attempt changes the reviewer's model in the
	// orders, which are read again before the next step.
	path := filepath.Join(f.e.Orders.Dir, "orders.yaml")
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed, _ := json.Marshal(strings.Replace(string(text), "model: m1", "model: m22", 1))
	plan := `{"trace": "trace.jsonl", "nodes": {
		"implement": [[{"result": {"outcome": "ok", "summary": "v1"}}], [{"write": "orders.yaml", "text": ` + string(changed) + `}, {"result": {"outcome": "ok", "summary": "v2"}}]],
		"review": [[{"session": "r-1"}, {"result": {"outcome": "needs_rework"}}], [{"result": {"outcome": "ok"}}]]}}`
	if err := os.WriteFile(filepath.Join(f.e.Orders.Dir, "plan.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	f.drive(Done)
	rs := f.reviewSteps()
	if len(rs) != 2 || rs[1].Model != "m22" || session(rs[1]) != "" {
		t.Fatalf("review steps %+v", rs)
	}
}

// When the attempt that continued a kept session fails, even with a
// permanent error such as an expired session, the next attempt starts
// afresh with the full prompt.
func TestFailedKeptSessionStartsAfresh(t *testing.T) {
	for _, kind := range []string{"transient", "permanent"} {
		t.Run(kind, func(t *testing.T) {
			f := keepWorld(t, `[[{"session": "r-1"}, {"result": {"outcome": "needs_rework"}}],
				[{"require_session": "r-1"}, {"error": "`+kind+`", "message": "no such session"}],
				[{"session": "r-3"}, {"result": {"outcome": "ok"}}]]`)
			f.drive(Done)
			want(t, f.finished(), []string{"implement#1 ok/", "review#1 needs_rework/", "implement#2 ok/", "review#2 /" + kind, "review#3 ok/"})
			rs := f.reviewSteps()
			if len(rs) != 3 || session(rs[1]) != "r-1" || session(rs[2]) != "" || rs[2].Prompt != "Review: v2" {
				t.Fatalf("review steps %+v", rs)
			}
		})
	}
}

// A permanent error of a kept session on the last attempt still ends the
// visit with fail.
func TestFailedKeptSessionOnTheLastAttempt(t *testing.T) {
	f := keepWorld(t, `[[{"session": "r-1"}, {"result": {"outcome": "needs_rework"}}],
		[{"error": "permanent", "message": "no such session"}]]`)
	f.drive(Failed)
	want(t, f.finished(), []string{"implement#1 ok/", "review#1 needs_rework/", "implement#2 ok/", "review#2 /permanent", "review#3 fail/permanent"})
}

// Keeping a session needs an adapter that can resume one; without it the
// campaign stops in the error state before any step starts.
func TestKeptSessionNeedsAnAdapterThatCanResume(t *testing.T) {
	f := setupAgent(t, keepOrders, map[string]string{
		"implement.md": "Implement.", "review.md": "Review.", "ok.json": okSchema, "review.json": reviewSchema,
		"plan.json": `{"features": []}`,
	}, "")
	f.drive(Errored)
	if e := f.events(chronicle.Stopped); len(e) != 1 || !strings.Contains(e[0].Error, `does not declare the "resume" feature`) ||
		!strings.Contains(e[0].Error, "keep_session") {
		t.Fatalf("errors %+v", e)
	}
}

// An engine stopped in the middle of an attempt that continued a kept
// session continues the same session after a restart.
func TestKeptSessionSurvivesARestart(t *testing.T) {
	f := keepWorld(t, `[[{"session": "r-1"}, {"result": {"outcome": "needs_rework"}}],
		[{"require_session": "r-1"}, {"touch": "started"}, {"hang": true}],
		[{"require_session": "r-1"}, {"result": {"outcome": "ok"}}]]`)
	ctx, cancel := context.WithCancel(context.Background())
	done := f.driveAsync(ctx)
	waitFile(t, filepath.Join(f.e.Orders.Dir, "started"))
	cancel()
	if r := <-done; r.err != ErrInterrupted {
		t.Fatalf("drive %+v", r)
	}
	f.e = &Engine{Orders: f.e.Orders, Chron: f.chron, Campaign: f.e.Campaign}
	f.drive(Done)
	want(t, f.finished(), []string{"implement#1 ok/", "review#1 needs_rework/", "implement#2 ok/", "review#3 ok/"})
	rs := f.reviewSteps()
	if len(rs) != 3 || session(rs[1]) != "r-1" || session(rs[2]) != "r-1" || rs[2].Prompt != "Review: v2" {
		t.Fatalf("review steps %+v", rs)
	}
	if log := f.file(LogFile("review", "", 3)); !strings.Contains(log, "continuing session r-1 of the previous visit") {
		t.Fatalf("log:\n%s", log)
	}
}
