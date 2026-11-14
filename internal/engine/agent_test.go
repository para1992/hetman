package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

const okSchema = `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}, "summary": {"type": "string"}}, "required": ["outcome"]}`

const reviewSchema = `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok", "needs_rework"]}, "notes": {"type": "string"}}, "required": ["outcome"], "additionalProperties": false}`

// setupAgent writes files (relative path to text) next to the orders, then
// the orders, in which PLAN stands for the stub adapter run with plan.json.
func setupAgent(t *testing.T, config string, files map[string]string, input string) *fixture {
	t.Helper()
	dir := tempDir(t)
	for rel, text := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	config = strings.ReplaceAll(config, "PLAN", teststep.Adapter(filepath.Join(dir, "plan.json")))
	path := filepath.Join(dir, orders.DefaultFile)
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := orders.Load(path)
	if err != nil {
		t.Fatalf("orders rejected:\n%v\n%s", err, config)
	}
	if err := os.MkdirAll(StateDir(o), 0o755); err != nil {
		t.Fatal(err)
	}
	ch, err := chronicle.Open(ChroniclePath(o))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Close() })
	var in json.RawMessage
	if input != "" {
		in = json.RawMessage(input)
	}
	id, err := ch.NewCampaign(o.Flow, o.Path, o.Start, in)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, e: &Engine{Orders: o, Chron: ch, Campaign: id}, work: filepath.Join(CampaignDir(o, id), "work"), chron: ch}
}

