package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"hetman/internal/chronicle"
)

// waitGlob waits until a file matching a pattern exists.
func waitGlob(t *testing.T, pattern string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m, _ := filepath.Glob(pattern); len(m) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", pattern)
}

// itemStates names the state of each item of the latest attempt of a node.
func (f *fixture) itemStates(node string) map[string]string {
	f.t.Helper()
	out := map[string]string{}
	for _, c := range f.children(node) {
		switch {
		case c.Queued:
			out[c.Item] = "queued"
		case c.ID == 0:
			out[c.Item] = "pending"
		case c.State.Error != "":
			out[c.Item] = "error"
		case c.State.End != "":
			out[c.Item] = c.State.End
		default:
			out[c.Item] = "open"
		}
	}
	return out
}

// A retry of a for-each whose items all stopped in the error state retries
// the first item and leaves the others to the running step, which runs
// each again when its turn comes: they wait, they have not failed, and they
// do not hold the campaign up.
func TestRetriedForEachShowsTheOtherErroredItemsAsQueued(t *testing.T) {
	items := `[{"id":"a","badonce":true,"wait":true},{"id":"b","badonce":true},{"id":"c","badonce":true},{"id":"d","needs":["b"]}]`
	f, sync := setupFiles(t, forEachOrders(items, 1, ""), map[string]string{"segments/item.yaml": segmentWith(arg("item", "SYNC"))})
	f.drive(Errored)
	// Stopped in the error state: every errored item has failed for now.
	if got := f.itemStates("each"); !reflect.DeepEqual(got, map[string]string{"a": "error", "b": "error", "c": "error", "d": "pending"}) {
		t.Fatalf("before the retry %v", got)
	}
	if ws, _ := Waiters(f.chron, f.events("")); len(ws) != 3 {
		t.Fatalf("waiters before the retry %+v", ws)
	}
	if node := f.retry(); node != "work" {
		t.Fatalf("retry ran %q again", node)
	}
	queued := map[string]string{"a": "open", "b": "queued", "c": "queued", "d": "pending"}
	if got := f.itemStates("each"); !reflect.DeepEqual(got, queued) {
		t.Fatalf("after the retry %v", got)
	}
	done := f.driveAsync(context.Background())
	waitGlob(t, filepath.Join(sync, "started.a.*"))
	if got := f.itemStates("each"); !reflect.DeepEqual(got, queued) {
		t.Fatalf("while the first item runs %v", got)
	}
	if ws, _ := Waiters(f.chron, f.events("")); len(ws) != 0 {
		t.Fatalf("queued items hold the campaign up %+v", ws)
	}
	// A child that stopped in the error state after the resume has failed
	// in this run and is not queued.
	c := f.children("each")[2]
	if _, err := f.chron.Append(chronicle.Event{Campaign: c.ID, Kind: chronicle.Stopped, Node: c.State.Node,
		Details: chronicle.Details{Error: "stopped again"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.itemStates("each"); got["c"] != "error" || got["b"] != "queued" {
		t.Fatalf("an item stopped after the resume reads %v", got)
	}
	if ws, _ := Waiters(f.chron, f.events("")); len(ws) != 1 || ws[0].Campaign != c.ID {
		t.Fatalf("waiters after a new stop %+v", ws)
	}
	writeRelease(t, sync)
	f.wait(done, Done)
	if got := f.itemStates("each"); !reflect.DeepEqual(got, map[string]string{"a": "done", "b": "done", "c": "done", "d": "done"}) {
		t.Fatalf("at the end %v", got)
	}
}

// A retried segment retries its one child itself, so nothing is queued.
func TestRetriedSegmentChildIsNotQueued(t *testing.T) {
	f, _ := setupFiles(t, `version: 0
start: outer
nodes:
  outer: {type: segment, segment: a.yaml, on: {done: done, failed: failed}}
`, map[string]string{
		"a.yaml": "version: 0\nflow: a\nstart: leaf\nnodes:\n  leaf: {type: script, run: " + arg("badonce", "SYNC") + ", on: {ok: done, fail: failed}}\n",
	})
	f.drive(Errored)
	if c := f.children("outer"); len(c) != 1 || c[0].Queued || c[0].State.Error == "" {
		t.Fatalf("before the retry %+v", c)
	}
	f.retry()
	if c := f.children("outer"); len(c) != 1 || c[0].Queued || c[0].State.Error != "" {
		t.Fatalf("after the retry %+v", c)
	}
	f.drive(Done)
}
