package explain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

// journal builds the events of a campaign the way the engine records them,
// one second apart.
type journal []chronicle.Event

func (j *journal) add(e chronicle.Event) *chronicle.Event {
	e.Seq = int64(len(*j) + 1)
	e.At = t0.Add(time.Duration(len(*j)) * time.Second)
	*j = append(*j, e)
	return &(*j)[len(*j)-1]
}

func exit(n int) *int { return &n }

func (j *journal) start(node string) {
	j.add(chronicle.Event{Kind: chronicle.CampaignStarted, Node: node})
}

// step records a script step that ran and its transition.
func (j *journal) step(node string, attempt int, outcome, to string, code int) {
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: node, Attempt: attempt})
	j.add(chronicle.Event{Kind: chronicle.StepFinished, Node: node, Attempt: attempt, Outcome: outcome,
		Details: chronicle.Details{Exit: exit(code), Millis: 1200}})
	j.add(chronicle.Event{Kind: chronicle.Transition, Node: node, Outcome: outcome, Details: chronicle.Details{To: to}})
}

// gate records a gate with checks: name=outcome, soft when the name ends
// in "?".
func (j *journal) gate(node string, attempt int, outcome, to string, checks ...string) {
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: node, Attempt: attempt})
	for _, c := range checks {
		name, out, _ := strings.Cut(c, "=")
		soft := strings.HasSuffix(name, "?")
		name = strings.TrimSuffix(name, "?")
		j.add(chronicle.Event{Kind: chronicle.CheckFinished, Node: node, Check: name, Attempt: attempt, Outcome: out,
			Details: chronicle.Details{Soft: soft, Millis: 300}})
	}
	j.add(chronicle.Event{Kind: chronicle.StepFinished, Node: node, Attempt: attempt, Outcome: outcome, Details: chronicle.Details{Millis: 600}})
	j.add(chronicle.Event{Kind: chronicle.Transition, Node: node, Outcome: outcome, Details: chronicle.Details{To: to}})
}

func explainAll(j journal) string {
	return Text(Campaign(chronicle.Campaign{ID: 3, Flow: "release", Started: t0}, j, Summary, "", time.UTC))
}

func same(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
		for i := range gl {
			if i >= len(wl) || gl[i] != wl[i] {
				w := ""
				if i < len(wl) {
					w = wl[i]
				}
				t.Fatalf("line %d:\ngot  %q\nwant %q\nwhole text:\n%s", i+1, gl[i], w, got)
			}
		}
		t.Fatalf("got:\n%s", got)
	}
}

