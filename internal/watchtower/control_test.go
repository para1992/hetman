package watchtower_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/chronicle"
	"hetman/internal/edit"
	"hetman/internal/engine"
	"hetman/internal/orders"
	"hetman/internal/teststep"
	"hetman/internal/watchtower"
)

// post sends a JSON body the way the page does, with the given headers,
// and decodes the JSON reply.
func (w *world) post(path string, body any, header map[string]string) (int, map[string]any) {
	w.t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, w.url+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		out = map[string]any{"error": strings.TrimSpace(string(raw))}
	}
	return resp.StatusCode, out
}

func (w *world) read(name string) string {
	w.t.Helper()
	data, err := os.ReadFile(filepath.Join(w.dir, name))
	if err != nil {
		w.t.Fatal(err)
	}
	return string(data)
}

func (w *world) write(name, text string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.dir, name), []byte(text), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) campaigns() int {
	w.t.Helper()
	list, err := w.chron.Campaigns(100)
	if err != nil {
		w.t.Fatal(err)
	}
	return len(list)
}

const twoSteps = `version: 0
flow: pair
start: a
language: uk
nodes:
  a: {type: script, run: A, on: {ok: b, fail: failed}}
  b: {type: script, run: B, on: {ok: done, fail: failed}}
  side: {type: script, run: A, on: {ok: done, fail: failed}}
`

func pair(a, b string) string { return strings.NewReplacer("A", a, "B", b).Replace(twoSteps) }

func TestStartFromThePage(t *testing.T) {
	ok := arg("exit", "0")
	w := newWorld(t, pair(ok, ok))
	w.write("other.yaml", "version: 0\nflow: other\nstart: x\nnodes:\n  x: {type: script, run: "+arg("input")+", on: {ok: done, fail: failed}}\n")
	w.write("missing.yaml", "version: 0\nflow: missing\nstart: x\nnodes:\n  x: {type: script, run: [./no-such-program], on: {ok: done, fail: failed}}\n")
	w.write("notes.yaml", "just: a list\n")

	var flows []watchtower.FlowFile
	w.get("/api/flows", &flows)
	var names []string
	for _, f := range flows {
		names = append(names, f.File)
	}
	if strings.Join(names, ",") != "orders.yaml,missing.yaml,other.yaml" || flows[0].Language == nil || flows[0].Language.Code != "uk" {
		t.Fatalf("flows %+v", flows)
	}

	// Invalid input: nothing starts.
	for _, body := range []string{`{"input": [1, 2]}`, `{"input": "text"}`, `{"input": null, "extra": 1}`, `{"input": 5}`} {
		var v any
		json.Unmarshal([]byte(body), &v)
		if code, res := w.post("/api/flows/orders.yaml/start", v, pageHeaders); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %v", body, code, res)
		}
	}
	// Orders whose program is missing: the problems instead of a start.
	code, res := w.post("/api/flows/missing.yaml/start", map[string]any{}, pageHeaders)
	if code != http.StatusUnprocessableEntity || !strings.Contains(res["error"].(string), "no-such-program") {
		t.Fatalf("missing program: %d %v", code, res)
	}
	// Not an orders file of this folder.
	for _, name := range []string{"notes.yaml", "..%2Forders.yaml", "nothing.yaml"} {
		if code, _ := w.post("/api/flows/"+name+"/start", map[string]any{}, pageHeaders); code != http.StatusNotFound {
			t.Fatalf("%s: %d", name, code)
		}
	}
	if n := w.campaigns(); n != 0 {
		t.Fatalf("%d campaigns after refused starts", n)
	}

	code, res = w.post("/api/flows/orders.yaml/start", map[string]any{"input": map[string]any{"ticket": "T-1"}}, pageHeaders)
	if code != http.StatusOK || res["id"].(float64) != 1 || res["result"] != watchtower.ResumeDriving {
		t.Fatalf("start: %d %v", code, res)
	}
	c := w.waitState(1, watchtower.StateDone)
	events, _ := w.chron.Events(1)
	if string(events[0].Input) != `{"ticket":"T-1"}` || events[0].Language != "uk" || c.Language.Code != "uk" {
		t.Fatalf("started with %s %q", events[0].Input, events[0].Language)
	}

	// Another orders file of the folder runs by its own orders.
	if code, res := w.post("/api/flows/other.yaml/start", map[string]any{}, pageHeaders); code != http.StatusOK || res["id"].(float64) != 2 {
		t.Fatalf("start other: %d %v", code, res)
	}
	c = w.waitState(2, watchtower.StateDone)
	if c.Flow != "other" || len(c.Columns) != 1 || c.Columns[0].Node != "x" || c.Nodes["x"][0].Status != watchtower.StatusPassed {
		t.Fatalf("other campaign %+v", c.Summary)
	}
}

