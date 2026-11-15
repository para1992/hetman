package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/adapter"
)

// The adapter rejects at once, as a permanent error naming the entry, a
// tools entry the client would accept and then never honour.
func TestToolsTheClientWouldNotHonour(t *testing.T) {
	for _, c := range []struct{ tool, want string }{
		{"Write(.hetman/campaigns/*/work/plan.md)", "write Edit(.hetman/campaigns/*/work/plan.md)"},
		{"NotebookEdit(a.ipynb)", "write Edit(a.ipynb)"},
		{"Glob(src/**)", "write Read(src/**)"},
		{"Bash(make", "must end at the closing parenthesis"},
		{"*)", "must end at the closing parenthesis"},
		{"Bash()", "empty parentheses"},
		{"read", "upper-case letter, such as Read"},
		{"Edit(src:*)", "the :* form belongs to Bash rules"},
		{"mcp__files__read", "tool servers are not loaded"},
	} {
		st := testStep(t.TempDir())
		st.Tools = []string{"Read", c.tool}
		f := final(stepRun(t, "normal", st))
		if f.Type != adapter.TypeError || f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, `tools entry "`+c.tool+`"`) || !strings.Contains(f.Message, c.want) {
			t.Errorf("%s: %+v", c.tool, f)
		}
	}
	for _, ok := range []string{"Read", "Edit(docs/**)", "Read(//etc/hosts)", "Bash(go test *)", "Bash(npm run:*)", "WebFetch(domain:example.com)", "Bash(make (x) *)"} {
		if msg := toolProblem(ok); msg != "" {
			t.Errorf("%s rejected: %s", ok, msg)
		}
	}
}

// With the check flag the adapter checks steps without running the client.
func TestCheckFlag(t *testing.T) {
	in := adapter.EncodeCheck([]adapter.Step{
		{NodeID: "plan", Tools: []string{"Read", "Write(plan.md)"}, Effort: "high"},
		{NodeID: "review", Effort: "huge"},
	})
	var out, diag bytes.Buffer
	if code := run([]string{"--client", "/nonexistent", adapter.CheckFlag}, bytes.NewReader(in), &out, &diag); code != 0 {
		t.Fatalf("exit %d: %s", code, diag.String())
	}
	problems, err := adapter.ParseCheckReply(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 2 || problems[0].NodeID != "plan" || problems[0].Field != adapter.FieldTools || problems[0].Item == nil || *problems[0].Item != 1 ||
		problems[1].NodeID != "review" || problems[1].Field != adapter.FieldEffort {
		t.Fatalf("problems %+v", problems)
	}
	var d bytes.Buffer
	run([]string{adapter.DescribeFlag}, strings.NewReader(""), &d, io.Discard)
	desc, _ := adapter.ParseDescription(d.Bytes())
	if !desc.Has(adapter.FeatureCheck) || !desc.Has(adapter.FeatureProtect) {
		t.Fatalf("description %+v", desc)
	}
}

// Protected paths become Edit deny rules, around the writable folder when
// it lies inside one; a writable folder outside the working folder is
// added as a working folder; an Edit rule with a path makes Write
// available.
func TestFence(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "flow", ".hetman")
	work := filepath.Join(state, "campaigns", "4", "work")
	for _, d := range []string{work, filepath.Join(state, "campaigns", "3"), filepath.Join(state, "campaigns", "4", "logs"), filepath.Join(root, "code")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(state, "chronicle.db"), nil, 0o644)
	orders := filepath.Join(root, "flow", "orders.yaml")
	rec := filepath.Join(root, "record.jsonl")
	t.Setenv(envRecord, rec)
	st := testStep(filepath.Join(root, "code"))
	st.Tools = []string{"Read", "Edit(src/**)"}
	st.Protect = []string{state, orders}
	st.Writable = []string{work}
	stepRun(t, "normal", st)
	args := strings.Join(recorded(t, rec)[0].Args, "\n")
	for _, want := range []string{
		"--tools=Read,Edit,Write",
		"--add-dir\n" + work,
		"--disallowedTools=Edit(" + rulePath(filepath.Join(state, "chronicle.db")) + ")",
		"--disallowedTools=Edit(" + rulePath(filepath.Join(state, "campaigns", "3")) + "/**)",
		"--disallowedTools=Edit(" + rulePath(filepath.Join(state, "campaigns", "4", "logs")) + "/**)",
		"--disallowedTools=Edit(" + rulePath(orders) + ")",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("arguments lack %q:\n%s", want, args)
		}
	}
	for _, never := range []string{rulePath(state) + ")", rulePath(work), rulePath(filepath.Join(state, "campaigns")) + ")", rulePath(filepath.Join(state, "campaigns", "4")) + ")"} {
		if strings.Contains(args, never) {
			t.Errorf("the way to the work folder is denied (%s):\n%s", never, args)
		}
	}
	if !strings.HasPrefix(rulePath(work), "//") {
		t.Errorf("rule path %s", rulePath(work))
	}
}