// A campaign that went back to build twice: once because a required check
// blocked the gate, once because a reviewing agent asked for rework. It
// then finished.
func TestReworkDone(t *testing.T) {
	var j journal
	j.start("build")
	j.step("build", 1, "ok", "ready", 0)
	j.gate("ready", 1, "blocked", "build", "lint?=fail", "tests=fail")
	j.step("build", 2, "ok", "ready", 0)
	j.gate("ready", 2, "pass", "review", "lint?=ok", "tests=ok")
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "review", Attempt: 1, Details: chronicle.Details{Executor: "stub", Model: "m"}})
	j.add(chronicle.Event{Kind: chronicle.StepFinished, Node: "review", Attempt: 1, Outcome: "needs_rework",
		Details: chronicle.Details{Millis: 2000, Output: json.RawMessage(`{"outcome":"needs_rework","notes":["name the log files"]}`)}})
	j.add(chronicle.Event{Kind: chronicle.Transition, Node: "review", Outcome: "needs_rework", Details: chronicle.Details{To: "build"}})
	j.step("build", 3, "ok", "ready", 0)
	j.gate("ready", 3, "pass", "review", "lint?=ok", "tests=ok")
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "review", Attempt: 2})
	j.add(chronicle.Event{Kind: chronicle.StepFinished, Node: "review", Attempt: 2, Outcome: "approved", Details: chronicle.Details{Millis: 1500}})
	j.add(chronicle.Event{Kind: chronicle.Transition, Node: "review", Outcome: "approved", Details: chronicle.Details{To: "done"}})

	same(t, explainAll(j), `Where it is:
  Campaign 3 of flow release started at 2026-03-02 09:00:00 at build.
  It is done: review finished with approved at 2026-03-02 09:00:30, which leads to done.

What ran:
  2026-03-02 09:00:02  build attempt 1 finished with ok after 1.2s. The flow went to ready.
  2026-03-02 09:00:07  ready attempt 1 finished with blocked after 600ms; checks: lint fail (soft, a warning), tests fail. The flow went to build.
  2026-03-02 09:00:10  build attempt 2 finished with ok after 1.2s. The flow went to ready.
  2026-03-02 09:00:15  ready attempt 2 finished with pass after 600ms; checks: lint ok, tests ok. The flow went to review.
  2026-03-02 09:00:18  review attempt 1 finished with needs_rework after 2s. The flow went to build.
  2026-03-02 09:00:21  build attempt 3 finished with ok after 1.2s. The flow went to ready.
  2026-03-02 09:00:26  ready attempt 3 finished with pass after 600ms; checks: lint ok, tests ok. The flow went to review.
  2026-03-02 09:00:29  review attempt 2 finished with approved after 1.5s. The flow went to done.

Rework:
  The flow went back to build twice.
  2026-03-02 09:00:08  ready finished with blocked and sent the flow back to build (required check tests reported fail).
  2026-03-02 09:00:19  review finished with needs_rework and sent the flow back to build (its output: {"notes":["name the log files"]}).

What failed:
  2026-03-02 09:00:07  ready attempt 1 finished with blocked: required check tests reported fail.

Warnings:
  2026-03-02 09:00:05  The soft check lint of ready reported fail in attempt 1; it only warned.
`)
	// The rework question about one node.
	got := Text(Campaign(chronicle.Campaign{ID: 3, Flow: "release"}, j, Rework, "build", time.UTC))
	same(t, got, `The flow went back to build twice.
2026-03-02 09:00:08  ready finished with blocked and sent the flow back to build (required check tests reported fail).
2026-03-02 09:00:19  review finished with needs_rework and sent the flow back to build (its output: {"notes":["name the log files"]}).
`)
	if got := Text(Campaign(chronicle.Campaign{ID: 3}, j, Rework, "ready", time.UTC)); got != "The flow never went back to ready.\n" {
		t.Fatal(got)
	}
}

