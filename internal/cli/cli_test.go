package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

// The test binary doubles as the hetman program ("cli ...") and as step
// programs ("step ..."), so the engine can be run and killed as a real
// separate process.
func TestMain(m *testing.M) {
	if os.Getenv(teststep.EnvHelper) == "1" && len(os.Args) > 1 {
		switch os.Args[1] {
		case "cli":
			os.Exit(Main(os.Args[2:], os.Stdout, os.Stderr))
		case "step":
			os.Exit(teststep.Main(os.Args[2:]))
		}
	}
	os.Setenv(teststep.EnvHelper, "1")
	os.Exit(m.Run())
}

func tempDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "hetman-cli-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 50 && os.RemoveAll(dir) != nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	})
	return dir
}

type world struct {
	t      *testing.T
	dir    string
	orders string
	sync   string // folder for block/release markers
}

func newWorld(t *testing.T, nodes string) *world {
	dir := tempDir(t)
	w := &world{t: t, dir: dir, orders: filepath.Join(dir, orders.DefaultFile), sync: filepath.Join(dir, "sync")}
	if err := os.Mkdir(w.sync, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "version: 0\nflow: test\nstart: a\nnodes:\n" + strings.ReplaceAll(nodes, "SYNC", strings.ReplaceAll(w.sync, "'", "''"))
	if err := os.WriteFile(w.orders, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return w
}

// process starts hetman as a separate process.
func (w *world) process(args ...string) (*exec.Cmd, *bytes.Buffer) {
	self, err := os.Executable()
	if err != nil {
		w.t.Fatal(err)
	}
	cmd := exec.Command(self, append([]string{"cli"}, append(args, "-f", w.orders)...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	return cmd, &out
}

// call runs hetman as a separate process and waits for it.
func (w *world) call(wantExit int, args ...string) string {
	w.t.Helper()
	cmd, out := w.process(args...)
	err := cmd.Wait()
	if code := cmd.ProcessState.ExitCode(); code != wantExit {
		w.t.Fatalf("hetman %v: exit %d (%v), want %d; output:\n%s", args, code, err, wantExit, out)
	}
	return out.String()
}

// waitFor waits for a marker written by a step of the engine whose output
// is out.
func (w *world) waitFor(name string, out *bytes.Buffer) {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(w.sync, name)); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for %s; engine output:\n%s", name, out)
}

func (w *world) release() {
	if err := os.WriteFile(filepath.Join(w.sync, "release"), nil, 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) work() string { return filepath.Join(w.dir, ".hetman", "campaigns", "1", "work") }

func (w *world) events() []chronicle.Event {
	w.t.Helper()
	ch, err := chronicle.Open(filepath.Join(w.dir, ".hetman", "chronicle.db"))
	if err != nil {
		w.t.Fatal(err)
	}
	defer ch.Close()
	ev, err := ch.Events(1)
	if err != nil {
		w.t.Fatal(err)
	}
	return ev
}

func steps(events []chronicle.Event) []string {
	var out []string
	for _, e := range events {
		switch e.Kind {
		case chronicle.StepStarted, chronicle.StepFinished, chronicle.StepInterrupted, chronicle.HaltRequested, chronicle.Halted, chronicle.Resumed:
			s := e.Kind + " " + e.Node
			if e.Attempt > 0 {
				s += " " + string(rune('0'+e.Attempt))
			}
			out = append(out, s)
		}
	}
	return out
}

var arg = teststep.Argv

func TestKilledEngineResumesInterruptedStep(t *testing.T) {
	w := newWorld(t, `  a: {type: script, run: `+arg("exit", "0")+`, on: {ok: b, fail: failed}}
  b: {type: script, run: `+arg("block", "SYNC")+`, on: {ok: c, fail: failed}}
  c: {type: script, run: `+arg("exit", "0")+`, on: {ok: d, fail: failed}}
  d: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	engineProc, out := w.process("run")
	w.waitFor("started.b.1", out)
	if err := engineProc.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	engineProc.Wait()
	t.Logf("killed engine output:\n%s", out)

	status := w.call(0, "status")
	if !strings.Contains(status, "running b (attempt 1), but no hetman process is running it") {
		t.Fatalf("status after kill:\n%s", status)
	}

	// Let a step orphaned by the kill finish, on systems where it survives
	// the engine (on Linux the kernel kills it with the engine).
	w.release()
	t.Cleanup(func() {
		if runtime.GOOS == "linux" {
			return
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(w.sync, "exited.b.1")); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	resumed := w.call(0, "resume")
	if !strings.Contains(resumed, "b attempt 1 was interrupted") || !strings.Contains(resumed, "campaign done") {
		t.Fatalf("resume output:\n%s", resumed)
	}
	trace := teststep.Trace(w.work())
	count := map[string]int{}
	for _, s := range trace {
		count[s]++
	}
	if count["a#1"] != 1 || count["b#2"] != 1 || count["c#1"] != 1 || count["d#1"] != 1 || count["a#2"] != 0 {
		t.Fatalf("trace %v", trace)
	}
	want := []string{
		"step_started a 1", "step_finished a 1",
		"step_started b 1",
		"resumed b",
		"step_interrupted b 1", "step_started b 2", "step_finished b 2",
		"step_started c 1", "step_finished c 1",
		"step_started d 1", "step_finished d 1",
	}
	if got := steps(w.events()); !reflect.DeepEqual(got, want) {
		t.Fatalf("chronicle\n got %v\nwant %v", got, want)
	}
	if status := w.call(0, "status"); !strings.Contains(status, "state: done") || !strings.Contains(status, "2 (1 interrupted)") {
		t.Fatalf("final status:\n%s", status)
	}
}

func TestHaltFromSecondProcessThenResume(t *testing.T) {
	w := newWorld(t, `  a: {type: script, run: `+arg("block", "SYNC")+`, on: {ok: b, fail: failed}}
  b: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	engineProc, out := w.process("run")
	w.waitFor("started.a.1", out)

	halt := w.call(0, "halt")
	if !strings.Contains(halt, "will halt after its current step") {
		t.Fatalf("halt output:\n%s", halt)
	}
	if status := w.call(0, "status"); !strings.Contains(status, "halt requested") {
		t.Fatalf("status during halt request:\n%s", status)
	}
	w.release()
	engineProc.Wait()
	if code := engineProc.ProcessState.ExitCode(); code != exitHalted {
		t.Fatalf("engine exit %d, want %d; output:\n%s", code, exitHalted, out)
	}
	if got := teststep.Trace(w.work()); !reflect.DeepEqual(got, []string{"a#1"}) {
		t.Fatalf("step after halt ran: %v", got)
	}
	// The halt survives the engine process: a new run of status sees it and
	// resume continues from the recorded position.
	if status := w.call(0, "status"); !strings.Contains(status, "halted before b") {
		t.Fatalf("status after halt:\n%s", status)
	}
	resumed := w.call(0, "resume")
	if !strings.Contains(resumed, "resumed at b") {
		t.Fatalf("resume output:\n%s", resumed)
	}
	if got := teststep.Trace(w.work()); !reflect.DeepEqual(got, []string{"a#1", "b#1"}) {
		t.Fatalf("trace %v", got)
	}
	want := []string{"step_started a 1", "halt_requested a", "step_finished a 1", "halted b", "resumed b", "step_started b 1", "step_finished b 1"}
	if got := steps(w.events()); !reflect.DeepEqual(got, want) {
		t.Fatalf("chronicle\n got %v\nwant %v", got, want)
	}
}

func TestCheckReportsLine(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "orders.yaml")
	os.WriteFile(path, []byte("version: 0\nstart: a\nnodes:\n  a: {type: script, run: [x], on: {ok: b, fail: failed}}\n"), 0o644)
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"check", "-f", path}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), path+":4:") || !strings.Contains(stderr.String(), `no such node`) {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestRunAndStatusInProcess(t *testing.T) {
	w := newWorld(t, `  a: {type: script, run: `+arg("exit", "0")+`, on: {ok: g, fail: failed}}
  g: {type: gate, checks: [style, unit], on: {pass: done, blocked: failed}}
checks:
  style: {run: `+arg("exit", "1")+`, soft: true}
  unit: {run: `+arg("exit", "0")+`}
`)
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"run", "-f", w.orders}, &stdout, &stderr); code != exitDone {
		t.Fatalf("run exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	Main([]string{"status", "-f", w.orders, "-events"}, &stdout, &stderr)
	s := stdout.String()
	for _, want := range []string{"state: done", "check style", "fail (warning)", "check unit", "warning: soft check style of g", "check_finished g check=style"} {
		if !strings.Contains(s, want) {
			t.Fatalf("status lacks %q:\n%s", want, s)
		}
	}
}
