package engine

import (
	"strings"
	"testing"
	"time"

	"hetman/internal/chronicle"
)

const strictSchema = `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}, "summary": {"type": "string"}}, "required": ["outcome"], "additionalProperties": false}`

// repairWorld is one agent node "a" with a strict schema, run by the stub
// with plan.
func repairWorld(t *testing.T, extra, plan string) *fixture {
	return setupAgent(t, one(extra), map[string]string{"p.md": "Do it.", "s.json": strictSchema, "plan.json": plan}, "")
}

func repairs(f *fixture) []chronicle.Event { return f.events(chronicle.OutputRepair) }

// An answer with one property too many goes back to the same session,
// which corrects it; the attempt then counts as the corrected answer, and
// what the first turn's client denied is kept.
func TestInvalidAnswerIsRepairedInTheSameSession(t *testing.T) {
	f := repairWorld(t, "    max_attempts: 1", `{"trace": "trace.jsonl",
		"nodes": {"a": [[{"session": "s-1"}, {"log": "working"},
			{"result": {"outcome": "ok", "summary": "done", "note": ""}, "denied": ["Bash(rm x): not allowed"]}]]},
		"repairs": {"a": [[{"require_session": "s-1"}, {"session": "s-1"}, {"result": {"outcome": "ok", "summary": "done"}}]]}}`)
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 ok/"})
	fin := f.events(chronicle.StepFinished)[0]
	if string(fin.Output) != `{"outcome":"ok","summary":"done"}` || strings.Join(fin.Denied, "|") != "Bash(rm x): not allowed" {
		t.Fatalf("finish %+v", fin)
	}
	rs := repairs(f)
	if len(rs) != 1 || rs[0].Round != 1 || rs[0].Attempt != 1 || rs[0].Session != "s-1" || rs[0].Prompt != RepairPromptFile("a", 1, 1) ||
		!strings.Contains(rs[0].Error, `property "note" is not allowed`) {
		t.Fatalf("repairs %+v", rs)
	}
	steps := f.steps()
	if len(steps) != 2 {
		t.Fatalf("steps %+v", steps)
	}
	r := steps[1]
	if r.Repair != 1 || r.Attempt != 1 || r.Session == nil || *r.Session != "s-1" || string(r.Schema) != string(steps[0].Schema) ||
		strings.Join(r.Tools, ",") != strings.Join(steps[0].Tools, ",") {
		t.Fatalf("repair step %+v", r)
	}
	for _, w := range []string{"Your answer was not accepted", "\n- $: property \"note\" is not allowed\n", "Do not redo", "only the corrected JSON object"} {
		if !strings.Contains(r.Prompt, w) {
			t.Fatalf("repair prompt lacks %q:\n%s", w, r.Prompt)
		}
	}
	if strings.Contains(r.Prompt, "Do it.") {
		t.Fatalf("repair prompt repeats the task:\n%s", r.Prompt)
	}
	if steps[0].Repair != 0 {
		t.Fatalf("first step %+v", steps[0])
	}
	if got := f.file(RepairPromptFile("a", 1, 1)); got != r.Prompt {
		t.Fatalf("stored repair prompt %q", got)
	}
	log := f.file(LogFile("a", "", 1))
	for _, w := range []string{"working\n", "hetman: answer rejected (invalid_output): ", `hetman: rejected answer: {"outcome":"ok","summary":"done","note":""}`,
		"hetman: asking session s-1 to correct only the answer (repair 1 of 2)"} {
		if !strings.Contains(log, w) {
			t.Fatalf("log lacks %q:\n%s", w, log)
		}
	}
	if !strings.Contains(f.file(ResultFile("a", "", 1)), `"summary": "done"`) || strings.Contains(f.file(ResultFile("a", "", 1)), "note") {
		t.Fatalf("result file %s", f.file(ResultFile("a", "", 1)))
	}
}

// Two rejected answers, then a good one: both repairs of the attempt are
// used, and it is still one attempt.
func TestInvalidTwiceThenRepaired(t *testing.T) {
	f := repairWorld(t, "    max_attempts: 1", `{"trace": "trace.jsonl",
		"nodes": {"a": [[{"session": "s-1"}, {"result": {"outcome": "done"}}]]},
		"repairs": {"a": [[{"result": {"outcome": "ok", "extra": 1}}], [{"require_session": "s-1"}, {"result": {"outcome": "ok"}}]]}}`)
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 ok/"})
	rs := repairs(f)
	if len(rs) != 2 || rs[0].Round != 1 || rs[1].Round != 2 || !strings.Contains(rs[0].Error, `must be one of "ok"`) ||
		!strings.Contains(rs[1].Error, `property "extra" is not allowed`) {
		t.Fatalf("repairs %+v", rs)
	}
	if steps := f.steps(); len(steps) != 3 || steps[1].Repair != 1 || steps[2].Repair != 2 || !strings.Contains(steps[2].Prompt, `"extra"`) {
		t.Fatalf("steps %+v", steps)
	}
}

