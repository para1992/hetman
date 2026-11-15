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

// An agent that says it cannot proceed after denials only of a tool the
// step allows otherwise: the stop message lists the entries it did not use.
func TestUnableAfterADenialOfAnAllowedTool(t *testing.T) {
	f := setupAgent(t, strings.Replace(unableAgent(""), "schema: s.json,", `schema: s.json, tools: [Read, "Bash(sh ../checks/try.sh *)"],`, 1), map[string]string{"p.md": "Do it.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "cannot_proceed", "cannot_proceed_reason": "Bash is denied"}, "denied": ["Bash ls; cat x: matched none of the step's entries Bash(sh ../checks/try.sh *)"]}]]}}`}, "")
	f.drive(Errored)
	stops := f.events(chronicle.Stopped)
	if len(stops) != 1 || !strings.Contains(stops[0].Error, "cannot proceed: Bash is denied (every call the client denied was of a tool the step allows otherwise, as Bash(sh ../checks/try.sh *); the agent may have taken the denial for the whole tool, so hetman retry is likely to succeed)") {
		t.Fatalf("stop %+v", stops)
	}
}

// The reason field next to an ordinary outcome is no part of the step's
// schema: it is left out before validation and of the stored output, even
// under additionalProperties false. A reason that says something is kept on
// the event; the answer still counts as the outcome it names.
func TestUnableReasonNextToAnotherOutcome(t *testing.T) {
	strict := `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}, "summary": {"type": "string"}}, "required": ["outcome"], "additionalProperties": false}`
	for _, c := range []struct{ field, dropped string }{
		{`""`, ""},
		{`"  \n "`, ""},
		{`null`, ""},
		{`" the tests were slow "`, "the tests were slow"},
		{`7`, "7"},
	} {
		f := setupAgent(t, unableAgent(""), map[string]string{"p.md": "Do it.", "s.json": strict,
			"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "ok", "summary": "done", "cannot_proceed_reason": ` + c.field + `}}]]}}`}, "")
		f.drive(Done)
		want(t, f.finished(), []string{"a#1 ok/"})
		fin := f.events(chronicle.StepFinished)[0]
		if string(fin.Output) != `{"outcome":"ok","summary":"done"}` || fin.Dropped != c.dropped || fin.Failure != "" {
			t.Fatalf("field %s: output %s, dropped %q, failure %q", c.field, fin.Output, fin.Dropped, fin.Failure)
		}
	}
	// Without the field the output is stored as given.
	f := setupAgent(t, unableAgent(""), map[string]string{"p.md": "Do it.", "s.json": strict,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"summary": "done", "outcome": "ok"}}]]}}`}, "")
	f.drive(Done)
	if fin := f.events(chronicle.StepFinished)[0]; string(fin.Output) != `{"summary":"done","outcome":"ok"}` || fin.Dropped != "" {
		t.Fatalf("output %s, dropped %q", fin.Output, fin.Dropped)
	}
}

// The instruction says plainly that the field stays out of an ordinary answer.
func TestUnableInstructionLeavesTheFieldOut(t *testing.T) {
	if s := unableInstruction(); !strings.Contains(s, `In every other answer leave the field "cannot_proceed_reason" out entirely`) {
		t.Fatalf("instruction %q", s)
	}
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
	// With a scope, only denied calls of the tools it names fail an attempt.
	scoped := strings.Replace(unableAgent(""), "schema: s.json,", "schema: s.json, fail_on_denied: [Bash],", 1)
	f = setupAgent(t, scoped, map[string]string{"p.md": "Do it.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "ok"}, "denied": ["Read /etc/hosts: not among the step's tools"]}]]}}`}, "")
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 ok/"})
	f = setupAgent(t, scoped, map[string]string{"p.md": "Do it.", "s.json": okSchema, "plan.json": plan}, "")
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 /denied", "a#2 ok/"})
}

// Orders read some time before a campaign moves, as a long-lived process
// such as the page server keeps them, are read again when a prompt template
// or a schema changed in between, though no orders file did.
func TestEditBeforeTheFirstMoveIsNotMissed(t *testing.T) {
	f := setupAgent(t, unableAgent(""), map[string]string{"p.md": "Old wording.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[{"result": {"outcome": "ok"}}]]}}`}, "")
	newSchema := strings.Replace(okSchema, `"summary": {"type": "string"}`, `"summary": {"type": "string", "description": "new"}`, 1)
	if err := os.WriteFile(filepath.Join(f.e.Orders.Dir, "p.md"), []byte("New wording, longer than before."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.e.Orders.Dir, "s.json"), []byte(newSchema), 0o644); err != nil {
		t.Fatal(err)
	}
	f.drive(Done)
	steps := f.steps()
	if len(steps) != 1 || !strings.HasPrefix(steps[0].Prompt, "New wording") || !strings.Contains(string(steps[0].Schema), `"description":"new"`) {
		t.Fatalf("steps %+v", steps)
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

// Progress messages keep a step that says nothing else alive past its
// idle_timeout.
func TestProgressResetsTheIdleTimer(t *testing.T) {
	p := `{"progress": true}, {"sleep": "600ms"}, `
	f := setupAgent(t, strings.Replace(unableAgent(""), "schema: s.json,", "schema: s.json, idle_timeout: 1s,", 1), map[string]string{"p.md": "Do it.", "s.json": okSchema,
		"plan.json": `{"trace": "trace.jsonl", "nodes": {"a": [[` + p + p + p + `{"result": {"outcome": "ok"}}]]}}`}, "")
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 ok/"})
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

// A symbolic link inside a folder does not let show lead out of it, and a
// workdir that reaches the state folder through a link is refused.
func TestLinksDoNotLeadOut(t *testing.T) {
	dir := tempDir(t)
	o := &orders.Orders{Dir: filepath.Join(dir, "flow"), Path: filepath.Join(dir, "flow", orders.DefaultFile)}
	os.MkdirAll(filepath.Join(o.Dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("x"), 0o644)
	if err := os.Symlink(dir, filepath.Join(o.Dir, "out")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if _, ok := ShowPath(o, nil, 1, "out/secret.txt"); ok {
		t.Error("show followed a link out of the orders folder")
	}
	if p, ok := ShowPath(o, nil, 1, "docs/plan.md"); !ok || p != filepath.Join(o.Dir, "docs", "plan.md") {
		t.Errorf("show of a plain file: %q %v", p, ok)
	}
	os.MkdirAll(filepath.Join(StateDir(o), "campaigns", "1"), 0o755)
	os.Symlink(filepath.Join(StateDir(o), "campaigns", "1"), filepath.Join(dir, "sneaky"))
	e := &Engine{Orders: o, Campaign: 1}
	if _, err := e.workdir(&orders.Node{ID: "a", Workdir: filepath.Join(dir, "sneaky")}); err == nil || !strings.Contains(err.Error(), "hetman's own files") {
		t.Errorf("workdir through a link into .hetman: %v", err)
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