func TestRetryAndRewindOverHTTP(t *testing.T) {
	ok := arg("exit", "0")
	w := newWorld(t, pair(ok, arg("exit", "1")))
	w.post("/api/flows/orders.yaml/start", map[string]any{}, pageHeaders)
	c := w.waitState(1, watchtower.StateFailed)
	if c.Retry != "b" || strings.Join(c.Rewind, ",") != "a,b" {
		t.Fatalf("retry %q, rewind %v", c.Retry, c.Rewind)
	}
	// Nothing to rewind to that the campaign did not visit.
	code, res := w.post("/api/campaigns/1/rewind?lang=uk", map[string]any{"node": "side"}, pageHeaders)
	if code != http.StatusConflict || !strings.Contains(res["error"].(string), "ніколи не був на side") {
		t.Fatalf("rewind to side: %d %v", code, res)
	}
	// The cause is fixed, then the failed step is retried.
	w.write("orders.yaml", pair(ok, ok))
	code, res = w.post("/api/campaigns/1/retry", map[string]any{"by": "ann"}, pageHeaders)
	if code != http.StatusOK || res["node"] != "b" {
		t.Fatalf("retry: %d %v", code, res)
	}
	c = w.waitState(1, watchtower.StateDone)
	if len(c.Nodes["b"]) != 2 || c.Nodes["b"][0].Status != watchtower.StatusFailed || len(c.Returns) != 1 ||
		c.Returns[0].By != "ann" || c.Returns[0].Via != "page" || c.Retry != "" {
		t.Fatalf("after retry: %+v %+v", c.Nodes["b"], c.Returns)
	}
	if code, _ := w.post("/api/campaigns/1/retry", map[string]any{}, pageHeaders); code != http.StatusConflict {
		t.Fatalf("retry of a done campaign: %d", code)
	}
	// Rewind to the start: everything runs again, history stays.
	if code, res := w.post("/api/campaigns/1/rewind", map[string]any{"node": "a"}, pageHeaders); code != http.StatusOK {
		t.Fatalf("rewind: %d %v", code, res)
	}
	c = w.waitState(1, watchtower.StateDone)
	if len(c.Nodes["a"]) != 2 || len(c.Nodes["b"]) != 3 || len(c.Returns) != 2 || c.Returns[1].Kind != chronicle.Rewound {
		t.Fatalf("after rewind: a %d, b %d, returns %+v", len(c.Nodes["a"]), len(c.Nodes["b"]), c.Returns)
	}
	work := filepath.Join(w.dir, ".hetman", "campaigns", "1", "work")
	if got := strings.Join(teststep.Trace(work), " "); got != "a#1 b#1 b#2 a#2 b#3" {
		t.Fatalf("trace %s", got)
	}
	var words bool
	for _, s := range c.Words {
		for _, l := range s.Lines {
			words = words || strings.Contains(l, "sent the campaign back to a")
		}
	}
	if !words {
		t.Fatalf("words %+v", c.Words)
	}
	// Sent back to a by another process, not driven yet: a waits, b will
	// run again.
	o, _ := orders.Load(w.orders)
	release, err := engine.Rewind(o, w.chron, 1, "a", "", engine.ViaCommand)
	if err != nil {
		t.Fatal(err)
	}
	release()
	c = w.campaign(1)
	if a, b := find(c.Columns, "a"), find(c.Columns, "b"); !a.Current || a.Status != watchtower.StatusWaiting || a.Again || !b.Again {
		t.Fatalf("after an undriven rewind: a %+v, b %+v", a, b)
	}
}