// steps reads the start messages the stub adapter received.
func (f *fixture) steps() []adapter.Step {
	f.t.Helper()
	file, err := os.Open(filepath.Join(f.e.Orders.Dir, "trace.jsonl"))
	if err != nil {
		return nil
	}
	defer file.Close()
	var out []adapter.Step
	sc := bufio.NewScanner(file)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var s adapter.Step
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (f *fixture) file(rel string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(CampaignDir(f.e.Orders, f.e.Campaign), filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *fixture) state() chronicle.State { return chronicle.Replay(f.events("")) }

// finished lists "node#attempt outcome/failure" for every step_finished.
func (f *fixture) finished() []string {
	var out []string
	for _, e := range f.events(chronicle.StepFinished) {
		out = append(out, e.Node+"#"+strconv.Itoa(e.Attempt)+" "+e.Outcome+"/"+e.Failure)
	}
	return out
}

func want(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

const pipelineOrders = `version: 0
start: build
executors:
  stub: {run: PLAN}
nodes:
  build: {type: script, run: ` + "%BUILD%" + `, on: {ok: write, fail: failed}}
  write:
    type: agent
    executor: stub
    prompt: prompts/write.md
    schema: schemas/ok.json
    model: model-a
    effort: high
    tools: [read, edit]
    on: {ok: review, fail: failed}
  review:
    type: agent
    executor: stub
    prompt: prompts/review.md
    schema: schemas/review.json
    on: {ok: done, needs_rework: write, fail: failed}
`

func TestAgentStepRendersPromptAndFollowsOutcome(t *testing.T) {
	f := setupAgent(t, strings.Replace(pipelineOrders, "%BUILD%", arg("artifact"), 1), map[string]string{
		"prompts/write.md":    "Task: {{input.task}} (priority {{ input.meta.priority }}){{rework}}",
		"prompts/review.md":   "Summary: {{output.write.summary}}\nBuild: {{artifact.build.out.txt}}Input: {{input}}\nNot run: [{{output.review}}]",
		"schemas/ok.json":     okSchema,
		"schemas/review.json": reviewSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {
			"write": [[{"log": "reading the code"}, {"stderr": "diagnostic line"}, {"result": {"outcome": "ok", "summary": "fixed it"}}]],
			"review": [[{"result": {"outcome": "ok", "notes": "fine"}}]]}}`,
	}, `{"task": "fix the bug", "meta": {"priority": 2}}`)
	f.drive(Done)
	steps := f.steps()
	if len(steps) != 2 {
		t.Fatalf("steps %+v", steps)
	}
	w := steps[0]
	if w.Prompt != "Task: fix the bug (priority 2)" || w.Model != "model-a" || w.Effort != "high" ||
		strings.Join(w.Tools, ",") != "read,edit" || w.Workdir != f.e.Orders.Dir || w.Session != nil || w.Attempt != 1 ||
		w.RunID != strconv.FormatInt(f.e.Campaign, 10) || string(w.Schema) != strings.ReplaceAll(okSchema, " ", "") {
		t.Fatalf("write step %+v", w)
	}
	r := steps[1]
	wantPrompt := "Summary: fixed it\nBuild: built\nInput: {\n  \"meta\": {\n    \"priority\": 2\n  },\n  \"task\": \"fix the bug\"\n}\nNot run: []"
	if r.Prompt != wantPrompt || r.Model != "" || len(r.Tools) != 0 {
		t.Fatalf("review prompt %q\nwant %q", r.Prompt, wantPrompt)
	}
	if got := f.file(PromptFile("review", 1)); got != wantPrompt {
		t.Fatalf("stored prompt %q", got)
	}
	if got := f.file(LogFile("write", "", 1)); got != "reading the code\n" {
		t.Fatalf("log %q", got)
	}
	if got := f.file(StderrFile("write", 1)); !strings.Contains(got, "diagnostic line") {
		t.Fatalf("stderr %q", got)
	}
	start := f.events(chronicle.StepStarted)[1]
	if start.Executor != "stub" || start.Model != "model-a" || start.Effort != "high" || start.Prompt != PromptFile("write", 1) {
		t.Fatalf("step_started %+v", start)
	}
	fin := f.events(chronicle.StepFinished)
	if string(fin[1].Output) != `{"outcome":"ok","summary":"fixed it"}` || fin[1].Outcome != "ok" || *fin[1].Exit != 0 {
		t.Fatalf("write finish %+v", fin[1])
	}
	if !strings.Contains(f.file(ResultFile("write", "", 1)), `"summary": "fixed it"`) {
		t.Fatal("output not written to the result file")
	}
	if s := f.events(chronicle.AgentSession); len(s) != 0 {
		t.Fatalf("sessions %+v", s)
	}
}

func TestInvalidOutputIsRetriedWithFeedback(t *testing.T) {
	f := setupAgent(t, `version: 0
start: review
executors: {stub: {run: PLAN}}
nodes:
  review: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: done, needs_rework: failed, fail: failed}}
`, map[string]string{"p.md": "Review.", "s.json": reviewSchema, "plan.json": `{"trace": "trace.jsonl", "nodes": {"review": [
		[{"result": {"outcome": "maybe", "score": 3}}],
		[{"result": "not an object"}],
		[{"result": {"outcome": "ok"}}]]}}`}, "")
	f.drive(Done)
	want(t, f.finished(), []string{"review#1 /invalid_output", "review#2 /invalid_output", "review#3 ok/"})
	fin := f.events(chronicle.StepFinished)
	if !strings.Contains(fin[0].Error, `$.outcome: must be one of "ok", "needs_rework", got "maybe"`) ||
		!strings.Contains(fin[0].Error, `property "score" is not allowed`) {
		t.Fatalf("error %q", fin[0].Error)
	}
	steps := f.steps()
	if steps[0].Prompt != "Review." {
		t.Fatalf("first prompt %q", steps[0].Prompt)
	}
	p2 := steps[1].Prompt
	if !strings.HasPrefix(p2, "Review.\n\n---\nYour previous answer (attempt 1) was rejected") || !strings.Contains(p2, `- $.outcome: must be one of`) ||
		!strings.Contains(p2, "\n- $: property \"score\" is not allowed") {
		t.Fatalf("second prompt %q", p2)
	}
	if p3 := steps[2].Prompt; !strings.Contains(p3, "(attempt 2)") || !strings.Contains(p3, "must be of type object, got string") {
		t.Fatalf("third prompt %q", p3)
	}
	if !strings.Contains(f.file(ResultFile("review", "", 1)), `"maybe"`) {
		t.Fatal("rejected output must be kept for a person")
	}
}

const oneAgent = `version: 0
start: a
executors: {stub: {run: PLAN}}
nodes:
  a:
    type: agent
    executor: stub
    prompt: p.md
    schema: s.json
%EXTRA%
    on: {ok: done, fail: failed}
`

func one(extra string) string { return strings.Replace(oneAgent, "%EXTRA%", extra, 1) }

func agentFiles(plan string) map[string]string {
	return map[string]string{"p.md": "Do it.", "s.json": okSchema, "plan.json": plan}
}

func TestTransientErrorIsRetried(t *testing.T) {
	f := setupAgent(t, one(""), agentFiles(`{"nodes": {"a": [[{"error": "transient", "message": "busy"}], [{"result": {"outcome": "ok"}}]]}}`), "")
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 /transient", "a#2 ok/"})
	if e := f.events(chronicle.StepFinished)[0].Error; e != "busy" {
		t.Fatalf("error %q", e)
	}
}

func TestPermanentErrorFailsAtOnce(t *testing.T) {
	f := setupAgent(t, one("    max_attempts: 5"), agentFiles(`{"nodes": {"a": [[{"error": "permanent", "message": "no access"}], [{"result": {"outcome": "ok"}}]]}}`), "")
	f.drive(Failed)
	want(t, f.finished(), []string{"a#1 fail/permanent"})
}

func TestAttemptsExhausted(t *testing.T) {
	f := setupAgent(t, one("    max_attempts: 4"), agentFiles(`{"nodes": {"a": [
		[{"log": "start"}, {"exit": 3}],
		[{"raw": "this is not json"}, {"sleep": "10s"}],
		[{"log": "nothing more"}],
		[{"result": {"outcome": "ok"}, "code": 1}],
		[{"result": {"outcome": "ok"}}]]}}`), "")
	f.drive(Failed)
	want(t, f.finished(), []string{"a#1 /crash", "a#2 /protocol", "a#3 /crash", "a#4 fail/crash"})
	fin := f.events(chronicle.StepFinished)
	for i, w := range []string{"exited with code 3 without a result", "not a JSON object", "exited with code 0 without a result", "exited with code 1 after its result"} {
		if !strings.Contains(fin[i].Error, w) {
			t.Fatalf("attempt %d error %q, want %q", i+1, fin[i].Error, w)
		}
	}
}

func TestTimeoutAndIdleTimeout(t *testing.T) {
	f := setupAgent(t, one("    timeout: 3s\n    idle_timeout: 1s\n    max_attempts: 4"), agentFiles(`{"nodes": {"a": [
		[{"log": "working"}, {"hang": true}],
		[{"log": "1"}, {"sleep": "600ms"}, {"log": "2"}, {"sleep": "600ms"}, {"log": "3"}, {"sleep": "600ms"}, {"log": "4"}, {"sleep": "600ms"}, {"log": "5"}, {"sleep": "600ms"}, {"log": "6"}, {"sleep": "600ms"}],
		[{"log": "1"}, {"sleep": "600ms"}, {"log": "2"}, {"sleep": "600ms"}, {"result": {"outcome": "ok"}}]]}}`), "")
	began := time.Now()
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 /idle_timeout", "a#2 /timeout", "a#3 ok/"})
	if d := time.Since(began); d > 20*time.Second {
		t.Fatalf("took %s", d)
	}
	if got := f.file(LogFile("a", "", 3)); got != "1\n2\n" {
		t.Fatalf("slow output log %q", got)
	}
}

// fakeClock is a clock the test moves by hand.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Now()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, fakeWaiter{c.now.Add(d), ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	var keep []fakeWaiter
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			keep = append(keep, w)
		}
	}
	c.waiters = keep
}

// blocked waits until something waits on the clock.
func (c *fakeClock) blocked(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.waiters)
		c.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nothing waits on the clock")
}

type driveResult struct {
	res Result
	err error
}

func (f *fixture) driveAsync(ctx context.Context) chan driveResult {
	done := make(chan driveResult, 1)
	go func() {
		res, err := f.e.Drive(ctx)
		done <- driveResult{res, err}
	}()
	return done
}

func TestLimitWaitSurvivesRestartAndDoesNotCount(t *testing.T) {
	f := setupAgent(t, one("    max_attempts: 1"), agentFiles(`{"trace": "trace.jsonl", "nodes": {"a": [
		[{"session": "s-1"}, {"error": "limit", "message": "usage limit reached", "retry_in": "30m"}],
		[{"result": {"outcome": "ok"}}]]}}`), "")
	clock := newFakeClock()
	f.e.Clock = clock
	ctx, cancel := context.WithCancel(context.Background())
	done := f.driveAsync(ctx)
	clock.blocked(t)
	st := f.state()
	if st.Waiting != chronicle.WaitLimit || st.WaitUntil.Before(time.Now().Add(29*time.Minute)) || st.Failures != 0 {
		t.Fatalf("state while waiting %+v", st)
	}
	fin := f.events(chronicle.StepFinished)
	if len(fin) != 1 || fin[0].Failure != chronicle.FailLimit || fin[0].RetryAt == nil || fin[0].Error != "usage limit reached" {
		t.Fatalf("finish %+v", fin)
	}
	// The engine stops while waiting, as when its process ends.
	cancel()
	if r := <-done; r.err != ErrInterrupted {
		t.Fatalf("drive %+v", r)
	}

	// A new engine with its own clock, still before the retry time, waits
	// and runs nothing.
	f.e = &Engine{Orders: f.e.Orders, Chron: f.chron, Campaign: f.e.Campaign, Clock: newFakeClock()}
	clock = f.e.Clock.(*fakeClock)
	clock.Advance(10 * time.Minute)
	done = f.driveAsync(context.Background())
	clock.blocked(t)
	if n := len(f.steps()); n != 1 {
		t.Fatalf("a step ran while waiting: %d starts", n)
	}
	clock.Advance(21 * time.Minute)
	r := <-done
	if r.err != nil || r.res != Done {
		t.Fatalf("drive after the wait %+v\n%s", r, f.dump())
	}
	want(t, f.finished(), []string{"a#1 /limit", "a#2 ok/"})
	steps := f.steps()
	if steps[1].Session == nil || *steps[1].Session != "s-1" {
		t.Fatalf("the conversation cut off by the limit must continue: %+v", steps[1])
	}
}

func TestHaltDuringLimitWait(t *testing.T) {
	f := setupAgent(t, one(""), agentFiles(`{"nodes": {"a": [[{"error": "limit", "retry_in": "1h"}], [{"result": {"outcome": "ok"}}]]}}`), "")
	clock := newFakeClock()
	f.e.Clock = clock
	done := f.driveAsync(context.Background())
	clock.blocked(t)
	f.record(chronicle.Event{Kind: chronicle.HaltRequested})
	clock.Advance(waitPoll)
	if r := <-done; r.res != Halted || r.err != nil {
		t.Fatalf("drive %+v", r)
	}
	// Resuming keeps the wait for the limit.
	f.record(chronicle.Event{Kind: chronicle.Resumed})
	if st := f.state(); st.Waiting != chronicle.WaitLimit || st.WaitUntil.IsZero() {
		t.Fatalf("state after resume %+v", st)
	}
	done = f.driveAsync(context.Background())
	clock.blocked(t)
	clock.Advance(2 * time.Hour)
	if r := <-done; r.res != Done || r.err != nil {
		t.Fatalf("drive %+v", r)
	}
}

func TestLimitWithoutRetryTimeAndOfflineWaitForAPerson(t *testing.T) {
	for _, c := range []struct {
		kind, wait string
		features   string
		session    bool
	}{
		{"limit", chronicle.WaitLimit, `["resume"]`, true},
		{"offline", chronicle.WaitOffline, `["resume"]`, true},
		{"offline", chronicle.WaitOffline, `[]`, false},
	} {
		t.Run(c.kind+c.features, func(t *testing.T) {
			f := setupAgent(t, one("    max_attempts: 1"), agentFiles(`{"trace": "trace.jsonl", "features": `+c.features+`, "nodes": {"a": [
				[{"session": "s-7"}, {"error": "`+c.kind+`", "message": "gone"}],
				[{"result": {"outcome": "ok"}}]]}}`), "")
			f.drive(Waiting)
			if st := f.state(); st.Waiting != c.wait || !st.WaitUntil.IsZero() || st.Running || st.Failures != 0 {
				t.Fatalf("state %+v", st)
			}
			f.drive(Waiting) // as after a restart: still waiting for a person
			if n := len(f.steps()); n != 1 {
				t.Fatalf("starts %d", n)
			}
			f.record(chronicle.Event{Kind: chronicle.Resumed})
			f.drive(Done)
			want(t, f.finished(), []string{"a#1 /" + c.kind, "a#2 ok/"})
			s := f.steps()[1].Session
			if c.session != (s != nil) || (s != nil && *s != "s-7") {
				t.Fatalf("session passed %v, want passed=%v", s, c.session)
			}
		})
	}
}

func TestInterruptedAgentStepContinuesItsSession(t *testing.T) {
	f := setupAgent(t, one(""), agentFiles(`{"trace": "trace.jsonl", "nodes": {"a": [
		[{"session": "s-9"}, {"log": "working"}, {"touch": "started"}, {"hang": true}],
		[{"result": {"outcome": "ok"}}]]}}`), "")
	ctx, cancel := context.WithCancel(context.Background())
	done := f.driveAsync(ctx)
	waitFile(t, filepath.Join(f.e.Orders.Dir, "started"))
	waitEvent(t, f, chronicle.AgentSession)
	cancel()
	if r := <-done; r.err != ErrInterrupted {
		t.Fatalf("drive %+v", r)
	}
	f.e = &Engine{Orders: f.e.Orders, Chron: f.chron, Campaign: f.e.Campaign}
	f.drive(Done)
	steps := f.steps()
	if len(steps) != 2 || steps[1].Session == nil || *steps[1].Session != "s-9" || steps[1].Attempt != 2 {
		t.Fatalf("steps %+v", steps)
	}
	if s := f.events(chronicle.StepStarted)[1]; s.Session != "s-9" {
		t.Fatalf("step_started %+v", s)
	}
	if c := f.events(chronicle.ProcessCleanup); len(c) != 1 {
		t.Fatalf("cleanup %+v", c)
	}
}

func TestReworkLoopEndsByExhaustingAttempts(t *testing.T) {
	f := setupAgent(t, `version: 0
start: implement
executors: {stub: {run: PLAN}}
nodes:
  implement:
    type: agent
    executor: stub
    prompt: implement.md
    schema: ok.json
    max_attempts: 2
    on: {ok: review, fail: failed}
  review:
    type: agent
    executor: stub
    prompt: review.md
    schema: review.json
    on: {ok: done, needs_rework: implement, fail: failed}
`, map[string]string{
		"implement.md": "Implement.\n{{rework}}",
		"review.md":    "Review: {{output.implement.summary}}",
		"ok.json":      okSchema,
		"review.json":  reviewSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {
			"implement": [[{"result": {"outcome": "ok", "summary": "v1"}}], [{"result": {"outcome": "nope"}}], [{"exit": 2}]],
			"review": [[{"result": {"outcome": "needs_rework", "notes": "add tests"}}]]}}`,
	}, "")
	f.drive(Failed)
	want(t, f.finished(), []string{"implement#1 ok/", "review#1 needs_rework/", "implement#2 /invalid_output", "implement#3 fail/crash"})
	steps := f.steps()
	if steps[0].Prompt != "Implement.\n" {
		t.Fatalf("first visit prompt %q", steps[0].Prompt)
	}
	p := steps[2].Prompt
	if !strings.Contains(p, `step "review" finished with outcome "needs_rework" and sent the flow back here`) ||
		!strings.Contains(p, `"notes": "add tests"`) {
		t.Fatalf("revisit prompt %q", p)
	}
	if p := steps[3].Prompt; !strings.Contains(p, "add tests") || !strings.Contains(p, "(attempt 2) was rejected") {
		t.Fatalf("retry prompt %q", p)
	}
}

