package watchtower_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hetman/internal/watchtower"
)

func (w *world) segment(name, text string) {
	w.t.Helper()
	text = strings.ReplaceAll(text, "SYNC1", strings.ReplaceAll(w.sync(1), "'", "''"))
	if err := os.WriteFile(filepath.Join(w.dir, name), []byte(text), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) campaignN(id int64) watchtower.Campaign {
	w.t.Helper()
	var c watchtower.Campaign
	if code := w.get("/api/campaigns/"+strconv.FormatInt(id, 10), &c); code != http.StatusOK {
		w.t.Fatalf("campaign %d: status %d", id, code)
	}
	return c
}

// The segment file must exist before the orders are read, so the world is
// built in two steps here.
func newSegmentWorld(t *testing.T, config, segment string) *world {
	dir := t.TempDir()
	seg := filepath.Join(dir, "seg.yaml")
	os.WriteFile(seg, []byte("version: 0\nstart: x\nnodes:\n  x: {type: script, run: [x], on: {ok: done, fail: failed}}\n"), 0o644)
	w := newWorld(t, strings.ReplaceAll(config, "SEG", strings.ReplaceAll(seg, "'", "''")))
	w.segment("seg.yaml", segment)
	os.Rename(filepath.Join(w.dir, "seg.yaml"), seg)
	return w
}

func TestForEachTilesAndChildView(t *testing.T) {
	items := strings.ReplaceAll(`[{"id":"a","wait":true},{"id":"b","fail":true},{"id":"c","needs":["b"]}]`, "'", "''")
	w := newSegmentWorld(t, `version: 0
start: plan
nodes:
  plan: {type: script, run: `+arg("write", "items.json", items)+`, on: {ok: each, fail: failed}}
  each: {type: foreach, segment: 'SEG', items: artifact.plan.items.json, parallel: 2, on: {done: done, failed: failed}}
`, `version: 0
flow: item
start: work
nodes:
  work: {type: script, run: `+arg("item", "SYNC1")+`, on: {ok: done, fail: failed}}
`)
	runner, _ := w.process("run")
	w.waitFile(filepath.Join(w.sync(1), "started.a.2"))
	var c watchtower.Campaign
	var each *watchtower.Column
	for i := 0; i < 1500; i++ {
		c = w.campaign(1)
		if each = find(c.Columns, "each"); each != nil && len(each.Tiles) == 3 && strings.Contains(each.Tiles[2].Note, "why_failed") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := []string{}
	for _, t := range each.Tiles {
		got = append(got, fmt.Sprintf("%s:%s:%d:%s%v", t.Item, t.Status, t.Child, t.Note, t.Args))
	}
	want := []string{"a:running:2:map[]", "b:failed:3:map[]", "c:notrun:0:page.tile.child.why_failedmap[item:b]"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("tiles %v, want %v\n%s", got, want, w.dump())
	}
	child := w.campaignN(2)
	if child.Retry != "" || len(child.Rewind) > 0 {
		t.Fatalf("a child page offers retry %q or rewind %v", child.Retry, child.Rewind)
	}
	if child.Parent != 1 || child.Top != 1 || child.Path != "each/a" || find(child.Columns, "work") == nil || child.State != watchtower.StateRunning {
		t.Fatalf("child view %+v", child.Summary)
	}
	var list []watchtower.Summary
	w.get("/api/campaigns", &list)
	if len(list) != 1 || list[0].ID != 1 {
		t.Fatalf("the list shows children: %+v", list)
	}
	if code, msg := w.control(2, "halt", pageHeaders); code != http.StatusConflict || !strings.Contains(msg, "halt or resume campaign 1") {
		t.Fatalf("halting a child from the page: %d %s", code, msg)
	}
	w.release(1)
	runner.Wait()
	c = w.waitState(1, watchtower.StateFailed)
	if find(c.Columns, "each").Tiles[0].Status != watchtower.StatusPassed || c.Retry != "each" {
		t.Fatalf("after the end: retry %q, tiles %+v", c.Retry, find(c.Columns, "each").Tiles)
	}
	if tile := find(c.Columns, "each").Tiles[0]; tile.Attempt != 1 || tile.Earlier {
		t.Fatalf("tile %+v", tile)
	}
}

// After a retry the page shows one tile per item, in list order, with its
// latest attempt; an earlier attempt of an item hangs under that tile.
func TestForEachEarlierAttemptsHangUnderTheirItem(t *testing.T) {
	items := strings.ReplaceAll(`[{"id":"a"},{"id":"b","failonce":true},{"id":"c"}]`, "'", "''")
	w := newSegmentWorld(t, `version: 0
start: plan
nodes:
  plan: {type: script, run: `+arg("write", "items.json", items)+`, on: {ok: each, fail: failed}}
  each: {type: foreach, segment: 'SEG', items: artifact.plan.items.json, parallel: 1, on: {done: done, failed: failed}}
`, `version: 0
flow: item
start: work
nodes:
  work: {type: script, run: `+arg("item", "SYNC1")+`, on: {ok: done, fail: failed}}
`)
	runner, _ := w.process("run")
	runner.Wait()
	w.waitState(1, watchtower.StateFailed)
	retry, out := w.process("retry")
	retry.Wait()
	c := w.waitState(1, watchtower.StateDone)
	tiles := find(c.Columns, "each").Tiles
	var got []string
	for _, t := range tiles {
		s := fmt.Sprintf("%s:%s:%d", t.Item, t.Status, len(t.Before))
		for _, b := range t.Before {
			s += fmt.Sprintf("[%s:%s:%d:%v]", b.Item, b.Status, b.Attempt, b.Earlier)
		}
		got = append(got, s)
	}
	if want := "a:passed:0 b:passed:1[b:failed:1:true] c:passed:0"; strings.Join(got, " ") != want {
		t.Fatalf("tiles %v, want %s\n%s", got, want, out)
	}
	if tiles[1].Child == tiles[1].Before[0].Child || tiles[1].Attempt != 2 {
		t.Fatalf("tile of b %+v", tiles[1])
	}
}

func TestChildQuestionShownAndAnsweredThroughTheParent(t *testing.T) {
	w := newSegmentWorld(t, `version: 0
start: s
nodes:
  s: {type: segment, segment: 'SEG', on: {done: done, failed: failed}}
`, `version: 0
flow: decide
start: pick
nodes:
  pick: {type: human, on: {go: done, stop: failed}}
`)
	runner, out := w.process("run")
	c := w.waitAsking()
	if c.Ask == nil || c.Ask.Campaign != 2 || c.Ask.Node != "pick" || c.Ask.Path != "s" || !strings.Contains(c.Ask.Command, "-c 1") {
		t.Fatalf("parent ask %+v", c.Ask)
	}
	if child := w.campaignN(2); child.State != watchtower.StateAsking || child.Ask == nil {
		t.Fatalf("child view %+v", child.Summary)
	}
	// The child's page answers through its own number.
	code, res := w.answer(2, fmt.Sprintf(`{"ask": %d, "outcome": "go"}`, c.Ask.Seq), pageHeaders)
	if code != http.StatusOK || res != watchtower.ResumeElsewhere {
		t.Fatalf("answer: %d %s", code, res)
	}
	runner.Wait()
	if code := runner.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("runner exit %d\n%s", code, out)
	}
}