func TestRetryAfterExhaustedAttemptsOverHTTP(t *testing.T) {
	w := newWorld(t, placeholder)
	plan := filepath.Join(w.dir, "plan.json")
	os.WriteFile(plan, []byte(`{"nodes": {"a": [[{"exit": 3}], [{"exit": 3}], [{"result": {"outcome": "ok"}}]]}}`), 0o644)
	os.WriteFile(filepath.Join(w.dir, "p.md"), []byte("Do it."), 0o644)
	os.WriteFile(filepath.Join(w.dir, "s.json"), []byte(`{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}, "required": ["outcome"]}`), 0o644)
	w.write("orders.yaml", `version: 0
flow: agent
start: a
executors: {stub: {run: `+teststep.Adapter(plan)+`}}
nodes:
  a: {type: agent, executor: stub, prompt: p.md, schema: s.json, max_attempts: 2, on: {ok: done, fail: failed}}
`)
	w.post("/api/flows/orders.yaml/start", map[string]any{}, pageHeaders)
	c := w.waitState(1, watchtower.StateFailed)
	if c.Retry != "a" || len(c.Nodes["a"]) != 2 {
		t.Fatalf("retry %q, attempts %d", c.Retry, len(c.Nodes["a"]))
	}
	if code, res := w.post("/api/campaigns/1/retry", map[string]any{}, pageHeaders); code != http.StatusOK {
		t.Fatalf("retry: %d %v", code, res)
	}
	c = w.waitState(1, watchtower.StateDone)
	if a := c.Nodes["a"]; len(a) != 3 || a[2].N != 3 || a[2].Outcome != "ok" || a[1].Outcome != "fail" {
		t.Fatalf("attempts %+v", a)
	}
}

func TestRetryAndRewindRefusedWhileDriven(t *testing.T) {
	w := newWorld(t, pair(arg("block", "SYNC1"), arg("exit", "0")))
	w.post("/api/flows/orders.yaml/start", map[string]any{}, pageHeaders)
	w.waitFile(filepath.Join(w.sync(1), "started.a.1"))
	before := w.eventCount()
	code, res := w.post("/api/campaigns/1/rewind", map[string]any{"node": "a"}, pageHeaders)
	if code != http.StatusConflict || !strings.Contains(res["error"].(string), "halt it first") {
		t.Fatalf("rewind while driven: %d %v", code, res)
	}
	if code, _ := w.post("/api/campaigns/1/retry", map[string]any{}, pageHeaders); code != http.StatusConflict {
		t.Fatalf("retry while driven: %d", code)
	}
	if n := w.eventCount(); n != before {
		t.Fatalf("refusals recorded %d events", n-before)
	}
	w.release(1)
	w.waitState(1, watchtower.StateDone)
}

const editable = `# A flow the page edits.
version: 0
flow: edit
start: build
executors:
  stub: {run: [stub-adapter]}
checks:
  lint: {run: LINT, soft: true}
  tests: {run: LINT}
nodes:
  build: {type: script, run: RUN, on: {ok: ready, fail: failed}}
  ready: {type: gate, checks: [lint], on: {pass: write, blocked: build}}
  write: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: decide, fail: failed}}
  decide: {type: human, on: {ship: done, stop: failed}}
`

func editWorld(t *testing.T) *world {
	ok := arg("exit", "0")
	w := newWorld(t, placeholder)
	w.write("p.md", "Write it. {{artifact.build.out.txt}}")
	w.write("s.json", `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}, "required": ["outcome"]}`)
	w.write("orders.yaml", strings.NewReplacer("LINT", ok, "RUN", ok).Replace(editable))
	return w
}

// change previews an operation, then writes it with the preview's base.
func (w *world) change(op map[string]any, runLine string) (watchtower.Change, int, map[string]any) {
	w.t.Helper()
	code, res := w.post("/api/flows/orders.yaml/preview", map[string]any{"op": op, "runLine": runLine}, pageHeaders)
	if code != http.StatusOK {
		w.t.Fatalf("preview %v: %d %v", op, code, res)
	}
	var c watchtower.Change
	data, _ := json.Marshal(res)
	json.Unmarshal(data, &c)
	code, res = w.post("/api/flows/orders.yaml/apply", map[string]any{"op": c.Op, "base": c.Base}, pageHeaders)
	return c, code, res
}

