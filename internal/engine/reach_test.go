package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

// withUnable is a node schema as the adapter gets it: with the reserved
// outcome and its reason field added.
func withUnable(t *testing.T, schema string) string {
	t.Helper()
	out, err := unableSchema([]byte(schema))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func unableAgent(on string) string {
	return `version: 0
workdir: .
start: a
executors: {stub: {run: PLAN}}
nodes:
  a: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: done, fail: failed` + on + `}}
  escalate: {type: script, run: ` + arg("exit", "0") + `, on: {ok: done, fail: failed}}
`
}

// Every attempt is told how to say it cannot proceed, and the schema the
// adapter gets allows it.
func TestUnableInstructionAndSchema(t *testing.T) {
	f := setupAgent(t, unableAgent(""), map[string]string{"p.md": "Do it.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "ok"}}]]}}`}, "")
	f.drive(Done)
	prompt, err := os.ReadFile(filepath.Join(CampaignDir(f.e.Orders, f.e.Campaign), filepath.FromSlash(PromptFile("a", 1))))
	if err != nil {
		t.Fatal(err)
	}
	if string(prompt) != "Do it."+unableInstruction() || !strings.Contains(string(prompt), `"cannot_proceed_reason"`) {
		t.Fatalf("prompt %q", prompt)
	}
	if s := string(f.steps()[0].Schema); !strings.Contains(s, `"cannot_proceed"`) || !strings.Contains(s, `"cannot_proceed_reason":{"type":"string"}`) {
		t.Fatalf("schema %s", s)
	}
}

// An agent that cannot proceed stops the campaign at once, with its reason,
// when the orders give no transition for it; nothing is retried.
func TestUnableStopsForAPerson(t *testing.T) {
	f := setupAgent(t, unableAgent(""), map[string]string{"p.md": "Do it.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "cannot_proceed", "cannot_proceed_reason": "Write is denied for plan.md"}}]]}}`}, "")
	f.drive(Errored)
	want(t, f.finished(), []string{"a#1 /unable"})
	stops := f.events(chronicle.Stopped)
	if len(stops) != 1 || !strings.Contains(stops[0].Error, "cannot proceed: Write is denied for plan.md") {
		t.Fatalf("stop %+v", stops)
	}
	// Without a reason the answer is invalid output, retried as usual.
	f = setupAgent(t, unableAgent(""), map[string]string{"p.md": "Do it.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "cannot_proceed"}}], [{"result": {"outcome": "ok"}}]]}}`}, "")
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 /invalid_output", "a#2 ok/"})
}

// With a transition for it, the reserved outcome is followed like any other.
func TestUnableFollowsItsTransition(t *testing.T) {
	f := setupAgent(t, unableAgent(", cannot_proceed: escalate"), map[string]string{"p.md": "Do it.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "cannot_proceed", "cannot_proceed_reason": "no access"}}]]}}`}, "")
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 cannot_proceed/", "escalate#1 ok/"})
}

// Denied tool calls come with the result and are recorded; a node with
// fail_on_denied fails such an attempt and quotes the calls in the next
// prompt.
func TestDeniedCallsAndFailOnDenied(t *testing.T) {
	plan := `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "ok"}, "denied": ["Bash go test ./...: matched none of the step's entries Bash(make *)"]}], [{"result": {"outcome": "ok"}}]]}}`
	f := setupAgent(t, unableAgent(""), map[string]string{"p.md": "Do it.", "s.json": okSchema, "plan.json": plan}, "")
	f.drive(Done)
	fin := f.events(chronicle.StepFinished)
	if len(fin) != 1 || len(fin[0].Denied) != 1 || fin[0].Outcome != "ok" {
		t.Fatalf("without fail_on_denied: %+v", fin)
	}
	f = setupAgent(t, strings.Replace(unableAgent(""), "schema: s.json,", "schema: s.json, fail_on_denied: true,", 1),
		map[string]string{"p.md": "Do it.", "s.json": okSchema, "plan.json": plan}, "")
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 /denied", "a#2 ok/"})
	if p := f.steps()[1].Prompt; !strings.Contains(p, "was not accepted because the client denied tool calls you made:\n- Bash go test ./...: matched none") {
		t.Fatalf("second prompt:\n%s", p)
	}
}

// An edit to a segment reaches its running child at the next step: here
// the first step of the child rewrites the segment, and the second step
// runs with the model the new version names.
func TestSegmentEditReachesTheRunningChild(t *testing.T) {
	seg := func(model string) string {
		return `version: 0
workdir: .
flow: item
start: s1
executors: {stub: {run: ` + teststep.Adapter("plan.json") + `}}
nodes:
  s1: {type: agent, executor: stub, prompt: p.md, schema: s.json, model: one, on: {ok: s2, fail: failed}}
  s2: {type: agent, executor: stub, prompt: p.md, schema: s.json, model: ` + model + `, on: {ok: done, fail: failed}}
`
	}
	newSeg, _ := json.Marshal(seg("second-version"))
	f, _ := setupFiles(t, `version: 0
start: run
nodes:
  run: {type: segment, segment: seg.yaml, on: {done: done, failed: failed}}
`, map[string]string{"seg.yaml": seg("first"), "p.md": "Do it.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"s1": [[{"write": "seg.yaml", "text": ` + string(newSeg) + `}, {"result": {"outcome": "ok"}}]]}}`})
	var out strings.Builder
	f.e.Out = &out
	f.drive(Done)
	steps := f.steps()
	if len(steps) != 2 || steps[1].Model != "second-version" {
		t.Fatalf("steps %+v", steps)
	}
	if !strings.Contains(out.String(), "seg.yaml changed; the next steps use it") {
		t.Fatalf("progress:\n%s", out.String())
	}
}

const limitOrders = `version: 0
workdir: .
start: fix
executors: {stub: {run: PLAN}}
checks:
  unit: {run: UNIT}
nodes:
  fix: {type: agent, executor: stub, prompt: p.md, schema: s.json, max_attempts: 1, on: {ok: tests, fail: failed}}
  tests: {type: gate, checks: [unit], on: {pass: done, blocked: {to: fix, max: 2, then: stuck}, stuck: failed}}
`

// A limited transition is taken as often as its limit allows; then the
// outcome it names is taken instead. A rewind to a node before the loop
// undoes the transitions taken since, so they count no more.
func TestTransitionLimit(t *testing.T) {
	ok := `[{"result": {"outcome": "ok"}}]`
	f := setupAgent(t, strings.Replace(limitOrders, "UNIT", arg("exit", "1"), 1), map[string]string{"p.md": "Fix.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"fix": [` + strings.Repeat(ok+",", 5) + ok + `]}}`}, "")
	f.drive(Failed)
	var moves []string
	for _, e := range f.events(chronicle.Transition) {
		moves = append(moves, e.Node+" "+e.Outcome+">"+e.To+" "+e.Instead)
	}
	want(t, moves, []string{"fix ok>tests ", "tests blocked>fix ", "fix ok>tests ", "tests blocked>fix ", "fix ok>tests ", "tests stuck>failed blocked"})
	if n := taken(f.events(""), "tests", "blocked"); n != 2 {
		t.Fatalf("taken %d", n)
	}
	release, err := Rewind(f.e.Orders, f.chron, f.e.Campaign, "fix", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if n := taken(f.events(""), "tests", "blocked"); n != 0 {
		t.Fatalf("taken after the rewind %d", n)
	}
	f.drive(Failed)
	release()
	if n := len(f.events(chronicle.Transition)); n != 12 {
		t.Fatalf("transitions after the rewind %d, want the loop twice more and the limit again:\n%s", n, f.dump())
	}
}

// A rewind to a node inside the loop undoes only what came after the
// campaign first reached it; a retry undoes nothing.
func TestTakenAfterRetryAndRewind(t *testing.T) {
	ev := func(kind, node, outcome, to string) chronicle.Event {
		return chronicle.Event{Kind: kind, Node: node, Outcome: outcome, Details: chronicle.Details{To: to}}
	}
	events := []chronicle.Event{
		ev(chronicle.CampaignStarted, "plan", "", ""),
		ev(chronicle.Transition, "plan", "ok", "fix"),
		ev(chronicle.Transition, "fix", "ok", "tests"),
		ev(chronicle.Transition, "tests", "blocked", "fix"),
		ev(chronicle.Transition, "fix", "ok", "tests"),
		ev(chronicle.Transition, "tests", "blocked", "fix"),
		ev(chronicle.Retried, "fix", "", ""),
	}
	if n := taken(events, "tests", "blocked"); n != 2 {
		t.Fatalf("after a retry %d", n)
	}
	if n := taken(append(events, ev(chronicle.Rewound, "tests", "", "")), "tests", "blocked"); n != 0 {
		t.Fatalf("after a rewind to tests %d", n)
	}
	if n := taken(append(events, ev(chronicle.Rewound, "plan", "", "")), "tests", "blocked"); n != 0 {
		t.Fatalf("after a rewind to plan %d", n)
	}
}

// {{rework}} names the step that sent the flow back, looking through a
// script node that only passes it on.
func TestReworkLooksThroughScripts(t *testing.T) {
	f := setupAgent(t, `version: 0
workdir: .
start: implement
executors: {stub: {run: PLAN}}
nodes:
  implement: {type: agent, executor: stub, prompt: implement.md, schema: ok.json, on: {ok: review, fail: failed}}
  review: {type: agent, executor: stub, prompt: review.md, schema: review.json, on: {ok: done, needs_rework: count, fail: failed}}
  count: {type: script, run: `+arg("exit", "0")+`, on: {ok: implement, fail: failed}}
`, map[string]string{"implement.md": "Implement.{{rework}}", "review.md": "Review.", "ok.json": okSchema, "review.json": reviewSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {
			"implement": [[{"result": {"outcome": "ok"}}], [{"result": {"outcome": "ok"}}]],
			"review": [[{"result": {"outcome": "needs_rework", "notes": "add tests"}}], [{"result": {"outcome": "ok"}}]]}}`}, "")
	f.drive(Done)
	p := f.steps()[2].Prompt
	if !strings.Contains(p, `step "review" finished with outcome "needs_rework" and sent the flow back here through "count"`) ||
		!strings.Contains(p, `"notes": "add tests"`) {
		t.Fatalf("rework prompt:\n%s", p)
	}
}

// The artifacts of a gate's checks are the gate's artifacts, and {{rework}}
// after a blocked gate names the check that failed, its log and artifacts.
func TestGateCheckArtifactsReachTheNextStep(t *testing.T) {
	f := setupAgent(t, `version: 0
workdir: .
start: fix
executors: {stub: {run: PLAN}}
checks:
  lint: {run: `+arg("writeexit", "make-lint.log", "lint clean", "0")+`}
  unit: {run: `+arg("writeexit", "make-test.log", "TestX failed", "1")+`}
nodes:
  fix: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: verify, fail: failed}}
  verify: {type: gate, checks: [lint, unit], on: {pass: done, blocked: {to: fix, max: 1, then: stuck}, stuck: failed}}
`, map[string]string{"p.md": "Fix.\n[{{artifact.verify.make-test.log}}] [{{artifact.verify.make-lint.log}}]\n{{rework}}", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"fix": [[{"result": {"outcome": "ok"}}]]}}`}, "")
	f.drive(Failed)
	p := f.steps()[1].Prompt
	for _, want := range []string{"[TestX failed] [lint clean]", `step "verify" finished with outcome "blocked"`,
		"Checks that did not pass: unit (fail, log " + filepath.Join(CampaignDir(f.e.Orders, f.e.Campaign), "logs", "verify.unit.1.log") + ", artifacts in the work folder: make-test.log)",
		`Artifacts of "verify" in the work folder: make-lint.log, make-test.log`} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
}

// A review loop with a limit and no helper node: each rework of implement
// carries the reviewer's output, and after two rounds the other outcome is
// taken.
func TestReviewLoopWithoutHelper(t *testing.T) {
	f := setupAgent(t, `version: 0
workdir: .
start: implement
executors: {stub: {run: PLAN}}
nodes:
  implement: {type: agent, executor: stub, prompt: implement.md, schema: ok.json, on: {ok: review, fail: failed}}
  review:
    type: agent
    executor: stub
    prompt: review.md
    schema: review.json
    on: {ok: done, needs_rework: {to: implement, max: 2, then: escalate}, escalate: failed, fail: failed}
`, map[string]string{"implement.md": "Implement.{{rework}}", "review.md": "Review.", "ok.json": okSchema, "review.json": reviewSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {
			"implement": [[{"result": {"outcome": "ok"}}]],
			"review": [[{"result": {"outcome": "needs_rework", "notes": "round one"}}], [{"result": {"outcome": "needs_rework", "notes": "round two"}}],
				[{"result": {"outcome": "needs_rework", "notes": "round three"}}]]}}`}, "")
	f.drive(Failed)
	var prompts []string
	for _, s := range f.steps() {
		if s.NodeID == "implement" {
			prompts = append(prompts, s.Prompt)
		}
	}
	if len(prompts) != 3 || !strings.Contains(prompts[1], `step "review" finished with outcome "needs_rework"`) || !strings.Contains(prompts[1], "round one") ||
		!strings.Contains(prompts[2], "round two") {
		t.Fatalf("implement prompts:\n%s", strings.Join(prompts, "\n----\n"))
	}
	last := f.events(chronicle.Transition)
	if e := last[len(last)-1]; e.Outcome != "escalate" || e.Instead != "needs_rework" || e.To != "failed" {
		t.Fatalf("last transition %+v", e)
	}
}

// An agent step works in its node's folder; the state folder and the files
// of the orders are protected, the campaign's work folder stays writable,
// and {{work}} names that folder.
func TestWorkdirFenceAndWorkPlaceholder(t *testing.T) {
	f := setupAgent(t, `version: 0
workdir: code
start: a
executors: {stub: {run: PLAN}}
nodes:
  a: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: done, fail: failed}}
`, map[string]string{"p.md": "Write {{work}}/plan.md.", "s.json": okSchema, "code/main.go": "package main\n",
		"plan.json": `{"trace": "../trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "ok"}}]]}}`}, "")
	f.drive(Done)
	st := f.steps()[0]
	o := f.e.Orders
	work := WorkDir(o, f.e.Campaign)
	if st.Workdir != filepath.Join(o.Dir, "code") || st.Prompt != "Write "+work+"/plan.md." {
		t.Fatalf("workdir %q prompt %q", st.Workdir, st.Prompt)
	}
	for _, p := range []string{StateDir(o), o.Path, filepath.Join(o.Dir, "p.md"), filepath.Join(o.Dir, "s.json")} {
		found := false
		for _, q := range st.Protect {
			found = found || q == p
		}
		if !found {
			t.Errorf("%s is not protected: %v", p, st.Protect)
		}
	}
	if len(st.Writable) != 1 || st.Writable[0] != work {
		t.Errorf("writable %v", st.Writable)
	}
}

// A workdir that does not exist stops the campaign before the step runs.
func TestMissingWorkdirStops(t *testing.T) {
	f := setupAgent(t, strings.Replace(unableAgent(""), "workdir: .", "workdir: gone", 1), map[string]string{"p.md": "Do it.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl"}`}, "")
	f.drive(Errored)
	if stops := f.events(chronicle.Stopped); len(stops) != 1 || !strings.Contains(stops[0].Error, "workdir") {
		t.Fatalf("stops %+v", stops)
	}
}

// An item whose needs are all done waits for a free place, not for them.
func TestOpenNeeds(t *testing.T) {
	list := []Child{{Item: "a", ID: 2, Outcome: orders.Done}, {Item: "b", ID: 3}, {Item: "c", Needs: []string{"a"}}, {Item: "d", Needs: []string{"a", "b"}}}
	if got := OpenNeeds(list, list[2]); len(got) != 0 {
		t.Errorf("c waits for %v", got)
	}
	if got := OpenNeeds(list, list[3]); len(got) != 1 || got[0] != "b" {
		t.Errorf("d waits for %v", got)
	}
}

func TestShowPath(t *testing.T) {
	o := &orders.Orders{Dir: filepath.FromSlash("/flows/x")}
	seg := &orders.Orders{Dir: filepath.FromSlash("/flows/x/segs")}
	cases := []struct {
		entry string
		no    *orders.Orders
		want  string
	}{
		{"plans/a.md", nil, filepath.FromSlash("/flows/x/plans/a.md")},
		{"a.md", seg, filepath.FromSlash("/flows/x/segs/a.md")},
		{"{{work}}/plan.md", seg, filepath.Join(WorkDir(o, 7), "plan.md")},
		{"{{work}}/../x", nil, ""},
		{"{{work}}/", nil, ""},
		{"../x", nil, ""},
	}
	for _, c := range cases {
		got, ok := ShowPath(o, c.no, 7, c.entry)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %v, want %q", c.entry, got, ok, c.want)
		}
	}
}
