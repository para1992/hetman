package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"hetman/internal/orders"
	"hetman/internal/scribe"
	"hetman/internal/teststep"
)

// scribeCall runs hetman in this process with the given input.
func scribeCall(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	Stdin = strings.NewReader(stdin)
	defer func() { Stdin = os.Stdin }()
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestScribeOperationCommands(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, orders.DefaultFile)
	f := []string{"-f", path}
	code, out, errOut := scribeCall(t, "", append([]string{"scribe", "new", "script", "prepare", "-flow", "demo", "-run", "sh steps/prepare.sh"}, f...)...)
	if code != exitDone || !strings.Contains(out, "+    run: [sh, steps/prepare.sh]") {
		t.Fatalf("new: %d\n%s%s", code, out, errOut)
	}
	before := readFile(t, path)

	// Refused before anything is changed: the check is not declared.
	code, _, errOut = scribeCall(t, "", append([]string{"scribe", "add", "gate", "ready", "-after", "prepare", "-checks", "lint"}, f...)...)
	if code != exitUsage || !strings.Contains(errOut, `check "lint" is not declared`) || readFile(t, path) != before {
		t.Fatalf("add with an undeclared check: %d %s", code, errOut)
	}
	if code, out, errOut = scribeCall(t, "", append([]string{"scribe", "check", "lint", "-run", "sh checks/lint.sh", "-soft"}, f...)...); code != exitDone {
		t.Fatalf("check: %d\n%s%s", code, out, errOut)
	}
	// A dry run shows the change and writes nothing.
	before = readFile(t, path)
	code, out, _ = scribeCall(t, "", append([]string{"scribe", "add", "gate", "ready", "-after", "prepare", "-checks", "lint", "-dry-run"}, f...)...)
	if code != exitDone || !strings.Contains(out, "+  ready:") || !strings.Contains(out, "Dry run: nothing was written.") || readFile(t, path) != before {
		t.Fatalf("dry run: %d\n%s", code, out)
	}
	if code, out, errOut = scribeCall(t, "", append([]string{"scribe", "add", "gate", "ready", "-after", "prepare", "-checks", "lint"}, f...)...); code != exitDone {
		t.Fatalf("add: %d\n%s%s", code, out, errOut)
	}
	// A result that fails the check is not written, unless forced.
	before = readFile(t, path)
	code, out, errOut = scribeCall(t, "", append([]string{"scribe", "unlink", "prepare", "fail"}, f...)...)
	if code != exitUsage || !strings.Contains(errOut, `outcome "fail" has no transition`) || !strings.Contains(errOut, "nothing was written") || readFile(t, path) != before {
		t.Fatalf("unlink: %d\n%s%s", code, out, errOut)
	}
	code, out, _ = scribeCall(t, "", append([]string{"scribe", "unlink", "prepare", "fail", "-force"}, f...)...)
	if code != exitDone || !strings.Contains(out, "although it does not pass the check") || strings.Contains(readFile(t, path), "fail: failed}\n  ready") {
		t.Fatalf("unlink -force: %d\n%s", code, out)
	}
	if code, _, _ = scribeCall(t, "", "check", "-f", path); code != exitUsage {
		t.Fatal("the forced file passes the check")
	}
	if code, out, errOut = scribeCall(t, "", append([]string{"scribe", "link", "prepare", "fail", "failed"}, f...)...); code != exitDone {
		t.Fatalf("link: %d\n%s%s", code, out, errOut)
	}
	// Wrong arguments name the form and an example.
	code, _, errOut = scribeCall(t, "", "scribe", "link", "prepare")
	if code != exitUsage || !strings.Contains(errOut, "usage: hetman scribe link NODE OUTCOME TARGET") {
		t.Fatalf("usage: %d %s", code, errOut)
	}
	code, out, _ = scribeCall(t, "", "scribe", "help")
	for _, u := range opUsage {
		if !strings.Contains(out, u.form) {
			t.Errorf("help lacks %q", u.form)
		}
	}
}

