package dispatch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
	"hetman/internal/cli"
	"hetman/internal/dispatch"
	"hetman/internal/engine"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

// The test binary doubles as the hetman program ("cli ...") and as step
// programs ("step ..."), so a dispatcher can be run and killed as a real
// separate process.
func TestMain(m *testing.M) {
	if os.Getenv(teststep.EnvHelper) == "1" && len(os.Args) > 1 {
		switch os.Args[1] {
		case "cli":
			os.Exit(cli.Main(os.Args[2:], os.Stdout, os.Stderr))
		case "step":
			os.Exit(step(os.Args[2:]))
		}
	}
	os.Setenv(teststep.EnvHelper, "1")
	os.Exit(m.Run())
}

// step runs the steps of these tests, and those of teststep otherwise.
//
//	span DIR LABEL TIME  write DIR/span.<campaign>.<label> holding the start
//	                     time, sleep, then add the end time (Unix nanoseconds)
//	hold DIR             write DIR/started.<campaign>.<node>.<attempt>, wait
//	                     for DIR/release, then exit 0
func step(args []string) int {
	run, node, attempt := os.Getenv("HETMAN_RUN_ID"), os.Getenv("HETMAN_NODE_ID"), os.Getenv("HETMAN_ATTEMPT")
	switch args[0] {
	case "span":
		path := filepath.Join(args[1], "span."+run+"."+args[2])
		start := strconv.FormatInt(time.Now().UnixNano(), 10)
		os.WriteFile(path+".tmp", []byte(start), 0o644)
		os.Rename(path+".tmp", path)
		d, _ := time.ParseDuration(args[3])
		time.Sleep(d)
		end := strconv.FormatInt(time.Now().UnixNano(), 10)
		os.WriteFile(path+".tmp", []byte(start+" "+end), 0o644)
		os.Rename(path+".tmp", path)
		return 0
	case "hold":
		appendLine(filepath.Join(args[1], "trace"), run+" "+node+" "+attempt)
		os.WriteFile(filepath.Join(args[1], "started."+run+"."+node+"."+attempt), nil, 0o644)
		for {
			if _, err := os.Stat(filepath.Join(args[1], "release")); err == nil {
				return 0
			}
			time.Sleep(20 * time.Millisecond)
		}
	case "once":
		appendLine(filepath.Join(args[1], "trace"), run+" "+node+" "+attempt)
		return 0
	}
	return teststep.Main(args)
}

