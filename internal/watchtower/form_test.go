package watchtower_test

import (
	"net/http"
	"strings"
	"testing"

	"hetman/internal/watchtower"
)

// Orders with an input section give the page a form in the page's
// language; a start is checked with the same code as hetman run, and the
// campaign gets the input with the defaults filled in.
func TestStartFormFromTheInputSection(t *testing.T) {
	w := newWorld(t, `version: 0
workdir: .
flow: tasks
start: a
input:
  project: {kind: text, default: demo, ask: false}
  tasks:
    kind: objects
    label: {en: Tasks, uk: Завдання}
    required: true
    fields:
      id: {kind: text, label: Task id, hint: {en: such as T-1, uk: наприклад T-1}, required: true}
      criteria: {kind: list, required: true}
      needs: {kind: list}
      description: {kind: longtext}
  size: {kind: choice, options: [small, large], default: small}
  note: {kind: text}
nodes:
  a: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	w.write("plain.yaml", "version: 0\nworkdir: .\nflow: plain\nstart: x\nnodes:\n  x: {type: script, run: "+arg("exit", "0")+", on: {ok: done, fail: failed}}\n")

	var flows []watchtower.FlowFile
	w.get("/api/flows?lang=uk", &flows)
	if len(flows) != 2 || flows[1].Input != nil {
		t.Fatalf("flows %+v", flows)
	}
	in := flows[0].Input
	if len(in) != 3 || in[0].Name != "tasks" || in[0].Label != "Завдання" || !in[0].Required || len(in[0].Fields) != 4 ||
		in[0].Fields[0].Hint != "наприклад T-1" || in[1].Default != "small" || strings.Join(in[1].Options, ",") != "small,large" {
		t.Fatalf("form %+v", in)
	}

	// An input that does not fit: the fields, in the page's language, and
	// nothing starts.
	code, res := w.post("/api/flows/orders.yaml/start?lang=uk", map[string]any{"input": map[string]any{
		"tasks": []any{map[string]any{"id": "T-1"}}, "size": "huge"}}, pageHeaders)
	fields, _ := res["fields"].([]any)
	if code != http.StatusUnprocessableEntity || len(fields) != 2 || fields[0] != "tasks[1].criteria — обов'язкове поле" {
		t.Fatalf("refused: %d %v", code, res)
	}
	// Each problem is also placed at its field, named by the labels of the
	// form, so the page can show it beside the field.
	at, _ := res["at"].([]any)
	if len(at) != 2 {
		t.Fatalf("at %v", res["at"])
	}
	first := at[0].(map[string]any)
	if first["path"] != "tasks[1].criteria" || first["text"] != "Завдання 1 › criteria — обов'язкове поле" || first["short"] != "criteria — обов'язкове поле" {
		t.Fatalf("first %v", first)
	}
	if second := at[1].(map[string]any); second["path"] != "size" || !strings.Contains(second["text"].(string), "size") {
		t.Fatalf("second %v", second)
	}
	if code, res := w.post("/api/flows/orders.yaml/start", map[string]any{}, pageHeaders); code != http.StatusUnprocessableEntity ||
		!strings.Contains(res["error"].(string), "tasks is required") {
		t.Fatalf("no input: %d %v", code, res)
	}
	if n := w.campaigns(); n != 0 {
		t.Fatalf("%d campaigns after refused starts", n)
	}

	code, res = w.post("/api/flows/orders.yaml/start", map[string]any{"input": map[string]any{
		"tasks": []any{map[string]any{"criteria": []any{"works"}, "id": "T-1"}}}}, pageHeaders)
	if code != http.StatusOK {
		t.Fatalf("start: %d %v", code, res)
	}
	w.waitState(1, watchtower.StateDone)
	events, _ := w.chron.Events(1)
	// The optional fields the form left empty are there with their empty
	// values, so a prompt that names one does not fail.
	if got := string(events[0].Input); got != `{"project":"demo","tasks":[{"id":"T-1","criteria":["works"],"needs":[],"description":""}],"size":"small","note":""}` {
		t.Fatalf("input %s", got)
	}
	// The cross-site protection holds for this start as for every other.
	if code, _ := w.post("/api/flows/orders.yaml/start", map[string]any{}, nil); code != http.StatusForbidden {
		t.Fatalf("start without the control header: %d", code)
	}
	// Orders without the section start with any input, as before.
	if code, res := w.post("/api/flows/plain.yaml/start", map[string]any{"input": map[string]any{"anything": 1}}, pageHeaders); code != http.StatusOK {
		t.Fatalf("plain: %d %v", code, res)
	}
}
