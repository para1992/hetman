package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
)

func init() { answerPoll = 20 * time.Millisecond }

// waitAsked waits until the campaign waits for an answer and returns the
// asked event; skip ignores asked events up to that seq.
func (f *fixture) waitAsked(skip int64) *chronicle.Event {
	f.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if q, _, err := Pending(f.chron, f.e.Campaign); err == nil && q != nil && q.Seq > skip {
			return q
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("the campaign does not ask a person\n%s", f.dump())
	return nil
}

func (f *fixture) answer(values map[string]string, comments map[string]string, outcome string) chronicle.Event {
	f.t.Helper()
	ev, err := Answer(f.chron, f.e.Campaign, Reply{Given: ask.Given{Values: values, Comments: comments, Outcome: outcome}, By: "tester", Via: ViaCommand})
	if err != nil {
		f.t.Fatal(err)
	}
	return ev
}

func (f *fixture) wait(done chan driveResult, want Result) {
	f.t.Helper()
	select {
	case r := <-done:
		if r.err != nil || r.res != want {
			f.t.Fatalf("drive %+v, want %s\n%s", r, want, f.dump())
		}
	case <-time.After(60 * time.Second):
		f.t.Fatalf("the drive did not end\n%s", f.dump())
	}
}

const gateOrders = `version: 0
workdir: .
start: fix
executors: {stub: {run: PLAN}}
checks:
  unit: {run: UNIT}
nodes:
  fix: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: review, fail: failed}}
  review:
    type: gate
    checks: [unit]
    ask:
      - {id: scope, question: "Is everything covered?"}
      - {id: risk, question: "Can it be undone?"}
    show: [notes.md]
    on: {pass: done, blocked: fix}
`

func TestGateQuestionsNoBlocksAndAnswersReachTheRework(t *testing.T) {
	f := setupAgent(t, strings.Replace(gateOrders, "UNIT", arg("exit", "0"), 1), map[string]string{
		"p.md": "Fix it.{{rework}}", "s.json": okSchema, "notes.md": "notes",
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"fix": [[{"result": {"outcome": "ok"}}]]}}`}, "")
	done := f.driveAsync(context.Background())
	q := f.waitAsked(0)
	if q.Reason != chronicle.ReasonGate || len(q.Questions) != 2 || q.Questions[0].Kind != ask.YesNo || strings.Join(q.Show, ",") != "notes.md" {
		t.Fatalf("asked %+v", q)
	}
	if c := f.events(chronicle.CheckFinished); len(c) != 1 || c[0].Seq > q.Seq {
		t.Fatalf("the checks must run before the person is asked: %+v", c)
	}
	f.answer(map[string]string{"scope": "y", "risk": "NO"}, map[string]string{"risk": "the migration drops a column"}, "")
	q2 := f.waitAsked(q.Seq)
	f.answer(map[string]string{"scope": "yes", "risk": "yes"}, nil, "")
	f.wait(done, Done)
	want(t, f.finished(), []string{"fix#1 ok/", "review#1 blocked/", "fix#2 ok/", "review#2 pass/"})
	if q2.Attempt != 2 {
		t.Fatalf("second ask %+v", q2)
	}
	p := f.steps()[1].Prompt
	for _, w := range []string{`step "review" finished with outcome "blocked"`, "tester answered the questions of \"review\"",
		"Can it be undone? (risk)\n  Answer: no\n  Comment: the migration drops a column"} {
		if !strings.Contains(p, w) {
			t.Fatalf("rework prompt lacks %q:\n%s", w, p)
		}
	}
	var saved struct {
		By      string `json:"by"`
		Answers []struct{ ID, Value string }
	}
	data, _ := os.ReadFile(filepath.Join(f.work, filepath.FromSlash(AnswersFile("review"))))
	if json.Unmarshal(data, &saved) != nil || saved.By != "tester" || saved.Answers[1].Value != "yes" {
		t.Fatalf("answers file %s", data)
	}
}

func TestGateWithFailedCheckAsksNobody(t *testing.T) {
	f := setupAgent(t, strings.Replace(gateOrders, "UNIT", arg("exit", "1"), 1), map[string]string{
		"p.md": "Fix it.", "s.json": okSchema, "notes.md": "",
		"plan.json": `{"nodes": {"fix": [[{"result": {"outcome": "ok"}}], [{"error": "permanent", "message": "stop"}]]}}`}, "")
	f.drive(Failed)
	if a := f.events(chronicle.Asked); len(a) != 0 {
		t.Fatalf("asked although the check failed: %+v", a)
	}
}

func TestHumanNodeOutcomeAndAnswersInTemplates(t *testing.T) {
	f := setupAgent(t, `version: 0
workdir: .
start: decide
executors: {stub: {run: PLAN}}
nodes:
  decide:
    type: human
    ask:
      - {id: channel, question: "Which channel?", kind: choice, options: [stable, beta]}
      - {id: notes, question: "Anything else?", kind: text}
      - {id: urgent, question: "Is it urgent?"}
    on: {ship: write, stop: failed}
  write: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: done, fail: failed}}
