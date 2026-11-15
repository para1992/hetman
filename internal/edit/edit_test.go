package edit

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hetman/internal/orders"
)

var update = flag.Bool("update", false, "rewrite testdata/ops.golden")

// fixture copies the commented orders and their files into a new folder
// and opens them.
func fixture(t *testing.T) *Doc {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range []string{"commented.yaml", "prompts/review.md", "schemas/review.json", "segments/deploy.yaml"} {
		data, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		name := rel
		if rel == "commented.yaml" {
			name = orders.DefaultFile
		}
		write(t, filepath.Join(dir, filepath.FromSlash(name)), string(data))
	}
	return open(t, filepath.Join(dir, orders.DefaultFile))
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, path string) *Doc {
	t.Helper()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

// commentLines lists the comment lines of a text and the comments at the
// end of lines.
func commentLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "# "); i >= 0 {
			out = append(out, strings.TrimSpace(line[i:]))
		}
	}
	return out
}

// Every operation, alone or with the one it needs first.
var opCases = []struct {
	name string
	ops  []Op
	gone []string // comments that go with what the operation removes
}{
	{name: "add script after", ops: []Op{{Op: OpAdd, Type: "script", Node: "package", After: "ready", Run: []string{"sh", "steps/package.sh"}, Timeout: "2m"}}},
	{name: "add script after via", ops: []Op{{Op: OpAdd, Type: "script", Node: "notes", After: "review", Via: "needs_rework", Run: []string{"sh", "steps/notes.sh"}}}},
	{name: "add gate before", ops: []Op{{Op: OpAdd, Type: "gate", Node: "quick", Before: "ready", Checks: []string{"lint"}, On: map[string]string{"blocked": "build"}}}},
	{name: "add agent before start", ops: []Op{{Op: OpAdd, Type: "agent", Node: "plan", Before: "build", Executor: "stub", Model: "m1", Effort: "low",
		Tools: []string{"read"}, Attempts: 2, Outcomes: []string{"ok", "unclear"}, Starters: true}}},
	{name: "add human after and ask", ops: []Op{{Op: OpAdd, Type: "human", Node: "decide", After: "ready", Outcomes: []string{"ship", "stop"}},
		{Op: OpAsk, Node: "decide", ID: "channel", Question: "Which channel?", Kind: "choice", Options: []string{"stable", "beta"}}}},
	{name: "remove", ops: []Op{{Op: OpRemove, Node: "review"}}, gone: []string{"# A model reads the change.", "# back for another round"}},
	{name: "rename", ops: []Op{{Op: OpRename, Node: "build", Name: "compile"}}},
	{name: "link new outcome", ops: []Op{{Op: OpLink, Node: "ship", Outcome: "skip", Target: "done"}}},
	{name: "link in a block", ops: []Op{{Op: OpLink, Node: "review", Outcome: "needs_rework", Target: "failed"}}},
	{name: "unlink", ops: []Op{{Op: OpUnlink, Node: "ship", Outcome: "again"}}},
	{name: "set timeout", ops: []Op{{Op: OpSet, Node: "ship", Field: "timeout", Value: "1m"}}},
	{name: "set existing timeout", ops: []Op{{Op: OpSet, Node: "build", Field: "timeout", Value: "10m"}}},
	{name: "unset timeout", ops: []Op{{Op: OpUnset, Node: "build", Field: "timeout"}}},
	{name: "set run", ops: []Op{{Op: OpSet, Node: "build", Field: "run", Value: "make build"}}},
	{name: "set executor model effort tools attempts", ops: []Op{
		{Op: OpExecutor, Name: "other", Run: []string{"./bin/other", "--quiet"}},
		{Op: OpSet, Node: "review", Field: "executor", Value: "other"},
		{Op: OpSet, Node: "review", Field: "model", Value: "strong-model"},
		{Op: OpSet, Node: "review", Field: "effort", Value: "high"},
		{Op: OpSet, Node: "review", Field: "tools", Value: "read, grep"},
		{Op: OpSet, Node: "review", Field: "attempts", Value: "2"}}},
	{name: "declare check", ops: []Op{{Op: OpCheck, Name: "style", Run: []string{"sh", "checks/style.sh"}, Soft: yes()}}},
	{name: "check required", ops: []Op{{Op: OpCheck, Name: "lint", Soft: no()}}},
	{name: "check soft and timeout", ops: []Op{{Op: OpCheck, Name: "tests", Soft: yes(), Timeout: "30m"}}},
	{name: "gate add at a position", ops: []Op{{Op: OpCheck, Name: "style", Run: []string{"sh", "checks/style.sh"}},
		{Op: OpGateAdd, Node: "ready", Check: "style", At: 1}}},
	{name: "gate remove", ops: []Op{{Op: OpGateRemove, Node: "ready", Check: "lint"}}},
	{name: "ask", ops: []Op{{Op: OpAsk, Node: "ready", ID: "docs", Question: "Are the docs updated?"}}},
	{name: "assistant", ops: []Op{{Op: OpAssistant, Executor: "stub", Model: "small-model", Effort: "low"}}},
	{name: "language", ops: []Op{{Op: OpLanguage, Language: "hi"}}},
	{name: "language changed", ops: []Op{{Op: OpLanguage, Language: "hi"}, {Op: OpLanguage, Language: "uk"}}},
	{name: "language removed", ops: []Op{{Op: OpLanguage, Language: "hi"}, {Op: OpUnlanguage}}},
	{name: "add segment after", ops: []Op{{Op: OpAdd, Type: "segment", Node: "deploy", After: "ready", Segment: "segments/deploy.yaml",
		Locks: []string{"prod"}}}},
	{name: "add foreach after and set", ops: []Op{{Op: OpAdd, Type: "foreach", Node: "each", After: "ship", Segment: "segments/deploy.yaml",
		Items: "input.services", Parallel: 2}, {Op: OpSet, Node: "each", Field: "parallel", Value: "3"}}},
	{name: "set and unset locks", ops: []Op{{Op: OpSet, Node: "ship", Field: "locks", Value: "prod, writer"},
		{Op: OpSet, Node: "build", Field: "locks", Value: "writer"}, {Op: OpUnset, Node: "build", Field: "locks"}}},
	{name: "check locks", ops: []Op{{Op: OpCheck, Name: "tests", Locks: []string{"stand"}}}},
	{name: "role declared and pointed at", ops: []Op{
		{Op: OpExecutor, Name: "other", Run: []string{"./bin/other"}},
		{Op: OpRole, Name: "coder", Select: "round_robin", Executor: "stub", Model: "m1", Effort: "high"},
		{Op: OpRoleAdd, Name: "coder", Executor: "other", Model: "m2"},
		{Op: OpSet, Node: "review", Field: "role", Value: "coder"}}},
	{name: "role mode, clients added and removed", ops: []Op{
		{Op: OpExecutor, Name: "other", Run: []string{"./bin/other"}},
		{Op: OpRole, Name: "coder", Select: "fixed", Executor: "stub"},
		{Op: OpRoleAdd, Name: "coder", Executor: "other", At: 1},
		{Op: OpRoleAdd, Name: "coder", Executor: "stub", Model: "m3"},
		{Op: OpRoleRemove, Name: "coder", At: 2},
		{Op: OpRole, Name: "coder", Select: "quota"}}},
	{name: "agent added through a role, then given an executor", ops: []Op{
		{Op: OpRole, Name: "coder", Select: "fixed", Executor: "stub"},
		{Op: OpAdd, Type: "agent", Node: "plan", Before: "build", Role: "coder", Starters: true},
		{Op: OpSet, Node: "plan", Field: "executor", Value: "stub"}}},
}