func appendLine(path, line string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

var arg = teststep.Argv

type world struct {
	t      *testing.T
	dir    string
	sync   string
	routes string
}

func newWorld(t *testing.T) *world {
	dir, err := os.MkdirTemp("", "hetman-dispatch-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 50 && os.RemoveAll(dir) != nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	})
	w := &world{t: t, dir: dir, sync: filepath.Join(dir, "sync"), routes: filepath.Join(dir, dispatch.DefaultRoutesFile)}
	if err := os.Mkdir(w.sync, 0o755); err != nil {
		t.Fatal(err)
	}
	return w
}

// write writes a file of the world; SYNC in it becomes the sync folder.
func (w *world) write(name, text string) {
	w.t.Helper()
	text = strings.ReplaceAll(text, "SYNC", strings.ReplaceAll(w.sync, "'", "''"))
	if err := os.WriteFile(filepath.Join(w.dir, name), []byte(text), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

// flow writes an orders file whose nodes are given.
func (w *world) flow(name, start, nodes string) {
	w.write(name, "version: 0\nflow: "+strings.TrimSuffix(name, ".yaml")+"\nstart: "+start+"\nnodes:\n"+nodes)
}

func (w *world) load() *dispatch.Routes {
	w.t.Helper()
	r, _, err := dispatch.LoadRoutes(w.routes)
	if err != nil {
		w.t.Fatalf("routes rejected: %v", err)
	}
	return r
}

type env struct {
	w      *world
	routes *dispatch.Routes
	queue  *dispatch.Queue
	chron  *chronicle.Chronicle
}

func (w *world) open() *env {
	w.t.Helper()
	r := w.load()
	if err := os.MkdirAll(filepath.Join(w.dir, ".hetman"), 0o755); err != nil {
		w.t.Fatal(err)
	}
	q, err := dispatch.OpenQueue(dispatch.QueuePath(w.dir))
	if err != nil {
		w.t.Fatal(err)
	}
	ch, err := chronicle.Open(filepath.Join(w.dir, ".hetman", "chronicle.db"))
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { q.Close(); ch.Close() })
	return &env{w: w, routes: r, queue: q, chron: ch}
}

func (e *env) add(kind, input, key string) dispatch.Task {
	e.w.t.Helper()
	in, err := dispatch.CheckInput([]byte(input))
	if err != nil {
		e.w.t.Fatal(err)
	}
	t, added, err := e.queue.Add(kind, in, key, false)
	if err != nil || !added {
		e.w.t.Fatalf("add %s: added %v, %v", kind, added, err)
	}
	return t
}

// dispatcher returns a dispatcher with a short poll.
func (e *env) dispatcher(once bool, out *syncBuffer) *dispatch.Dispatcher {
	return &dispatch.Dispatcher{Routes: e.routes, Queue: e.queue, Chron: e.chron, Once: once, Poll: 30 * time.Millisecond, Out: out}
}

// runOnce dispatches until nothing is queued or running.
func (e *env) runOnce() string {
	e.w.t.Helper()
	var out syncBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := e.dispatcher(true, &out).Run(ctx); err != nil {
		e.w.t.Fatalf("dispatch: %v\n%s", err, out.String())
	}
	if ctx.Err() != nil {
		e.w.t.Fatalf("dispatch did not finish:\n%s", out.String())
	}
	return out.String()
}

func (e *env) views() map[int64]dispatch.View {
	e.w.t.Helper()
	list, err := e.queue.List(1000)
	if err != nil {
		e.w.t.Fatal(err)
	}
	views, err := dispatch.Views(list, e.chron, e.w.dir, nil)
	if err != nil {
		e.w.t.Fatal(err)
	}
	out := map[int64]dispatch.View{}
	for _, v := range views {
		out[v.ID] = v
	}
	return out
}

func (e *env) waitState(id int64, state string) dispatch.View {
	e.w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if v := e.views()[id]; v.State == state {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.w.t.Fatalf("task %d did not reach %s: %+v", id, state, e.views()[id])
	return dispatch.View{}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// hetman runs the command line in this process.
func (w *world) hetman(wantExit int, stdin string, args ...string) string {
	w.t.Helper()
	self, err := os.Executable()
	if err != nil {
		w.t.Fatal(err)
	}
	cmd := exec.Command(self, append([]string{"cli"}, append(args, "-r", w.routes)...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Run()
	if code := cmd.ProcessState.ExitCode(); code != wantExit {
		w.t.Fatalf("hetman %v: exit %d, want %d; output:\n%s", args, code, wantExit, out.String())
	}
	return out.String()
}

const quickFlow = "  a:\n    type: script\n    run: " + "STEP" + "\n    on: {ok: done, fail: failed}\n"

func quick(stepArgs ...string) string { return strings.ReplaceAll(quickFlow, "STEP", arg(stepArgs...)) }

func TestDeduplication(t *testing.T) {
	w := newWorld(t)
	w.flow("fix.yaml", "a", quick("exit", "0"))
	w.write(dispatch.DefaultRoutesFile, "version: 0\nroutes:\n  finding: {orders: fix.yaml}\n")
	e := w.open()
	in := []byte(`{"file": "a.txt"}`)

	first, added, err := e.queue.Add("finding", in, "k1", false)
	if err != nil || !added {
		t.Fatalf("first add: %v %v", added, err)
	}
	// While queued, the same key adds nothing.
	again, added, err := e.queue.Add("finding", in, "k1", false)
	if err != nil || added || again.ID != first.ID {
		t.Fatalf("queued duplicate: added %v, task %d, %v", added, again.ID, err)
	}
	// No key never de-duplicates.
	for i := 0; i < 2; i++ {
		if _, added, _ := e.queue.Add("finding", in, "", false); !added {
			t.Fatal("a task without a key was not added")
		}
	}
	// A cancelled task frees its key.
	if _, err := e.queue.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	second, added, err := e.queue.Add("finding", in, "k1", false)
	if err != nil || !added {
		t.Fatalf("add after cancel: %v %v", added, err)
	}
	// Once finished, the key still blocks.
	e.runOnce()
	if v := e.views()[second.ID]; v.State != dispatch.StateDone {
		t.Fatalf("task %d: %s", second.ID, v.State)
	}
	if got, added, _ := e.queue.Add("finding", in, "k1", false); added || got.ID != second.ID {
		t.Fatalf("finished duplicate: added %v, task %d", added, got.ID)
	}
	// Force adds it again.
	forced, added, err := e.queue.Add("finding", in, "k1", true)
	if err != nil || !added || forced.ID == second.ID {
		t.Fatalf("forced: %v %v", added, err)
	}
	// The command line says so and exits 0.
	out := w.hetman(0, `{"file": "a.txt"}`, "task", "add", "-kind", "finding", "-key", "k1", "-input", "-")
	if !strings.Contains(out, fmt.Sprintf("task %d already has the key \"k1\" and is queued; nothing added", forced.ID)) {
		t.Fatalf("duplicate add said:\n%s", out)
	}
}

func TestUnknownKindAndBadInputRefused(t *testing.T) {
	w := newWorld(t)
	w.flow("fix.yaml", "a", quick("exit", "0"))
	w.write(dispatch.DefaultRoutesFile, "version: 0\nroutes:\n  finding: {orders: fix.yaml}\n  remark: {orders: fix.yaml}\n")
	out := w.hetman(2, "", "task", "add", "-kind", "typo")
	if !strings.Contains(out, `no route for task kind "typo"`) || !strings.Contains(out, "routed kinds: finding, remark") ||
		!strings.Contains(out, "nothing was added") {
		t.Fatalf("unknown kind said:\n%s", out)
	}
	out = w.hetman(2, `{"hetman_task": 1}`, "task", "add", "-kind", "finding", "-input", "-")
	if !strings.Contains(out, `must not have the field "hetman_task"`) {
		t.Fatalf("reserved field said:\n%s", out)
	}
	w.hetman(2, `[1, 2]`, "task", "add", "-kind", "finding", "-input", "-")
	if out := w.hetman(0, "", "task", "list"); !strings.Contains(out, "no tasks") {
		t.Fatalf("something was added:\n%s", out)
	}
	out = w.hetman(0, `{"n": 1}`, "task", "add", "-kind", "finding", "-input", "-")
	if strings.TrimSpace(out) != "task 1 queued (kind finding)" {
		t.Fatalf("add said:\n%s", out)
	}
}

// spans reads the recorded step times: label and interval per campaign.
type span struct {
	campaign   int
	label      string
	start, end int64
}

func readSpans(t *testing.T, dir string) []span {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "span.*"))
	var out []span
	for _, f := range files {
		parts := strings.SplitN(filepath.Base(f), ".", 3)
		data, _ := os.ReadFile(f)
		fields := strings.Fields(string(data))
		if len(fields) != 2 {
			t.Fatalf("%s: unfinished span %q", f, data)
		}
		var s span
		s.campaign, _ = strconv.Atoi(parts[1])
		s.label = parts[2]
		s.start, _ = strconv.ParseInt(fields[0], 10, 64)
		s.end, _ = strconv.ParseInt(fields[1], 10, 64)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}

// most returns the highest number of spans with the label (all for "")
// that overlap at one moment.
func most(spans []span, label string) int {
	type edge struct {
		at    int64
		delta int
	}
	var edges []edge
	for _, s := range spans {
		if label == "" || s.label == label {
			edges = append(edges, edge{s.start, 1}, edge{s.end, -1})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].at != edges[j].at {
			return edges[i].at < edges[j].at
		}
		return edges[i].delta < edges[j].delta
	})
	n, max := 0, 0
	for _, e := range edges {
		n += e.delta
		if n > max {
			max = n
		}
	}
	return max
}

func TestPriorityThenAge(t *testing.T) {
	w := newWorld(t)
	w.flow("low.yaml", "a", quick("span", "SYNC", "low", "10ms"))
	w.flow("high.yaml", "a", quick("span", "SYNC", "high", "10ms"))
	w.write(dispatch.DefaultRoutesFile, "version: 0\nlimit: 1\nroutes:\n  low: {orders: low.yaml}\n  high: {orders: high.yaml, priority: 5}\n")
	e := w.open()
	l1 := e.add("low", `{}`, "")
	h1 := e.add("high", `{}`, "")
	l2 := e.add("low", `{}`, "")
	h2 := e.add("high", `{}`, "")
	e.runOnce()
	byCampaign := map[int64]int64{}
	for id, v := range e.views() {
		if v.State != dispatch.StateDone {
			t.Fatalf("task %d: %s", id, v.State)
		}
		byCampaign[v.Campaign] = id
	}
	var order []int64
	for _, s := range readSpans(t, w.sync) {
		order = append(order, byCampaign[int64(s.campaign)])
	}
	want := []int64{h1.ID, h2.ID, l1.ID, l2.ID}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("tasks ran in the order %v, want %v (high priority first, then the oldest)", order, want)
	}
}

func TestLimits(t *testing.T) {
	w := newWorld(t)
	w.flow("x.yaml", "a", quick("span", "SYNC", "x", "250ms"))
	w.flow("y.yaml", "a", quick("span", "SYNC", "y", "250ms"))
	w.write(dispatch.DefaultRoutesFile, "version: 0\nlimit: 2\nroutes:\n  x: {orders: x.yaml, limit: 1}\n  y: {orders: y.yaml}\n")
	e := w.open()
	for i := 0; i < 4; i++ {
		e.add("x", `{}`, "")
		e.add("y", `{}`, "")
	}
	e.runOnce()
	spans := readSpans(t, w.sync)
	if len(spans) != 8 {
		t.Fatalf("%d spans recorded, want 8", len(spans))
	}
	if n := most(spans, ""); n != 2 {
		t.Fatalf("at most %d campaigns ran at once, want exactly the limit 2", n)
	}
	if n := most(spans, "x"); n != 1 {
		t.Fatalf("at most %d campaigns of route x ran at once, want 1", n)
	}

	// The dispatcher's own limit overrides the routing file.
	w2 := newWorld(t)
	w2.flow("y.yaml", "a", quick("span", "SYNC", "y", "200ms"))
	w2.write(dispatch.DefaultRoutesFile, "version: 0\nlimit: 1\nroutes:\n  y: {orders: y.yaml}\n")
	e2 := w2.open()
	for i := 0; i < 6; i++ {
		e2.add("y", `{}`, "")
	}
	var out syncBuffer
	d := e2.dispatcher(true, &out)
	d.Limit = 3
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := most(readSpans(t, w2.sync), ""); n != 3 {
		t.Fatalf("with -limit 3 at most %d ran at once\n%s", n, out.String())
	}
}

func TestKilledDispatcherRestarts(t *testing.T) {
	w := newWorld(t)
	nodes := "  a:\n    type: script\n    run: " + arg("once", "SYNC") + "\n    on: {ok: b, fail: failed}\n" +
		"  b:\n    type: script\n    run: " + arg("hold", "SYNC") + "\n    on: {ok: done, fail: failed}\n"
	w.flow("fix.yaml", "a", nodes)
	w.write(dispatch.DefaultRoutesFile, "version: 0\nlimit: 2\nroutes:\n  finding: {orders: fix.yaml}\n")
	e := w.open()
	t1 := e.add("finding", `{"n": 1}`, "")
	t2 := e.add("finding", `{"n": 2}`, "")

	self, _ := os.Executable()
	first := exec.Command(self, "cli", "dispatch", "-r", w.routes, "-poll", "30ms")
	var out1 bytes.Buffer
	first.Stdout, first.Stderr = &out1, &out1
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"1", "2"} {
		waitFile(t, filepath.Join(w.sync, "started."+c+".b.1"), &out1)
	}
	// A second dispatcher for the same folder is refused while one runs.
	if out := w.hetman(1, "", "dispatch", "-once"); !strings.Contains(out, "another dispatcher runs") {
		t.Fatalf("second dispatcher said:\n%s", out)
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	first.Wait()
	for _, id := range []int64{t1.ID, t2.ID} {
		if v := e.views()[id]; v.State != dispatch.StateRunning || v.Driven {
			t.Fatalf("after the kill task %d is %s (driven %v)", id, v.State, v.Driven)
		}
	}

	second := exec.Command(self, "cli", "dispatch", "-r", w.routes, "-poll", "30ms", "-once")
	var out2 bytes.Buffer
	second.Stdout, second.Stderr = &out2, &out2
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"1", "2"} {
		waitFile(t, filepath.Join(w.sync, "started."+c+".b.2"), &out2)
	}
	os.WriteFile(filepath.Join(w.sync, "release"), nil, 0o644)
	if err := second.Wait(); err != nil {
		t.Fatalf("second dispatcher: %v\n%s", err, out2.String())
	}

	list, _ := e.chron.Campaigns(100)
	if len(list) != 2 {
		t.Fatalf("%d campaigns, want 2: a task was started twice\n%s", len(list), out2.String())
	}
	for _, id := range []int64{t1.ID, t2.ID} {
		if v := e.views()[id]; v.State != dispatch.StateDone {
			t.Fatalf("task %d: %s\n%s", id, v.State, out2.String())
		}
	}
	// The finished step ran once per campaign; only the step cut off by
	// the kill ran again.
	trace, _ := os.ReadFile(filepath.Join(w.sync, "trace"))
	lines := strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(trace)), " ", "/"))
	sort.Strings(lines)
	want := []string{"1/a/1", "1/b/1", "1/b/2", "2/a/1", "2/b/1", "2/b/2"}
	if fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Fatalf("steps ran %v, want %v", lines, want)
	}
	for _, k := range list {
		events, _ := e.chron.Events(k.ID)
		interrupted := 0
		for _, ev := range events {
			if ev.Kind == chronicle.StepInterrupted {
				interrupted++
			}
		}
		if interrupted != 1 {
			t.Fatalf("campaign %d: %d interrupted steps, want 1", k.ID, interrupted)
		}
	}
	if !strings.Contains(out2.String(), "continues at b") {
		t.Fatalf("restarted dispatcher said:\n%s", out2.String())
	}
}