func TestFlowEditOverHTTP(t *testing.T) {
	w := editWorld(t)
	var fl watchtower.Flow
	if code := w.get("/api/flows/orders.yaml", &fl); code != http.StatusOK || len(fl.Nodes) != 4 || len(fl.Checks) != 2 ||
		strings.Join(fl.Executors, ",") != "stub" || len(fl.Columns) == 0 || fl.Base == "" {
		t.Fatalf("flow %d %+v", code, fl)
	}
	steps := []struct {
		op      map[string]any
		run     string
		words   string
		applied func(o *orders.Orders) bool
	}{
		{map[string]any{"op": "add", "type": "script", "node": "prepare", "after": "build"}, "prepare.sh --fast",
			"Add the script node prepare after build", func(o *orders.Orders) bool {
				return o.Nodes["build"].On["ok"] == "prepare" && strings.Join(o.Nodes["prepare"].Run, " ") == "prepare.sh --fast"
			}},
		{map[string]any{"op": "add", "type": "script", "node": "fetch", "before": "build"}, "fetch.sh",
			"Add the script node fetch before build", func(o *orders.Orders) bool { return o.Start == "fetch" }},
		{map[string]any{"op": "add", "type": "gate", "node": "checked", "after": "prepare", "checks": []string{"tests"}}, "",
			"Add the gate node checked after prepare", func(o *orders.Orders) bool { return o.Nodes["prepare"].On["ok"] == "checked" }},
		{map[string]any{"op": "remove", "node": "fetch"}, "", "Remove the node fetch", func(o *orders.Orders) bool { return o.Nodes["fetch"] == nil && o.Start == "build" }},
		{map[string]any{"op": "rename", "node": "build", "name": "compile"}, "", "Rename the node build to compile", func(o *orders.Orders) bool { return o.Nodes["compile"] != nil }},
		{map[string]any{"op": "link", "node": "ready", "outcome": "blocked", "target": "failed"}, "", "Make the outcome blocked of ready lead to failed",
			func(o *orders.Orders) bool { return o.Nodes["ready"].On["blocked"] == "failed" }},
		{map[string]any{"op": "set", "node": "compile", "field": "timeout", "value": "5m"}, "", "Set timeout of compile to 5m",
			func(o *orders.Orders) bool { return o.Nodes["compile"].Timeout.String() == "5m0s" }},
		{map[string]any{"op": "set", "node": "write", "field": "model", "value": "m2"}, "", "Set model of write to m2",
			func(o *orders.Orders) bool { return o.Nodes["write"].Model == "m2" }},
		{map[string]any{"op": "ask", "node": "ready", "id": "docs", "question": "Docs updated?"}, "", "Add the yesno question docs to ready",
			func(o *orders.Orders) bool { return len(o.Nodes["ready"].Ask) == 1 }},
		{map[string]any{"op": "gate-add", "node": "ready", "check": "tests"}, "", "Add the check tests to the gate ready",
			func(o *orders.Orders) bool { return strings.Join(o.Nodes["ready"].Checks, ",") == "lint,tests" }},
		{map[string]any{"op": "gate-remove", "node": "ready", "check": "lint"}, "", "Remove the check lint from the gate ready",
			func(o *orders.Orders) bool { return strings.Join(o.Nodes["ready"].Checks, ",") == "tests" }},
		{map[string]any{"op": "check", "name": "tests", "soft": true}, "", "Declare or change the check tests",
			func(o *orders.Orders) bool { return o.Checks["tests"].Soft }},
		{map[string]any{"op": "unset", "node": "compile", "field": "timeout"}, "", "Remove timeout from compile",
			func(o *orders.Orders) bool { return o.Nodes["compile"].Timeout == 0 }},
	}
	for _, s := range steps {
		c, code, res := w.change(s.op, s.run)
		if code != http.StatusOK || res["result"] != watchtower.ApplyWritten {
			t.Fatalf("%v: %d %v\n%+v", s.op, code, res, c)
		}
		if !strings.HasPrefix(c.Words, s.words) || c.Diff == "" || !c.Changed {
			t.Fatalf("%v: words %q, diff %q", s.op, c.Words, c.Diff)
		}
		o, err := orders.Load(w.orders)
		if err != nil || !s.applied(o) {
			t.Fatalf("%v not applied: %v\n%s", s.op, err, w.read("orders.yaml"))
		}
	}
	if text := w.read("orders.yaml"); !strings.HasPrefix(text, "# A flow the page edits.\n") {
		t.Fatalf("the comment is gone:\n%s", text)
	}
	if p := w.read("p.md"); p != "Write it. {{artifact.compile.out.txt}}" {
		t.Fatalf("rename did not change the template: %q", p)
	}
}