`, map[string]string{"p.md": "Channel {{answers.decide.channel}}; urgent: {{answers.decide.urgent}} ({{answers.decide.urgent.comment}})\n{{answers.decide}}",
		"s.json": okSchema, "plan.json": `{"trace": "trace.jsonl"}`}, "")
	done := f.driveAsync(context.Background())
	q := f.waitAsked(0)
	if q.Reason != chronicle.ReasonHuman || strings.Join(q.Outcomes, ",") != "ship,stop" {
		t.Fatalf("asked %+v", q)
	}
	// A reply that does not fit changes nothing.
	before := len(f.events(""))
	for _, g := range []ask.Given{
		{Values: map[string]string{"channel": "nightly", "notes": "", "urgent": "yes"}, Outcome: "ship"},
		{Values: map[string]string{"channel": "beta", "notes": ""}, Outcome: "ship"},
		{Values: map[string]string{"channel": "beta", "notes": "", "urgent": "yes"}},
		{Values: map[string]string{"channel": "beta", "notes": "", "urgent": "yes"}, Outcome: "maybe"},
	} {
		if _, err := Answer(f.chron, f.e.Campaign, Reply{Given: g}); !errors.Is(err, ask.ErrInvalid) {
			t.Fatalf("reply %+v: %v", g, err)
		}
	}
	if _, err := Answer(f.chron, f.e.Campaign, Reply{Ask: q.Seq + 100}); !errors.Is(err, ErrOtherAsk) {
		t.Fatalf("stale ask: %v", err)
	}
	if n := len(f.events("")); n != before {
		t.Fatalf("a rejected reply was recorded: %d events, was %d", n, before)
	}
	f.answer(map[string]string{"channel": "beta", "notes": "say thanks", "urgent": "y"}, map[string]string{"urgent": "today"}, "ship")
	if _, err := Answer(f.chron, f.e.Campaign, Reply{Given: ask.Given{Values: map[string]string{"channel": "stable", "notes": "", "urgent": "no"}, Outcome: "stop"}}); !errors.Is(err, ErrAnswered) && !errors.Is(err, ErrNothingAsked) {
		t.Fatalf("second answer: %v", err)
	}
	f.wait(done, Done)
	want(t, f.finished(), []string{"decide#1 ship/", "write#1 ok/"})
	p := f.steps()[0].Prompt
	for _, w := range []string{"Channel beta; urgent: yes (today)", "- Anything else? (notes)\n  Answer: say thanks", "Decision: ship"} {
		if !strings.Contains(p, w) {
			t.Fatalf("prompt lacks %q:\n%s", w, p)
		}
	}
}

const dialogueOrders = `version: 0
workdir: .
start: a
executors: {stub: {run: PLAN}}
nodes:
  a:
    type: agent
    executor: stub
    prompt: p.md
    schema: s.json
    dialogue: %ROUNDS%
    max_attempts: %ATTEMPTS%
    on: {ok: done, fail: failed}