func waitFile(t *testing.T, path string, out *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; output:\n%s", path, out.String())
}

func TestTaskWaitingForAPersonIsAnsweredAndCompletes(t *testing.T) {
	w := newWorld(t)
	w.write("remark.yaml", "version: 0\nflow: remark\nstart: decide\nnotify: {run: "+arg("notice", "SYNC")+"}\nnodes:\n"+"  decide:\n    type: human\n    ask:\n      - id: how\n        question: What should be done?\n        kind: text\n"+
		"    on: {fix: a, decline: done}\n"+strings.ReplaceAll(quickFlow, "STEP", arg("once", "SYNC")))
	quickFile := filepath.Join(w.dir, "quick.yaml")
	w.flow("quick.yaml", "a", quick("span", "SYNC", "q", "10ms"))
	w.write(dispatch.DefaultRoutesFile, "version: 0\nlimit: 1\nroutes:\n  remark: {orders: remark.yaml}\n  quick: {orders: quick.yaml}\n")
	e := w.open()
	r := e.add("remark", `{}`, "")
	q := e.add("quick", `{}`, "")

	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.dispatcher(false, &out).Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()

	v := e.waitState(r.ID, dispatch.StateWaiting)
	if !strings.Contains(v.Waits, "a person to decide at decide (fix, decline)") || v.Command == "" {
		t.Fatalf("waiting task: %+v", v)
	}
	// While it waits it takes no place: the other task runs despite limit 1.
	e.waitState(q.ID, dispatch.StateDone)
	if v := e.views()[r.ID]; v.Driven {
		t.Fatal("the dispatcher keeps the waiting campaign locked")
	}
	if _, err := engine.Answer(e.chron, v.Campaign, engine.Reply{By: "tester", Via: engine.ViaCommand,
		Given: ask.Given{Values: map[string]string{"how": "fix it"}, Outcome: "fix"}}); err != nil {
		t.Fatal(err)
	}
	e.waitState(r.ID, dispatch.StateDone)
	if !strings.Contains(out.String(), "continues at decide") {
		t.Fatalf("dispatcher said:\n%s", out.String())
	}
	// The notification hook was told which task the campaign handles.
	data, err := os.ReadFile(filepath.Join(w.sync, "notice.human.decide.json"))
	if err != nil {
		t.Fatal(err)
	}
	var n engine.Notice
	if err := json.Unmarshal(data, &n); err != nil || n.Task == nil || n.Task.ID != r.ID || n.Task.Kind != "remark" {
		t.Fatalf("notice: %s (%v)", data, err)
	}
	// Status shows the campaign by its own flow even with the other
	// orders file of the folder, and names its task.
	st := w.hetmanF(0, quickFile, "status", "-c", strconv.FormatInt(v.Campaign, 10))
	if !strings.Contains(st, "decide  human") || !strings.Contains(st, fmt.Sprintf("task %d (kind remark)", r.ID)) {
		t.Fatalf("status said:\n%s", st)
	}
}