// TestRoleOperationsRefused: a role operation that would leave the file
// wrong changes nothing and says why.
func TestRoleOperationsRefused(t *testing.T) {
	cases := []struct {
		ops  []Op
		want string
	}{
		{[]Op{{Op: OpRole, Name: "coder", Select: "fixed"}}, "a new role needs select"},
		{[]Op{{Op: OpRole, Name: "coder", Executor: "stub"}}, "a new role needs select"},
		{[]Op{{Op: OpRole, Name: "coder", Select: "cheapest", Executor: "stub"}}, `unknown select "cheapest"`},
		{[]Op{{Op: OpRole, Name: "coder", Select: "fixed", Executor: "nobody"}}, `executor "nobody" is not declared`},
		{[]Op{{Op: OpRole, Name: "coder", Select: "fixed", Executor: "stub"}, {Op: OpRole, Name: "coder", Select: "fixed", Executor: "stub"}}, "add a client with role-add"},
		{[]Op{{Op: OpRole, Name: "coder", Select: "fixed", Executor: "stub"}, {Op: OpRoleRemove, Name: "coder", At: 1}}, "a role needs at least one"},
		{[]Op{{Op: OpRole, Name: "coder", Select: "fixed", Executor: "stub"}, {Op: OpRoleRemove, Name: "coder", At: 3}}, "at must be from 1 to 1"},
		{[]Op{{Op: OpRoleAdd, Name: "coder", Executor: "stub"}}, `role "coder" is not declared`},
		{[]Op{{Op: OpSet, Node: "review", Field: "role", Value: "coder"}}, `role "coder" is not declared`},
		{[]Op{{Op: OpRole, Name: "coder", Select: "fixed", Executor: "stub"}, {Op: OpSet, Node: "review", Field: "role", Value: "coder"},
			{Op: OpSet, Node: "review", Field: "model", Value: "m"}}, "runs through role coder"},
		{[]Op{{Op: OpSet, Node: "build", Field: "role", Value: "coder"}}, "role is set only on an agent node"},
	}
	for _, c := range cases {
		d := fixture(t)
		orig := string(d.Text())
		var err error
		for _, op := range c.ops {
			if err = d.Apply(op); err != nil {
				break
			}
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.ops, err, c.want)
		}
		if len(c.ops) == 1 && string(d.Text()) != orig {
			t.Errorf("%v changed the file", c.ops)
		}
	}
	if err := (Op{Op: OpAdd, Type: "agent", Node: "x", After: "build", Executor: "stub", Role: "coder"}).Validate(); err == nil || !strings.Contains(err.Error(), "either a role or an executor") {
		t.Errorf("validate: %v", err)
	}
}

