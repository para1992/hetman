package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

func TestMain(m *testing.M) {
	if os.Getenv(teststep.EnvHelper) == "1" && len(os.Args) > 1 && os.Args[1] == "step" {
		os.Exit(teststep.Main(os.Args[2:]))
	}
	os.Setenv(teststep.EnvHelper, "1")
	os.Exit(m.Run())
}

// tempDir is like t.TempDir but tolerates files still held open by stray
// processes on systems that refuse to delete them.
func tempDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "hetman-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20 && os.RemoveAll(dir) != nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	})
	return dir
}

type fixture struct {
	t     *testing.T
	e     *Engine
	work  string
	chron *chronicle.Chronicle
}

func setup(t *testing.T, config string) *fixture {
	t.Helper()
	dir := tempDir(t)
	path := filepath.Join(dir, orders.DefaultFile)
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := orders.Load(path)
	if err != nil {
		t.Fatalf("orders rejected:\n%v\n%s", err, config)
	}
	if err := os.MkdirAll(StateDir(o), 0o755); err != nil {
		t.Fatal(err)
	}
	ch, err := chronicle.Open(ChroniclePath(o))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ch.Close() })
	id, err := ch.NewCampaign(o.Flow, o.Path, o.Start)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, e: &Engine{Orders: o, Chron: ch, Campaign: id}, work: filepath.Join(CampaignDir(o, id), "work"), chron: ch}
}