// scribeWorld is a flow whose orders let Scribe ask the stub adapter,
// which answers from plan.json.
func scribeWorld(t *testing.T, plan string) *world {
	dir := tempDir(t)
	w := &world{t: t, dir: dir, orders: filepath.Join(dir, orders.DefaultFile), sync: filepath.Join(dir, "sync")}
	os.Mkdir(w.sync, 0o755)
	os.WriteFile(filepath.Join(dir, "plan.json"), []byte(plan), 0o644)
	config := `# The flow of a test. The model never sees this comment.
version: 0
flow: demo
start: build
scribe: {executor: stub, model: m1, effort: low}

executors:
  stub: {run: ` + teststep.Adapter(filepath.Join(dir, "plan.json")) + `}

checks:
  lint: {run: ` + arg("exit", "0") + `, soft: true}

nodes:
  build:
    type: script
    run: ` + arg("exit", "0") + `
    on: {ok: test, fail: failed}
  test:
    type: script
    run: ` + arg("exit", "0") + `
    on: {ok: done, fail: build}
`
	os.WriteFile(w.orders, []byte(config), 0o644)
	return w
}

func (w *world) requests() []map[string]any {
	w.t.Helper()
	data, _ := os.ReadFile(filepath.Join(w.dir, "trace.jsonl"))
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// plan answers a sentence with the given results, one per attempt.
func plan(sentence string, results ...string) string {
	var moves []string
	for _, r := range results {
		moves = append(moves, `[{"result": `+r+`}]`)
	}
	return `{"trace": "trace.jsonl", "when": [{"prompt": "` + sentence + `", "moves": [` + strings.Join(moves, ", ") + `]}]}`
}

const addGate = `{"outcome": "operation", "operation": {"op": "add", "type": "gate", "node": "lint_gate", "after": "build", "checks": ["lint"]}}`

func TestScribeSentenceClear(t *testing.T) {
	w := scribeWorld(t, plan("add a gate with the lint check after build", addGate))
	before := readFile(t, w.orders)
	// Not confirmed: nothing changes.
	code, out, _ := scribeCall(t, "n\n", "scribe", "-f", w.orders, "add a gate with the lint check after build")
	if code != exitFailed || !strings.Contains(out, "Scribe proposes: add the gate node lint_gate after build, with the checks lint.") ||
		!strings.Contains(out, "+  lint_gate:") || !strings.Contains(out, "Nothing was changed.") || readFile(t, w.orders) != before {
		t.Fatalf("declined: %d\n%s", code, out)
	}
	// Confirmed: written.
	code, out, _ = scribeCall(t, "y\n", "scribe", "-f", w.orders, "add a gate with the lint check after build")
	if code != exitDone || !strings.Contains(out, "Apply this change? [y/N] y\nWrote ") {
		t.Fatalf("confirmed: %d\n%s", code, out)
	}
	o, err := orders.Load(w.orders)
	if err != nil {
		t.Fatal(err)
	}
	if o.Nodes["build"].On["ok"] != "lint_gate" || o.Nodes["lint_gate"].On["pass"] != "test" {
		t.Fatalf("not applied: %v", o.Order)
	}
	// What the model received: one request per sentence, no tools, the
	// sentence, the flow described and the schema; never the file's text.
	reqs := w.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2", len(reqs))
	}
	r := reqs[0]
	prompt := r["prompt"].(string)
	schemaJSON, _ := json.Marshal(r["schema"])
	var want any
	json.Unmarshal(scribe.Schema(), &want)
	wantJSON, _ := json.Marshal(want)
	switch {
	case r["node_id"] != "scribe" || r["model"] != "m1" || r["effort"] != "low" || len(r["tools"].([]any)) != 0:
		t.Fatalf("request %v", r)
	case !strings.Contains(prompt, "The sentence:\n  add a gate with the lint check after build\n"):
		t.Fatal(prompt)
	case !strings.Contains(prompt, "    build: script; ok -> test, fail -> failed\n") || !strings.Contains(prompt, "declared checks: lint (soft)"):
		t.Fatal(prompt)
	case strings.Contains(prompt, "never sees this comment") || strings.Contains(prompt, "exit"):
		t.Fatal("the prompt holds the text of the file")
	case string(schemaJSON) != string(wantJSON):
		t.Fatalf("schema %s", schemaJSON)
	}
	// -yes applies a clear operation without asking.
	w2 := scribeWorld(t, plan("add a gate with the lint check after build", addGate))
	if code, out, _ := scribeCall(t, "", "scribe", "-f", w2.orders, "-yes", "add a gate with the lint check after build"); code != exitDone || strings.Contains(out, "Apply this change?") {
		t.Fatalf("-yes: %d\n%s", code, out)
	}
}