// A campaign waiting at a gate whose checks passed and that asks a person.
func TestWaitingForPerson(t *testing.T) {
	var j journal
	j.start("plan")
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "plan", Attempt: 1, Details: chronicle.Details{Executor: "stub"}})
	j.add(chronicle.Event{Kind: chronicle.StepFinished, Node: "plan", Attempt: 1, Outcome: "ok", Details: chronicle.Details{Millis: 61000}})
	j.add(chronicle.Event{Kind: chronicle.Transition, Node: "plan", Outcome: "ok", Details: chronicle.Details{To: "approve"}})
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "approve", Attempt: 1})
	j.add(chronicle.Event{Kind: chronicle.CheckFinished, Node: "approve", Check: "format", Attempt: 1, Outcome: "ok"})
	j.add(chronicle.Event{Kind: chronicle.Asked, Node: "approve", Attempt: 1, Details: chronicle.Details{Reason: chronicle.ReasonGate,
		Questions: []ask.Question{{ID: "scope", Question: "Does the plan cover everything?", Kind: ask.YesNo}, {ID: "risk", Question: "Can every step be undone?", Kind: ask.YesNo}}}})
	j.add(chronicle.Event{Kind: chronicle.HaltRequested, Node: "approve"})

	same(t, explainAll(j), `Where it is:
  Campaign 3 of flow release started at 2026-03-02 09:00:00 at plan.
  It has been waiting for a person since 2026-03-02 09:00:06: the gate approve asks 2 question(s): "Does the plan cover everything?"; "Can every step be undone?".
  A halt was requested at 2026-03-02 09:00:07; it takes effect after the current step.

What ran:
  2026-03-02 09:00:02  plan attempt 1 finished with ok after 1m1s. The flow went to approve.
  2026-03-02 09:00:06  approve asked a person: "Does the plan cover everything?"; "Can every step be undone?".
  2026-03-02 09:00:07  A halt was requested.

Rework:
  The flow never went back to an earlier step.

What failed:
  Nothing failed.

Warnings:
  No soft check warned.
`)

	// A human node waiting for a decision, then answered and not continued.
	var h journal
	h.start("release")
	h.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "release", Attempt: 1})
	asked := h.add(chronicle.Event{Kind: chronicle.Asked, Node: "release", Attempt: 1, Details: chronicle.Details{Reason: chronicle.ReasonHuman,
		Outcomes: []string{"ship", "rework", "stop"}, Questions: []ask.Question{{ID: "channel", Question: "Which channel?", Kind: ask.Choice, Options: []string{"a", "b"}}}}})
	where := Text(Campaign(chronicle.Campaign{ID: 4, Flow: "f"}, h, Where, "", time.UTC))
	same(t, where, `Campaign 4 of flow f started at 2026-03-02 09:00:00 at release.
It has been waiting for a person since 2026-03-02 09:00:02: release asks for a decision between ship, rework or stop and 1 question(s): "Which channel?".
`)
	h.add(chronicle.Event{Kind: chronicle.Answered, Node: "release", Attempt: 1, Outcome: "ship", Details: chronicle.Details{
		Ask: asked.Seq, By: "dana", Via: "page", Answers: []ask.Answer{{ID: "channel", Value: "a", Comment: "the usual one"}}}})
	same(t, Text(Campaign(chronicle.Campaign{ID: 4, Flow: "f"}, h, Summary, "", time.UTC)), `Where it is:
  Campaign 4 of flow f started at 2026-03-02 09:00:00 at release.
  dana answered the questions of release at 2026-03-02 09:00:03; release has not finished since.

What ran:
  2026-03-02 09:00:02  release asked a person to choose ship, rework or stop and to answer "Which channel?".
  2026-03-02 09:00:03  dana answered release on the page: channel: a (comment "the usual one"); chose ship.

Rework:
  The flow never went back to an earlier step.

What failed:
  Nothing failed.

Warnings:
  No soft check warned.
`)
}

// A campaign stopped because an agent step lost connectivity.
func TestStoppedOffline(t *testing.T) {
	var j journal
	j.start("draft")
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "draft", Attempt: 1, Details: chronicle.Details{Executor: "stub"}})
	j.add(chronicle.Event{Kind: chronicle.StepFinished, Node: "draft", Attempt: 1, Details: chronicle.Details{Failure: chronicle.FailInvalidOutput,
		Error: `missing required field "title"`, Output: json.RawMessage(`{"outcome":"ok"}`)}})
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "draft", Attempt: 2, Details: chronicle.Details{Executor: "stub"}})
	j.add(chronicle.Event{Kind: chronicle.StepFinished, Node: "draft", Attempt: 2, Details: chronicle.Details{Failure: chronicle.FailOffline,
		Error: "network is unreachable"}})

	same(t, explainAll(j), `Where it is:
  Campaign 3 of flow release started at 2026-03-02 09:00:00 at draft.
  It has been stopped at draft since 2026-03-02 09:00:04: attempt 2 lost connectivity (network is unreachable). The attempt does not count; the step runs again when the campaign is resumed.

What ran:
  2026-03-02 09:00:02  draft attempt 1 failed (invalid_output): missing required field "title".
  2026-03-02 09:00:04  draft attempt 2 was cut off (offline): network is unreachable; it does not count.

Rework:
  The flow never went back to an earlier step.

What failed:
  2026-03-02 09:00:02  draft attempt 1 failed (invalid_output): missing required field "title".
  2026-03-02 09:00:04  draft attempt 2 was cut off (offline): network is unreachable; it does not count.

Warnings:
  No soft check warned.
`)
	// A limit with a time to retry.
	retry := t0.Add(time.Hour)
	j[len(j)-1].Failure, j[len(j)-1].Error, j[len(j)-1].RetryAt = chronicle.FailLimit, "usage limit reached", &retry
	same(t, Text(Campaign(chronicle.Campaign{ID: 3, Flow: "release"}, j, Where, "", time.UTC)), `Campaign 3 of flow release started at 2026-03-02 09:00:00 at draft.
It is waiting at draft: attempt 2 hit the subscription limit at 2026-03-02 09:00:04 (usage limit reached); the step is due to run again at 2026-03-02 10:00:00. The attempt does not count.
`)
}