// TestOperationsOnCommentedFile applies every operation to a file full of
// comments, blank lines and alignment: the change is exactly the diff in
// testdata/ops.golden, every comment not removed with its node survives,
// and the result passes the orders check.
func TestOperationsOnCommentedFile(t *testing.T) {
	var golden strings.Builder
	for _, c := range opCases {
		d := fixture(t)
		orig := string(d.Text())
		for _, op := range c.ops {
			if err := d.Apply(op); err != nil {
				t.Fatalf("%s: %s: %v", c.name, op.Words(), err)
			}
		}
		if _, err := d.Check(); err != nil {
			t.Errorf("%s: the result does not pass the check:\n%v", c.name, err)
		}
		if n := d.Notes(); len(n) > 0 {
			t.Errorf("%s: not made in place: %v", c.name, n)
		}
		got := string(d.Text())
		for _, comment := range commentLines(orig) {
			if !strings.Contains(got, comment) && !contains(c.gone, comment) {
				t.Errorf("%s: the comment %q is lost", c.name, comment)
			}
		}
		golden.WriteString("== " + c.name + "\n" + d.Diff())
		if err := d.Save(false); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, err := orders.Load(d.Path); err != nil {
			t.Fatalf("%s: saved file: %v", c.name, err)
		}
	}
	path := filepath.Join("testdata", "ops.golden")
	if *update {
		write(t, path, golden.String())
	}
	want := strings.ReplaceAll(read(t, path), "\r\n", "\n")
	if got := golden.String(); got != want {
		gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
		for i := range gl {
			if i >= len(wl) || gl[i] != wl[i] {
				t.Fatalf("diffs differ from %s at line %d:\ngot  %q\nwant %q\n(go test -run %s -update rewrites it)", path, i+1, gl[i], wl[min(i, len(wl)-1)], t.Name())
			}
		}
		t.Fatalf("diffs differ from %s: want %d lines, got %d", path, len(wl), len(gl))
	}
}

