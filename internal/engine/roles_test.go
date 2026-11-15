package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

// planRef is PLAN_x in a test's orders: the stub adapter run with the plan
// x.json next to the orders.
var planRef = regexp.MustCompile(`PLAN_([a-z])`)

// setupRole writes files next to the orders and the orders, in which PLAN_x
// stands for the stub adapter run with the plan x.json (in every file).
func setupRole(t *testing.T, config string, files map[string]string) *fixture {
	t.Helper()
	dir := tempDir(t)
	plans := func(s string) string {
		return planRef.ReplaceAllStringFunc(s, func(m string) string {
			return teststep.Adapter(filepath.Join(dir, m[len("PLAN_"):]+".json"))
		})
	}
	for rel, text := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(plans(text)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, orders.DefaultFile)
	if err := os.WriteFile(path, []byte(plans(config)), 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := orders.Load(path)
	if err != nil {
		t.Fatalf("orders rejected:\n%v\n%s", err, config)
	}
	os.MkdirAll(StateDir(o), 0o755)
	ch, err := chronicle.Open(ChroniclePath(o))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Close() })
	id, err := ch.NewCampaign(o.Flow, o.Path, o.Start, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, e: &Engine{Orders: o, Chron: ch, Campaign: id}, work: filepath.Join(CampaignDir(o, id), "work"), chron: ch}
}

// traced reads the start messages the stub adapter with plan x.json
// received, when the plan traces to x.jsonl.
func (f *fixture) traced(x string) []adapter.Step {
	f.t.Helper()
	file, err := os.Open(filepath.Join(f.e.Orders.Dir, x+".jsonl"))
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
		s.Prompt = strings.Replace(s.Prompt, unableInstruction(), "", 1)
		out = append(out, s)
	}
	return out
}

// ran lists "node executor reason" for every attempt of the campaign
// tree, in the order the clients were chosen.
func ran(t *testing.T, ch *chronicle.Chronicle, top int64) []string {
	t.Helper()
	tree, err := ch.Tree(top)
	if err != nil {
		t.Fatal(err)
	}
	starts, err := ch.OfKinds(chronicle.StepStarted)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range starts {
		if tree[e.Campaign] && e.Executor != "" {
			reason := ""
			if e.Choice != nil {
				reason = e.Choice.Reason
				if e.Choice.From > 0 {
					reason += " from " + strconv.Itoa(e.Choice.From)
				}
			}
			out = append(out, e.Node+" "+e.Executor+" "+reason)
		}
	}
	return out
}

// roleOrders has three agent steps x, y and z run through role r.
const roleOrders = `version: 0
workdir: .
start: x
executors:
  a: {run: PLAN_a}
  b: {run: PLAN_b}
roles:
  r:
    select: %SELECT%
    clients:
      - {executor: a, model: model-a, effort: high%ALLOW_A%}
      - {executor: b, model: model-b%ALLOW_B%}
nodes:
  x: {type: agent, role: r, prompt: p.md, schema: s.json%EXTRA%, on: {ok: y, fail: failed}}
  y: {type: agent, role: r, prompt: p.md, schema: s.json, on: {ok: z, fail: failed}}
  z: {type: agent, role: r, prompt: p.md, schema: s.json, on: {ok: done, fail: failed}}
`

func roles(sel, allowA, allowB, extra string) string {
	return strings.NewReplacer("%SELECT%", sel, "%ALLOW_A%", allowA, "%ALLOW_B%", allowB, "%EXTRA%", extra).Replace(roleOrders)
}

func roleFiles(planA, planB string) map[string]string {
	return map[string]string{"p.md": "Do it.", "s.json": okSchema, "a.json": planA, "b.json": planB}
}

