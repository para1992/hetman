package engine

import (
	"path/filepath"
	"testing"

	"hetman/internal/chronicle"
)

// When a drive stops at a question in a child, nothing stays open: the
// driver lock of the child and the resource lock its step held can be taken
// at once, in this process as by another. On Windows a handle left open
// keeps a file locked; flock on Linux and macOS refuses a second lock from
// the same process too, so a leak shows on every system.
func TestNothingStaysLockedWhenAChildAsks(t *testing.T) {
	f, _ := setupFiles(t, `version: 0
start: s
nodes:
  s: {type: segment, segment: seg.yaml, locks: [a-copy], on: {done: done, failed: failed}}
`, map[string]string{"seg.yaml": `version: 0
flow: seg
start: work
nodes:
  work: {type: script, run: ` + arg("exit", "0") + `, locks: [b-stand], on: {ok: decide, fail: failed}}
  decide: {type: human, on: {go: done, stop: failed}}
`})
	release, err := Lock(f.e.Orders, f.e.Campaign)
	if err != nil {
		t.Fatal(err)
	}
	f.e.StopAtAsk = true
	f.drive(Asking)
	release()
	child := f.events(chronicle.ChildStarted)[0].Child
	for _, id := range []int64{f.e.Campaign, child} {
		if driven, err := Driven(f.e.Orders, id); driven || err != nil {
			t.Fatalf("campaign %d is still locked after the drive stopped: %v", id, err)
		}
		free, err := Lock(f.e.Orders, id)
		if err != nil {
			t.Fatalf("campaign %d: %v", id, err)
		}
		free()
	}
	for _, name := range []string{"a-copy", "b-stand"} {
		free, err := lockFile(filepath.Join(LockDir(f.e.Orders), name+".lock"))
		if err != nil {
			t.Fatalf("lock %s is still held: %v", name, err)
		}
		free()
	}
}