// TestBrokenResultWritesNothing: an operation whose result fails the check
// leaves every file as it was, unless forced.
func TestBrokenResultWritesNothing(t *testing.T) {
	d := fixture(t)
	before := read(t, d.Path)
	if err := d.Apply(Op{Op: OpUnlink, Node: "ship", Outcome: "fail"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(Op{Op: OpAdd, Type: "agent", Node: "plan", After: "ship", Executor: "stub", Starters: true}); err != nil {
		t.Fatal(err)
	}
	_, err := d.Check()
	var ps orders.Problems
	if !errors.As(err, &ps) || !strings.Contains(err.Error(), `node "ship": outcome "fail" has no transition`) {
		t.Fatalf("check: %v", err)
	}
	if err := d.Save(false); !errors.Is(err, ErrNotChecked) {
		t.Fatalf("save: %v", err)
	}
	if read(t, d.Path) != before {
		t.Fatal("the orders were written")
	}
	for _, rel := range []string{"prompts/plan.md", "schemas/plan.json"} {
		if _, err := os.Stat(filepath.Join(d.Dir, filepath.FromSlash(rel))); err == nil {
			t.Fatalf("%s was written", rel)
		}
	}
	if err := d.Save(true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, d.Path), "on: {ok: plan, again: ship}") {
		t.Fatalf("forced save did not write:\n%s", read(t, d.Path))
	}
}

// TestRenameUpdatesEveryReference: the key, the start, every transition and
// every placeholder in a prompt template.
func TestRenameUpdatesEveryReference(t *testing.T) {
	d := fixture(t)
	if err := d.Apply(Op{Op: OpRename, Node: "ready", Name: "approval"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(Op{Op: OpRename, Node: "build", Name: "compile"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(false); err != nil {
		t.Fatal(err)
	}
	o, err := orders.Load(d.Path)
	if err != nil {
		t.Fatal(err)
	}
	if o.Start != "compile" || strings.Join(o.Order, ",") != "compile,review,approval,ship" {
		t.Fatalf("start %s, nodes %v", o.Start, o.Order)
	}
	for _, id := range o.Order {
		for out, to := range o.Nodes[id].On {
			if to == "build" || to == "ready" {
				t.Errorf("%s: %s still leads to %s", id, out, to)
			}
		}
	}
	text := read(t, d.Path)
	if strings.Count(text, "build") != 1 || !strings.Contains(text, "steps/build.sh") || strings.Contains(text, "ready") {
		t.Fatalf("old names left:\n%s", text)
	}
	prompt := read(t, filepath.Join(d.Dir, "prompts", "review.md"))
	if want := "Review the change built by {{ artifact.compile.report.txt }}.\nThe person said: {{answers.approval.notes}}\n{{rework}}\n"; prompt != want {
		t.Fatalf("prompt:\n%s", prompt)
	}
}

// TestUnknownNamesChangeNothing: an operation naming something that does
// not exist is refused before anything changes.
func TestUnknownNamesChangeNothing(t *testing.T) {
	for _, op := range []Op{
		{Op: OpRemove, Node: "deploy"},
		{Op: OpRename, Node: "deploy", Name: "x"},
		{Op: OpRename, Node: "build", Name: "ship"},
		{Op: OpLink, Node: "deploy", Outcome: "ok", Target: "done"},
		{Op: OpLink, Node: "build", Outcome: "ok", Target: "deploy"},
		{Op: OpUnlink, Node: "build", Outcome: "nope"},
		{Op: OpSet, Node: "deploy", Field: "timeout", Value: "1m"},
		{Op: OpSet, Node: "review", Field: "executor", Value: "ghost"},
		{Op: OpSet, Node: "build", Field: "model", Value: "m"},
		{Op: OpAdd, Type: "script", Node: "x", After: "deploy", Run: []string{"true"}},
		{Op: OpAdd, Type: "script", Node: "x", Before: "deploy", Run: []string{"true"}},
		{Op: OpAdd, Type: "gate", Node: "x", After: "build", Checks: []string{"ghost"}},
		{Op: OpAdd, Type: "agent", Node: "x", After: "build", Executor: "ghost"},
		{Op: OpAdd, Type: "script", Node: "build", After: "ship", Run: []string{"true"}},
		{Op: OpGateAdd, Node: "ready", Check: "ghost"},
		{Op: OpGateAdd, Node: "build", Check: "lint"},
		{Op: OpGateRemove, Node: "ready", Check: "ghost"},
		{Op: OpAsk, Node: "build", ID: "q", Question: "?"},
		{Op: OpAsk, Node: "ready", ID: "notes", Question: "Again?"},
		{Op: OpAssistant, Executor: "ghost", Model: "m"},
		{Op: OpLanguage, Language: "Spanish"},
		{Op: OpNew, Type: "script", Node: "x", Run: []string{"true"}},
	} {
		d := fixture(t)
		before := string(d.Text())
		if err := d.Apply(op); err == nil {
			t.Errorf("%s: applied", op.Words())
		}
		if string(d.Text()) != before || len(d.Files()) > 0 || d.Changed() {
			t.Errorf("%s: changed the document", op.Words())
		}
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		op   Op
		want string
	}{
		{Op{Op: "move", Node: "a"}, "unknown operation"},
		{Op{Op: OpRemove}, "remove needs node"},
		{Op{Op: OpRemove, Node: "a", Model: "m"}, "remove does not take model"},
		{Op{Op: OpLanguage}, "language needs language"},
		{Op{Op: OpUnlanguage, Language: "de"}, "unlanguage does not take language"},
		{Op{Op: OpAdd, Type: "script", Node: "a"}, "exactly one of after and before"},
		{Op{Op: OpAdd, Type: "script", Node: "a", After: "b", Before: "c"}, "exactly one of after and before"},
		{Op{Op: OpAdd, Type: "script", Node: "a", Before: "b", Via: "ok"}, "via is given only with after"},
		{Op{Op: OpSet, Node: "a", Field: "speed", Value: "x"}, `unknown field "speed"`},
		{Op{Op: OpAsk, Node: "a", ID: "b", Question: "two\nlines"}, "one line"},
	} {
		if err := c.op.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.op, err, c.want)
		}
	}
}

// TestStartersNeverOverwrite: starters are created only where no file is.
func TestStartersNeverOverwrite(t *testing.T) {
	d := fixture(t)
	mine := "Plan the release.\n{{rework}}\n"
	write(t, filepath.Join(d.Dir, "prompts", "plan.md"), mine)
	if err := d.Apply(Op{Op: OpAdd, Type: "agent", Node: "plan", Before: "build", Executor: "stub", Starters: true}); err != nil {
		t.Fatal(err)
	}
	files := d.Files()
	if len(files) != 1 || files[0].Path != filepath.Join(d.Dir, "schemas", "plan.json") {
		t.Fatalf("files %+v", files)
	}
	if err := d.Save(false); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(d.Dir, "prompts", "plan.md")) != mine {
		t.Fatal("the existing prompt was overwritten")
	}
	// An existing schema decides the outcomes.
	d = open(t, d.Path)
	if err := d.Apply(Op{Op: OpAdd, Type: "agent", Node: "review2", After: "ship", Executor: "stub", Schema: "schemas/review.json", Prompt: "prompts/review.md"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Text()), "on: {approved: done, needs_rework: failed, fail: failed}") {
		t.Fatalf("outcomes not taken from the schema:\n%s", d.Text())
	}
	if err := d.Apply(Op{Op: OpAdd, Type: "agent", Node: "review3", After: "ship", Executor: "stub", Schema: "schemas/review.json", Outcomes: []string{"ok"}}); err == nil {
		t.Fatal("outcomes that differ from the schema were accepted")
	}
}

// TestNewFlow starts a file from nothing.
func TestNewFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo", orders.DefaultFile)
	os.MkdirAll(filepath.Dir(path), 0o755)
	d := open(t, path)
	if err := d.Apply(Op{Op: OpRemove, Node: "x"}); err == nil {
		t.Fatal("an operation on a missing file was accepted")
	}
	if err := d.Apply(Op{Op: OpNew, Type: "script", Node: "prepare", Run: []string{"sh", "steps/prepare.sh"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(false); err != nil {
		t.Fatal(err)
	}
	want := `version: 0
flow: demo
start: prepare

nodes:
  prepare:
    type: script
    run: [sh, steps/prepare.sh]
    on: {ok: done, fail: failed}
`
	if got := read(t, path); got != want {
		t.Fatalf("got:\n%s", got)
	}
	if err := open(t, path).Apply(Op{Op: OpNew, Type: "script", Node: "x", Run: []string{"true"}}); err == nil {
		t.Fatal("new over an existing file was accepted")
	}
}

// TestOneLineNodes: nodes written on one line get new nodes and fields on
// one line too.
func TestOneLineNodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), orders.DefaultFile)
	write(t, path, `version: 0
start: build
nodes:
  build:  {type: script, run: [b], on: {ok: test, fail: failed}}   # first
  test:   {type: script, run: [t], on: {ok: done, fail: build}}
`)
	d := open(t, path)
	for _, op := range []Op{
		{Op: OpAdd, Type: "script", Node: "lint", After: "build", Run: []string{"l"}},
		{Op: OpSet, Node: "test", Field: "timeout", Value: "1m"},
		{Op: OpRename, Node: "build", Name: "compile"},
	} {
		if err := d.Apply(op); err != nil {
			t.Fatal(err)
		}
	}
	want := `version: 0
start: compile
nodes:
  compile:  {type: script, run: [b], on: {ok: lint, fail: failed}}   # first
  lint: {type: script, run: [l], on: {ok: test, fail: failed}}
  test:   {type: script, run: [t], timeout: 1m, on: {ok: done, fail: compile, timeout: failed}}
`
	if got := string(d.Text()); got != want {
		t.Fatalf("got:\n%s", got)
	}
	if len(d.Notes()) > 0 {
		t.Fatal(d.Notes())
	}
}

// TestRewriteWhenNotInPlace: a value written in a form that is not edited
// in place makes the whole file written out again, with the same meaning
// and its comments, and a note saying so.
func TestRewriteWhenNotInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), orders.DefaultFile)
	write(t, path, `version: 0
start: build
nodes:
  # Builds it.
  build:
    type: script
    run:
      - make
      - build
    on: {ok: done, fail: failed}
`)
	d := open(t, path)
	if err := d.Apply(Op{Op: OpSet, Node: "build", Field: "run", Value: "make all"}); err != nil {
		t.Fatal(err)
	}
	if len(d.Notes()) != 1 || !strings.Contains(d.Notes()[0], "written out again") {
		t.Fatalf("notes %v", d.Notes())
	}
	if !strings.Contains(string(d.Text()), "# Builds it.") {
		t.Fatalf("comment lost:\n%s", d.Text())
	}
	o, err := orders.Parse(path, d.Text())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.Nodes["build"].Run, []string{"make", "all"}) {
		t.Fatalf("run %v", o.Nodes["build"].Run)
	}
}

