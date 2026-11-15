package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"hetman/internal/adapter"
)

// envBeat names the heartbeat file of the process the fake client leaves
// behind in the escape scenario.
const envBeat = "FAKE_CLIENT_BEAT"

func init() {
	// escape: start a process in a session of its own, as the client's
	// sandbox helper does, wait until it runs, then answer and exit.
	extraScenarios["escape"] = func(thread string) int {
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), envFake+"=beat")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			return 2
		}
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(os.Getenv(envBeat)); err == nil {
				break
			}
		}
		return replay("ok", thread, 0)
	}
	// beat: rewrite the heartbeat file with a counter, forever.
	extraScenarios["beat"] = func(string) int {
		for i := 0; ; i++ {
			os.WriteFile(os.Getenv(envBeat), []byte(strconv.Itoa(i)), 0o644)
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// A process the client started in a session of its own, outside the
// adapter's process group, does not outlive the adapter.
func TestNothingLeftRunning(t *testing.T) {
	beat := filepath.Join(t.TempDir(), "heartbeat")
	t.Setenv(envFake, "escape")
	t.Setenv(envBeat, beat)
	cmd := exec.Command(self(t), append([]string{"adapter", "--env", envBeat}, fakeArgs(t)...)...)
	cmd.Stdin = bytes.NewReader(adapter.EncodeStart(testStep(t.TempDir())))
	var out, diag bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diag
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v; standard error:\n%s", err, diag.String())
	}
	if f := final(parse(t, out.Bytes())); f.Type != adapter.TypeResult {
		t.Fatalf("final %+v", f)
	}
	first, err := os.ReadFile(beat)
	if err != nil {
		t.Fatalf("the process left behind never ran: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if second, _ := os.ReadFile(beat); !bytes.Equal(first, second) {
		t.Fatal("a process the client started is still alive after the adapter exited")
	}
}