func (f *fixture) drive(want Result) {
	f.t.Helper()
	got, err := f.e.Drive(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	if got != want {
		f.t.Fatalf("drive ended %s, want %s; events:\n%s", got, want, f.dump())
	}
}

func (f *fixture) events(kind string) []chronicle.Event {
	f.t.Helper()
	all, err := f.chron.Events(f.e.Campaign)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []chronicle.Event
	for _, e := range all {
		if kind == "" || e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func (f *fixture) dump() string {
	var b strings.Builder
	for _, e := range f.events("") {
		b.WriteString(e.Kind + " " + e.Node + " " + e.Check + " " + strconv.Itoa(e.Attempt) + " " + e.Outcome + " " + e.Error + "\n")
	}
	return b.String()
}

func (f *fixture) record(e chronicle.Event) {
	f.t.Helper()
	if err := f.e.record(e); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) trace(want ...string) {
	f.t.Helper()
	if got := teststep.Trace(f.work); !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
		f.t.Fatalf("trace %v, want %v", got, want)
	}
}

var arg = teststep.Argv

func TestLinearFlowEnvironmentAndResultFile(t *testing.T) {
	f := setup(t, `version: 0
flow: linear
start: a
nodes:
  a: {type: script, run: `+arg("env")+`, on: {ok: b, fail: failed}}
  b: {type: script, run: `+arg("artifact")+`, on: {ok: c, fail: failed}}
  c: {type: script, run: `+arg("outcome", "skip", "1")+`, on: {ok: failed, fail: failed, skip: d}}
  d: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	f.drive(Done)
	f.trace("a#1", "b#1", "c#1", "d#1")

	data, err := os.ReadFile(filepath.Join(f.work, "env.a.1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]string
	json.Unmarshal(data, &env)
	dir := CampaignDir(f.e.Orders, f.e.Campaign)
	want := map[string]string{
		"HETMAN_RUN_ID":      strconv.FormatInt(f.e.Campaign, 10),
		"HETMAN_NODE_ID":     "a",
		"HETMAN_ATTEMPT":     "1",
		"HETMAN_RUN_DIR":     filepath.Join(dir, "work"),
		"HETMAN_RESULT_FILE": filepath.Join(dir, "results", "a.1.json"),
	}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env %v, want %v", env, want)
	}
	fin := f.events(chronicle.StepFinished)
	if fin[1].Node != "b" || !reflect.DeepEqual(fin[1].Artifacts, []string{"out.txt"}) {
		t.Fatalf("artifacts of b: %+v", fin[1])
	}
	if fin[2].Outcome != "skip" || *fin[2].Exit != 1 {
		t.Fatalf("result file must override exit code: %+v", fin[2])
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "a.1.log")); err != nil {
		t.Fatalf("step log: %v", err)
	}
}

func TestNonZeroExitIsFail(t *testing.T) {
	f := setup(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("exit", "3")+`, on: {ok: done, fail: failed}}
`)
	f.drive(Failed)
	if fin := f.events(chronicle.StepFinished); fin[0].Outcome != orders.Fail || *fin[0].Exit != 3 {
		t.Fatalf("finish %+v", fin[0])
	}
}

func TestGateRequiredCheckBlocksAndStopsTheRest(t *testing.T) {
	f := setup(t, `version: 0
start: g
checks:
  lint:  {run: `+arg("exit", "1")+`, soft: true}
  unit:  {run: `+arg("exit", "0")+`}
  deep:  {run: `+arg("exit", "1")+`}
  never: {run: `+arg("exit", "0")+`}
nodes:
  g:
    type: gate
    checks: [lint, unit, deep, never]
    on: {pass: done, blocked: failed}
`)
	f.drive(Failed)
	f.trace("g.lint#1", "g.unit#1", "g.deep#1")
	checks := f.events(chronicle.CheckFinished)
	got := []string{}
	for _, c := range checks {
		got = append(got, c.Check+"="+c.Outcome+"/soft="+strconv.FormatBool(c.Soft))
	}
	want := []string{"lint=fail/soft=true", "unit=ok/soft=false", "deep=fail/soft=false"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checks %v, want %v", got, want)
	}
	if fin := f.events(chronicle.StepFinished); fin[0].Outcome != orders.Blocked {
		t.Fatalf("gate outcome %q", fin[0].Outcome)
	}
}

func TestGateSoftCheckFailureOnlyWarns(t *testing.T) {
	f := setup(t, `version: 0
start: g
checks:
  style: {run: `+arg("exit", "1")+`, soft: true}
  unit:  {run: `+arg("exit", "0")+`}
nodes:
  g: {type: gate, checks: [style, unit], on: {pass: done, blocked: failed}}
`)
	f.drive(Done)
	f.trace("g.style#1", "g.unit#1")
	if fin := f.events(chronicle.StepFinished); fin[0].Outcome != orders.Pass {
		t.Fatalf("gate outcome %q", fin[0].Outcome)
	}
}

func TestSingleCommandGate(t *testing.T) {
	for code, want := range map[string]Result{"0": Done, "1": Failed} {
		f := setup(t, `version: 0
start: g
nodes:
  g: {type: gate, run: `+arg("exit", code)+`, on: {pass: done, blocked: failed}}
`)
		f.drive(want)
	}
}

func TestTimeoutKillsProcessTree(t *testing.T) {
	f := setup(t, `version: 0
start: a
nodes:
  a:
    type: script
    run: `+arg("spawn", "1h")+`
    timeout: 1s
    on: {ok: failed, fail: failed, timeout: done}
`)
	began := time.Now()
	f.drive(Done)
	if d := time.Since(began); d > 20*time.Second {
		t.Fatalf("timeout took %s", d)
	}
	beat := filepath.Join(f.work, "heartbeat")
	time.Sleep(300 * time.Millisecond)
	first, err := os.ReadFile(beat)
	if err != nil {
		t.Fatalf("grandchild never ran: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	second, _ := os.ReadFile(beat)
	if string(first) != string(second) {
		t.Fatalf("grandchild still alive after timeout: heartbeat %s -> %s", first, second)
	}
}

func TestOutcomeWithoutTransitionStopsInError(t *testing.T) {
	config := `version: 0
start: a
nodes:
  a: {type: script, run: ` + arg("outcome", "weird") + `, on: {ok: done, fail: failed%s}}
`
	f := setup(t, strings.Replace(config, "%s", "", 1))
	f.drive(Errored)
	if errs := f.events(chronicle.Stopped); len(errs) != 1 || !strings.Contains(errs[0].Error, `"weird"`) {
		t.Fatalf("error events %+v", errs)
	}
	// A person fixes the orders and resumes: the finished step is not run
	// again, its recorded outcome now has a transition.
	fixed, err := orders.Parse(f.e.Orders.Path, []byte(strings.Replace(config, "%s", ", weird: done", 1)))
	if err != nil {
		t.Fatal(err)
	}
	f.e.Orders = fixed
	f.record(chronicle.Event{Kind: chronicle.Resumed})
	f.drive(Done)
	f.trace("a#1")
}

func TestBadResultFileStopsInError(t *testing.T) {
	f := setup(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("badresult")+`, on: {ok: done, fail: failed}}
`)
	f.drive(Errored)
}

func TestMissingProgramStopsInError(t *testing.T) {
	f := setup(t, `version: 0
start: a
nodes:
  a: {type: script, run: [./no-such-program], on: {ok: done, fail: failed}}
`)
	f.drive(Errored)
	if errs := f.events(chronicle.Stopped); !strings.Contains(errs[0].Error, "cannot start") {
		t.Fatalf("error %q", errs[0].Error)
	}
}

const twoSteps = `version: 0
start: a
nodes:
  a: {type: script, run: %s, on: {ok: b, fail: failed}}
  b: {type: script, run: %s, on: {ok: done, fail: failed}}
`

func twoStepOrders() string {
	return strings.Replace(strings.Replace(twoSteps, "%s", arg("exit", "0"), 1), "%s", arg("exit", "0"), 1)
}

func TestFinishedStepIsNeverRunAgain(t *testing.T) {
	f := setup(t, twoStepOrders())
	// The previous engine finished a and died before recording the transition.
	f.record(chronicle.Event{Kind: chronicle.StepStarted, Node: "a", Attempt: 1})
	f.record(chronicle.Event{Kind: chronicle.StepFinished, Node: "a", Attempt: 1, Outcome: orders.OK})
	f.drive(Done)
	f.trace("b#1")
}

func TestInterruptedStepRunsAgainWithNextAttempt(t *testing.T) {
	f := setup(t, twoStepOrders())
	f.record(chronicle.Event{Kind: chronicle.StepStarted, Node: "a", Attempt: 1})
	f.record(chronicle.Event{Kind: chronicle.StepFinished, Node: "a", Attempt: 1, Outcome: orders.OK})
	f.record(chronicle.Event{Kind: chronicle.Transition, Node: "a", Outcome: orders.OK, Details: chronicle.Details{To: "b"}})
	f.record(chronicle.Event{Kind: chronicle.StepStarted, Node: "b", Attempt: 1})
	f.drive(Done)
	f.trace("b#2")
	if in := f.events(chronicle.StepInterrupted); len(in) != 1 || in[0].Node != "b" || in[0].Attempt != 1 {
		t.Fatalf("interrupted events %+v", in)
	}
	// No process_started was recorded for b attempt 1, and the chronicle
	// still says that the attempt was checked.
	if c := f.events(chronicle.ProcessCleanup); len(c) != 1 || c[0].Outcome != cleanupUnrecorded || c[0].Node != "b" || c[0].Attempt != 1 {
		t.Fatalf("cleanup events %+v", c)
	}
}

func TestHaltTakesEffectBeforeTheNextStep(t *testing.T) {
	f := setup(t, twoStepOrders())
	f.record(chronicle.Event{Kind: chronicle.HaltRequested})
	f.drive(Halted)
	f.trace()
	if h := f.events(chronicle.Halted); len(h) != 1 || h[0].Node != "a" {
		t.Fatalf("halted events %+v", h)
	}
	f.drive(Halted) // still halted on a second drive, as after a restart
	f.record(chronicle.Event{Kind: chronicle.Resumed})
	f.drive(Done)
	f.trace("a#1", "b#1")
}

func TestLockIsExclusive(t *testing.T) {
	f := setup(t, twoStepOrders())
	release, err := Lock(f.e.Orders, f.e.Campaign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(f.e.Orders, f.e.Campaign); err != ErrLocked {
		t.Fatalf("second lock: %v, want ErrLocked", err)
	}
	release()
	release, err = Lock(f.e.Orders, f.e.Campaign)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release()
}

func TestCleanupStaleGuardsAgainstReusedIds(t *testing.T) {
	ident, alive := processIdent(os.Getpid())
	if !alive || ident == "" {
		t.Fatalf("cannot read own start time: %q %v", ident, alive)
	}
	if again, _ := processIdent(os.Getpid()); again != ident {
		t.Fatalf("start time changed: %q -> %q", ident, again)
	}
	// A recorded start time that does not match means the id was reused.
	if got := cleanupStale(os.Getpid(), ident+"0"); got != cleanupReused {
		t.Fatalf("got %s, want %s", got, cleanupReused)
	}
	if got := cleanupStale(os.Getpid(), ""); got != cleanupUnverified {
		t.Fatalf("got %s, want %s", got, cleanupUnverified)
	}
}

func TestCleanupStaleKillsLiveTreeAndReportsGone(t *testing.T) {
	self, _ := os.Executable()
	beat := filepath.Join(tempDir(t), "beat")
	cmd := exec.Command(self, "step", "heartbeat", beat)
	configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	ident, alive := processIdent(cmd.Process.Pid)
	if !alive {
		t.Fatal("helper not alive")
	}
	if got := cleanupStale(cmd.Process.Pid, ident); got != cleanupKilled {
		t.Fatalf("got %s, want %s", got, cleanupKilled)
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("helper still running after cleanup")
	}
	if got := cleanupStale(cmd.Process.Pid, ident); got != cleanupGone {
		t.Fatalf("second cleanup: got %s, want %s", got, cleanupGone)
	}
}