// TestLineEndsKept: a file with CR LF line ends keeps them.
func TestLineEndsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), orders.DefaultFile)
	text := "version: 0\r\nstart: a\r\nnodes:\r\n  a:   # step\r\n    type: script\r\n    run: [x]\r\n    on: {ok: done, fail: failed}\r\n"
	write(t, path, text)
	d := open(t, path)
	if err := d.Apply(Op{Op: OpSet, Node: "a", Field: "timeout", Value: "1m"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(false); err != nil {
		t.Fatal(err)
	}
	want := "version: 0\r\nstart: a\r\nnodes:\r\n  a:   # step\r\n    type: script\r\n    run: [x]\r\n    timeout: 1m\r\n    on: {ok: done, fail: failed, timeout: failed}\r\n"
	if got := read(t, path); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestSplitCommand(t *testing.T) {
	for in, want := range map[string][]string{
		"sh steps/build.sh":           {"sh", "steps/build.sh"},
		`  python3 -c 'print("a b")'`: {"python3", "-c", `print("a b")`},
		`run "a \" b" ''`:             {"run", `a " b`, ""},
	} {
		got, err := SplitCommand(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := SplitCommand(`a "b`); err == nil {
		t.Error("unclosed quote accepted")
	}
}

func TestDiff(t *testing.T) {
	a := []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")
	b := []byte("1\n2\nthree\n4\n5\n6\n7\n8\n9\nten\n")
	want := "--- f\n+++ f\n@@ -1,5 +1,5 @@\n 1\n 2\n-3\n+three\n 4\n 5\n@@ -8,2 +8,3 @@\n 8\n 9\n+ten\n"
	if got := unified("f", a, b, true); got != want {
		t.Fatalf("got:\n%s", got)
	}
	if got := unified("f", a, a, true); got != "" {
		t.Fatalf("equal texts give %q", got)
	}
	if !bytes.HasPrefix([]byte(unified("f", nil, b, false)), []byte("--- /dev/null\n+++ f\n@@ -0,0 +1,10 @@\n")) {
		t.Fatal(unified("f", nil, b, false))
	}
}

// TestLanguage sets, changes and removes the language for the person; an
// unknown code says which code was probably meant, and removing a language
// that is not set changes nothing.
func TestLanguage(t *testing.T) {
	d := fixture(t)
	err := d.Apply(Op{Op: OpLanguage, Language: "Spanish"})
	if err == nil || !strings.Contains(err.Error(), "did you mean es (Spanish)?") {
		t.Fatalf("unknown code: %v", err)
	}
	if err := d.Apply(Op{Op: OpUnlanguage}); err != nil || d.Changed() {
		t.Fatalf("removing nothing: %v, changed %v", err, d.Changed())
	}
	for _, code := range []string{"es", "zh"} {
		if err := d.Apply(Op{Op: OpLanguage, Language: code}); err != nil {
			t.Fatal(err)
		}
		o, err := d.Check()
		if err != nil || o.Language != code {
			t.Fatalf("language %q, %v", o.Language, err)
		}
	}
	if w := (Op{Op: OpLanguage, Language: "zh"}).Words(); w != "write what the flow addresses to a person in Mandarin Chinese (普通话), code zh" {
		t.Fatalf("words %q", w)
	}
	if err := d.Apply(Op{Op: OpUnlanguage}); err != nil {
		t.Fatal(err)
	}
	if o, err := d.Check(); err != nil || o.Language != "" || d.Changed() {
		t.Fatalf("after removal: %v, changed %v", err, d.Changed())
	}
}