func TestRefusedEditsWriteNothing(t *testing.T) {
	w := editWorld(t)
	before := w.read("orders.yaml")
	// An operation that cannot be made.
	c, code, _ := w.change(map[string]any{"op": "link", "node": "build", "outcome": "ok", "target": "nowhere"}, "")
	if code != http.StatusBadRequest || c.Error == "" {
		t.Fatalf("bad link: %d %+v", code, c)
	}
	// A result that does not pass the orders check: a required transition removed.
	c, code, _ = w.change(map[string]any{"op": "unlink", "node": "build", "outcome": "fail"}, "")
	if code != http.StatusUnprocessableEntity || len(c.Problems) == 0 || c.Diff == "" {
		t.Fatalf("unlink: %d %+v", code, c)
	}
	// A starter file outside the folder, or in its .hetman directory.
	for _, prompt := range []string{"../outside.md", ".hetman/inside.md"} {
		c, code, _ = w.change(map[string]any{"op": "add", "type": "agent", "node": "extra", "after": "write", "executor": "stub",
			"prompt": prompt, "starters": true}, "")
		if code != http.StatusForbidden || c.Refused == "" {
			t.Fatalf("starter %s: %d %+v", prompt, code, c)
		}
	}
	// The page never starts a new orders file.
	if c, code, _ = w.change(map[string]any{"op": "new", "type": "script", "node": "x", "run": []string{"x"}}, ""); code != http.StatusBadRequest {
		t.Fatalf("new: %d %+v", code, c)
	}
	// A stale preview: the file changed after it was shown.
	code, res := w.post("/api/flows/orders.yaml/apply", map[string]any{"op": map[string]any{"op": "set", "node": "build", "field": "timeout", "value": "1m"},
		"base": "0000"}, pageHeaders)
	if code != http.StatusConflict || res["stale"] != "1" {
		t.Fatalf("stale: %d %v", code, res)
	}
	if after := w.read("orders.yaml"); after != before {
		t.Fatalf("a refused change was written:\n%s", after)
	}
	for _, p := range []string{filepath.Join(filepath.Dir(w.dir), "outside.md"), filepath.Join(w.dir, ".hetman", "inside.md"),
		filepath.Join(w.dir, "schemas", "extra.json")} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s was written", p)
		}
	}
	// Starter files inside the folder are written, and the change says so
	// while a campaign of these orders has not ended.
	id, _ := w.chron.NewCampaign("edit", w.orders, "build", nil, "")
	c, code, res = w.change(map[string]any{"op": "add", "type": "agent", "node": "extra", "after": "write", "executor": "stub", "starters": true}, "")
	if code != http.StatusOK || len(c.Active) != 1 || c.Active[0] != id {
		t.Fatalf("starters: %d %v %+v", code, res, c)
	}
	for _, rel := range []string{"prompts/extra.md", "schemas/extra.json"} {
		if _, err := os.Stat(filepath.Join(w.dir, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSentenceOverHTTP(t *testing.T) {
	w := editWorld(t)
	plan := filepath.Join(w.dir, "plan.json")
	text := strings.Replace(w.read("orders.yaml"), "executors:\n  stub: {run: [stub-adapter]}",
		"scribe: {executor: stub, model: m1}\nexecutors:\n  stub: {run: "+teststep.Adapter(plan)+"}", 1)
	w.write("orders.yaml", text)
	const addGate = `{"outcome": "operation", "operation": {"op": "add", "type": "gate", "node": "lint_gate", "after": "build", "checks": ["lint"]}}`
	const unclear = `{"outcome": "unclear", "choices": [
		{"question": "Do you mean a time limit of 1m on build?", "operation": {"op": "set", "node": "build", "field": "timeout", "value": "1m"}},
		{"question": "Do you mean where the latest campaign is?", "query": {"kind": "where"}}]}`
	os.WriteFile(plan, []byte(`{"trace": "trace.jsonl", "when": [
		{"prompt": "lint gate after build", "moves": [[{"result": `+addGate+`}]]},
		{"prompt": "make it stricter", "moves": [[{"result": `+unclear+`}]]},
		{"prompt": "speed it up", "moves": [[{"result": {"outcome": "operation"}}], [{"result": {"outcome": "operation"}}]]}]}`), 0o644)
	requests := func() int {
		data, _ := os.ReadFile(filepath.Join(w.dir, "trace.jsonl"))
		return strings.Count(string(data), "\n")
	}
	// Cross-site: refused before any model is asked.
	if code, _ := w.post("/api/flows/orders.yaml/sentence", map[string]any{"sentence": "lint gate after build"},
		map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site", watchtower.ControlHeader: "1"}); code != http.StatusForbidden || requests() != 0 {
		t.Fatalf("cross-site sentence: %d, %d requests", code, requests())
	}
	before := w.read("orders.yaml")
	// Clear: one operation shown, nothing written until confirmed.
	code, res := w.post("/api/flows/orders.yaml/sentence", map[string]any{"sentence": "lint gate after build"}, pageHeaders)
	var reply watchtower.SentenceReply
	data, _ := json.Marshal(res)
	json.Unmarshal(data, &reply)
	if code != http.StatusOK || reply.Kind != "operation" || reply.Change == nil || !strings.Contains(reply.Change.Diff, "+  lint_gate:") ||
		!strings.HasPrefix(reply.Change.Words, "Add the gate node lint_gate after build") || w.read("orders.yaml") != before || requests() != 1 {
		t.Fatalf("clear: %d %s", code, data)
	}
	if code, res := w.post("/api/flows/orders.yaml/apply", map[string]any{"op": reply.Change.Op, "base": reply.Change.Base}, pageHeaders); code != http.StatusOK {
		t.Fatalf("apply: %d %v", code, res)
	}
	if o, err := orders.Load(w.orders); err != nil || o.Nodes["build"].On["ok"] != "lint_gate" {
		t.Fatalf("not applied: %v", err)
	}
	// Unclear: the interpretations to choose from.
	code, res = w.post("/api/flows/orders.yaml/sentence?lang=uk", map[string]any{"sentence": "make it stricter"}, pageHeaders)
	data, _ = json.Marshal(res)
	reply = watchtower.SentenceReply{}
	json.Unmarshal(data, &reply)
	if code != http.StatusOK || reply.Kind != "unclear" || len(reply.Choices) != 2 || reply.Choices[0].Op == nil ||
		reply.Choices[0].Op.Op != edit.OpSet || !strings.Contains(reply.Choices[0].Words, "timeout") || reply.Choices[1].Text == "" {
		t.Fatalf("unclear: %d %s", code, data)
	}
	// Invalid twice: nothing changes.
	before = w.read("orders.yaml")
	code, res = w.post("/api/flows/orders.yaml/sentence", map[string]any{"sentence": "speed it up"}, pageHeaders)
	if code != http.StatusUnprocessableEntity || res["invalid"] != true || w.read("orders.yaml") != before || requests() != 4 {
		t.Fatalf("invalid: %d %v, %d requests", code, res, requests())
	}
	// Orders without a Scribe setting: no model is asked.
	w.write("orders.yaml", strings.Replace(w.read("orders.yaml"), "scribe: {executor: stub, model: m1}\n", "", 1))
	if code, _ := w.post("/api/flows/orders.yaml/sentence", map[string]any{"sentence": "lint gate after build"}, pageHeaders); code != http.StatusUnprocessableEntity || requests() != 4 {
		t.Fatalf("without scribe: %d", code)
	}
}

func TestControlRoutesRefuseCrossSite(t *testing.T) {
	w := editWorld(t)
	w.chron.NewCampaign("edit", w.orders, "build", nil, "")
	before, events := w.read("orders.yaml"), w.eventCount()
	var fl watchtower.Flow
	w.get("/api/flows/orders.yaml", &fl)
	op := map[string]any{"op": "set", "node": "build", "field": "timeout", "value": "1m"}
	routes := map[string]any{
		"/api/flows/orders.yaml/start":    map[string]any{},
		"/api/flows/orders.yaml/preview":  map[string]any{"op": op},
		"/api/flows/orders.yaml/apply":    map[string]any{"op": op, "base": fl.Base},
		"/api/flows/orders.yaml/sentence": map[string]any{"sentence": "x"},
		"/api/campaigns/1/retry":          map[string]any{},
		"/api/campaigns/1/rewind":         map[string]any{"node": "build"},
	}
	for path, body := range routes {
		for name, hd := range map[string]map[string]string{
			"cross-site form":            {"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site", "Content-Type": "text/plain"},
			"cross-site with header":     {"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site", watchtower.ControlHeader: "1"},
			"foreign origin, no headers": {"Origin": "http://evil.example", watchtower.ControlHeader: "1"},
			"no control header":          {"Sec-Fetch-Site": "same-origin"},
		} {
			if code, _ := w.post(path, body, hd); code != http.StatusForbidden {
				t.Errorf("%s, %s: status %d, want 403", path, name, code)
			}
		}
	}
	if w.read("orders.yaml") != before || w.eventCount() != events || w.campaigns() != 1 {
		t.Fatal("a refused request changed something")
	}
}

// placeholder is orders a world starts with before a test writes its own.
const placeholder = "version: 0\nstart: a\nnodes:\n  a: {type: script, run: [x], on: {ok: done, fail: failed}}\n"