const unclear = `{"outcome": "unclear", "choices": [
	{"question": "Do you mean the lint check should block the gate?", "operation": {"op": "check", "name": "lint", "soft": false}},
	{"question": "Do you mean a time limit of 1m on build?", "operation": {"op": "set", "node": "build", "field": "timeout", "value": "1m"}},
	{"question": "Do you mean where the latest campaign is?", "query": {"kind": "where"}}]}`

func TestScribeSentenceUnclear(t *testing.T) {
	w := scribeWorld(t, plan("make it stricter", unclear))
	before := readFile(t, w.orders)
	code, out, _ := scribeCall(t, "0\n", "scribe", "-f", w.orders, "make it stricter")
	if code != exitFailed || readFile(t, w.orders) != before {
		t.Fatalf("none chosen: %d\n%s", code, out)
	}
	for _, s := range []string{"Did you mean:", "  1) Do you mean the lint check should block the gate?", "     (declare or change the check lint, required (it blocks the gate))",
		"  2) Do you mean a time limit of 1m on build?", "  3) Do you mean where the latest campaign is?", "  0) none of these", "Nothing was changed."} {
		if !strings.Contains(out, s) {
			t.Fatalf("output lacks %q:\n%s", s, out)
		}
	}
	// -yes never picks one.
	code, out, _ = scribeCall(t, "", "scribe", "-f", w.orders, "-yes", "make it stricter")
	if code != exitFailed || !strings.Contains(out, "Nothing was changed: choosing needs an answer") || readFile(t, w.orders) != before {
		t.Fatalf("-yes: %d\n%s", code, out)
	}
	// A choice, then the confirmation, applies it.
	code, out, _ = scribeCall(t, "2\ny\n", "scribe", "-f", w.orders, "make it stricter")
	if code != exitDone || !strings.Contains(readFile(t, w.orders), "    timeout: 1m\n    on: {ok: test, fail: failed, timeout: failed}") {
		t.Fatalf("chosen: %d\n%s\n%s", code, out, readFile(t, w.orders))
	}
	if n := len(w.requests()); n != 3 {
		t.Fatalf("%d requests, want one per sentence", n)
	}
}

func TestScribeSentenceInvalid(t *testing.T) {
	// Invalid twice: nothing changes; the retry says why.
	w := scribeWorld(t, plan("speed up the build",
		`{"outcome": "operation", "operation": {"op": "set", "node": "build", "field": "speed", "value": "fast"}}`,
		`{"outcome": "operation", "comment": "sure"}`))
	before := readFile(t, w.orders)
	code, out, _ := scribeCall(t, "y\n", "scribe", "-f", w.orders, "speed up the build")
	if code != exitFailed || !strings.Contains(out, "Scribe could not use the model's answer") || readFile(t, w.orders) != before {
		t.Fatalf("%d\n%s", code, out)
	}
	reqs := w.requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1]["prompt"].(string), `Your previous answer was rejected: set: unknown field "speed"`) {
		t.Fatalf("requests %d", len(reqs))
	}
	// Invalid, then valid: the retry is used.
	w = scribeWorld(t, plan("add a gate with the lint check after build", `{"outcome": "operation"}`, addGate))
	if code, out, _ := scribeCall(t, "y\n", "scribe", "-f", w.orders, "add a gate with the lint check after build"); code != exitDone {
		t.Fatalf("%d\n%s", code, out)
	}
	if n := len(w.requests()); n != 2 {
		t.Fatalf("%d requests", n)
	}
	// An adapter that fails: nothing changes.
	w = scribeWorld(t, `{"when": [{"prompt": "anything", "moves": [[{"error": "offline", "message": "no network"}]]}]}`)
	before = readFile(t, w.orders)
	code, out, _ = scribeCall(t, "y\n", "scribe", "-f", w.orders, "anything")
	if code != exitFailed || !strings.Contains(out, "the adapter reported a offline error: no network") || readFile(t, w.orders) != before {
		t.Fatalf("%d\n%s", code, out)
	}
}

