package watchtower

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

func parse(t *testing.T, config string) *orders.Orders {
	t.Helper()
	o, err := orders.Parse(filepath.Join(t.TempDir(), "orders.yaml"), []byte(config))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// A flow with a rework loop and a node reached only on failure.
const branchy = `version: 0
workdir: .
start: build
nodes:
  build:  {type: script, run: [b], on: {ok: test, fail: fix}}
  test:   {type: script, run: [t], on: {ok: ship, fail: build}}
  fix:    {type: script, run: [f], on: {ok: build, fail: failed}}
  ship:   {type: script, run: [s], on: {ok: done, fail: failed}}
  orphan: {type: script, run: [o], on: {ok: done, fail: failed}}
`

type script []chronicle.Event

func (s *script) add(kind, node string, attempt int, outcome, to string) {
	*s = append(*s, chronicle.Event{Seq: int64(len(*s) + 1), At: time.Unix(int64(len(*s)), 0), Kind: kind, Node: node,
		Attempt: attempt, Outcome: outcome, Details: chronicle.Details{To: to}})
}

func (s *script) step(node string, attempt int, outcome, to string) {
	s.add(chronicle.StepStarted, node, attempt, "", "")
	s.add(chronicle.StepFinished, node, attempt, outcome, "")
	s.add(chronicle.Transition, node, 0, outcome, to)
}

func names(cols []Column) (line []string, side map[string][]string) {
	side = map[string][]string{}
	for _, c := range cols {
		line = append(line, c.Node)
		for _, s := range c.Side {
			side[c.Node] = append(side[c.Node], s.Node)
		}
	}
	return line, side
}

func TestLayoutBeforeAnyStep(t *testing.T) {
	o := parse(t, branchy)
	var ev script
	ev.add(chronicle.CampaignStarted, "build", 0, "", "")
	c := build(chronicle.Campaign{ID: 1}, ev, o, t.TempDir(), func() bool { return true })
	line, side := names(c.Columns)
	if !reflect.DeepEqual(line, []string{"build", "test", "ship"}) {
		t.Fatalf("main line %v", line)
	}
	if !reflect.DeepEqual(side, map[string][]string{"build": {"fix"}, "ship": {"orphan"}}) {
		t.Fatalf("side nodes %v", side)
	}
	if c.Columns[0].Status != StatusWaiting || !c.Columns[0].Current || c.Columns[1].Status != StatusNotRun {
		t.Fatalf("statuses %+v", c.Columns)
	}
	if ex := c.Columns[0].Exits; ex[0].Outcome != "ok" || !ex[0].Main || ex[1].Main {
		t.Fatalf("exits of build %+v", ex)
	}
	if ex := c.Columns[2].Exits; !ex[0].Main || ex[0].To != "done" {
		t.Fatalf("exits of ship %+v", ex)
	}
}

func TestLayoutAfterRework(t *testing.T) {
	o := parse(t, branchy)
	var ev script
	ev.add(chronicle.CampaignStarted, "build", 0, "", "")
	ev.step("build", 1, "fail", "fix")
	ev.step("fix", 1, "ok", "build")
	ev.step("build", 2, "ok", "test")
	ev.step("test", 1, "fail", "build")
	ev.step("build", 3, "ok", "test")
	ev.add(chronicle.StepStarted, "test", 2, "", "")
	c := build(chronicle.Campaign{ID: 1}, ev, o, t.TempDir(), func() bool { return true })
	line, side := names(c.Columns)
	// build's last transition was ok -> test; test's was fail -> build,
	// which is already on the line, so the line continues by ok -> ship.
	if !reflect.DeepEqual(line, []string{"build", "test", "ship"}) || !reflect.DeepEqual(side["build"], []string{"fix"}) {
		t.Fatalf("main line %v, side %v", line, side)
	}
	b, test := c.Columns[0], c.Columns[1]
	if b.Attempts != 3 || b.Status != StatusPassed || test.Attempts != 2 || test.Status != StatusRunning || !test.Current {
		t.Fatalf("build %+v\ntest %+v", b, test)
	}
	if b.Exits[0].Taken != 2 || !b.Exits[0].Last || b.Exits[1].Taken != 1 {
		t.Fatalf("exits of build %+v", b.Exits)
	}
	if fix := b.Side[0]; fix.Attempts != 1 || fix.Status != StatusPassed {
		t.Fatalf("fix %+v", fix)
	}
	if c.State != StateRunning || len(c.Nodes["build"]) != 3 || c.Nodes["build"][0].Transition != "fix" {
		t.Fatalf("state %s, history %+v", c.State, c.Nodes["build"])
	}
	// The same events without a driver: the running step was interrupted.
	c = build(chronicle.Campaign{ID: 1}, ev, o, t.TempDir(), func() bool { return false })
	if c.State != StateInterrupted || c.Columns[1].Status != StatusInterrupted {
		t.Fatalf("without a driver: %s, test %s", c.State, c.Columns[1].Status)
	}
}

func TestLayoutStopsAtFailedEnd(t *testing.T) {
	o := parse(t, branchy)
	var ev script
	ev.add(chronicle.CampaignStarted, "build", 0, "", "")
	ev.step("build", 1, "fail", "fix")
	ev.step("fix", 1, "fail", "failed")
	c := build(chronicle.Campaign{ID: 1}, ev, o, t.TempDir(), func() bool { return false })
	line, side := names(c.Columns)
	// build's last transition went to fix, which becomes the main line; fix
	// ended the campaign, so the line ends there.
	if !reflect.DeepEqual(line, []string{"build", "fix"}) || c.State != StateFailed || c.End != orders.Failed {
		t.Fatalf("line %v, state %s", line, c.State)
	}
	// test is first reached from build, ship from test (so also under
	// build), and orphan from nowhere (under the last column).
	if !reflect.DeepEqual(side, map[string][]string{"build": {"test", "ship"}, "fix": {"orphan"}}) {
		t.Fatalf("side %v", side)
	}
	if ex := c.Columns[1].Exits; !ex[1].Main || ex[1].To != "failed" || ex[0].Main {
		t.Fatalf("exits of fix %+v", ex)
	}
}

func TestGateTilesWhileRunning(t *testing.T) {
	o := parse(t, `version: 0
workdir: .
start: g
checks:
  a: {run: [a], soft: true}
  b: {run: [b]}
  c: {run: [c]}
nodes:
  g: {type: gate, checks: [a, b, c], on: {pass: done, blocked: failed}}
`)
	var ev script
	ev.add(chronicle.CampaignStarted, "g", 0, "", "")
	ev.add(chronicle.StepStarted, "g", 1, "", "")
	ev = append(ev, chronicle.Event{Seq: 3, At: time.Unix(3, 0), Kind: chronicle.CheckFinished, Node: "g", Check: "a", Attempt: 1,
		Outcome: "fail", Details: chronicle.Details{Soft: true, Millis: 500}})
	c := build(chronicle.Campaign{ID: 1}, ev, o, t.TempDir(), func() bool { return true })
	var got []string
	for _, tl := range c.Columns[0].Tiles {
		got = append(got, tl.Check+"="+tl.Status)
	}
	if want := []string{"a=warning", "b=running", "c=waiting"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tiles %v, want %v", got, want)
	}
	if r := c.Nodes["g"][0].Checks[1]; r.Log != "logs/g.b.1.log" || r.Started == nil || !r.Started.Equal(time.Unix(3, 0)) {
		t.Fatalf("running check %+v", r)
	}
}

func TestWatcherRunsOnlyWhileStreamsAreOpen(t *testing.T) {
	o := parse(t, branchy)
	ch, err := chronicle.Open(filepath.Join(t.TempDir(), "chronicle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, o, ch)
	s.Poll = 10 * time.Millisecond
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	time.Sleep(100 * time.Millisecond)
	if n := s.hub.looks.Load(); n != 0 {
		t.Fatalf("%d looks at the chronicle with no page connected", n)
	}
	reqCtx, stop := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, ts.URL+"/api/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := s.hub.looks.Load(); n == 0 {
		t.Fatal("no looks at the chronicle while a page is connected")
	}
	stop()
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.hub.mu.Lock()
		running := s.hub.running
		s.hub.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher still running after the page left")
		}
		time.Sleep(10 * time.Millisecond)
	}
	n := s.hub.looks.Load()
	time.Sleep(100 * time.Millisecond)
	if m := s.hub.looks.Load(); m != n {
		t.Fatalf("looks went on after the page left: %d then %d", n, m)
	}
}