// When both repairs are rejected too, the attempt fails with invalid_output
// as it would have without them, and the next attempt starts afresh with
// the last rejection as feedback.
func TestRepairsExhausted(t *testing.T) {
	f := repairWorld(t, "    max_attempts: 2", `{"trace": "trace.jsonl",
		"nodes": {"a": [[{"session": "s-1"}, {"result": {"outcome": "done"}}], [{"session": "s-2"}, {"result": {"outcome": "ok"}}]]},
		"repairs": {"a": [[{"result": {"outcome": "ok", "x": 1}}], [{"result": {"outcome": "ok", "y": 2}}]]}}`)
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 /invalid_output", "a#2 ok/"})
	fin := f.events(chronicle.StepFinished)[0]
	if !strings.Contains(fin.Error, `property "y" is not allowed`) {
		t.Fatalf("finish %+v", fin)
	}
	if rs := repairs(f); len(rs) != 2 || rs[0].Attempt != 1 || rs[1].Attempt != 1 {
		t.Fatalf("repairs %+v", rs)
	}
	steps := f.steps()
	if len(steps) != 4 || steps[3].Repair != 0 || steps[3].Session != nil || steps[3].Attempt != 2 ||
		!strings.Contains(steps[3].Prompt, "Your previous answer (attempt 1) was rejected") || !strings.Contains(steps[3].Prompt, `"y"`) {
		t.Fatalf("steps %+v", steps)
	}
}

// An adapter that does not declare the repair feature, or a turn that
// reported no session, gets no repair: the attempt fails as before.
func TestNoRepairWithoutTheFeatureOrASession(t *testing.T) {
	for name, plan := range map[string]string{
		"no feature": `{"trace": "trace.jsonl", "features": ["resume"],
			"nodes": {"a": [[{"session": "s-1"}, {"result": {"outcome": "done"}}], [{"result": {"outcome": "ok"}}]]},
			"repairs": {"a": [[{"result": {"outcome": "ok"}}]]}}`,
		"no session": `{"trace": "trace.jsonl",
			"nodes": {"a": [[{"result": {"outcome": "done"}}], [{"result": {"outcome": "ok"}}]]},
			"repairs": {"a": [[{"result": {"outcome": "ok"}}]]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := repairWorld(t, "    max_attempts: 2", plan)
			f.drive(Done)
			want(t, f.finished(), []string{"a#1 /invalid_output", "a#2 ok/"})
			if rs := repairs(f); len(rs) != 0 {
				t.Fatalf("repairs %+v", rs)
			}
			if steps := f.steps(); len(steps) != 2 {
				t.Fatalf("steps %+v", steps)
			}
		})
	}
}

// A repair round that gives no answer, here because the adapter cannot
// continue the session, leaves the attempt failed with the rejection of
// its own answer.
func TestRepairWithoutAnAnswer(t *testing.T) {
	f := repairWorld(t, "    max_attempts: 1", `{"trace": "trace.jsonl",
		"nodes": {"a": [[{"session": "s-1"}, {"result": {"outcome": "ok", "note": ""}, "denied": ["Read(/etc): outside"]}]]},
		"repairs": {"a": [[{"error": "permanent", "message": "no session with the id", "denied": []}]]}}`)
	f.drive(Failed)
	want(t, f.finished(), []string{"a#1 fail/invalid_output"})
	fin := f.events(chronicle.StepFinished)[0]
	if !strings.Contains(fin.Error, `property "note" is not allowed`) || strings.Join(fin.Denied, "|") != "Read(/etc): outside" {
		t.Fatalf("finish %+v", fin)
	}
	if rs := repairs(f); len(rs) != 1 {
		t.Fatalf("repairs %+v", rs)
	}
	if log := f.file(LogFile("a", "", 1)); !strings.Contains(log, "hetman: the repair gave no answer (permanent: no session with the id)") {
		t.Fatalf("log:\n%s", log)
	}
}

// The step's timeout bounds the attempt with its repairs: a repair that
// would run past it is cut off, and the attempt fails with invalid_output.
func TestRepairIsBoundedByTheTimeout(t *testing.T) {
	f := repairWorld(t, "    max_attempts: 1\n    timeout: 3s", `{"trace": "trace.jsonl",
		"nodes": {"a": [[{"session": "s-1"}, {"sleep": "1500ms"}, {"result": {"outcome": "done"}}]]},
		"repairs": {"a": [[{"hang": true}]]}}`)
	began := time.Now()
	f.drive(Failed)
	if d := time.Since(began); d > 10*time.Second {
		t.Fatalf("took %s", d)
	}
	want(t, f.finished(), []string{"a#1 fail/invalid_output"})
	if log := f.file(LogFile("a", "", 1)); !strings.Contains(log, "the repair gave no answer (timeout: killed after timeout") {
		t.Fatalf("log:\n%s", log)
	}
}

// A repair turn whose client denies a call fails the attempt as denied
// under fail_on_denied, with the denials of both turns.
func TestRepairKeepsDenialsForFailOnDenied(t *testing.T) {
	f := repairWorld(t, "    max_attempts: 1\n    fail_on_denied: [Bash]", `{"trace": "trace.jsonl",
		"nodes": {"a": [[{"session": "s-1"}, {"result": {"outcome": "ok", "note": ""}, "denied": ["Read /etc/x: outside"]}]]},
		"repairs": {"a": [[{"result": {"outcome": "ok"}, "denied": ["Bash ls: not allowed"]}]]}}`)
	f.drive(Failed)
	want(t, f.finished(), []string{"a#1 fail/denied"})
	fin := f.events(chronicle.StepFinished)[0]
	if strings.Join(fin.Denied, "|") != "Read /etc/x: outside|Bash ls: not allowed" {
		t.Fatalf("finish %+v", fin)
	}
}

func TestMergeDenied(t *testing.T) {
	got := mergeDenied([]string{"a", "b"}, []string{"b", "c"})
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("got %v", got)
	}
	if got := mergeDenied(nil, []string{"x", "x"}); strings.Join(got, ",") != "x,x" {
		t.Fatalf("got %v", got)
	}
}