func TestScribeSentenceUnknownNode(t *testing.T) {
	w := scribeWorld(t, `{"trace": "trace.jsonl", "when": [
		{"prompt": "remove the deploy step", "moves": [[{"result": {"outcome": "operation", "operation": {"op": "remove", "node": "deploy"}}}]]},
		{"prompt": "build may fail", "moves": [[{"result": {"outcome": "operation", "operation": {"op": "unlink", "node": "build", "outcome": "fail"}}}]]}]}`)
	before := readFile(t, w.orders)
	code, out, _ := scribeCall(t, "y\n", "scribe", "-f", w.orders, "remove the deploy step")
	if code != exitFailed || !strings.Contains(out, `It cannot be applied: there is no node "deploy" (the nodes are: build, test)`) || readFile(t, w.orders) != before {
		t.Fatalf("%d\n%s", code, out)
	}
	// An operation whose result would not pass the check.
	code, out, _ = scribeCall(t, "y\n", "scribe", "-f", w.orders, "build may fail")
	if code != exitFailed || !strings.Contains(out, "would not pass the orders check") || readFile(t, w.orders) != before {
		t.Fatalf("%d\n%s", code, out)
	}
	if n := len(w.requests()); n != 2 {
		t.Fatalf("%d requests", n)
	}
}

func TestScribeSentenceQuery(t *testing.T) {
	w := scribeWorld(t, plan("why did campaign 1 go back to build",
		`{"outcome": "query", "query": {"kind": "rework", "campaign": 1, "node": "build"}}`))
	if code, _, _ := scribeCall(t, "", "run", "-f", w.orders); code != exitDone {
		t.Fatal("run failed")
	}
	before := readFile(t, w.orders)
	code, out, _ := scribeCall(t, "", "scribe", "-f", w.orders, "why did campaign 1 go back to build")
	if code != exitDone || out != "The flow never went back to build.\n" || readFile(t, w.orders) != before {
		t.Fatalf("%d %q", code, out)
	}
}

func TestScribeSentenceNeedsSetting(t *testing.T) {
	w := scribeWorld(t, plan("x", addGate))
	text := strings.Replace(readFile(t, w.orders), "scribe: {executor: stub, model: m1, effort: low}\n", "", 1)
	os.WriteFile(w.orders, []byte(text), 0o644)
	code, out, errOut := scribeCall(t, "y\n", "scribe", "-f", w.orders, "x")
	if code != exitUsage || !strings.Contains(errOut, "names no model for Scribe, and there is no default") ||
		!strings.Contains(errOut, "the wizard asks fixed questions: hetman scribe") || out != "" {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "trace.jsonl")); err == nil {
		t.Fatal("the adapter was started")
	}
}

type countingModel struct{ n *int }

func (m countingModel) Ask(context.Context, string, []byte, int) (json.RawMessage, error) {
	*m.n++
	return nil, context.Canceled
}