func TestHaltedTaskIsLeftToAPersonWhoResumesIt(t *testing.T) {
	w := newWorld(t)
	nodes := "  a:\n    type: script\n    run: " + arg("hold", "SYNC") + "\n    on: {ok: b, fail: failed}\n" +
		"  b:\n    type: script\n    run: " + arg("once", "SYNC") + "\n    on: {ok: done, fail: failed}\n"
	w.flow("fix.yaml", "a", nodes)
	w.write(dispatch.DefaultRoutesFile, "version: 0\nroutes:\n  finding: {orders: fix.yaml}\n")
	e := w.open()
	task := e.add("finding", `{}`, "")

	var out syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.dispatcher(false, &out).Run(ctx) }()
	defer func() { cancel(); <-done }()

	waitFile(t, filepath.Join(w.sync, "started.1.a.1"), &bytes.Buffer{})
	o, err := orders.Load(filepath.Join(w.dir, "fix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if res, _, err := engine.Halt(o, e.chron, 1); err != nil || res != engine.HaltPending {
		t.Fatalf("halt: %s %v", res, err)
	}
	os.WriteFile(filepath.Join(w.sync, "release"), nil, 0o644)
	e.waitState(task.ID, dispatch.StateHalted)
	time.Sleep(200 * time.Millisecond)
	if v := e.views()[task.ID]; v.State != dispatch.StateHalted || v.Driven {
		t.Fatalf("the dispatcher did not leave the halted campaign alone: %+v", v)
	}
	// A person resumes it from the command line; that process drives it.
	if got := w.hetmanF(0, filepath.Join(w.dir, "fix.yaml"), "resume", "-c", "1"); !strings.Contains(got, "campaign done") {
		t.Fatalf("resume said:\n%s", got)
	}
	e.waitState(task.ID, dispatch.StateDone)
	trace, _ := os.ReadFile(filepath.Join(w.sync, "trace"))
	if strings.Count(string(trace), "1 b 1") != 1 || strings.Count(string(trace), "1 a ") != 1 {
		t.Fatalf("steps ran:\n%s", trace)
	}
}