func TestRoleFixedUsesTheFirstClient(t *testing.T) {
	f := setupRole(t, roles("fixed", "", "", ""), roleFiles(`{"trace": "a.jsonl"}`, `{"trace": "b.jsonl"}`))
	f.drive(Done)
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a fixed", "y a fixed", "z a fixed"})
	steps := f.traced("a")
	if len(steps) != 3 || steps[0].Model != "model-a" || steps[0].Effort != "high" || len(f.traced("b")) != 0 {
		t.Fatalf("steps %+v", steps)
	}
	c := f.events(chronicle.StepStarted)[0].Choice
	if c.Role != "r" || c.Mode != orders.SelectFixed || c.Client != 1 || len(c.Usage) != 2 || c.Usage[1].Executor != "b" {
		t.Fatalf("choice %+v", c)
	}
	if fin := f.events(chronicle.StepFinished)[0]; fin.Role != "r" || fin.Executor != "a" {
		t.Fatalf("finish %+v", fin)
	}
}

func TestRoleRoundRobinTakesTurnsAcrossARestart(t *testing.T) {
	f := setupRole(t, roles("round_robin", "", "", ""), roleFiles(
		`{"trace": "a.jsonl", "nodes": {"x": [[{"touch": "x-started"}, {"sleep": "300ms"}, {"result": {"outcome": "ok"}}]]}}`, `{"trace": "b.jsonl"}`))
	done := f.driveAsync(context.Background())
	waitFile(t, filepath.Join(f.e.Orders.Dir, "x-started"))
	f.record(chronicle.Event{Kind: chronicle.HaltRequested})
	if r := <-done; r.err != nil || r.res != Halted {
		t.Fatalf("drive %+v", r)
	}
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a turn"})
	// A new engine, as after a restart, knows the turn from the chronicle.
	f.record(chronicle.Event{Kind: chronicle.Resumed})
	f.e = &Engine{Orders: f.e.Orders, Chron: f.chron, Campaign: f.e.Campaign}
	f.drive(Done)
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a turn", "y b turn", "z a turn"})
	if s := f.traced("b"); len(s) != 1 || s[0].Model != "model-b" || s[0].Effort != "" {
		t.Fatalf("b steps %+v", s)
	}
}

const turnSegment = `version: 0
workdir: .
flow: item
start: work
executors:
  a: {run: PLAN_a}
  b: {run: PLAN_b}
roles:
  r:
    select: round_robin
    clients: [{executor: a}, {executor: b}]
nodes:
  work: {type: agent, role: r, prompt: ../p.md, schema: ../s.json, on: {ok: done, fail: failed}}
`

func TestRoleRoundRobinTakesTurnsAcrossChildren(t *testing.T) {
	for _, parallel := range []string{"1", "3"} {
		t.Run("parallel "+parallel, func(t *testing.T) {
			f := setupRole(t, `version: 0
workdir: .
start: plan
executors:
  a: {run: PLAN_a}
  b: {run: PLAN_b}
roles:
  r:
    select: round_robin
    clients: [{executor: a}, {executor: b}]
nodes:
  plan: {type: agent, role: r, prompt: p.md, schema: s.json, on: {ok: each, fail: failed}}
  each: {type: foreach, segment: segments/item.yaml, items: output.plan.items, parallel: `+parallel+`, on: {done: done, failed: failed}}
`, map[string]string{"p.md": "Do it.", "s.json": okSchema, "segments/item.yaml": turnSegment,
				"a.json": `{"nodes": {"plan": [[{"result": {"outcome": "ok", "items": [{"id": "i1"}, {"id": "i2"}, {"id": "i3"}, {"id": "i4"}]}}]]}}`,
				"b.json": `{}`})
			f.drive(Done)
			got := ran(t, f.chron, f.e.Campaign)
			if len(got) != 5 {
				t.Fatalf("attempts %v", got)
			}
			// The turns alternate in the order the choices were made, whether
			// the children run one by one or at the same time.
			for i, r := range got {
				x := []string{"a", "b"}[i%2]
				if !strings.Contains(r, " "+x+" turn") {
					t.Fatalf("choice %d is %q; all: %v", i+1, r, got)
				}
			}
		})
	}
}

