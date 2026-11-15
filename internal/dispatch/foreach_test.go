package dispatch_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"hetman/internal/dispatch"
)

// A task whose campaign runs a for-each fails on one item; a person retries
// the campaign, which runs only the items that did not reach done, and the
// task ends done.
func TestTaskWithAForEachRetriedEndsDone(t *testing.T) {
	w := newWorld(t)
	items := strings.ReplaceAll(`[{"id":"a"},{"id":"b","failonce":true},{"id":"c","needs":["b"]}]`, "'", "''")
	w.flow("build.yaml", "plan", "  plan: {type: script, run: "+arg("write", "items.json", items)+", on: {ok: each, fail: failed}}\n"+
		"  each: {type: foreach, segment: item.yaml, items: artifact.plan.items.json, parallel: 2, on: {done: done, failed: failed}}\n")
	w.write("item.yaml", "version: 0\nflow: item\nstart: work\nnodes:\n  work: {type: script, run: "+arg("item", "SYNC")+", on: {ok: done, fail: failed}}\n")
	w.write(dispatch.DefaultRoutesFile, "version: 0\nroutes:\n  build: {orders: build.yaml}\n")
	e := w.open()
	task := e.add("build", `{}`, "")
	e.runOnce()
	if v := e.views()[task.ID]; v.State != dispatch.StateFailed {
		t.Fatalf("after the first run: %+v", v)
	}
	rec, _ := e.queue.Get(task.ID)
	out := w.hetmanF(0, filepath.Join(w.dir, "build.yaml"), "retry", "-c", strconv.FormatInt(rec.Campaign, 10))
	if !strings.Contains(out, "item a is kept from attempt 1") || !strings.Contains(out, "campaign done") {
		t.Fatalf("retry said:\n%s", out)
	}
	if v := e.views()[task.ID]; v.State != dispatch.StateDone {
		t.Fatalf("after the retry: %+v", v)
	}
	for id, want := range map[string]int{"a": 1, "b": 2, "c": 1} {
		runs, _ := filepath.Glob(filepath.Join(w.sync, "started."+id+".*"))
		if len(runs) != want {
			t.Fatalf("item %s ran in %d campaign(s), want %d: %v", id, len(runs), want, runs)
		}
	}
}
