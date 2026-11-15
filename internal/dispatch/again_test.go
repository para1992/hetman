package dispatch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/dispatch"
	"hetman/internal/engine"
	"hetman/internal/orders"
)

// A task whose campaign failed follows the campaign when a person retries or
// rewinds it: the kept outcome no longer holds once the campaign goes on.
func TestTaskFollowsRetriedAndRewoundCampaign(t *testing.T) {
	w := newWorld(t)
	w.flow("fix.yaml", "a", "  a:\n    type: script\n    run: "+arg("exit", "1")+"\n    on: {ok: done, fail: failed}\n")
	w.flow("other.yaml", "a", quick("exit", "1"))
	w.write(dispatch.DefaultRoutesFile, "version: 0\nroutes:\n  finding: {orders: fix.yaml}\n")
	e := w.open()
	task := e.add("finding", `{}`, "")
	e.runOnce()
	if v := e.views()[task.ID]; v.State != dispatch.StateFailed {
		t.Fatalf("after the first run: %+v", v)
	}
	if rec, _ := e.queue.Get(task.ID); rec.State != dispatch.Failed {
		t.Fatalf("recorded %s", rec.State)
	}

	// Retried and not driven yet: running again, not failed.
	fix := filepath.Join(w.dir, "fix.yaml")
	o, err := orders.Load(fix)
	if err != nil {
		t.Fatal(err)
	}
	release, node, err := engine.Retry(o, e.chron, 1, "", engine.ViaCommand)
	if err != nil || node != "a" {
		t.Fatalf("retry: %s %v", node, err)
	}
	release()
	if v := e.views()[task.ID]; v.State != dispatch.StateRunning || v.Node != "a" {
		t.Fatalf("after the retry: %+v", v)
	}

	// The cause is fixed; a rewind from the command line, given another
	// orders file of the folder, drives the campaign by its own orders.
	text, _ := os.ReadFile(fix)
	os.WriteFile(fix, []byte(strings.Replace(string(text), "'exit', '1'", "'exit', '0'", 1)), 0o644)
	if out := w.hetmanF(0, filepath.Join(w.dir, "other.yaml"), "rewind", "-c", "1", "-to", "a"); !strings.Contains(out, "campaign done") {
		t.Fatalf("rewind said:\n%s", out)
	}
	if v := e.views()[task.ID]; v.State != dispatch.StateDone || v.Ended == nil {
		t.Fatalf("after the rewind: %+v", v)
	}
}
