package watchtower_test

import (
	"encoding/json"
	"strings"
	"testing"

	"hetman/internal/watchtower"
)

// Orders with none of the optional sections (no checks, executors, roles,
// input or language) give the page lists, never null: the flow view maps
// over every one of them.
func TestFlowAnswerHasNoNullLists(t *testing.T) {
	w := newWorld(t, pair(arg("exit", "0"), arg("exit", "0")))
	// The server reads the files of its folder on every request.
	w.write("part.yaml", "version: 0\nworkdir: .\nflow: part\nstart: x\nnodes:\n  x: {type: script, run: "+arg("exit", "0")+", on: {ok: done, fail: failed}}\n")
	w.write("orders.yaml", `version: 0
workdir: .
flow: plain
start: build-all
start_label: {en: New task, uk: Нове завдання}
nodes:
  build-all:
    type: foreach
    segment: part.yaml
    items: input.items
    on: {done: done, failed: failed}
`)

	for _, path := range []string{"/api/flows/orders.yaml", "/api/flows/part.yaml", "/api/flows"} {
		var raw json.RawMessage
		w.get(path, &raw)
		if strings.Contains(string(raw), "null") {
			t.Errorf("%s has null: %s", path, raw)
		}
	}
	var fl watchtower.Flow
	w.get("/api/flows/orders.yaml?lang=uk", &fl)
	if len(fl.Nodes) != 1 || fl.Nodes[0].ID != "build-all" || fl.StartLabel != "Нове завдання" {
		t.Fatalf("flow %+v", fl)
	}

	// A file the folder's other orders run as a segment is part of them,
	// and says so.
	var flows []watchtower.FlowFile
	w.get("/api/flows?lang=en", &flows)
	if len(flows) != 2 || flows[0].File != "orders.yaml" || flows[0].PartOf != "" || flows[0].StartLabel != "New task" ||
		flows[1].File != "part.yaml" || flows[1].PartOf != "orders.yaml" || flows[1].StartLabel != "" {
		t.Fatalf("flows %+v", flows)
	}
}

// A folder whose only orders file does not pass the check still answers
// with lists.
func TestFlowAnswerOfBrokenOrders(t *testing.T) {
	w := newWorld(t, pair(arg("exit", "0"), arg("exit", "0")))
	w.write("orders.yaml", "version: 0\nnodes: {}\n")
	var raw json.RawMessage
	w.get("/api/flows/orders.yaml", &raw)
	if strings.Contains(string(raw), "null") || !strings.Contains(string(raw), `"problem"`) {
		t.Fatalf("broken orders: %s", raw)
	}
}
