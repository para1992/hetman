package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/stubadapter"
	"hetman/internal/teststep"
)

// newAgentWorld writes orders with one agent node "a" driven by the stub
// adapter with the given plan, whose relative paths are taken from the
// orders folder. In the plan, SYNC stands for the sync folder.
func newAgentWorld(t *testing.T, plan, prompt string) *world {
	dir := tempDir(t)
	w := &world{t: t, dir: dir, orders: filepath.Join(dir, orders.DefaultFile), sync: filepath.Join(dir, "sync")}
	files := map[string]string{
		"sync/.keep": "",
		"p.md":       prompt,
		"s.json":     `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}, "required": ["outcome"]}`,
		"plan.json":  plan,
		orders.DefaultFile: `version: 0
workdir: .
flow: test
start: a
executors:
  stub: {run: ` + teststep.Adapter(filepath.Join(dir, "plan.json")) + `}
nodes:
  a: {type: agent, executor: stub, prompt: p.md, schema: s.json, model: m1, effort: e1, max_attempts: 1, on: {ok: done, fail: failed}}
`,
	}
	for rel, text := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func (w *world) trace() []adapter.Step {
	w.t.Helper()
	data, _ := os.ReadFile(filepath.Join(w.dir, "trace.jsonl"))
	var out []adapter.Step
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var s adapter.Step
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestKilledEngineDuringAgentStepContinuesSession(t *testing.T) {
	w := newAgentWorld(t, `{"trace": "trace.jsonl", "nodes": {"a": [
		[{"session": "s-1"}, {"log": "working"}, {"touch": "sync/started"}, {"hang": true}],
		[{"result": {"outcome": "ok"}}]]}}`, "Do it.")
	engineProc, out := w.process("run")
	w.waitFor("started", out)
	w.waitForEvent(func(e chronicle.Event) bool { return e.Kind == chronicle.AgentSession }, out)
	w.waitForEvent(func(e chronicle.Event) bool { return e.Kind == chronicle.ProcessStarted }, out)
	if err := engineProc.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	engineProc.Wait()
	killed := out.String()

	status := w.call(0, "status")
	if !strings.Contains(status, "running a (attempt 1), but no hetman process is running it") || !strings.Contains(status, "session  s-1") {
		t.Fatalf("status after kill:\n%s", status)
	}
	resumed := w.call(0, "resume")
	if !strings.Contains(resumed, "continuing session s-1") || !strings.Contains(resumed, "campaign done") {
		t.Fatalf("resume output:\n%s\n%s", resumed, w.diagnose(killed, resumed))
	}
	steps := w.trace()
	if len(steps) != 2 || steps[0].Session != nil || steps[1].Session == nil || *steps[1].Session != "s-1" {
		t.Fatalf("starts %+v", steps)
	}
	status = w.call(0, "status")
	for _, want := range []string{"state: done", "attempt 1  interrupted  [executor stub, model m1, effort e1]", "attempt 2  outcome ok", "continued session s-1", `output   {"outcome":"ok"}`} {
		if !strings.Contains(status, want) {
			t.Fatalf("final status lacks %q:\n%s", want, status)
		}
	}
}

func TestLimitWaitSurvivesKilledEngine(t *testing.T) {
	w := newAgentWorld(t, `{"trace": "trace.jsonl", "nodes": {"a": [
		[{"error": "limit", "message": "five-hour window used up", "retry_in": "3s"}],
		[{"result": {"outcome": "ok"}}]]}}`, "Do it.")
	engineProc, out := w.process("run")
	w.waitForEvent(func(e chronicle.Event) bool {
		return e.Kind == chronicle.StepFinished && e.Failure == chronicle.FailLimit
	}, out)
	status := w.call(0, "status")
	if !strings.Contains(status, "waiting at a for the subscription limit to reset; runs again at") {
		t.Fatalf("status while waiting:\n%s", status)
	}
	if err := engineProc.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	engineProc.Wait()
	status = w.call(0, "status")
	if !strings.Contains(status, "but no hetman process is running it (hetman resume to continue; it waits until then)") {
		t.Fatalf("status after kill:\n%s", status)
	}
	resumed := w.call(0, "resume")
	if !strings.Contains(resumed, "campaign done") {
		t.Fatalf("resume output:\n%s", resumed)
	}
	var retryAt, resumedAt, second time.Time
	for _, e := range w.events() {
		switch {
		case e.Kind == chronicle.StepFinished && e.RetryAt != nil:
			retryAt = *e.RetryAt
		case e.Kind == chronicle.Resumed:
			resumedAt = e.At
		case e.Kind == chronicle.StepStarted && e.Attempt == 2:
			second = e.At
		}
	}
	if second.Before(retryAt) {
		t.Fatalf("attempt 2 started at %s, before the retry time %s", second, retryAt)
	}
	// On a slow machine the retry time may already have passed at resume.
	if resumedAt.Before(retryAt) && !strings.Contains(resumed, "waits for the subscription limit to reset") {
		t.Fatalf("resumed before the retry time without waiting:\n%s", resumed)
	}
}

func TestOfflineStopsAndResumeRunsTheStepAgain(t *testing.T) {
	w := newAgentWorld(t, `{"trace": "trace.jsonl", "nodes": {"a": [
		[{"error": "offline", "message": "network unreachable"}],
		[{"result": {"outcome": "ok"}}]]}}`, "Do it.")
	out := w.call(exitWaiting, "run")
	if !strings.Contains(out, "waits for a person") {
		t.Fatalf("run output:\n%s", out)
	}
	status := w.call(0, "status")
	if !strings.Contains(status, "stopped at a: connectivity was lost (hetman resume runs the step again)") ||
		!strings.Contains(status, "offline, waits for a person (does not count): network unreachable") {
		t.Fatalf("status:\n%s", status)
	}
	if out := w.call(0, "resume"); !strings.Contains(out, "campaign done") {
		t.Fatalf("resume output:\n%s", out)
	}
	if n := len(w.trace()); n != 2 {
		t.Fatalf("starts %d", n)
	}
}

// A cannot-proceed reason given next to an ordinary outcome does not fail
// the step; status shows that it was left out of the output.
func TestStatusShowsADroppedUnableReason(t *testing.T) {
	w := newAgentWorld(t, `{"trace": "trace.jsonl", "nodes": {"a": [
		[{"result": {"outcome": "ok", "cannot_proceed_reason": "the docs were thin"}}]]}}`, "Do it.")
	if out := w.call(exitDone, "run"); !strings.Contains(out, "campaign done") {
		t.Fatalf("run output:\n%s", out)
	}
	status := w.call(0, "status")
	if !strings.Contains(status, "ignored  cannot_proceed_reason next to outcome ok, left out of the output: the docs were thin") ||
		!strings.Contains(status, `output   {"outcome":"ok"}`) {
		t.Fatalf("status:\n%s", status)
	}
}

// An answer outside the schema that the same session corrected shows as a
// repair next to its attempt, and in the events.
func TestStatusShowsARepairedAnswer(t *testing.T) {
	w := newAgentWorld(t, `{"trace": "trace.jsonl",
		"nodes": {"a": [[{"session": "s-1"}, {"result": {"outcome": "okay"}}]]},
		"repairs": {"a": [[{"result": {"outcome": "ok"}}]]}}`, "Do it.")
	if out := w.call(exitDone, "run"); !strings.Contains(out, "a attempt 1: the answer does not match the schema") ||
		!strings.Contains(out, "(repair 1 of 2)") {
		t.Fatalf("run output:\n%s", out)
	}
	status := w.call(0, "status")
	if !strings.Contains(status, "repair   1 of 2: the answer did not match the schema, the same conversation was asked to correct it: $.outcome: must be one of") ||
		!strings.Contains(status, `output   {"outcome":"ok"}`) || !strings.Contains(status, "attempt 1  outcome ok") {
		t.Fatalf("status:\n%s", status)
	}
	if events := w.call(0, "status", "-events"); !strings.Contains(events, "output_repair a attempt=1 prompt=prompts/a.1.repair1.txt session=s-1 round=1 error=") {
		t.Fatalf("events:\n%s", events)
	}
}

func TestRunInput(t *testing.T) {
	w := newAgentWorld(t, `{"trace": "trace.jsonl"}`, "Task: {{input.task}}")
	input := filepath.Join(w.dir, "input.json")
	os.WriteFile(input, []byte(`{"task": "write the docs"}`), 0o644)
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"run", "-f", w.orders, "-input", input}, &stdout, &stderr); code != exitDone {
		t.Fatalf("run exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if steps := w.trace(); len(steps) != 1 || !strings.HasPrefix(steps[0].Prompt, "Task: write the docs\n\n---\nIf you cannot do this step") {
		t.Fatalf("starts %+v", steps)
	}
	stdout.Reset()
	Main([]string{"status", "-f", w.orders, "-events"}, &stdout, &stderr)
	if s := stdout.String(); !strings.Contains(s, `input={"task":"write the docs"}`) || !strings.Contains(s, "prompt   prompts/a.1.txt") {
		t.Fatalf("status:\n%s", s)
	}

	for _, bad := range []string{`[1, 2]`, `null`, `{"task": `} {
		os.WriteFile(input, []byte(bad), 0o644)
		stderr.Reset()
		if code := Main([]string{"run", "-f", w.orders, "-input", input}, &stdout, &stderr); code != exitUsage || !strings.Contains(stderr.String(), "must hold one JSON object") {
			t.Fatalf("input %s: exit %d\n%s", bad, code, stderr.String())
		}
	}
	// Without input, a template that needs a field stops in the error state.
	stderr.Reset()
	if code := Main([]string{"run", "-f", w.orders}, &stdout, &stderr); code != exitError {
		t.Fatalf("run without input: exit %d\n%s", code, stderr.String())
	}
}

// The agent example runs its stub adapter as a separately built program;
// here the test binary stands in for it.
func TestAgentExample(t *testing.T) {
	src := filepath.Join("..", "..", "examples", "agent")
	dir := tempDir(t)
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, orders.DefaultFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const run = "[./bin/stub-adapter, plans/happy.json]"
	if !strings.Contains(string(data), run) {
		t.Fatalf("the example no longer runs %s", run)
	}
	os.WriteFile(path, []byte(strings.Replace(string(data), run, teststep.Adapter("plans/happy.json"), 1)), 0o644)
	input := filepath.Join(dir, "input.json")
	for _, c := range []struct {
		plan  string
		exit  int
		wants []string
	}{
		{"", exitDone, []string{"review -> draft", "draft attempt 2 started", "review finished: approved", "campaign done"}},
		{"plans/invalid.json", exitDone, []string{"draft attempt 1 failed (invalid_output)", "campaign done"}},
		{"plans/exhausted.json", exitFailed, []string{"(transient)", "all 3 attempts used", "campaign failed"}},
		{"plans/offline.json", exitWaiting, []string{"no connectivity", "waits for a person"}},
	} {
		t.Setenv(stubadapter.EnvPlan, c.plan)
		var stdout, stderr bytes.Buffer
		code := Main([]string{"run", "-f", path, "-input", input}, &stdout, &stderr)
		out := stdout.String() + stderr.String()
		if code != c.exit {
			t.Fatalf("plan %q: exit %d, want %d\n%s", c.plan, code, c.exit, out)
		}
		for _, w := range c.wants {
			if !strings.Contains(out, w) {
				t.Fatalf("plan %q: output lacks %q\n%s", c.plan, w, out)
			}
		}
	}
	// The draft of the first campaign was written again with the review's
	// notes in its prompt.
	prompt, err := os.ReadFile(filepath.Join(dir, ".hetman", "campaigns", "1", "prompts", "draft.2.txt"))
	if err != nil || !strings.Contains(string(prompt), "Mention compression.") || !strings.Contains(string(prompt), "people new to operating servers") {
		t.Fatalf("second draft prompt (%v):\n%s", err, prompt)
	}
}