// hetmanF runs a campaign command with an orders file.
func (w *world) hetmanF(wantExit int, ordersFile string, args ...string) string {
	w.t.Helper()
	self, _ := os.Executable()
	cmd := exec.Command(self, append([]string{"cli"}, append(args, "-f", ordersFile)...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Run()
	if code := cmd.ProcessState.ExitCode(); code != wantExit {
		w.t.Fatalf("hetman %v: exit %d, want %d; output:\n%s", args, code, wantExit, out.String())
	}
	return out.String()
}

func TestCancel(t *testing.T) {
	w := newWorld(t)
	w.flow("fix.yaml", "a", quick("exit", "0"))
	w.write(dispatch.DefaultRoutesFile, "version: 0\nroutes:\n  finding: {orders: fix.yaml}\n")
	e := w.open()
	keep := e.add("finding", `{}`, "")
	drop := e.add("finding", `{}`, "")
	if out := w.hetman(0, "", "task", "cancel", strconv.FormatInt(drop.ID, 10)); !strings.Contains(out, "cancelled") {
		t.Fatalf("cancel said:\n%s", out)
	}
	e.runOnce()
	views := e.views()
	if views[keep.ID].State != dispatch.StateDone || views[drop.ID].State != dispatch.StateCancelled || views[drop.ID].Campaign != 0 {
		t.Fatalf("after dispatch: %+v", views)
	}
	if _, err := e.queue.Cancel(keep.ID); !errors.Is(err, dispatch.ErrNotQueued) {
		t.Fatalf("cancelling a done task: %v", err)
	}
	if out := w.hetman(1, "", "task", "cancel", "99"); !strings.Contains(out, "no such task") {
		t.Fatalf("cancel of a missing task said:\n%s", out)
	}
	out := w.hetman(0, "", "task", "show", strconv.FormatInt(keep.ID, 10))
	for _, want := range []string{"state: done", "campaign 1 (flow fix", `"hetman_task": {`, `"kind": "finding"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("show lacks %q:\n%s", want, out)
		}
	}
}

func TestRoutingCheck(t *testing.T) {
	w := newWorld(t)
	w.flow("fix.yaml", "a", quick("exit", "0"))
	w.write("bad.yaml", "version: 0\nstart: a\nnodes:\n  a:\n    type: script\n    run: [x]\n    on: {ok: nowhere, fail: failed}\n")
	sub := filepath.Join(w.dir, "sub")
	os.Mkdir(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "other.yaml"), []byte("version: 0\nstart: a\nnodes: {}\n"), 0o644)
	cases := []struct {
		name, text string
		want       []string // substrings, each a problem with its position
	}{
		{"unknown key", "version: 0\nroutes:\n  finding: {orders: fix.yaml, prio: 1}\nextra: 1\n",
			[]string{":3:31: route finding: unknown key \"prio\"", ":4:1: routing file: unknown key \"extra\""}},
		{"missing version and routes", "limit: 2\n",
			[]string{":1:1: missing required key \"version\"", ":1:1: missing required key \"routes\""}},
		{"missing orders file", "version: 0\nroutes:\n  finding: {orders: nope.yaml}\n",
			[]string{":3:21: route finding: orders file \"nope.yaml\" does not exist"}},
		{"no orders key", "version: 0\nroutes:\n  finding: {priority: 2}\n",
			[]string{":3:12: route finding: missing required key \"orders\""}},
		{"orders elsewhere", "version: 0\nroutes:\n  finding:\n    orders: sub/other.yaml\n",
			[]string{":4:13: route finding: orders file \"sub/other.yaml\" must be in the folder of the routing file"}},
		{"rejected orders", "version: 0\nroutes:\n  finding: {orders: bad.yaml}\n",
			[]string{":3:21: route finding: orders file \"bad.yaml\" is rejected", "bad.yaml:7:"}},
		{"bad values", "version: 1\nlimit: 0\nroutes:\n  bad kind: {orders: fix.yaml}\n  finding: {orders: fix.yaml, limit: -1, priority: high}\n",
			[]string{":1:10: unsupported version", ":2:8: limit must be a whole number of at least 1", ":4:3: task kind \"bad kind\"",
				":5:38: route finding: limit must be", ":5:52: route finding: priority must be a whole number"}},
		{"empty routes", "version: 0\nroutes: {}\n", []string{":2:9: routes must route at least one task kind"}},
	}
	for _, c := range cases {
		w.write(dispatch.DefaultRoutesFile, c.text)
		out := w.hetman(2, "", "task", "check")
		for _, want := range c.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", c.name, want, out)
			}
		}
		if !strings.Contains(out, "routing file rejected") {
			t.Errorf("%s: not rejected:\n%s", c.name, out)
		}
	}
	w.write(dispatch.DefaultRoutesFile, "version: 0\nlimit: 3\nroutes:\n  finding: {orders: fix.yaml, priority: 2, limit: 1}\n")
	out := w.hetman(0, "", "task", "check")
	if !strings.Contains(out, "ok (1 route(s), up to 3 campaign(s) at once)") || !strings.Contains(out, "finding -> fix.yaml (priority 2, at most 1 at once)") {
		t.Fatalf("check said:\n%s", out)
	}
}

func TestUkrainianInterface(t *testing.T) {
	w := newWorld(t)
	w.flow("fix.yaml", "a", quick("exit", "0"))
	w.write(dispatch.DefaultRoutesFile, "version: 0\nroutes:\n  finding: {orders: fix.yaml}\n")
	t.Setenv("HETMAN_LANGUAGE", "uk")
	if out := w.hetman(0, "", "task", "add", "-kind", "finding", "-key", "k"); strings.TrimSpace(out) != "завдання 1 поставлено в чергу (вид finding)" {
		t.Fatalf("add said:\n%s", out)
	}
	out := w.hetman(0, "", "task", "add", "-kind", "finding", "-key", "k")
	if !strings.Contains(out, `завдання 1 уже має ключ "k" (стан: у черзі); нічого не додано`) {
		t.Fatalf("duplicate said:\n%s", out)
	}
	out = w.hetman(0, "", "dispatch", "-once", "-poll", "30ms")
	if !strings.Contains(out, "завдання 1: похід 1 завершено") || !strings.Contains(out, "більше нічого починати") {
		t.Fatalf("dispatch said:\n%s", out)
	}
	// The STATE column is interface text; the -state filter takes the
	// English identifier in every language.
	out = w.hetman(0, "", "task", "list", "-state", "done")
	if !strings.Contains(out, "ЗАВДАННЯ") || !strings.Contains(out, "завершено") {
		t.Fatalf("list said:\n%s", out)
	}
	w.write(dispatch.DefaultRoutesFile, "version: 0\nroutes:\n  finding: {orders: fix.yaml}\n  other: {orders: nope.yaml}\n")
	out = w.hetman(2, "", "task", "check")
	if !strings.Contains(out, `orders file "nope.yaml" does not exist`) || !strings.Contains(out, "файл маршрутів відхилено: 1 проблема") {
		t.Fatalf("check said:\n%s", out)
	}
}