// A campaign that failed: a step exhausted its attempts, and one that
// stopped in the error state.
func TestFailed(t *testing.T) {
	var j journal
	j.start("build")
	j.step("build", 1, "ok", "test", 0)
	j.step("test", 1, "fail", "failed", 3)
	same(t, explainAll(j), `Where it is:
  Campaign 3 of flow release started at 2026-03-02 09:00:00 at build.
  It failed: test finished with fail at 2026-03-02 09:00:06, which leads to failed.

What ran:
  2026-03-02 09:00:02  build attempt 1 finished with ok after 1.2s. The flow went to test.
  2026-03-02 09:00:05  test attempt 1 finished with fail after 1.2s. The flow went to failed.

Rework:
  The flow never went back to an earlier step.

What failed:
  2026-03-02 09:00:05  test attempt 1 finished with fail (exit code 3).

Warnings:
  No soft check warned.
`)

	var e journal
	e.start("ship")
	e.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "ship", Attempt: 1})
	e.add(chronicle.Event{Kind: chronicle.StepFinished, Node: "ship", Attempt: 1, Details: chronicle.Details{Error: `cannot start ./ship.sh: no such file`}})
	e.add(chronicle.Event{Kind: chronicle.Stopped, Node: "ship", Details: chronicle.Details{Error: `ship attempt 1: cannot start ./ship.sh: no such file`}})
	same(t, Text(Campaign(chronicle.Campaign{ID: 5, Flow: "f"}, e, Summary, "", time.UTC)), `Where it is:
  Campaign 5 of flow f started at 2026-03-02 09:00:00 at ship.
  It stopped in the error state at ship at 2026-03-02 09:00:03: ship attempt 1: cannot start ./ship.sh: no such file.

What ran:
  2026-03-02 09:00:02  ship attempt 1 could not finish: cannot start ./ship.sh: no such file.
  2026-03-02 09:00:03  The campaign stopped in the error state at ship: ship attempt 1: cannot start ./ship.sh: no such file.

Rework:
  The flow never went back to an earlier step.

What failed:
  2026-03-02 09:00:02  ship attempt 1 could not finish: cannot start ./ship.sh: no such file.
  2026-03-02 09:00:03  The campaign stopped in the error state at ship: ship attempt 1: cannot start ./ship.sh: no such file.

Warnings:
  No soft check warned.
`)
}

// TestOnlyRecordedFacts: every node, check and outcome named in an
// explanation appears in the events.
func TestOnlyRecordedFacts(t *testing.T) {
	var j journal
	j.start("build")
	j.step("build", 1, "ok", "ready", 0)
	j.gate("ready", 1, "blocked", "build", "lint?=fail", "tests=fail")
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "build", Attempt: 2})
	text := explainAll(j)
	for _, word := range []string{"review", "ship", "publish", "approved", "done."} {
		if strings.Contains(text, word) {
			t.Errorf("the explanation names %q, which no event records:\n%s", word, text)
		}
	}
	if !strings.Contains(text, "build attempt 2 started at 2026-03-02 09:00:09 and has not finished.") {
		t.Fatal(text)
	}
}
