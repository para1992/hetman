package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

// setupFiles is setup with more files next to the orders, such as segments;
// "SYNC" in any of them is replaced by a folder for markers.
func setupFiles(t *testing.T, config string, files map[string]string) (*fixture, string) {
	t.Helper()
	dir := tempDir(t)
	sync := filepath.Join(dir, "sync")
	if err := os.Mkdir(sync, 0o755); err != nil {
		t.Fatal(err)
	}
	q := strings.ReplaceAll(sync, "'", "''")
	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(text, "SYNC", q)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, orders.DefaultFile)
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(config, "SYNC", q)), 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := orders.Load(path)
	if err != nil {
		t.Fatalf("orders rejected:\n%v", err)
	}
	if err := os.MkdirAll(StateDir(o), 0o755); err != nil {
		t.Fatal(err)
	}
	ch, err := chronicle.Open(ChroniclePath(o))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Close() })
	id, err := ch.NewCampaign(o.Flow, o.Path, o.Start, json.RawMessage(`{"task":"T-1"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, e: &Engine{Orders: o, Chron: ch, Campaign: id}, work: filepath.Join(CampaignDir(o, id), "work"), chron: ch}, sync
}

// span is when the step of a node ran in a campaign, from its chronicle.
type span struct {
	item       string
	start, end time.Time
}

func (f *fixture) spans(node string, children []Child) []span {
	f.t.Helper()
	var out []span
	for _, c := range children {
		s := span{item: c.Item}
		for _, ev := range c.Events {
			switch {
			case ev.Kind == chronicle.StepStarted && ev.Node == node:
				s.start = ev.At
			case ev.Kind == chronicle.StepFinished && ev.Node == node:
				s.end = ev.At
			}
		}
		if !s.start.IsZero() {
			out = append(out, s)
		}
	}
	return out
}

func (f *fixture) children(node string) []Child {
	f.t.Helper()
	list, err := ChildrenOf(f.chron, f.events(""), node)
	if err != nil {
		f.t.Fatal(err)
	}
	return list
}

func overlap(a, b span) bool { return a.start.Before(b.end) && b.start.Before(a.end) }

func byItem(spans []span) map[string]span {
	m := map[string]span{}
	for _, s := range spans {
		m[s.item] = s
	}
	return m
}

const itemSegment = `version: 0
flow: one-item
start: work
nodes:
  work: {type: script, run: %s, on: {ok: done, fail: failed}}
`

func segmentWith(run string) string { return strings.Replace(itemSegment, "%s", run, 1) }

func forEachOrders(items string, parallel int, extra string) string {
	list := strings.ReplaceAll(items, "'", "''")
	return `version: 0
flow: many
start: plan
nodes:
  plan: {type: script, run: ` + arg("write", "items.json", list) + `, on: {ok: each, fail: failed}}
  each:
    type: foreach
    segment: segments/item.yaml
    items: artifact.plan.items.json
    parallel: ` + string(rune('0'+parallel)) + `
` + extra + `    on: {done: done, failed: failed}
`
}

func TestForEachRunsIndependentItemsTogetherAndDependentOnesInOrder(t *testing.T) {
	items := `[{"id":"a","sleep":"400ms"},{"id":"b","needs":["a"],"sleep":"100ms"},{"id":"c","sleep":"400ms"},
{"id":"d","needs":["b","c"],"sleep":"100ms"},{"id":"e","sleep":"100ms"}]`
	f, _ := setupFiles(t, forEachOrders(items, 2, ""), map[string]string{"segments/item.yaml": segmentWith(arg("item", "SYNC"))})
	f.drive(Done)
	children := f.children("each")
	if len(children) != 5 {
		t.Fatalf("children %+v", children)
	}
	for _, c := range children {
		if c.Outcome != orders.Done || c.ID == 0 {
			t.Fatalf("item %s: %+v", c.Item, c)
		}
		k, _ := f.chron.Campaign(c.ID)
		if k.Parent != f.e.Campaign || c.Events[0].Item != c.Item || c.Events[0].ParentNode != "each" {
			t.Fatalf("child %d is not linked to its parent: %+v %+v", c.ID, k, c.Events[0])
		}
	}
	s := byItem(f.spans("work", children))
	// Independent items overlap; an item starts only after what it needs.
	if !overlap(s["a"], s["c"]) {
		t.Fatalf("a and c did not run at the same time: %+v %+v", s["a"], s["c"])
	}
	for _, dep := range [][2]string{{"a", "b"}, {"b", "d"}, {"c", "d"}} {
		if s[dep[1]].start.Before(s[dep[0]].end) {
			t.Fatalf("%s started before %s finished: %+v %+v", dep[1], dep[0], s[dep[1]], s[dep[0]])
		}
	}
	// Never more than two at a time.
	all := f.spans("work", children)
	for _, x := range all {
		n := 0
		for _, y := range all {
			if !y.start.After(x.start) && y.end.After(x.start) {
				n++
			}
		}
		if n > 2 {
			t.Fatalf("%d items ran at once at %s: %+v", n, x.start, all)
		}
	}
	fin := f.events(chronicle.StepFinished)
	if last := fin[len(fin)-1]; last.Node != "each" || last.Outcome != orders.Done {
		t.Fatalf("for-each finished %+v", last)
	}
	if got := len(f.events(chronicle.ItemFinished)); got != 5 {
		t.Fatalf("%d item_finished events", got)
	}
}

func TestForEachFailedItemSkipsItsDependentsOnly(t *testing.T) {
	items := `[{"id":"a"},{"id":"b","needs":["a"],"fail":true},{"id":"c","needs":["b"]},{"id":"x","needs":["c"]},{"id":"d"},{"id":"e","needs":["d"]}]`
	f, sync := setupFiles(t, forEachOrders(items, 3, ""), map[string]string{"segments/item.yaml": segmentWith(arg("item", "SYNC"))})
	f.drive(Failed)
	got := map[string]string{}
	for _, c := range f.children("each") {
		got[c.Item] = c.Outcome
		if c.NotRun != "" {
			got[c.Item] = "not run: " + c.NotRun
		}
	}
	want := map[string]string{"a": "done", "b": "failed", "c": "not run: needs b, which failed",
		"x": "not run: needs c, which did not run", "d": "done", "e": "done"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items %v, want %v", got, want)
	}
	for _, id := range []string{"a", "d", "e"} {
		if _, err := os.Stat(filepath.Join(sync, "finished."+id)); err != nil {
			t.Fatalf("item %s did not finish", id)
		}
	}
	st := chronicle.Replay(f.events(""))
	if st.End != orders.Failed {
		t.Fatalf("end %q", st.End)
	}
}

func TestForEachRejectsABadList(t *testing.T) {
	for list, want := range map[string]string{
		`[{"id":"a","needs":["b"]},{"id":"b","needs":["a"]}]`: "cycle: a -> b -> a",
		`[{"id":"a","needs":["zz"]}]`:                         `item a needs "zz", which is not in the list`,
		`[{"id":"a"},{"id":"a"}]`:                             `items 1 and 2 have the same id "a"`,
		`{"id":"a"}`:                                          "must be a list of objects",
	} {
		f, _ := setupFiles(t, forEachOrders(list, 2, ""), map[string]string{"segments/item.yaml": segmentWith(arg("exit", "0"))})
		f.drive(Failed)
		fin := f.events(chronicle.StepFinished)
		last := fin[len(fin)-1]
		if last.Node != "each" || last.Outcome != orders.Failed || !strings.Contains(last.Error, want) {
			t.Fatalf("list %s: finish %+v, want error with %q", list, last, want)
		}
		if n := len(f.events(chronicle.ChildStarted)); n != 0 {
			t.Fatalf("list %s: %d children started", list, n)
		}
	}
}

func TestLockSharedByItemsIsNeverHeldTwice(t *testing.T) {
	items := `[{"id":"a","sleep":"150ms"},{"id":"b","sleep":"150ms"},{"id":"c","sleep":"150ms"},{"id":"d","sleep":"150ms"}]`
	seg := `version: 0
flow: one-item
start: prepare
nodes:
  prepare: {type: script, run: ` + arg("sleep", "50ms") + `, on: {ok: work, fail: failed}}
  work: {type: script, run: ` + arg("item", "SYNC") + `, locks: [stand], on: {ok: done, fail: failed}}
`
	f, _ := setupFiles(t, forEachOrders(items, 4, ""), map[string]string{"segments/item.yaml": seg})
	f.drive(Done)
	children := f.children("each")
	all := f.spans("work", children)
	if len(all) != 4 {
		t.Fatalf("spans %+v", all)
	}
	for i := range all {
		for j := range all {
			if i != j && overlap(all[i], all[j]) {
				t.Fatalf("items %s and %s held the lock at the same time: %+v %+v", all[i].item, all[j].item, all[i], all[j])
			}
		}
	}
	// The prepare steps, which take no lock, ran together.
	if p := f.spans("prepare", children); !overlap(p[0], p[1]) {
		t.Fatalf("steps without a lock did not run together: %+v", p)
	}
	waited := 0
	for _, c := range children {
		for _, ev := range c.Events {
			if ev.Kind == chronicle.LockWaiting {
				waited++
				if ev.Lock != "stand" || !strings.Contains(ev.Holder, "at work") {
					t.Fatalf("lock_waiting %+v", ev)
				}
			}
			if ev.Kind == chronicle.StepStarted && ev.Node == "work" && !reflect.DeepEqual(ev.Locks, []string{"stand"}) {
				t.Fatalf("step_started without its locks: %+v", ev)
			}
		}
	}
	if waited == 0 {
		t.Fatal("no item ever waited for the lock")
	}
}

func TestLocksOfAGateAndItsChecksAreTakenInNameOrder(t *testing.T) {
	f := setup(t, `version: 0
start: g
checks:
  unit: {run: `+arg("exit", "0")+`, locks: [b-stand]}
nodes:
  g: {type: gate, checks: [unit], locks: [a-copy], on: {pass: done, blocked: failed}}
`)
	f.drive(Done)
	if s := f.events(chronicle.StepStarted); !reflect.DeepEqual(s[0].Locks, []string{"a-copy", "b-stand"}) {
		t.Fatalf("locks %v", s[0].Locks)
	}
	// Both are free again.
	for _, name := range []string{"a-copy", "b-stand"} {
		free, err := lockFile(filepath.Join(LockDir(f.e.Orders), name+".lock"))
		if err != nil {
			t.Fatalf("lock %s still held: %v", name, err)
		}
		free()
	}
}

func TestStepWaitingForALockIsVisibleAndHaltable(t *testing.T) {
	f := setup(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("exit", "0")+`, locks: [stand], on: {ok: done, fail: failed}}
`)
	dir := LockDir(f.e.Orders)
	os.MkdirAll(dir, 0o755)
	free, err := lockFile(filepath.Join(dir, "stand.lock"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "stand.holder"), []byte("someone else"), 0o644)
	done := f.driveAsync(context.Background())
	deadline := time.Now().Add(10 * time.Second)
	for len(f.events(chronicle.LockWaiting)) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	st := chronicle.Replay(f.events(""))
	if st.LockWait != "stand" || st.LockHolder != "someone else" || st.Running {
		t.Fatalf("state while waiting: %+v", st)
	}
	f.record(chronicle.Event{Kind: chronicle.HaltRequested})
	f.wait(done, Halted)
	free()
	f.record(chronicle.Event{Kind: chronicle.Resumed})
	f.drive(Done)
	f.trace("a#1")
}

func TestSegmentGetsAnOutputAsInputAndEndsAsItsOutcome(t *testing.T) {
	config := `version: 0
start: plan
executors:
  stub: {run: ` + teststep.Adapter() + `}
nodes:
  plan: {type: agent, executor: stub, prompt: p.md, schema: s.json, on: {ok: review, fail: failed}}
  review: {type: segment, segment: seg/review.yaml, input: output.plan.task, on: {done: after, failed: failed}}
  after: {type: segment, segment: seg/review.yaml, on: {done: done, failed: failed}}
`
	f, _ := setupFiles(t, config, map[string]string{
		"p.md":   "plan",
		"s.json": `{"type":"object","properties":{"outcome":{"type":"string","enum":["ok"]},"task":{"type":"object"}},"required":["outcome"]}`,
		"seg/review.yaml": `version: 0
flow: review
start: look
nodes:
  look: {type: script, run: ` + arg("input") + `, on: {ok: done, fail: failed}}
`,
	})
	// The stub answers with the first outcome and no task; give it one.
	plan := filepath.Join(filepath.Dir(f.e.Orders.Path), "plan.json")
	os.WriteFile(plan, []byte(`{"nodes":{"plan":[[{"result":{"outcome":"ok","task":{"key":"T-9"}}}]]}}`), 0o644)
	f.e.Orders.Executors["stub"].Run = append(f.e.Orders.Executors["stub"].Run, plan)
	f.drive(Done)
	children := f.events(chronicle.ChildStarted)
	if len(children) != 2 {
		t.Fatalf("children %+v", children)
	}
	seen := func(id int64) string {
		data, _ := os.ReadFile(filepath.Join(CampaignDir(f.e.Orders, id), "work", "seen.look.1.json"))
		return strings.Join(strings.Fields(string(data)), "")
	}
	if got := seen(children[0].Child); got != `{"key":"T-9"}` {
		t.Fatalf("first segment input %s", got)
	}
	if got := seen(children[1].Child); got != `{"task":"T-1"}` {
		t.Fatalf("second segment input %s (want the campaign's own)", got)
	}
	fin := f.events(chronicle.StepFinished)
	if fin[1].Node != "review" || fin[1].Outcome != orders.Done || fin[1].Child != children[0].Child {
		t.Fatalf("segment finish %+v", fin[1])
	}
}

func TestNestedSegments(t *testing.T) {
	f, _ := setupFiles(t, `version: 0
start: outer
nodes:
  outer: {type: segment, segment: a.yaml, on: {done: done, failed: failed}}
`, map[string]string{
		"a.yaml": `version: 0
flow: a
start: mid
nodes:
  mid: {type: segment, segment: deep/b.yaml, on: {done: done, failed: failed}}
`,
		"deep/b.yaml": `version: 0
flow: b
start: leaf
nodes:
  leaf: {type: script, run: ` + arg("env") + `, on: {ok: fail, fail: done}}
  fail: {type: script, run: ` + arg("exit", "1") + `, on: {ok: done, fail: failed}}
`,
	})
	f.drive(Failed)
	outer := f.children("outer")
	if len(outer) != 1 || outer[0].Outcome != orders.Failed {
		t.Fatalf("outer children %+v", outer)
	}
	mid, err := ChildrenOf(f.chron, outer[0].Events, "mid")
	if err != nil || len(mid) != 1 || mid[0].Outcome != orders.Failed {
		t.Fatalf("mid children %+v %v", mid, err)
	}
	if top, _ := Top(f.chron, mid[0].ID); top != f.e.Campaign {
		t.Fatalf("top of %d is %d", mid[0].ID, top)
	}
	o, err := OrdersOf(f.e.Orders, f.chron, mid[0].ID)
	if err != nil || o.Flow != "b" {
		t.Fatalf("orders of the deepest child: %v %v", o, err)
	}
	// The deepest steps run in their own campaign folder, from the folder of
	// their segment file.
	data, err := os.ReadFile(filepath.Join(CampaignDir(f.e.Orders, mid[0].ID), "work", "env.leaf.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]string
	json.Unmarshal(data, &env)
	if env["HETMAN_RUN_DIR"] != filepath.Join(CampaignDir(f.e.Orders, mid[0].ID), "work") {
		t.Fatalf("env %v", env)
	}
	if err := func() error { _, _, err := Halt(f.e.Orders, f.chron, mid[0].ID); return err }(); !errors.Is(err, ErrChild) {
		t.Fatalf("halting a child directly: %v", err)
	}
}

func TestChildAskingIsAnsweredThroughTheParent(t *testing.T) {
	f, _ := setupFiles(t, `version: 0
start: s
nodes:
  s: {type: segment, segment: seg.yaml, on: {done: done, failed: failed}}
`, map[string]string{"seg.yaml": `version: 0
flow: seg
start: decide
nodes:
  decide:
    type: human
    ask: [{id: why, question: 'Why?', kind: text}]
    on: {go: done, stop: failed}
`})
	done := f.driveAsync(context.Background())
	var asked *chronicle.Event
	deadline := time.Now().Add(10 * time.Second)
	for asked == nil && time.Now().Before(deadline) {
		asked, _, _ = Pending(f.chron, f.e.Campaign)
		time.Sleep(20 * time.Millisecond)
	}
	if asked == nil || asked.Campaign == f.e.Campaign || asked.Node != "decide" {
		t.Fatalf("pending through the parent: %+v", asked)
	}
	ws, err := Waiters(f.chron, f.events(""))
	if err != nil || len(ws) != 1 || ws[0].Campaign != asked.Campaign || ws[0].Path != "s" || !ws[0].State.Asking() {
		t.Fatalf("waiters %+v %v", ws, err)
	}
	ev, err := Answer(f.chron, f.e.Campaign, Reply{Ask: asked.Seq, Given: ask.Given{Values: map[string]string{"why": "fine"}, Outcome: "go"}})
	if err != nil || ev.Campaign != asked.Campaign {
		t.Fatalf("answer %+v %v", ev, err)
	}
	f.wait(done, Done)
}

func TestHaltReachesTheChildAndResumeContinuesIt(t *testing.T) {
	f, sync := setupFiles(t, `version: 0
start: s
nodes:
  s: {type: segment, segment: seg.yaml, on: {done: done, failed: failed}}
`, map[string]string{"seg.yaml": `version: 0
flow: seg
start: a
nodes:
  a: {type: script, run: ` + arg("block", "SYNC") + `, on: {ok: b, fail: failed}}
  b: {type: script, run: ` + arg("exit", "0") + `, on: {ok: done, fail: failed}}
`})
	done := f.driveAsync(context.Background())
	waitFile(t, filepath.Join(sync, "started.a.1"))
	f.record(chronicle.Event{Kind: chronicle.HaltRequested})
	time.Sleep(1500 * time.Millisecond) // the child looks at its parent between steps and while waiting
	os.WriteFile(filepath.Join(sync, "release"), nil, 0o644)
	f.wait(done, Halted)
	child := f.events(chronicle.ChildStarted)[0].Child
	cevents, _ := f.chron.Events(child)
	cst := chronicle.Replay(cevents)
	if !cst.Halted || cst.Node != "b" {
		t.Fatalf("child state after the halt: %+v", cst)
	}
	if st := chronicle.Replay(f.events("")); !st.Halted || !st.Running || st.Node != "s" {
		t.Fatalf("parent state after the halt: %+v", st)
	}
	f.record(chronicle.Event{Kind: chronicle.Resumed})
	f.drive(Done)
	var kinds []string
	cevents, _ = f.chron.Events(child)
	for _, ev := range cevents {
		if ev.Kind == chronicle.StepStarted {
			kinds = append(kinds, ev.Node)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"a", "b"}) {
		t.Fatalf("child steps %v", kinds)
	}
	if n := len(f.events(chronicle.StepStarted)); n != 1 {
		t.Fatalf("the segment step started %d times", n)
	}
}

func TestParseItemsOrderIsKept(t *testing.T) {
	var v any
	json.Unmarshal([]byte(`[{"id":"z"},{"id":"a","needs":["z"],"x":1}]`), &v)
	list, err := ParseItems(v)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{list[0].ID, list[1].ID}
	if !sort.StringsAreSorted([]string{"a", "z"}) || ids[0] != "z" || string(list[1].Input) != `{"id":"a","needs":["z"],"x":1}` {
		t.Fatalf("items %+v", list)
	}
}
