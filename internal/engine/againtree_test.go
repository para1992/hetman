package engine

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

func (f *fixture) childEvents(id int64) []chronicle.Event {
	f.t.Helper()
	ev, err := f.chron.Events(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return ev
}

func (f *fixture) retry() string {
	f.t.Helper()
	release, node, err := Retry(f.e.Orders, f.chron, f.e.Campaign, "tester", ViaCommand)
	if err != nil {
		f.t.Fatalf("retry: %v", err)
	}
	release()
	return node
}

func startsIn(events []chronicle.Event) []string {
	var out []string
	for _, e := range events {
		if e.Kind == chronicle.StepStarted {
			out = append(out, e.Node)
		}
	}
	return out
}

func TestRetryOfAForEachKeepsTheItemsThatWereDone(t *testing.T) {
	items := `[{"id":"a"},{"id":"b","failonce":true},{"id":"c","needs":["b"]},{"id":"d"}]`
	f, _ := setupFiles(t, forEachOrders(items, 2, ""), map[string]string{"segments/item.yaml": segmentWith(arg("item", "SYNC"))})
	f.drive(Failed)
	first := map[string]int64{}
	for _, c := range f.children("each") {
		first[c.Item] = c.ID
	}
	if node := f.retry(); node != "each" {
		t.Fatalf("retry runs %q again", node)
	}
	f.drive(Done)
	got := map[string]string{}
	for _, c := range f.children("each") {
		switch {
		case c.KeptFrom == 1 && c.ID == first[c.Item]:
			got[c.Item] = "kept"
		case c.ID != 0 && c.ID != first[c.Item] && c.Outcome == orders.Done:
			got[c.Item] = "new"
		default:
			got[c.Item] = "?"
		}
		if c.Attempt != 2 {
			t.Fatalf("item %s belongs to attempt %d", c.Item, c.Attempt)
		}
	}
	if want := map[string]string{"a": "kept", "b": "new", "c": "new", "d": "kept"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("items after the retry %v, want %v", got, want)
	}
	// The kept items ran once; the failed one keeps its first child, which
	// belongs to the earlier attempt and was not driven again.
	for _, id := range []string{"a", "d"} {
		if starts := startsIn(f.childEvents(first[id])); len(starts) != 1 {
			t.Fatalf("item %s ran %v", id, starts)
		}
	}
	earlier, err := EarlierChildren(f.chron, f.events(""), "each")
	if err != nil || len(earlier) != 1 || earlier[0].Item != "b" || earlier[0].Attempt != 1 || earlier[0].Outcome != orders.Failed {
		t.Fatalf("earlier children %+v %v", earlier, err)
	}
	if starts := startsIn(f.childEvents(first["b"])); len(starts) != 1 {
		t.Fatalf("the failed child of attempt 1 ran again: %v", starts)
	}
	list := f.children("each")
	if rest := AttachEarlier(list, earlier); len(rest) != 0 {
		t.Fatalf("unattached %+v", rest)
	}
	for _, c := range list {
		if n := len(c.Before); (c.Item == "b") != (n == 1) || (n == 1 && c.Before[0].ID != first["b"]) {
			t.Fatalf("item %s has earlier attempts %+v", c.Item, c.Before)
		}
	}
}

// Earlier children go under the latest child of their item, newest first;
// a segment's go under its one child; one whose item is gone stays apart.
func TestAttachEarlier(t *testing.T) {
	list := []Child{{Item: "a", ID: 9, Attempt: 3}, {Item: "b", ID: 0, Attempt: 3}}
	earlier := []Child{{Item: "a", ID: 1, Attempt: 1}, {Item: "gone", ID: 2, Attempt: 1}, {Item: "a", ID: 5, Attempt: 2}, {Item: "b", ID: 6, Attempt: 2}}
	rest := AttachEarlier(list, earlier)
	if len(rest) != 1 || rest[0].ID != 2 {
		t.Fatalf("rest %+v", rest)
	}
	if b := list[0].Before; len(b) != 2 || b[0].ID != 5 || b[1].ID != 1 {
		t.Fatalf("a before %+v", b)
	}
	if b := list[1].Before; len(b) != 1 || b[0].ID != 6 {
		t.Fatalf("b before %+v", b)
	}
	seg := []Child{{ID: 7, Attempt: 2}}
	if rest := AttachEarlier(seg, []Child{{ID: 3, Attempt: 1}}); len(rest) != 0 || len(seg[0].Before) != 1 {
		t.Fatalf("segment %+v %+v", seg, rest)
	}
}

func TestRetryOfASegmentRetriesItsChildAtTheFailedNode(t *testing.T) {
	f, sync := setupFiles(t, `version: 0
start: s
nodes:
  s: {type: segment, segment: seg.yaml, on: {done: done, failed: failed}}
`, map[string]string{"seg.yaml": `version: 0
flow: seg
start: x
nodes:
  x: {type: script, run: ` + arg("exit", "0") + `, on: {ok: y, fail: failed}}
  y: {type: script, run: ` + arg("failonce", "SYNC") + `, on: {ok: done, fail: failed}}
`})
	f.drive(Failed)
	child := f.events(chronicle.ChildStarted)[0].Child
	f.retry()
	f.drive(Done)
	started := f.events(chronicle.ChildStarted)
	if len(started) != 2 || started[1].Child != child || started[1].Outcome != chronicle.Retried || started[1].Attempt != 2 {
		t.Fatalf("children %+v", started)
	}
	if got := teststep.Trace(f.childWork(child)); !reflect.DeepEqual(got, []string{"x#1", "y#1", "y#2"}) {
		t.Fatalf("child trace %v (sync %s)", got, sync)
	}
}

func (f *fixture) childWork(id int64) string {
	return filepath.Join(CampaignDir(f.e.Orders, id), "work")
}

func TestRetryReachesTheChildThatStoppedInTheErrorState(t *testing.T) {
	f, _ := setupFiles(t, `version: 0
start: outer
nodes:
  outer: {type: segment, segment: a.yaml, on: {done: done, failed: failed}}
`, map[string]string{
		"a.yaml": "version: 0\nflow: a\nstart: mid\nnodes:\n  mid: {type: segment, segment: b.yaml, on: {done: done, failed: failed}}\n",
		"b.yaml": "version: 0\nflow: b\nstart: leaf\nnodes:\n  leaf: {type: script, run: " + arg("badonce", "SYNC") + ", on: {ok: done, fail: failed}}\n",
	})
	f.drive(Errored)
	node := f.retry()
	if node != "leaf" {
		t.Fatalf("retry ran %q again", node)
	}
	f.drive(Done)
	// The campaign kept its one attempt of outer; the deepest child ran
	// leaf twice.
	if starts := startsIn(f.events("")); !reflect.DeepEqual(starts, []string{"outer"}) {
		t.Fatalf("parent steps %v", starts)
	}
	mid := f.children("outer")[0]
	leaf, _ := ChildrenOf(f.chron, mid.Events, "mid")
	if starts := startsIn(leaf[0].Events); !reflect.DeepEqual(starts, []string{"leaf", "leaf"}) {
		t.Fatalf("leaf steps %v", starts)
	}
	if r := f.events(chronicle.Resumed); len(r) != 1 || r[0].Child != leaf[0].ID || r[0].By != "tester" {
		t.Fatalf("resumed events of the parent %+v", r)
	}
}

func TestRewindToAForEachRunsItAgainWithNewChildren(t *testing.T) {
	items := `[{"id":"a"},{"id":"b"}]`
	f, _ := setupFiles(t, forEachOrders(items, 2, ""), map[string]string{"segments/item.yaml": segmentWith(arg("item", "SYNC"))})
	f.drive(Done)
	var old []int64
	for _, c := range f.children("each") {
		old = append(old, c.ID)
	}
	before := map[int64]int{}
	for _, id := range old {
		before[id] = len(f.childEvents(id))
	}
	release, err := Rewind(f.e.Orders, f.chron, f.e.Campaign, "plan", "", ViaCommand)
	if err != nil {
		t.Fatal(err)
	}
	release()
	f.drive(Done)
	var fresh []int64
	for _, c := range f.children("each") {
		fresh = append(fresh, c.ID)
		if c.Attempt != 2 || c.KeptFrom != 0 {
			t.Fatalf("after a rewind %+v", c)
		}
	}
	for _, id := range old {
		if len(f.childEvents(id)) != before[id] {
			t.Fatalf("old child %d changed", id)
		}
		for _, n := range fresh {
			if n == id {
				t.Fatalf("old child %d reused", id)
			}
		}
	}
	earlier, _ := EarlierChildren(f.chron, f.events(""), "each")
	var ids []int64
	for _, c := range earlier {
		ids = append(ids, c.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	sort.Slice(old, func(i, j int) bool { return old[i] < old[j] })
	if !reflect.DeepEqual(ids, old) {
		t.Fatalf("earlier children %v, want %v", ids, old)
	}
}

func TestRetryAndRewindRefusedForAChildAndWhileAChildIsDriven(t *testing.T) {
	f, _ := setupFiles(t, `version: 0
start: s
nodes:
  s: {type: segment, segment: seg.yaml, on: {done: done, failed: failed}}
`, map[string]string{"seg.yaml": "version: 0\nflow: seg\nstart: x\nnodes:\n  x: {type: script, run: " + arg("exit", "1") + ", on: {ok: done, fail: failed}}\n"})
	f.drive(Failed)
	child := f.events(chronicle.ChildStarted)[0].Child
	if _, _, err := Retry(f.e.Orders, f.chron, child, "", ViaCommand); !errors.Is(err, ErrChild) {
		t.Fatalf("retry of the child: %v", err)
	}
	if _, err := Rewind(f.e.Orders, f.chron, child, "x", "", ViaCommand); !errors.Is(err, ErrChild) {
		t.Fatalf("rewind of the child: %v", err)
	}

	// A child that is driven, as a stray process could, blocks both.
	f2, sync := setupFiles(t, `version: 0
start: s
nodes:
  s: {type: segment, segment: seg.yaml, on: {done: done, failed: failed}}
`, map[string]string{"seg.yaml": "version: 0\nflow: seg\nstart: x\nnodes:\n  x: {type: script, run: " + arg("block", "SYNC") + ", on: {ok: done, fail: failed}}\n"})
	done := f2.driveAsync(t.Context())
	waitFile(t, filepath.Join(sync, "started.x.1"))
	child2 := f2.events(chronicle.ChildStarted)[0].Child
	// This drive holds no lock of the parent, as a stray process driving the
	// child alone would not: the driven child is what refuses.
	if busy, err := busyChild(f2.e.Orders, f2.chron, f2.events(""), 0); err != nil || busy != child2 {
		t.Fatalf("busy child %d %v", busy, err)
	}
	if _, err := Rewind(f2.e.Orders, f2.chron, f2.e.Campaign, "s", "", ViaCommand); !errors.Is(err, ErrChildBusy) {
		t.Fatalf("rewind while a child is driven: %v", err)
	}
	writeRelease(t, sync)
	f2.wait(done, Done)
}

func writeRelease(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}
