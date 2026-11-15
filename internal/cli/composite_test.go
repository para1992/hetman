package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/teststep"
)

func (w *world) file(name, text string) {
	w.t.Helper()
	p := filepath.Join(w.dir, filepath.FromSlash(name))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(text, "SYNC", strings.ReplaceAll(w.sync, "'", "''"))), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) eventsOf(id int64) []chronicle.Event {
	w.t.Helper()
	ch, err := chronicle.Open(filepath.Join(w.dir, ".hetman", "chronicle.db"))
	if err != nil {
		w.t.Fatal(err)
	}
	defer ch.Close()
	ev, err := ch.Events(id)
	if err != nil {
		w.t.Fatal(err)
	}
	return ev
}

// waitIn waits until campaign id has an event matching match.
func (w *world) waitIn(id int64, match func(chronicle.Event) bool, outs ...interface{ String() string }) chronicle.Event {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(w.dir, ".hetman", "chronicle.db")); err == nil {
			for _, e := range w.eventsOf(id) {
				if match(e) {
					return e
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	var texts []string
	for _, o := range outs {
		texts = append(texts, o.String())
	}
	w.t.Fatalf("timed out waiting for an event of campaign %d\n%s", id, w.diagnose(texts...))
	return chronicle.Event{}
}

func (w *world) input(name, text string) string {
	p := filepath.Join(w.dir, name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		w.t.Fatal(err)
	}
	return p
}

func stepSpan(t *testing.T, events []chronicle.Event, node string) (time.Time, time.Time) {
	t.Helper()
	var start, end time.Time
	for _, e := range events {
		switch {
		case e.Kind == chronicle.StepStarted && e.Node == node:
			start = e.At
		case e.Kind == chronicle.StepFinished && e.Node == node:
			end = e.At
		}
	}
	if start.IsZero() || end.IsZero() {
		t.Fatalf("%s did not run: %v", node, events)
	}
	return start, end
}

func TestKilledEngineInAForEachResumesWithoutRepeatingFinishedItems(t *testing.T) {
	items := strings.ReplaceAll(`[{"id":"x"},{"id":"y","wait":true},{"id":"z","needs":["y"]}]`, "'", "''")
	w := newWorld(t, `  a: {type: script, run: `+arg("write", "items.json", items)+`, on: {ok: each, fail: failed}}
  each: {type: foreach, segment: seg.yaml, items: artifact.a.items.json, parallel: 2, on: {done: done, failed: failed}}
`)
	w.file("seg.yaml", `version: 0
flow: item
start: work
nodes:
  work: {type: script, run: `+arg("item", "SYNC")+`, on: {ok: done, fail: failed}}
`)
	engineProc, out := w.process("run")
	finished := w.waitIn(1, func(e chronicle.Event) bool { return e.Kind == chronicle.ItemFinished && e.Item == "x" }, out)
	var yChild int64
	w.waitIn(1, func(e chronicle.Event) bool {
		if e.Kind == chronicle.ChildStarted && e.Item == "y" {
			yChild = e.Child
			return true
		}
		return false
	}, out)
	w.waitIn(yChild, func(e chronicle.Event) bool { return e.Kind == chronicle.ProcessStarted && e.Node == "work" }, out)
	// A child can be shown on its own while it runs and once it finished,
	// although its segment lies next to the orders.
	for _, child := range []int64{yChild, finished.Child} {
		if status := w.call(0, "status", "-c", itoa(child)); !strings.Contains(status, "work") {
			t.Fatalf("status of child %d:\n%s", child, status)
		}
	}
	if err := engineProc.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	engineProc.Wait()
	killed := out.String()

	status := w.call(0, "status")
	for _, want := range []string{"> each", "item x", "finished", "item y", "interrupted", "item z", "pending", "needs y"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status after the kill lacks %q:\n%s", want, status)
		}
	}
	if status := w.call(0, "status", "-c", itoa(yChild)); !strings.Contains(status, "work") {
		t.Fatalf("status of the interrupted child:\n%s", status)
	}
	w.release()
	resumed := w.call(0, "resume")
	if !strings.Contains(resumed, "campaign done") {
		t.Fatalf("resume:\n%s\n%s", resumed, w.diagnose(killed))
	}
	// x ran once, in the one child it ever had; y ran again in its own child.
	starts := map[string]int{}
	for _, e := range w.events() {
		if e.Kind == chronicle.ChildStarted {
			starts[e.Item]++
		}
	}
	if !reflect.DeepEqual(starts, map[string]int{"x": 1, "y": 1, "z": 1}) {
		t.Fatalf("children started %v\n%s", starts, w.diagnose(killed, resumed))
	}
	xWork := filepath.Join(w.dir, ".hetman", "campaigns", itoa(finished.Child), "work")
	if got := teststep.Trace(xWork); !reflect.DeepEqual(got, []string{"work#1"}) {
		t.Fatalf("x trace %v", got)
	}
	yWork := filepath.Join(w.dir, ".hetman", "campaigns", itoa(yChild), "work")
	if got := teststep.Trace(yWork); len(got) != 2 || got[1] != "work#2" {
		t.Fatalf("y trace %v", got)
	}
	if status := w.call(0, "status"); !strings.Contains(status, "state: done") {
		t.Fatalf("final status:\n%s", status)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

const lockedStep = `  a: {type: script, run: ARG, locks: [stand], on: {ok: done, fail: failed}}
`

func TestLockSharedByTwoCampaignsInTwoProcesses(t *testing.T) {
	w := newWorld(t, strings.Replace(lockedStep, "ARG", arg("item", "SYNC"), 1))
	first, out1 := w.process("run", "-input", w.input("one.json", `{"id":"one","wait":true}`))
	w.waitFor("started.one.1", out1)
	second, out2 := w.process("run", "-input", w.input("two.json", `{"id":"two"}`))
	wait := w.waitIn(2, func(e chronicle.Event) bool { return e.Kind == chronicle.LockWaiting }, out1, out2)
	if wait.Lock != "stand" || !strings.Contains(wait.Holder, "campaign 1 at a") {
		t.Fatalf("lock_waiting %+v", wait)
	}
	if status := w.call(0, "status", "-c", "2"); !strings.Contains(status, "waiting at a for the lock stand, held by campaign 1") {
		t.Fatalf("status of the waiting campaign:\n%s", status)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(w.sync, "started.two.2")); err == nil {
		t.Fatal("the second campaign ran while the first held the lock")
	}
	w.release()
	for _, p := range []interface {
		Wait() error
	}{first, second} {
		p.Wait()
	}
	if first.ProcessState.ExitCode() != 0 || second.ProcessState.ExitCode() != 0 {
		t.Fatalf("exit codes %d %d\n%s\n%s", first.ProcessState.ExitCode(), second.ProcessState.ExitCode(), out1, out2)
	}
	_, end1 := stepSpan(t, w.eventsOf(1), "a")
	start2, _ := stepSpan(t, w.eventsOf(2), "a")
	if start2.Before(end1) {
		t.Fatalf("campaign 2 started a at %s, before campaign 1 finished it at %s", start2, end1)
	}
}

func TestLockIsFreeAfterAKilledEngine(t *testing.T) {
	w := newWorld(t, strings.Replace(lockedStep, "ARG", arg("item", "SYNC"), 1))
	first, out1 := w.process("run", "-input", w.input("one.json", `{"id":"one","wait":true}`))
	w.waitFor("started.one.1", out1)
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	first.Wait()
	began := time.Now()
	w.call(0, "run", "-input", w.input("two.json", `{"id":"two"}`))
	if d := time.Since(began); d > 20*time.Second {
		t.Fatalf("the second run took %s", d)
	}
	for _, e := range w.eventsOf(2) {
		if e.Kind == chronicle.LockWaiting {
			t.Fatalf("the lock of the killed engine was still held: %+v", e)
		}
	}
	w.release() // lets a step that outlived its engine end
}

func TestChildAskingIsAnsweredThroughTheParentCampaign(t *testing.T) {
	w := newWorld(t, `  a: {type: segment, segment: segments/decide.yaml, on: {done: done, failed: failed}}
`)
	w.file("segments/decide.yaml", `version: 0
flow: decide
start: pick
nodes:
  pick:
    type: human
    ask: [{id: why, question: 'Why?', kind: text}]
    on: {go: done, stop: failed}
`)
	engineProc, out := w.process("run")
	w.waitIn(2, func(e chronicle.Event) bool { return e.Kind == chronicle.Asked }, out)
	status := w.call(0, "status")
	for _, want := range []string{"a (campaign 2) waits for a person at pick", "segment", "campaign 2", "asking",
		"human node pick in campaign 2 asks", "hetman answer -f"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}
	if halt := w.call(1, "halt", "-c", "2"); !strings.Contains(halt, "halt or resume campaign 1") {
		t.Fatalf("halting the child directly:\n%s", halt)
	}
	if retry := w.call(1, "retry", "-c", "2"); !strings.Contains(retry, "campaign 2 is run by campaign 1; retry or rewind campaign 1") {
		t.Fatalf("retrying the child directly:\n%s", retry)
	}
	answered := w.call(0, "answer", "-a", "why=fine", "-outcome", "go")
	if !strings.Contains(answered, "answers recorded for campaign 1 at pick") {
		t.Fatalf("answer:\n%s", answered)
	}
	engineProc.Wait()
	if code := engineProc.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("engine exit %d\n%s", code, out)
	}
	if status := w.call(0, "status", "-c", "2"); !strings.Contains(status, "part of campaign 1") || !strings.Contains(status, "state: done") {
		t.Fatalf("status of the child:\n%s", status)
	}
}