// A working folder reached through a symbolic link, as the temporary folder
// is on macOS: the client reports resolved paths, and the log still shows
// them relative to the folder; the denied calls come with the error; the
// protection denies both spellings.
func TestWorkdirBehindALink(t *testing.T) {
	dir := t.TempDir()
	real, link := filepath.Join(dir, "real"), filepath.Join(dir, "link")
	os.MkdirAll(filepath.Join(real, ".hetman", "campaigns", "1", "work"), 0o755)
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	rec := filepath.Join(dir, "record.jsonl")
	t.Setenv(envRecord, rec)
	st := testStep(link)
	st.Tools = []string{"Read", "Edit(src/**)"}
	st.Protect = []string{filepath.Join(link, ".hetman")}
	st.Writable = []string{filepath.Join(link, ".hetman", "campaigns", "1", "work")}
	msgs := stepRun(t, "denied", st)
	if log := logText(msgs); !strings.Contains(log, "denied: Write plan.md: outside the paths of Edit(src/**)") {
		t.Errorf("log:\n%s", log)
	}
	f := final(msgs)
	if !strings.Contains(f.Message, "(Write plan.md: ") || len(f.Denied) != 1 || !strings.HasPrefix(f.Denied[0], "Write plan.md: ") {
		t.Errorf("final %+v", f)
	}
	args := strings.Join(recorded(t, rec)[0].Args, "\n")
	resolved, _ := filepath.EvalSymlinks(real)
	for _, base := range []string{link, resolved} {
		if want := "--disallowedTools=Edit(" + rulePath(filepath.Join(base, ".hetman", "campaigns", "1", "lock")); strings.Contains(args, want) {
			t.Errorf("a path that does not exist is denied: %s", want)
		}
		if want := "--disallowedTools=Edit(" + rulePath(filepath.Join(base, ".hetman", "campaigns", "1")) + ")"; strings.Contains(args, want) {
			t.Errorf("the way to the work folder is denied:\n%s", args)
		}
	}
	if strings.Contains(args, "--add-dir") {
		t.Errorf("the work folder inside the working folder was added:\n%s", args)
	}
}

// A denied tool call is logged with the tool, its target and the client's
// reason; an answer without JSON after a denial is permanent, since a retry
// meets the same denial.
func TestDeniedCall(t *testing.T) {
	st := testStep(t.TempDir())
	st.Tools = []string{"Read", "Edit(src/**)"}
	msgs := stepRun(t, "denied", st)
	log := logText(msgs)
	if !strings.Contains(log, "denied: Write plan.md: outside the paths of Edit(src/**), or on a path hetman protects; the client said: Permission to use Write has been denied") {
		t.Errorf("log:\n%s", log)
	}
	s := &stream{allowed: []string{"Read", "Bash(make -C *)", "Bash(go test *)"}}
	if got := s.why("Bash"); got != "matched none of the step's entries Bash(make -C *), Bash(go test *)" {
		t.Errorf("why %q", got)
	}
	if got := s.why("WebFetch"); got != "not among the step's tools" {
		t.Errorf("why %q", got)
	}
	if strings.Contains(log, readOnlyNote) {
		t.Errorf("read-only note without Bash:\n%s", log)
	}
	st.Tools = []string{"Bash(go test *)"}
	if log := logText(stepRun(t, "normal", st)); !strings.Contains(log, readOnlyNote) {
		t.Errorf("no read-only note with Bash:\n%s", log)
	}
	if f := final(msgs); f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, "the client denied 1 tool call(s) (Write plan.md: ") {
		t.Errorf("final %+v", f)
	}
}