func TestRoleQuotaWithMeasuredUsage(t *testing.T) {
	f := setupRole(t, roles("quota", "", "", ""), roleFiles(
		`{"trace": "a.jsonl", "usage": {"five_hour": {"used": 0.8, "resets_in": "2h"}, "weekly": {"used": 0.2}}}`,
		`{"trace": "b.jsonl", "usage": {"five_hour": {"used": 0.3}, "weekly": {"used": 0.5}}}`))
	f.drive(Done)
	// b's worst share (weekly 0.5) is smaller than a's (five hours 0.8).
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x b quota", "y b quota", "z b quota"})
	c := f.events(chronicle.StepStarted)[0].Choice
	a := c.Usage[0]
	if len(a.Windows) != 2 || a.Windows[0].Name != "five_hour" || a.Windows[0].Source != chronicle.SourceMeasured ||
		*a.Windows[0].Used != 0.8 || a.Windows[0].ResetsAt == nil || a.Windows[0].At == nil || a.Windows[1].ResetsAt != nil || a.Exhausted {
		t.Fatalf("usage of a %+v", a)
	}
}

func TestRoleQuotaSkipsAClientWithAWindowUsedUp(t *testing.T) {
	clock := newFakeClock()
	f := setupRole(t, roles("quota", "", "", ""), roleFiles(
		`{"trace": "a.jsonl", "usage": {"five_hour": {"used": 1, "resets_in": "2h"}, "weekly": {"used": 0.1}}}`,
		`{"trace": "b.jsonl", "usage": {"five_hour": {"used": 0.2}, "weekly": {"used": 0.9}}}`))
	f.e.Clock = clock
	f.drive(Done)
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x b quota", "y b quota", "z b quota"})
	a := f.events(chronicle.StepStarted)[0].Choice.Usage[0]
	if !a.Exhausted || a.Why != chronicle.SourceMeasured || a.Until == nil || a.Until.Before(time.Now().Add(110*time.Minute)) {
		t.Fatalf("usage of a %+v", a)
	}
}