func TestGateReturnExplainsFailedChecks(t *testing.T) {
	f := setupAgent(t, `version: 0
start: fix
executors: {stub: {run: PLAN}}
checks:
  unit: {run: `+arg("exit", "1")+`}
nodes:
  fix: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: tests, fail: failed}}
  tests: {type: gate, checks: [unit], on: {pass: done, blocked: fix}}
`, map[string]string{"p.md": "Fix.{{rework}}", "s.json": okSchema, "plan.json": `{"trace": "trace.jsonl", "nodes": {"fix": [[{"result": {"outcome": "ok"}}], [{"exit": 9}]]}}`}, "")
	// The check fails, so the gate sends the flow back to fix, whose next
	// attempts crash until the budget is used up.
	f.drive(Failed)
	p := f.steps()[1].Prompt
	if !strings.Contains(p, `step "tests" finished with outcome "blocked"`) || !strings.Contains(p, "Checks that did not pass: unit (fail)") {
		t.Fatalf("prompt %q", p)
	}
}

func TestMissingInputFieldStopsInError(t *testing.T) {
	f := setupAgent(t, one(""), map[string]string{"p.md": "Do {{input.task}}.", "s.json": okSchema, "plan.json": `{}`}, `{"other": 1}`)
	f.drive(Errored)
	if e := f.events(chronicle.Stopped); len(e) != 1 || !strings.Contains(e[0].Error, `the run input has no field "task"`) {
		t.Fatalf("errors %+v", e)
	}
	if s := f.events(chronicle.StepStarted); len(s) != 0 {
		t.Fatalf("a step started: %+v", s)
	}
}