`

func dialogue(rounds, attempts string) string {
	return strings.NewReplacer("%ROUNDS%", rounds, "%ATTEMPTS%", attempts).Replace(dialogueOrders)
}

const q1 = `{"outcome": "questions", "questions": [{"id": "color", "question": "Which color?", "kind": "choice", "options": ["red", "blue"]}, {"id": "why", "question": "Why?", "kind": "text"}]}`
const q2 = `{"outcome": "questions", "questions": [{"id": "sure", "question": "Sure?", "kind": "yesno"}]}`

func TestDialogueOfTwoRoundsInOneSession(t *testing.T) {
	f := setupAgent(t, dialogue("2", "1"), agentFiles(`{"trace": "trace.jsonl", "nodes": {"a": [
		[{"session": "s-1"}, {"result": `+q1+`}],
		[{"require_session": "s-1"}, {"session": "s-1"}, {"result": `+q2+`}],
		[{"require_session": "s-1"}, {"session": "s-1"}, {"result": {"outcome": "ok", "summary": "blue it is"}}]]}}`), "")
	done := f.driveAsync(context.Background())
	r1 := f.waitAsked(0)
	if r1.Reason != chronicle.ReasonDialogue || r1.Round != 1 || r1.Session != "s-1" || len(r1.Questions) != 2 || r1.Questions[0].Options[1] != "blue" {
		t.Fatalf("round 1 %+v", r1)
	}
	f.answer(map[string]string{"color": "blue", "why": "it is calm"}, nil, "")
	r2 := f.waitAsked(r1.Seq)
	if r2.Round != 2 || r2.Attempt != 2 {
		t.Fatalf("round 2 %+v", r2)
	}
	f.answer(map[string]string{"sure": "yes"}, map[string]string{"sure": "quite"}, "")
	f.wait(done, Done)
	// Rounds of questions use no attempts, even with max_attempts 1.
	want(t, f.finished(), []string{"a#1 /", "a#2 /", "a#3 ok/"})
	steps := f.steps()
	if steps[0].Session != nil || !strings.Contains(steps[0].Prompt, `you may ask instead of answering`) || !strings.Contains(steps[0].Prompt, "You may ask 2 more time(s)") ||
		!strings.Contains(string(steps[0].Schema), `"questions"`) {
		t.Fatalf("first start %+v", steps[0])
	}
	if s := steps[1]; s.Session == nil || *s.Session != "s-1" || !strings.HasPrefix(s.Prompt, "The person answered your questions (round 1):") ||
		!strings.Contains(s.Prompt, "Which color? (color)\n  Answer: blue") || !strings.Contains(s.Prompt, "Answer: it is calm") ||
		!strings.Contains(s.Prompt, "1 more time(s)") || strings.Contains(s.Prompt, "Do it.") {
		t.Fatalf("second start %+v", s)
	}
	if s := steps[2]; s.Session == nil || *s.Session != "s-1" || !strings.Contains(s.Prompt, "Comment: quite") ||
		!strings.Contains(s.Prompt, "You may not ask any more questions") || strings.Contains(string(s.Schema), `"questions"`) {
		t.Fatalf("third start %+v", s)
	}
	if out := f.events(chronicle.StepFinished)[2].Output; string(out) != `{"outcome":"ok","summary":"blue it is"}` {
		t.Fatalf("final output %s", out)
	}
}

func TestDialogueRoundLimit(t *testing.T) {
	f := setupAgent(t, dialogue("1", "3"), agentFiles(`{"trace": "trace.jsonl", "nodes": {"a": [
		[{"session": "s-1"}, {"result": `+q2+`}],
		[{"result": `+q2+`}],
		[{"result": {"outcome": "ok"}}]]}}`), "")
	done := f.driveAsync(context.Background())
	f.waitAsked(0)
	f.answer(map[string]string{"sure": "no"}, nil, "")
	f.wait(done, Done)
	// A question beyond the limit is an answer that does not match the schema.
	want(t, f.finished(), []string{"a#1 /", "a#2 /invalid_output", "a#3 ok/"})
	if n := len(f.events(chronicle.Asked)); n != 1 {
		t.Fatalf("%d rounds asked, want 1", n)
	}
	s := f.steps()[2]
	if s.Session == nil || *s.Session != "s-1" || !strings.Contains(s.Prompt, "The person answered your questions (round 1)") ||
		!strings.Contains(s.Prompt, "(attempt 2) was rejected") {
		t.Fatalf("third start %+v", s)
	}
}

func TestDialogueQuestionsMustBeWellFormed(t *testing.T) {
	f := setupAgent(t, dialogue("2", "2"), agentFiles(`{"nodes": {"a": [
		[{"session": "s-1"}, {"result": {"outcome": "questions", "questions": [{"id": "x", "question": "Pick", "kind": "choice", "options": ["only"]}]}}],
		[{"result": {"outcome": "questions", "questions": [{"id": "x", "question": "Ok?", "kind": "yesno"}], "summary": "extra"}}]]}}`), "")
	f.drive(Failed)
	fin := f.events(chronicle.StepFinished)
	if !strings.Contains(fin[0].Error, "a choice needs at least two options") || !strings.Contains(fin[1].Error, `holds only "questions"`) {
		t.Fatalf("errors %q / %q", fin[0].Error, fin[1].Error)
	}
}

func TestDialogueNeedsAnAdapterThatCanResume(t *testing.T) {
	f := setupAgent(t, dialogue("2", "2"), agentFiles(`{"features": []}`), "")
	f.drive(Errored)
	if e := f.events(chronicle.Stopped); len(e) != 1 || !strings.Contains(e[0].Error, `does not declare the "resume" feature`) {
		t.Fatalf("errors %+v", e)
	}
	if s := f.events(chronicle.StepStarted); len(s) != 0 {
		t.Fatalf("a step started: %+v", s)
	}
}

func TestDialogueWithoutSessionFailsTheAttempt(t *testing.T) {
	f := setupAgent(t, dialogue("2", "1"), agentFiles(`{"nodes": {"a": [[{"result": `+q2+`}]]}}`), "")
	f.drive(Failed)
	if fin := f.events(chronicle.StepFinished); fin[0].Failure != chronicle.FailProtocol || !strings.Contains(fin[0].Error, "reported no session") {
		t.Fatalf("finish %+v", fin)
	}
}

func TestNotifyHookGetsTheNoticeAndNeverBlocks(t *testing.T) {
	notices := filepath.Join(tempDir(t))
	config := strings.Replace(gateOrders, "UNIT", arg("exit", "0"), 1) + "notify: {run: " + arg("notice", notices) + "}\n"
	f := setupAgent(t, config, map[string]string{"p.md": "Fix.", "s.json": okSchema, "notes.md": "n",
		"plan.json": `{"nodes": {"fix": [[{"result": {"outcome": "ok"}}]]}}`}, "")
	done := f.driveAsync(context.Background())
	q := f.waitAsked(0)
	path := filepath.Join(notices, "notice.gate.review.json")
	waitFile(t, path)
	var n Notice
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &n); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	if n.Campaign != f.e.Campaign || n.Node != "review" || n.Reason != chronicle.ReasonGate || n.Event != q.Seq || len(n.Questions) != 2 ||
		n.Show[0] != filepath.Join(f.e.Orders.Dir, "notes.md") || n.Command != AnswerCommand(f.e.Orders, f.e.Campaign) ||
		!strings.Contains(n.Message, "asks 2 question(s)") || lines(data) != 1 {
		t.Fatalf("notice %+v\n%s", n, data)
	}
	f.answer(map[string]string{"scope": "yes", "risk": "yes"}, nil, "")
	f.wait(done, Done)
	f.e.WaitHooks()
	if ev := f.events(chronicle.Notified); len(ev) != 1 || ev[0].Outcome != "ok" || ev[0].Ask != q.Seq || ev[0].Reason != chronicle.ReasonGate {
		t.Fatalf("notified %+v", ev)
	}

	// A hook that hangs is killed after its timeout; the campaign goes on
	// meanwhile.
	config = strings.Replace(gateOrders, "UNIT", arg("exit", "0"), 1) + "notify: {run: " + arg("sleep", "1h") + ", timeout: 8s}\n"
	f = setupAgent(t, config, map[string]string{"p.md": "Fix.", "s.json": okSchema, "notes.md": "n",
		"plan.json": `{"nodes": {"fix": [[{"result": {"outcome": "ok"}}]]}}`}, "")
	done = f.driveAsync(context.Background())
	f.waitAsked(0)
	f.answer(map[string]string{"scope": "yes", "risk": "yes"}, nil, "")
	began := time.Now()
	f.wait(done, Done)
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("the campaign waited %s for a hanging hook", d)
	}
	if ev := f.events(chronicle.Notified); len(ev) != 0 {
		t.Fatalf("the hook ended already: %+v", ev)
	}
	f.e.WaitHooks()
	if ev := f.events(chronicle.Notified); len(ev) != 1 || ev[0].Outcome != "timeout" {
		t.Fatalf("notified %+v", ev)
	}
}

// lines counts the lines of a notice; the hook gets exactly one.
func lines(data []byte) int { return strings.Count(string(data), "\n") }

func TestNotifyHookOnOfflineAndError(t *testing.T) {
	notices := tempDir(t)
	f := setupAgent(t, one("")+"notify: {run: "+arg("notice", notices)+"}\n",
		agentFiles(`{"nodes": {"a": [[{"error": "offline", "message": "no network"}], [{"result": {"outcome": "ok"}}]]}}`), "")
	f.drive(Waiting)
	f.e.WaitHooks()
	data, err := os.ReadFile(filepath.Join(notices, "notice.offline.a.json"))
	if err != nil || !strings.Contains(string(data), `"reason":"offline"`) || !strings.Contains(string(data), "hetman resume") {
		t.Fatalf("offline notice (%v): %s", err, data)
	}
	f = setupAgent(t, one("")+"notify: {run: "+arg("notice", notices)+"}\n", agentFiles(`{"protocol": [1]}`), "")
	f.drive(Errored)
	f.e.WaitHooks()
	data, err = os.ReadFile(filepath.Join(notices, "notice.error.a.json"))
	if err != nil || !strings.Contains(string(data), "speaks protocol versions") {
		t.Fatalf("error notice (%v): %s", err, data)
	}
}

func TestHaltWhileWaitingForAnAnswer(t *testing.T) {
	f := setupAgent(t, strings.Replace(gateOrders, "UNIT", arg("exit", "0"), 1), map[string]string{"p.md": "Fix.", "s.json": okSchema, "notes.md": "n",
		"plan.json": `{"nodes": {"fix": [[{"result": {"outcome": "ok"}}]]}}`}, "")
	done := f.driveAsync(context.Background())
	f.waitAsked(0)
	f.record(chronicle.Event{Kind: chronicle.HaltRequested})
	f.wait(done, Halted)
	// The question stays open across the halt; answered, a resume goes on.
	f.answer(map[string]string{"scope": "yes", "risk": "yes"}, nil, "")
	f.record(chronicle.Event{Kind: chronicle.Resumed})
	f.drive(Done)
}