func TestHostAllowed(t *testing.T) {
	s := &Server{Hosts: []string{"devbox"}}
	for host, want := range map[string]bool{
		"127.0.0.1:8740": true, "localhost:8740": true, "[::1]:8740": true, "LOCALHOST": true,
		"devbox:8740": true, "evil.example:8740": false, "127.0.0.1.evil.example": false,
	} {
		if got := s.hostAllowed(host); got != want {
			t.Errorf("%s: %v, want %v", host, got, want)
		}
	}
}

func TestCompleteRunes(t *testing.T) {
	text := []byte("abé中")
	for cut := 0; cut <= len(text); cut++ {
		n := completeRunes(text[:cut])
		if !strings.HasPrefix(string(text), string(text[:n])) || (n < cut && cut-n >= 4) {
			t.Fatalf("cut %d: kept %d", cut, n)
		}
		if n != cut && (cut == 2 || cut == 4 || cut == len(text)) {
			t.Fatalf("cut %d at a character boundary: kept %d", cut, n)
		}
	}
}

func TestAgentAttemptsAndWaits(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "p.md"), []byte("Do it."), 0o644)
	os.WriteFile(filepath.Join(dir, "s.json"), []byte(`{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}, "required": ["outcome"]}`), 0o644)
	o, err := orders.Parse(filepath.Join(dir, "orders.yaml"), []byte(`version: 0
workdir: .
start: a
executors: {stub: {run: [x]}}
nodes:
  a: {type: agent, executor: stub, prompt: p.md, schema: s.json, model: m, effort: e, max_attempts: 2, on: {ok: done, fail: failed}}
`))
	if err != nil {
		t.Fatal(err)
	}
	retry := time.Unix(1000, 0).UTC()
	agent := func(attempt int, d chronicle.Details) chronicle.Details {
		d.Executor, d.Model, d.Effort, d.Prompt = "stub", "m", "e", "prompts/a."+string(rune('0'+attempt))+".txt"
		return d
	}
	ev := []chronicle.Event{
		{Seq: 1, Kind: chronicle.CampaignStarted, Node: "a"},
		{Seq: 2, Kind: chronicle.StepStarted, Node: "a", Attempt: 1, Details: agent(1, chronicle.Details{})},
		{Seq: 3, Kind: chronicle.AgentSession, Node: "a", Attempt: 1, Details: chronicle.Details{Session: "s-1"}},
		{Seq: 4, Kind: chronicle.StepFinished, Node: "a", Attempt: 1, Details: chronicle.Details{Failure: chronicle.FailInvalidOutput, Error: "$.outcome: bad"}},
		{Seq: 5, Kind: chronicle.StepStarted, Node: "a", Attempt: 2, Details: agent(2, chronicle.Details{})},
		{Seq: 6, Kind: chronicle.StepFinished, Node: "a", Attempt: 2, Details: chronicle.Details{Failure: chronicle.FailLimit, Error: "limit", RetryAt: &retry}},
	}
	// A repair round of attempt 1, recorded before its step_finished in a
	// real chronicle, belongs to that attempt wherever it stands.
	c := build(chronicle.Campaign{ID: 1}, append(ev[:3:3], append([]chronicle.Event{{Seq: 3, Kind: chronicle.OutputRepair, Node: "a", Attempt: 1,
		Details: chronicle.Details{Round: 1, Error: "$.extra: not allowed", Session: "s-1", Prompt: "prompts/a.1.repair1.txt"}}}, ev[3:]...)...), o, dir, func() bool { return true })
	if c.State != StateWaiting || c.Wait != chronicle.WaitLimit || c.WaitUntil == nil || !c.WaitUntil.Equal(retry) {
		t.Fatalf("state %s wait %s until %v", c.State, c.Wait, c.WaitUntil)
	}
	col := c.Columns[0]
	if col.Status != StatusWaiting || col.Executor != "stub" || col.Model != "m" || col.Effort != "e" || col.MaxAttempts != 2 {
		t.Fatalf("column %+v", col)
	}
	a := c.Nodes["a"]
	if a[0].Status != StatusFailed || a[0].Failure != chronicle.FailInvalidOutput || a[0].Session != "s-1" || a[0].Prompt != "prompts/a.1.txt" ||
		a[0].Log != "logs/a.1.log" || a[0].Stderr != "logs/a.1.stderr.log" || a[0].Executor != "stub" {
		t.Fatalf("attempt 1 %+v", a[0])
	}
	if r := a[0].Repairs; len(r) != 1 || r[0].Round != 1 || r[0].Max != 2 || r[0].Error != "$.extra: not allowed" || r[0].Prompt != "prompts/a.1.repair1.txt" {
		t.Fatalf("repairs of attempt 1 %+v", r)
	}
	if a[1].Status != StatusInterrupted || a[1].RetryAt == nil {
		t.Fatalf("attempt 2 %+v", a[1])
	}
	if c := build(chronicle.Campaign{ID: 1}, ev, o, dir, func() bool { return false }); c.State != StateInterrupted || c.Wait != chronicle.WaitLimit {
		t.Fatalf("without a driver: %s %s", c.State, c.Wait)
	}
	ev[5].RetryAt = nil
	if c := build(chronicle.Campaign{ID: 1}, ev, o, dir, func() bool { return false }); c.State != StateLimit {
		t.Fatalf("limit without a time: %s", c.State)
	}
	ev[5].Failure = chronicle.FailOffline
	if c := build(chronicle.Campaign{ID: 1}, ev, o, dir, func() bool { return true }); c.State != StateOffline {
		t.Fatalf("offline: %s", c.State)
	}
	ev = append(ev, chronicle.Event{Seq: 7, Kind: chronicle.StepStarted, Node: "a", Attempt: 3, Details: agent(3, chronicle.Details{Session: "s-1"})},
		chronicle.Event{Seq: 8, Kind: chronicle.StepFinished, Node: "a", Attempt: 3, Outcome: "ok", Details: chronicle.Details{Output: []byte(`{"outcome":"ok"}`)}})
	c = build(chronicle.Campaign{ID: 1}, ev, o, dir, func() bool { return true })
	if last := c.Nodes["a"][2]; last.Status != StatusPassed || last.SessionIn != "s-1" || string(last.Output) != `{"outcome":"ok"}` || c.State != StateRunning {
		t.Fatalf("attempt 3 %+v, state %s", last, c.State)
	}
}

// TestCampaignLanguage: the page shows the language recorded when the
// campaign started, and nothing for a campaign without one.
func TestCampaignLanguage(t *testing.T) {
	o := parse(t, branchy)
	ev := []chronicle.Event{{Seq: 1, Kind: chronicle.CampaignStarted, Node: "build", Details: chronicle.Details{Language: "uk"}}}
	c := build(chronicle.Campaign{ID: 1}, ev, o, t.TempDir(), func() bool { return true })
	if c.Language == nil || *c.Language != (Language{Code: "uk", English: "Ukrainian", Native: "українська"}) {
		t.Fatalf("language %+v", c.Language)
	}
	if e := eventsJSON(ev); e[0].Language != "uk" {
		t.Fatalf("event %+v", e[0])
	}
	ev[0].Language = ""
	if c := build(chronicle.Campaign{ID: 1}, ev, o, t.TempDir(), func() bool { return true }); c.Language != nil {
		t.Fatalf("language %+v", c.Language)
	}
}
