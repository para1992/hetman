package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"hetman/internal/chronicle"
	"hetman/internal/teststep"
)

// answers joins the lines a person types.
func answers(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// formRun runs hetman with the given answers on standard input.
func formRun(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	Stdin = strings.NewReader(stdin)
	taskStdin = Stdin
	defer func() { Stdin, taskStdin = os.Stdin, os.Stdin }()
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

var wrotePattern = regexp.MustCompile(`input written to (.+)`)

// The task list of the foreach example is given through the form, changed
// before it is confirmed, written under .hetman/inputs and run; the input
// the campaign gets is the one the form showed.
func TestTasksExampleForm(t *testing.T) {
	dir := tempDir(t)
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "..", "examples", "foreach"))); err != nil {
		t.Fatal(err)
	}
	seg := filepath.Join(dir, "segments", "item.yaml")
	data, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, step := range []string{"[sh, steps/build.sh]", "[sh, steps/test.sh]"} {
		if !strings.Contains(text, step) {
			t.Fatalf("the segment no longer runs %s", step)
		}
		text = strings.Replace(text, step, teststep.Argv("exit", "0"), 1)
	}
	os.WriteFile(seg, []byte(text), 0o644)
	tasks := filepath.Join(dir, "tasks.yaml")

	typed := answers(
		// The first task: every field.
		"TASK-5", "", "Shelf", "line one", "line two", "", "a", "b", "",
		"y",
		// The second needs the first.
		"TASK-6", "TASK-5", "", "Window", "", "c", "",
		"y",
		// A third one, removed below.
		"TASK-7", "", "Lamp", "", "z", "",
		"n",
		// Change a field: Tasks, remove an item, the third.
		"2", "1", "5", "3",
		// Change a field: Tasks, the second item; its title only.
		"2", "1", "2", "", "", "Door", "", "",
		"1", // use it
	)
	want := `{"project":"demo","tasks":[{"id":"TASK-5","title":"Shelf","description":"line one\nline two","criteria":["a","b"]},` +
		`{"id":"TASK-6","needs":["TASK-5"],"title":"Door","criteria":["c"]}]}`

	code, out, errOut := formRun(t, typed, "run", "-f", tasks, "-ask", "-save-only")
	if code != exitDone {
		t.Fatalf("save-only: exit %d\n%s%s", code, out, errOut)
	}
	m := wrotePattern.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no file named:\n%s", out)
	}
	path := strings.TrimSpace(m[1])
	if filepath.Dir(path) != filepath.Join(dir, ".hetman", "inputs") {
		t.Fatalf("written to %s", path)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := readInput(nil, path); string(got) != want {
		t.Fatalf("written input\n%s\nwant %s", written, want)
	}
	if _, err := os.Stat(filepath.Join(dir, ".hetman", "chronicle.db")); err == nil {
		t.Fatal("save-only started a campaign")
	}
	for _, s := range []string{"Tasks 1\n", "Task id: TASK-6", "project (fixed): demo", "Another item of Tasks?", "Remove which item?",
		"change Tasks 2 (TASK-6)", "Task id [TASK-6]", "Title [Window]: Door", "Now: c (Enter on the first item keeps them)"} {
		if !strings.Contains(out, s) {
			t.Errorf("transcript lacks %q", s)
		}
	}

	// The same answers start the campaign; the file of the first run stays.
	code, out, errOut = formRun(t, typed, "run", "-f", tasks, "-ask")
	if code != exitDone || !strings.Contains(out, "campaign done") {
		t.Fatalf("run -ask: exit %d\n%s%s", code, out, errOut)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, ".hetman", "inputs"))
	if len(entries) != 2 {
		t.Fatalf("%d input files", len(entries))
	}
	ch, err := chronicle.Open(filepath.Join(dir, ".hetman", "chronicle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	events, err := ch.Events(1)
	if err != nil || string(events[0].Input) != want {
		t.Fatalf("campaign input %s (%v)", events[0].Input, err)
	}

	// The file from the example folder fits; a wrong one is refused before
	// anything starts, naming the field.
	if code, out, errOut := formRun(t, "", "run", "-f", tasks, "-input", filepath.Join(dir, "tasks.json")); code != exitDone {
		t.Fatalf("tasks.json: exit %d\n%s%s", code, out, errOut)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"tasks": [{"id": "X", "title": "t"}], "ticket": "T-1"}`), 0o644)
	code, _, errOut = formRun(t, "", "run", "-f", tasks, "-input", bad)
	if code != exitUsage || !strings.Contains(errOut, "tasks[1].criteria is required") || !strings.Contains(errOut, "ticket is not a field") {
		t.Fatalf("bad input: exit %d\n%s", code, errOut)
	}
	if code, _, errOut = formRun(t, "", "run", "-f", tasks); code != exitUsage || !strings.Contains(errOut, "tasks is required") {
		t.Fatalf("no input: exit %d\n%s", code, errOut)
	}
	if n, _ := ch.Campaigns(10); len(n) != 2 {
		t.Fatalf("%d campaigns", len(n))
	}
}

func TestRunAskArguments(t *testing.T) {
	w := newWorld(t, "  a: {type: script, run: "+teststep.Argv("exit", "0")+", on: {ok: done, fail: failed}}\n")
	if code, _, errOut := formRun(t, "", "run", "-f", w.orders, "-ask"); code != exitUsage || !strings.Contains(errOut, "has no input section") {
		t.Fatalf("no section: %d %s", code, errOut)
	}
	if code, _, errOut := formRun(t, "", "run", "-f", w.orders, "-ask", "-input", "x.json"); code != exitUsage || !strings.Contains(errOut, "either -ask or -input") {
		t.Fatalf("both: %d %s", code, errOut)
	}
	if code, _, errOut := formRun(t, "", "run", "-f", w.orders, "-save-only"); code != exitUsage || !strings.Contains(errOut, "-save-only goes with -ask") {
		t.Fatalf("save-only alone: %d %s", code, errOut)
	}
	// Without an input section any input runs as before.
	in := filepath.Join(w.dir, "in.json")
	os.WriteFile(in, []byte(`{"anything": [1, 2]}`), 0o644)
	if code, out, errOut := formRun(t, "", "run", "-f", w.orders, "-input", in); code != exitDone {
		t.Fatalf("no section: %d %s%s", code, out, errOut)
	}
	// A cancelled form starts nothing.
	os.WriteFile(w.orders, []byte("version: 0\nstart: a\ninput:\n  goal: {kind: text, required: true}\nnodes:\n  a: {type: script, run: "+
		teststep.Argv("exit", "0")+", on: {ok: done, fail: failed}}\n"), 0o644)
	code, out, _ := formRun(t, answers("a goal", "3"), "run", "-f", w.orders, "-ask")
	if code != exitFailed || !strings.Contains(out, "Cancelled; nothing was started") {
		t.Fatalf("cancelled: %d %s", code, out)
	}
}

// task new asks through the form of the routed orders and queues the task;
// task add refuses an input that does not fit them.
func TestTaskNewAndAddCheckTheInput(t *testing.T) {
	dir := tempDir(t)
	os.WriteFile(filepath.Join(dir, "routes.yaml"), []byte("version: 0\nroutes:\n  remark: {orders: remark.yaml}\n  plain: {orders: plain.yaml}\n"), 0o644)
	step := teststep.Argv("exit", "0")
	os.WriteFile(filepath.Join(dir, "remark.yaml"), []byte("version: 0\nflow: remark\nstart: a\ninput:\n  file: {kind: text, required: true}\n  line: {kind: number}\n"+
		"  source: {kind: text, default: reviewer, ask: false}\nnodes:\n  a: {type: script, run: "+step+", on: {ok: done, fail: failed}}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "plain.yaml"), []byte("version: 0\nflow: plain\nstart: a\nnodes:\n  a: {type: script, run: "+step+", on: {ok: done, fail: failed}}\n"), 0o644)
	routes := filepath.Join(dir, "routes.yaml")

	code, out, errOut := formRun(t, answers("main.go", "12", "1"), "task", "new", "-r", routes, "-kind", "remark", "-key", "k1")
	if code != exitDone || !strings.Contains(out, "task 1 queued") || !strings.Contains(out, "input written to "+filepath.Join(dir, ".hetman", "inputs", "remark-")) {
		t.Fatalf("task new: %d\n%s%s", code, out, errOut)
	}
	code, out, _ = formRun(t, "", "task", "show", "-r", routes, "1")
	if code != exitDone || !strings.Contains(out, `"file": "main.go"`) || !strings.Contains(out, `"line": 12`) || !strings.Contains(out, `"source": "reviewer"`) {
		t.Fatalf("task show: %s", out)
	}
	if code, _, errOut = formRun(t, `{"line": "twelve"}`, "task", "add", "-r", routes, "-kind", "remark", "-input", "-"); code != exitUsage ||
		!strings.Contains(errOut, "file is required") || !strings.Contains(errOut, "line must be a number") || !strings.Contains(errOut, "nothing was added") {
		t.Fatalf("task add: %d %s", code, errOut)
	}
	if code, out, errOut = formRun(t, `{"anything": 1}`, "task", "add", "-r", routes, "-kind", "plain", "-input", "-"); code != exitDone {
		t.Fatalf("task add without a section: %d %s%s", code, out, errOut)
	}
	if code, _, errOut = formRun(t, "", "task", "new", "-r", routes, "-kind", "plain"); code != exitUsage || !strings.Contains(errOut, "has no input section") {
		t.Fatalf("task new without a section: %d %s", code, errOut)
	}
	code, out, _ = formRun(t, answers("x.go", "", "1"), "task", "new", "-r", routes, "-kind", "remark", "-save-only")
	if code != exitDone || strings.Contains(out, "queued") || !strings.Contains(out, "input written to") {
		t.Fatalf("save-only: %d %s", code, out)
	}
}