// TestWizardAsksNoModel builds a flow of four steps and a gate from nothing
// through the command line, and changes a flow whose orders name a model:
// no model is asked either time.
func TestWizardAsksNoModel(t *testing.T) {
	calls := 0
	defer func(f func(*orders.Orders) scribe.Model) { newModel = f }(newModel)
	newModel = func(*orders.Orders) scribe.Model { return countingModel{&calls} }
	dir := filepath.Join(tempDir(t), "toy")
	os.Mkdir(dir, 0o755)
	path := filepath.Join(dir, orders.DefaultFile)
	answers := []string{"toy", "", "1", "prepare", "", "", "", "",
		"1", "", "build", "", "", "", "5m", "", "",
		"1", "", "test", "", "", "", "", "", "",
		"2", "ready", "", "", "lint", "", "2", "", "y", "tests", "", "", "1m", "n", "", "",
		"1", "", "publish", "", "", "", "", "", "", "11"}
	code, out, errOut := scribeCall(t, strings.Join(answers, "\n")+"\n", "scribe", "-f", path)
	if code != exitDone {
		t.Fatalf("%d\n%s%s", code, out, errOut)
	}
	o, err := orders.Load(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Join(o.Order, " ") != "prepare build test ready publish" || strings.Join(o.Nodes["ready"].Checks, " ") != "lint tests" {
		t.Fatalf("nodes %v", o.Order)
	}
	w := scribeWorld(t, plan("x", addGate))
	if code, out, _ := scribeCall(t, "6\nbuild\n2m\n\n11\n", "scribe", "-f", w.orders); code != exitDone || !strings.Contains(out, "Wrote orders.yaml.") {
		t.Fatalf("%d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "trace.jsonl")); err == nil || calls != 0 {
		t.Fatalf("a model was asked (%d calls)", calls)
	}
}

var (
	stamp    = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`)
	duration = regexp.MustCompile(`after [0-9.]+(ms|s|m[0-9.]*s)`)
)

// explainOf explains the latest campaign with times and durations blanked.
func explainOf(t *testing.T, w *world, args ...string) string {
	t.Helper()
	code, out, errOut := scribeCall(t, "", append([]string{"scribe", "explain", "-f", w.orders}, args...)...)
	if code != exitDone {
		t.Fatalf("explain: %d %s", code, errOut)
	}
	return duration.ReplaceAllString(stamp.ReplaceAllString(out, "T"), "after D")
}

// TestExplainRealCampaigns explains campaigns run by the engine: one that
// went back for rework, one waiting for a person, one stopped offline, and
// one that failed.
func TestExplainRealCampaigns(t *testing.T) {
	// Rework: a review sends the flow back to build once.
	w := newAgentWorld(t, `{"nodes": {"a": [[{"result": {"outcome": "rework", "why": "too short"}}], [{"result": {"outcome": "ok"}}]]}}`, "Review.")
	os.WriteFile(filepath.Join(w.dir, "s.json"), []byte(`{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok", "rework"]}, "why": {"type": "string"}}, "required": ["outcome"]}`), 0o644)
	text := strings.Replace(readFile(t, w.orders), "start: a", "start: b", 1)
	text = strings.Replace(text, "max_attempts: 1, on: {ok: done, fail: failed}}", "max_attempts: 1, on: {ok: g, rework: b, fail: failed}}\n  b: {type: script, run: "+arg("exit", "0")+", on: {ok: a, fail: failed}}\n  g: {type: gate, checks: [style], on: {pass: done, blocked: failed}}\nchecks:\n  style: {run: "+arg("exit", "1")+", soft: true}", 1)
	os.WriteFile(w.orders, []byte(text), 0o644)
	if code, out, errOut := scribeCall(t, "", "run", "-f", w.orders); code != exitDone {
		t.Fatalf("run %d\n%s%s", code, out, errOut)
	}
	if got, want := explainOf(t, w), `Where it is:
  Campaign 1 of flow test started at T at b.
  It is done: g finished with pass at T, which leads to done.

What ran:
  T  b attempt 1 finished with ok after D. The flow went to a.
  T  a attempt 1 finished with rework after D. The flow went to b.
  T  b attempt 2 finished with ok after D. The flow went to a.
  T  a attempt 2 finished with ok after D. The flow went to g.
  T  g attempt 1 finished with pass after D; checks: style fail (soft, a warning). The flow went to done.

Rework:
  The flow went back to b once.
  T  a finished with rework and sent the flow back to b (its output: {"why":"too short"}).

What failed:
  Nothing failed.

Warnings:
  T  The soft check style of g reported fail in attempt 1; it only warned.
`; got != want {
		t.Fatalf("rework:\n%s", got)
	}

	// Waiting for a person at a gate.
	w = newAskingWorld(t)
	proc, _ := w.process("run")
	w.waitAsked(0)
	proc.Process.Kill()
	proc.Wait()
	if got, want := explainOf(t, w, "-kind", "where"), `Campaign 1 of flow test started at T at a.
It has been waiting for a person since T: the gate g asks 2 question(s): "Is it all there?"; "Can it be undone?".
`; got != want {
		t.Fatalf("waiting:\n%s", got)
	}

	// Stopped offline.
	w = newAgentWorld(t, `{"nodes": {"a": [[{"error": "offline", "message": "network is unreachable"}]]}}`, "Do it.")
	if code, _, _ := scribeCall(t, "", "run", "-f", w.orders); code != exitWaiting {
		t.Fatalf("run exit %d", code)
	}
	if got, want := explainOf(t, w, "-kind", "where"), `Campaign 1 of flow test started at T at a.
It has been stopped at a since T: attempt 1 lost connectivity (network is unreachable). The attempt does not count; the step runs again when the campaign is resumed.
`; got != want {
		t.Fatalf("offline:\n%s", got)
	}

	// Failed.
	w = newWorld(t, `  a: {type: script, run: `+arg("exit", "3")+`, on: {ok: done, fail: failed}}
`)
	if code, _, _ := scribeCall(t, "", "run", "-f", w.orders); code != exitFailed {
		t.Fatal("run did not fail")
	}
	if got, want := explainOf(t, w, "-kind", "failures"), "T  a attempt 1 finished with fail (exit code 3).\n"; got != want {
		t.Fatalf("failed:\n%s", got)
	}
	if got, want := explainOf(t, w, "-kind", "where"), "Campaign 1 of flow test started at T at a.\nIt failed: a finished with fail at T, which leads to failed.\n"; got != want {
		t.Fatalf("failed:\n%s", got)
	}
}
