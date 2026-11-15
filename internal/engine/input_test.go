package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"hetman/internal/adapter"
	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

const taskListOrders = `version: 0
workdir: .
flow: tasks
start: each
executors: {stub: {run: PLAN}}
input:
  tasks:
    kind: objects
    required: true
    fields:
      id: {kind: text, required: true}
      title: {kind: text, required: true}
nodes:
  each:
    type: foreach
    segment: segments/task.yaml
    items: input.tasks
    parallel: 2
    on: {done: done, failed: failed}
`

// The segment declares no input of its own: each item's fields come from
// the list of objects the parent declares.
const taskSegment = `version: 0
workdir: .
flow: task
start: work
executors: {stub: {run: PLAN}}
nodes:
  work: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: done, fail: failed}}
`

// A campaign whose recorded input lacks an optional field its prompts name,
// as a form once left an empty list out, stopped in the error state; once
// the orders declare the field, a retry runs the items with the field
// present and empty, without the tasks being given again.
func TestRetryFillsAnOptionalFieldTheStoredInputLacks(t *testing.T) {
	plan := `{"trace": "trace.jsonl", "nodes": {"work": [[{"result": {"outcome": "ok"}}]]}}`
	files := map[string]string{"segments/p.md": "Do {{input.title}} after [{{input.needs}}].", "segments/s.json": okSchema,
		"plan.json": plan, "segments/task.yaml": taskSegment}
	f := setupAgent(t, taskListOrders, files,
		`{"tasks":[{"id":"A","title":"first"},{"id":"B","title":"second"}]}`)
	f.drive(Errored)
	child := f.children("each")
	if len(child) == 0 {
		t.Fatalf("no child: %s", f.dump())
	}
	if in := string(child[0].Events[0].Input); strings.Contains(in, "needs") {
		t.Fatalf("child input %s", in)
	}
	stopped := filterKind(f.childEvents(child[0].ID), chronicle.Stopped)
	if len(stopped) != 1 || !strings.Contains(stopped[0].Error, `{{input.needs}}: the run input has no field "needs", and the input section of the orders does not declare it`) {
		t.Fatalf("child stopped %+v", stopped)
	}

	// The orders now declare the field; nothing else changes.
	path := f.e.Orders.Path
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(data), "      title: {kind: text, required: true}\n",
		"      title: {kind: text, required: true}\n      needs: {kind: list}\n", 1)), 0o644)
	o, err := orders.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	f.e.Orders = o
	release, _, err := Retry(o, f.chron, f.e.Campaign, "ann", ViaCommand)
	if err != nil {
		t.Fatal(err)
	}
	f.drive(Done)
	release()
	// The stub records the steps under the segment's working folder.
	trace, err := os.ReadFile(filepath.Join(o.Dir, "segments", "trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	for _, line := range strings.Split(strings.TrimSpace(string(trace)), "\n") {
		var step adapter.Step
		if err := json.Unmarshal([]byte(line), &step); err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(step.Prompt, "\n")
		prompts = append(prompts, first)
	}
	sort.Strings(prompts)
	if got := strings.Join(prompts, "|"); got != "Do first after [[]].|Do second after [[]]." {
		t.Fatalf("prompts %q", got)
	}
}
