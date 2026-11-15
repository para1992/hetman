package edit

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hetman/internal/orders"
)

// A list flag splits only where an entry ends: parentheses and quotes keep
// their contents. The second case is the tools list of the README.
func TestSplitList(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"Read,Bash(make -C *),Bash(go test *)", []string{"Read", "Bash(make -C *)", "Bash(go test *)"}},
		{"Read, Edit, Write, Grep, Glob, Bash(go test *)", []string{"Read", "Edit", "Write", "Grep", "Glob", "Bash(go test *)"}},
		{`lint tests "a b",'c,d'`, []string{"lint", "tests", "a b", "c,d"}},
		{"Bash(make (x) y),Read", []string{"Bash(make (x) y)", "Read"}},
		{"Bash(make -C", []string{"Bash(make -C"}},
		{" , ", nil},
	} {
		got, err := SplitList(c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %q %v, want %q", c.in, got, err, c.want)
		}
	}
	if _, err := SplitList(`a "b`); err == nil {
		t.Error("an unclosed quote was accepted")
	}
}

// set tools writes entries with spaces whole, and the check rejects an
// entry whose parentheses do not pair up.
func TestSetToolsKeepsPatterns(t *testing.T) {
	d := fixture(t)
	if err := d.Apply(Op{Op: OpSet, Node: "review", Field: "tools", Value: "Read,Bash(make -C *),Bash(go test *)"}); err != nil {
		t.Fatal(err)
	}
	o, err := d.Check()
	if err != nil {
		t.Fatal(err)
	}
	if got := o.Nodes["review"].Tools; !reflect.DeepEqual(got, []string{"Read", "Bash(make -C *)", "Bash(go test *)"}) {
		t.Fatalf("tools %q", got)
	}
	if !strings.Contains(string(d.Text()), "tools: [Read, Bash(make -C *), Bash(go test *)]") {
		t.Fatalf("text:\n%s", d.Text())
	}
	d = fixture(t)
	if err := d.Apply(Op{Op: OpSet, Node: "review", Field: "tools", Value: "Read Bash(make"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Check(); err == nil || !strings.Contains(err.Error(), "an opening parenthesis without a closing one") {
		t.Fatalf("check: %v", err)
	}
}

// link with max and then writes a limited transition, which rename and
// remove keep pointing at the right node; link without them makes it plain
// again.
func TestLinkWithLimit(t *testing.T) {
	d := fixture(t)
	if err := d.Apply(Op{Op: OpLink, Node: "review", Outcome: "needs_rework", Target: "build", Max: 3, Then: "stuck"}); err != nil {
		t.Fatal(err)
	}
	o, err := d.Check()
	if err != nil {
		t.Fatalf("%v\n%s", err, d.Text())
	}
	if lim := o.Nodes["review"].Limits["needs_rework"]; lim.Max != 3 || lim.Then != "stuck" || o.Nodes["review"].On["needs_rework"] != "build" {
		t.Fatalf("limit %+v on %v", lim, o.Nodes["review"].On)
	}
	if !strings.Contains(string(d.Text()), "needs_rework: {to: build, max: 3, then: stuck}") || o.Nodes["review"].On["stuck"] != "failed" {
		t.Fatalf("text:\n%s", d.Text())
	}
	if w := (Op{Op: OpLink, Node: "review", Outcome: "needs_rework", Target: "build", Max: 3, Then: "stuck"}).Words(); !strings.Contains(w, "at most 3 time(s) per campaign, and then take the outcome stuck") {
		t.Fatalf("words %q", w)
	}
	if err := d.Apply(Op{Op: OpRename, Node: "build", Name: "compile"}); err != nil {
		t.Fatal(err)
	}
	if o, err = d.Check(); err != nil {
		t.Fatalf("after rename: %v\n%s", err, d.Text())
	}
	if o.Nodes["review"].On["needs_rework"] != "compile" {
		t.Fatalf("after rename: %v", o.Nodes["review"].On)
	}
	if err := d.Apply(Op{Op: OpLink, Node: "review", Outcome: "needs_rework", Target: "compile"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Check(); err == nil || !strings.Contains(err.Error(), `outcome "stuck" can never happen`) {
		t.Fatalf("an outcome no limit names any more: %v", err)
	}
	if err := d.Apply(Op{Op: OpUnlink, Node: "review", Outcome: "stuck"}); err != nil {
		t.Fatal(err)
	}
	if o, err = d.Check(); err != nil {
		t.Fatalf("plain again: %v", err)
	}
	if len(o.Nodes["review"].Limits) != 0 {
		t.Fatalf("plain again: %+v", o.Nodes["review"].Limits)
	}
	if err := (Op{Op: OpLink, Node: "a", Outcome: "b", Target: "c", Max: 2}).Validate(); err == nil {
		t.Fatal("max without then accepted")
	}
}

// A prompt may name a node that is added later: while building it is a
// warning that says so, and the orders check still rejects it.
func TestPromptNamingANodeAddedLater(t *testing.T) {
	d := fixture(t)
	write(t, filepath.Join(d.Dir, "prompts", "fix.md"), "Fix.\n{{artifact.prove.self-test.log}}\n")
	if err := d.Apply(Op{Op: OpAdd, Type: "agent", Node: "fix", After: "review", Executor: "stub", Prompt: "prompts/fix.md", Schema: "schemas/review.json"}); err != nil {
		t.Fatal(err)
	}
	o, err := d.Check()
	if err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(Warnings(o), "\n"); !strings.Contains(w, `{{artifact.prove.self-test.log}}: there is no node "prove" in the orders (yet)`) {
		t.Fatalf("warnings:\n%s", w)
	}
	if err := d.Save(false); err != nil {
		t.Fatal(err)
	}
	if _, err := orders.Load(d.Path); err == nil || !strings.Contains(err.Error(), `there is no node "prove"`) {
		t.Fatalf("check: %v", err)
	}
}

// show-add and show-remove change what a gate or human node shows.
func TestShowOperations(t *testing.T) {
	d := fixture(t)
	for _, f := range []string{"notes.md", "{{work}}/plan.md"} {
		if err := d.Apply(Op{Op: OpShowAdd, Node: "ready", File: f}); err != nil {
			t.Fatal(err)
		}
	}
	o, err := d.Check()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(o.Nodes["ready"].Show, " "); got != "notes.md {{work}}/plan.md" {
		t.Fatalf("show %q\n%s", got, d.Text())
	}
	if err := d.Apply(Op{Op: OpShowAdd, Node: "ready", File: "notes.md"}); err == nil {
		t.Error("a file shown twice")
	}
	if err := d.Apply(Op{Op: OpShowAdd, Node: "build", File: "x.md"}); err == nil {
		t.Error("show on a script")
	}
	for _, f := range []string{"notes.md", "{{work}}/plan.md"} {
		if err := d.Apply(Op{Op: OpShowRemove, Node: "ready", File: f}); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(string(d.Text()), "show:") {
		t.Fatalf("the empty list stayed:\n%s", d.Text())
	}
	if w := (Op{Op: OpShowAdd, Node: "ready", File: "{{work}}/plan.md"}).Words(); w != "show {{work}}/plan.md to the person at ready" {
		t.Errorf("words %q", w)
	}
}

// fail_on_denied takes true, false or the tools whose denials count.
func TestSetFailOnDenied(t *testing.T) {
	d := fixture(t)
	if err := d.Apply(Op{Op: OpSet, Node: "review", Field: "fail_on_denied", Value: "Bash"}); err != nil {
		t.Fatal(err)
	}
	o, err := d.Check()
	if err != nil || strings.Join(o.Nodes["review"].FailOnDeniedTools, ",") != "Bash" || o.Nodes["review"].FailOnDenied {
		t.Fatalf("%v %+v", err, o.Nodes["review"])
	}
	if err := d.Apply(Op{Op: OpSet, Node: "review", Field: "fail_on_denied", Value: "true"}); err != nil {
		t.Fatal(err)
	}
	if o, err = d.Check(); err != nil || !o.Nodes["review"].FailOnDenied {
		t.Fatalf("%v", err)
	}
}

// workdir sets the default folder of agent steps; set and unset change the
// folder of one agent node.
func TestWorkdirOperations(t *testing.T) {
	d := fixture(t)
	if err := d.Apply(Op{Op: OpWorkdir, Workdir: ".."}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(d.Text()), "workdir: ..      # agents work next to this file") {
		t.Fatalf("text:\n%s", d.Text())
	}
	if err := d.Apply(Op{Op: OpSet, Node: "review", Field: "workdir", Value: "."}); err != nil {
		t.Fatal(err)
	}
	o, err := d.Check()
	if err != nil {
		t.Fatal(err)
	}
	if o.Nodes["review"].Workdir != o.Dir {
		t.Fatalf("node workdir %q", o.Nodes["review"].Workdir)
	}
	if err := d.Apply(Op{Op: OpSet, Node: "build", Field: "workdir", Value: "."}); err == nil {
		t.Fatal("workdir on a script accepted")
	}
	if err := d.Apply(Op{Op: OpUnset, Node: "review", Field: "workdir"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(Op{Op: OpSet, Node: "review", Field: "workdir", Value: ".hetman"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Check(); err == nil || !strings.Contains(err.Error(), "closed to agents") {
		t.Fatalf("workdir in the state folder: %v", err)
	}
}
