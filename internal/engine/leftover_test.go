package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hetman/internal/teststep"
)

// waitBeat waits until the heartbeat file at path exists, that is until the
// process left behind has started.
func waitBeat(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatal("the process left behind never ran")
}

// A script step that exits 0 while a process it started still runs does
// not leave that process alive.
func TestScriptStepLeavesNothingRunning(t *testing.T) {
	f := setup(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("spawn", "0s")+`, on: {ok: done, fail: failed}}
`)
	f.drive(Done)
	beat := filepath.Join(f.work, "heartbeat")
	waitBeat(t, beat)
	if teststep.Beating(beat) {
		t.Fatal("a process the step started is still alive after the step")
	}
}

// An agent step whose adapter answers and exits 0 while a process it
// started still runs does not leave that process alive.
func TestAgentStepLeavesNothingRunning(t *testing.T) {
	dir := tempDir(t)
	beat := filepath.Join(dir, "heartbeat")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spawn, _ := json.Marshal([]string{self, "step", "heartbeat", beat})
	await, _ := json.Marshal(beat)
	f := setupAgent(t, one(""), agentFiles(`{"nodes": {"a": [[{"spawn": `+string(spawn)+`, "await": `+string(await)+`}, {"result": {"outcome": "ok"}}]]}}`), "")
	f.drive(Done)
	want(t, f.finished(), []string{"a#1 ok/"})
	waitBeat(t, beat)
	if teststep.Beating(beat) {
		t.Fatal("a process the adapter started is still alive after the step")
	}
}
