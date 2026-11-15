package cli

import (
	"bytes"
	"fmt"
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
	"hetman/internal/scribe"
	"hetman/internal/stubadapter"
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
		case "adapter":
			os.Exit(stubadapter.Main(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		}
	}
	os.Setenv(teststep.EnvHelper, "1")
	// The interface language of the person running the tests is not the
	// one the tests expect.
	os.Unsetenv(languageEnv)
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

func TestInputFileAfterAKilledEngine(t *testing.T) {
	w := newWorld(t, `  a: {type: script, run: `+arg("block", "SYNC")+`, on: {ok: b, fail: failed}}
  b: {type: script, run: `+arg("input")+`, on: {ok: done, fail: failed}}
`)
	input := filepath.Join(w.dir, "input.json")
	if err := os.WriteFile(input, []byte(`{"task": "T-7"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	engineProc, out := w.process("run", "-input", input)
	w.waitFor("started.a.1", out)
	if err := engineProc.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	engineProc.Wait()
	// Someone edits the input file while nothing runs the campaign.
	file := filepath.Join(w.work(), "input.json")
	if data, err := os.ReadFile(file); err != nil || !strings.Contains(string(data), `"T-7"`) {
		t.Fatalf("input file before the kill: %q (%v)", data, err)
	}
	if err := os.WriteFile(file, []byte(`{"task": "other"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	w.release()
	w.call(0, "resume")
	for _, name := range []string{"seen.b.1.json", "input.json"} {
		if data, err := os.ReadFile(filepath.Join(w.work(), name)); err != nil || string(data) != "{\n  \"task\": \"T-7\"\n}\n" {
			t.Fatalf("%s after resume: %q (%v)", name, data, err)
		}
	}
}

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

	// A step orphaned by the kill on systems where it survives the engine
	// is killed by resume; releasing it lets it end by itself before that.
	w.release()

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

// diagnose renders everything needed to understand a failed campaign from
// a CI log alone: the full chronicle, the output of every engine process and
// every step log.
func (w *world) diagnose(outputs ...string) string {
	var b strings.Builder
	b.WriteString("chronicle:\n")
	for _, e := range w.events() {
		fmt.Fprintf(&b, "  %d %s\n", e.Seq, eventLine(e))
	}
	for i, o := range outputs {
		fmt.Fprintf(&b, "engine output %d:\n%s\n", i+1, o)
	}
	logs, _ := filepath.Glob(filepath.Join(w.dir, ".hetman", "campaigns", "1", "logs", "*.log"))
	for _, l := range logs {
		data, _ := os.ReadFile(l)
		fmt.Fprintf(&b, "log %s:\n%s\n", filepath.Base(l), data)
	}
	return b.String()
}

// waitForEvent waits until the chronicle holds an event matching match.
func (w *world) waitForEvent(match func(chronicle.Event) bool, out *bytes.Buffer) {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(w.dir, ".hetman", "chronicle.db")); err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		for _, e := range w.events() {
			if match(e) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for an event\n%s", w.diagnose(out.String()))
}

func TestResumeKillsProcessesLeftByKilledEngine(t *testing.T) {
	w := newWorld(t, `  a: {type: script, run: `+arg("orphan", "SYNC")+`, on: {ok: done, fail: failed}}
`)
	engineProc, out := w.process("run")
	w.waitFor("started.a.1", out)
	w.waitFor("heartbeat", out)
	// The step can be running before the engine has committed its
	// process_started event; kill only once the chronicle knows the process.
	w.waitForEvent(func(e chronicle.Event) bool {
		return e.Kind == chronicle.ProcessStarted && e.Node == "a" && e.Attempt == 1
	}, out)
	if err := engineProc.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	engineProc.Wait()
	killed := out.String()
	beat := filepath.Join(w.sync, "heartbeat")
	// On Unix the grandchild outlives the engine; on Windows the job object
	// kills it together with the engine.
	if alive := teststep.Beating(beat); alive != (runtime.GOOS != "windows") {
		t.Fatalf("grandchild alive after engine kill: %v\n%s", alive, w.diagnose(killed))
	}

	resumed := w.call(0, "resume")
	state, err := os.ReadFile(filepath.Join(w.sync, "old-grandchild"))
	if err != nil || string(state) != "dead" {
		t.Fatalf("attempt 2 saw the old grandchild %q (%v)\n%s", state, err, w.diagnose(killed, resumed))
	}
	if teststep.Beating(beat) {
		t.Fatalf("old grandchild still alive after resume\n%s", w.diagnose(killed, resumed))
	}
	var cleanup []chronicle.Event
	order := []string{}
	for _, e := range w.events() {
		switch e.Kind {
		case chronicle.ProcessCleanup:
			cleanup = append(cleanup, e)
			order = append(order, e.Kind)
		case chronicle.StepInterrupted, chronicle.StepStarted:
			order = append(order, e.Kind)
		}
	}
	want := []string{chronicle.StepStarted, chronicle.ProcessCleanup, chronicle.StepInterrupted, chronicle.StepStarted}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("event order %v, want %v\n%s", order, want, w.diagnose(killed, resumed))
	}
	wantOutcome := "killed"
	if runtime.GOOS == "windows" {
		wantOutcome = "gone"
	}
	if c := cleanup[0]; c.Outcome != wantOutcome || c.Attempt != 1 || c.Pid == 0 || c.Ident == "" {
		t.Fatalf("cleanup event %+v, want outcome %s\n%s", c, wantOutcome, w.diagnose(killed, resumed))
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

func TestScribeWritesSkill(t *testing.T) {
	t.Chdir(tempDir(t))
	path := filepath.Join(filepath.FromSlash(DefaultScribeDir), scribe.FileName)
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"scribe", "skill"}, &stdout, &stderr); code != exitDone || stdout.String() != "wrote "+path+"\n" {
		t.Fatalf("first scribe: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, scribe.Skill()) {
		t.Fatalf("skill file not written (%v)", err)
	}
	stdout.Reset()
	if code := Main([]string{"scribe", "skill"}, &stdout, &stderr); code != exitDone || stdout.String() != path+" is up to date\n" {
		t.Fatalf("second scribe: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
}

func TestScribeRefusesDifferentFile(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, scribe.FileName)
	os.WriteFile(path, []byte("my own notes\n"), 0o644)
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"scribe", "skill", "-dir", dir}, &stdout, &stderr); code != exitFailed {
		t.Fatalf("exit %d, want %d\n%s%s", code, exitFailed, stdout.String(), stderr.String())
	}
	if want := "hetman scribe skill: " + path + ": a different file already exists; use -force to replace it\n"; stderr.String() != want || stdout.Len() != 0 {
		t.Fatalf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	if data, _ := os.ReadFile(path); string(data) != "my own notes\n" {
		t.Fatal("existing file was changed without -force")
	}
	stderr.Reset()
	if code := Main([]string{"scribe", "skill", "-dir", dir, "-force"}, &stdout, &stderr); code != exitDone || stdout.String() != "wrote "+path+"\n" {
		t.Fatalf("scribe -force: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, scribe.Skill()) {
		t.Fatal("file not replaced with -force")
	}
}

func TestVersion(t *testing.T) {
	t.Chdir(tempDir(t))
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"version"}, &stdout, &stderr); code != exitDone || stdout.String() != "hetman dev\n" {
		t.Fatalf("exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
}

func TestCheckAndRunLookForPrograms(t *testing.T) {
	w := newWorld(t, `  a: {type: script, run: [hetman-no-such-program], on: {ok: done, fail: failed}}
`)
	warning := w.orders + `:5:27: warning: program "hetman-no-such-program" is not on the system path of this computer`
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"check", "-f", w.orders}, &stdout, &stderr); code != exitDone || !strings.Contains(stderr.String(), warning) {
		t.Fatalf("check with a missing bare program: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	// A warning does not stop run; the step that cannot start stops the
	// campaign in the error state.
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"run", "-f", w.orders}, &stdout, &stderr); code != exitError || !strings.Contains(stderr.String(), warning) {
		t.Fatalf("run with a missing bare program: exit %d\n%s%s", code, stdout.String(), stderr.String())
	}

	w = newWorld(t, `  a: {type: script, run: `+arg("exit", "0")+`, on: {ok: b, fail: failed}}
  b: {type: script, run: [./steps/missing.sh], on: {ok: done, fail: failed}}
`)
	problem := w.orders + `:6:27: program "./steps/missing.sh" does not exist`
	for _, cmd := range []string{"check", "run"} {
		stdout.Reset()
		stderr.Reset()
		if code := Main([]string{cmd, "-f", w.orders}, &stdout, &stderr); code != exitUsage || !strings.Contains(stderr.String(), problem) || !strings.Contains(stderr.String(), "orders rejected: 1 problem(s)") {
			t.Fatalf("%s with a missing path program: exit %d\n%s%s", cmd, code, stdout.String(), stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(w.dir, ".hetman")); !os.IsNotExist(err) {
		t.Fatalf("run with a missing program created the state folder (%v)", err)
	}
}

// The toy example runs its steps through sh; this runs it where sh exists.
func TestToyExample(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on the path")
	}
	src := filepath.Join("..", "..", "examples", "toy")
	dir := tempDir(t)
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOY_SLEEP", "0")
	path := filepath.Join(dir, orders.DefaultFile)
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"run", "-f", path}, &stdout, &stderr); code != exitDone {
		t.Fatalf("run exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	release, err := os.ReadFile(filepath.Join(dir, ".hetman", "campaigns", "1", "work", "release.txt"))
	if err != nil || strings.TrimSpace(string(release)) != "HELLO FROM CAMPAIGN 1" {
		t.Fatalf("release.txt %q (%v)\n%s", release, err, stdout.String())
	}
}