func TestRoleQuotaWithEstimates(t *testing.T) {
	// No adapter reports usage: the engine counts the attempts it saw
	// against the allowance of each client. a may make 2 attempts in five
	// hours, b 4: a is first on the tie, then b until its share is as
	// large, then a on the tie again.
	f := setupRole(t, roles("quota", ", allowance: {five_hour: 2}", ", allowance: {five_hour: 4}", ""), roleFiles(`{}`, `{}`))
	f.drive(Done)
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a quota", "y b quota", "z b quota"})
	u := f.events(chronicle.StepStarted)[2].Choice.Usage
	if w := u[0].Windows[0]; w.Source != chronicle.SourceEstimated || *w.Used != 0.5 || w.Attempts != 1 || w.Allowance != "2" {
		t.Fatalf("a five hours %+v", w)
	}
	if w := u[0].Windows[1]; w.Source != chronicle.SourceUnknown || w.Used != nil || w.Attempts != 1 {
		t.Fatalf("a weekly %+v", w)
	}
	if w := u[1].Windows[0]; *w.Used != 0.25 {
		t.Fatalf("b five hours %+v", w)
	}

	// Usage is shared by every campaign of the folder: a second campaign
	// sees a at 1 of 2 and b at 2 of 4, a tie, and then a at its allowance.
	id, err := f.chron.NewCampaign(f.e.Orders.Flow, f.e.Orders.Path, f.e.Orders.Start, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	f.e = &Engine{Orders: f.e.Orders, Chron: f.chron, Campaign: id}
	f.drive(Done)
	want(t, ran(t, f.chron, id), []string{"x a quota", "y b quota", "z b quota"})
	u = f.events(chronicle.StepStarted)[2].Choice.Usage
	if !u[0].Exhausted || u[0].Why != chronicle.SourceEstimated || u[0].Until == nil {
		t.Fatalf("a at its allowance %+v", u[0])
	}
}

func TestRoleQuotaWithAMonthlyAllowance(t *testing.T) {
	// 13 attempts a month are 3 a week: a is at a third after one attempt,
	// b (4 a week) at a quarter, then at a half.
	f := setupRole(t, roles("quota", ", allowance: {monthly: 13}", ", allowance: {weekly: 4}", ""), roleFiles(`{}`, `{}`))
	r := f.e.Orders.Roles["r"]
	if a := r.Clients[0].Allowance[orders.WindowWeekly]; a.Attempts != 3 || !a.Monthly || a.Text != "13/monthly" {
		t.Fatalf("allowance %+v", a)
	}
	f.drive(Done)
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a quota", "y b quota", "z b quota"})
	if w := f.events(chronicle.StepStarted)[2].Choice.Usage[0].Windows[1]; w.Name != "weekly" || w.Allowance != "13/monthly" {
		t.Fatalf("weekly %+v", w)
	}
}

func TestRoleSwitchOnLimitDoesNotUseAnAttempt(t *testing.T) {
	f := setupRole(t, roles("fixed", "", "", ", max_attempts: 1"), roleFiles(
		`{"trace": "a.jsonl", "nodes": {"x": [[{"session": "s-a"}, {"error": "limit", "message": "usage limit reached", "retry_in": "1h"}]]}}`,
		`{"trace": "b.jsonl"}`))
	f.e.Clock = newFakeClock()
	f.drive(Done)
	// x ends on b with max_attempts 1: the limit did not count. y and z
	// run on b too: a's last attempt hit a limit that resets in an hour.
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a fixed", "x b fixed from 1", "y b fixed", "z b fixed"})
	want(t, f.finished(), []string{"x#1 /limit", "x#2 ok/", "y#1 ok/", "z#1 ok/"})
	if s := f.traced("b")[0]; s.Session != nil || s.Prompt != "Do it." || s.Attempt != 2 {
		t.Fatalf("b continued %+v", s)
	}
	c := f.events(chronicle.StepStarted)[1].Choice
	if !c.Usage[0].Exhausted || c.Usage[0].Why != chronicle.SourceLimit || c.Usage[0].Until == nil {
		t.Fatalf("a after its limit %+v", c.Usage[0])
	}
	if n := len(f.events(chronicle.NoClient)); n != 0 {
		t.Fatalf("%d no_client events", n)
	}
}

func TestRoleWaitsForTheEarliestResetWhenEveryClientIsExhausted(t *testing.T) {
	f := setupRole(t, roles("fixed", "", "", ""), roleFiles(
		`{"trace": "a.jsonl", "nodes": {"x": [[{"error": "limit", "retry_in": "2h"}]]}}`,
		`{"trace": "b.jsonl", "nodes": {"x": [[{"exit": 9}], [{"session": "s-b"}, {"error": "limit", "retry_in": "1h"}], [{"require_session": "s-b"}, {"result": {"outcome": "ok"}}]]}}`))
	clock := newFakeClock()
	f.e.Clock = clock
	done := f.driveAsync(context.Background())
	clock.blocked(t)
	nc := f.events(chronicle.NoClient)
	if len(nc) != 1 || nc[0].WaitUntil == nil || nc[0].WaitUntil.Before(time.Now().Add(59*time.Minute)) || nc[0].WaitUntil.After(time.Now().Add(61*time.Minute)) ||
		nc[0].Choice.Reason != chronicle.ChoseNone || nc[0].Choice.Client != 0 {
		t.Fatalf("no_client %+v", nc)
	}
	if st := f.state(); st.Waiting != chronicle.WaitLimit || st.WaitUntil.IsZero() || st.Failures != 0 {
		t.Fatalf("state %+v", st)
	}
	clock.Advance(61 * time.Minute)
	if r := <-done; r.err != nil || r.res != Done {
		t.Fatalf("drive %+v\n%s", r, f.dump())
	}
	// b's limit reset first; b continues its conversation, a still waits.
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a fixed", "x b fixed from 1", "x b resume", "y b fixed", "z b fixed"})
}

func TestRoleWithoutAResetTimeWaitsForAPerson(t *testing.T) {
	f := setupRole(t, roles("round_robin", "", "", ""), roleFiles(
		`{"nodes": {"x": [[{"error": "limit"}]]}}`,
		`{"nodes": {"x": [[{"exit": 9}], [{"error": "limit"}], [{"result": {"outcome": "ok"}}]]}}`))
	f.drive(Waiting)
	if nc := f.events(chronicle.NoClient); len(nc) != 1 || nc[0].WaitUntil != nil {
		t.Fatalf("no_client %+v", nc)
	}
	f.drive(Waiting) // as after a restart: still waiting for a person
	f.record(chronicle.Event{Kind: chronicle.Resumed})
	f.drive(Done)
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a turn", "x b turn from 1", "x b resume", "y a turn", "z b turn"})
}

func TestRoleDialogueOnAnotherClientKeepsTheAnswers(t *testing.T) {
	f := setupRole(t, roles("fixed", "", "", ", dialogue: 2"), roleFiles(
		`{"trace": "a.jsonl", "nodes": {"x": [[{"session": "s-a"}, {"result": `+q1+`}], [{"require_session": "s-a"}, {"error": "limit", "retry_in": "1h"}]]}}`,
		`{"trace": "b.jsonl"}`))
	done := f.driveAsync(context.Background())
	f.waitAsked(0)
	f.answer(map[string]string{"color": "blue", "why": "it is calm"}, nil, "")
	f.wait(done, Done)
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a fixed", "x a resume", "x b fixed from 1", "y b fixed", "z b fixed"})
	s := f.traced("b")[0]
	for _, w := range []string{"Do it.", "Earlier in this step you asked a person questions", "Round 1:", "Which color? (color)\n  Answer: blue", "Answer: it is calm", "You may ask 1 more time(s)"} {
		if !strings.Contains(s.Prompt, w) {
			t.Fatalf("prompt on b lacks %q:\n%s", w, s.Prompt)
		}
	}
	if s.Session != nil || strings.Contains(s.Prompt, "The person answered your questions") {
		t.Fatalf("b continued a conversation it does not have: %+v", s)
	}
}

func TestRoleInterruptedStepResumesOnItsClient(t *testing.T) {
	f := setupRole(t, roles("round_robin", "", "", ""), roleFiles(
		`{"trace": "a.jsonl", "nodes": {"x": [[{"session": "s-1"}, {"touch": "started"}, {"hang": true}], [{"require_session": "s-1"}, {"result": {"outcome": "ok"}}]]}}`,
		`{"trace": "b.jsonl"}`))
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
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a turn", "x a resume", "y b turn", "z a turn"})
	if s := f.traced("a")[1]; s.Session == nil || *s.Session != "s-1" {
		t.Fatalf("resumed %+v", s)
	}
}

func TestRoleRetryAndRewindKeepTakingTurns(t *testing.T) {
	f := setupRole(t, roles("round_robin", "", "", ""), roleFiles(
		`{"nodes": {"z": [[{"error": "permanent", "message": "broken"}]]}}`, `{}`))
	f.drive(Failed)
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a turn", "y b turn", "z a turn"})
	release, node, err := Retry(f.e.Orders, f.chron, f.e.Campaign, "tester", ViaCommand)
	if err != nil || node != "z" {
		t.Fatalf("retry %q %v", node, err)
	}
	release()
	f.drive(Done)
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a turn", "y b turn", "z a turn", "z b turn"})
	release, err = Rewind(f.e.Orders, f.chron, f.e.Campaign, "y", "tester", ViaCommand)
	if err != nil {
		t.Fatal(err)
	}
	release()
	f.drive(Done)
	want(t, ran(t, f.chron, f.e.Campaign), []string{"x a turn", "y b turn", "z a turn", "z b turn", "y a turn", "z b turn"})
}

func TestNodeWithAnExecutorRecordsNoChoice(t *testing.T) {
	f := setupAgent(t, one(""), agentFiles(`{}`), "")
	f.drive(Done)
	if c := f.events(chronicle.StepStarted)[0].Choice; c != nil {
		t.Fatalf("choice %+v", c)
	}
	if fin := f.events(chronicle.StepFinished)[0]; fin.Executor != "stub" || fin.Role != "" {
		t.Fatalf("finish %+v", fin)
	}
}
