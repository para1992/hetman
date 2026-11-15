package engine

import (
	"context"
	"testing"
	"time"

	"hetman/internal/chronicle"
)

// A rewind of a campaign stopped in the middle of a for-each checks the
// steps its children were running, leaves those children to the earlier
// attempt, and runs the node again with new children.
func TestRewindOfAnInterruptedForEachCleansUpItsChildren(t *testing.T) {
	items := `[{"id":"a","wait":true}]`
	f, sync := setupFiles(t, forEachOrders(items, 1, ""), map[string]string{"segments/item.yaml": segmentWith(arg("item", "SYNC"))})
	ctx, cancel := context.WithCancel(context.Background())
	done := f.driveAsync(ctx)
	child := int64(0)
	deadline := time.Now().Add(20 * time.Second)
	for child == 0 && time.Now().Before(deadline) {
		if s := f.events(chronicle.ChildStarted); len(s) > 0 {
			child = s[0].Child
		}
		time.Sleep(20 * time.Millisecond)
	}
	for len(filterKind(f.childEvents(child), chronicle.ProcessStarted)) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if r := <-done; r.err != ErrInterrupted {
		t.Fatalf("drive %+v", r)
	}
	release, err := Rewind(f.e.Orders, f.chron, f.e.Campaign, "each", "", ViaCommand)
	if err != nil {
		t.Fatal(err)
	}
	release()
	old := f.childEvents(child)
	if len(filterKind(old, chronicle.ProcessCleanup)) != 1 || len(filterKind(old, chronicle.StepInterrupted)) != 1 {
		t.Fatalf("the running step of the old child was not checked:\n%v", old)
	}
	writeRelease(t, sync)
	f.drive(Done)
	if n := len(f.childEvents(child)); n != len(old) {
		t.Fatalf("the old child was driven again: %d events, had %d", n, len(old))
	}
	now := f.children("each")
	if len(now) != 1 || now[0].ID == child || now[0].Attempt != 2 {
		t.Fatalf("children after the rewind %+v", now)
	}
}

func filterKind(events []chronicle.Event, kind string) []chronicle.Event {
	var out []chronicle.Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}