func TestAdapterOfAnotherProtocolStopsInError(t *testing.T) {
	f := setupAgent(t, one(""), agentFiles(`{"protocol": [1]}`), "")
	f.drive(Errored)
	if e := f.events(chronicle.Stopped); len(e) != 1 || !strings.Contains(e[0].Error, "speaks protocol versions [1], not 0") {
		t.Fatalf("errors %+v", e)
	}
}

func TestReplayOfAgentEvents(t *testing.T) {
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	ev := []chronicle.Event{
		{Kind: chronicle.CampaignStarted, Node: "a"},
		{Kind: chronicle.StepStarted, Node: "a", Attempt: 1},
		{Kind: chronicle.StepFinished, Node: "a", Attempt: 1, Details: chronicle.Details{Failure: chronicle.FailInvalidOutput}},
		{Kind: chronicle.StepStarted, Node: "a", Attempt: 2},
		{Kind: chronicle.StepFinished, Node: "a", Attempt: 2, Details: chronicle.Details{Failure: chronicle.FailLimit, RetryAt: &at}},
	}
	st := chronicle.Replay(ev)
	if st.Failures != 1 || st.Waiting != chronicle.WaitLimit || !st.WaitUntil.Equal(at) || st.HasOutcome || st.Running {
		t.Fatalf("state %+v", st)
	}
	st = chronicle.Replay(append(ev, chronicle.Event{Kind: chronicle.Resumed}))
	if st.Waiting != chronicle.WaitLimit {
		t.Fatal("resume must keep a wait with a time")
	}
	ev = append(ev, chronicle.Event{Kind: chronicle.StepStarted, Node: "a", Attempt: 3},
		chronicle.Event{Kind: chronicle.StepFinished, Node: "a", Attempt: 3, Outcome: "ok"},
		chronicle.Event{Kind: chronicle.Transition, Node: "a", Outcome: "ok", Details: chronicle.Details{To: "b"}})
	if st := chronicle.Replay(ev); st.Failures != 0 || st.Waiting != "" || st.Node != "b" {
		t.Fatalf("state %+v", st)
	}
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func waitEvent(t *testing.T, f *fixture, kind string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.events(kind)) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a %s event", kind)
}
