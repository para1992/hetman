package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hetman/internal/adapter"
)

// The adapter rejects at once, as a permanent error naming the entry, a
// tools entry the client would not enforce as written.
func TestToolsTheClientWouldNotEnforce(t *testing.T) {
	for _, c := range []struct{ tool, want string }{
		{"bash(go test *)", "cannot limit commands by pattern"},
		{"shell", "not a tool of its own"},
		{"read(*.md)", "cannot limit reading"},
		{"write(src/*.go)", "only whole folders"},
		{"edit", "governed by the client's sandbox"},
		{"Glob", "not an entry of this adapter"},
		{"Read", "lower case"},
		{"network(example.com)", "takes no pattern"},
		{"mcp__files__read", "tool servers are not loaded"},
		{"write(docs", "must end at the closing parenthesis"},
		{"write()", "empty parentheses"},
	} {
		st := testStep(t.TempDir())
		st.Tools = []string{"read", c.tool}
		f := final(stepRun(t, "ok", st))
		if f.Type != adapter.TypeError || f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, `tools entry "`+c.tool+`"`) || !strings.Contains(f.Message, c.want) {
			t.Errorf("%s: %+v", c.tool, f)
		}
	}
	st := testStep(t.TempDir())
	st.Tools = []string{"network"}
	if f := final(stepRun(t, "ok", st)); f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, "add read or write") {
		t.Errorf("network alone: %+v", f)
	}
}

// With the check flag the adapter checks steps without running a model.
func TestCheckFlag(t *testing.T) {
	t.Setenv(envFake, "ok")
	in := adapter.EncodeCheck([]adapter.Step{
		{NodeID: "plan", Tools: []string{"read", "bash"}},
		{NodeID: "a", Model: "gpt-5.6-luna", Effort: "ultra"},
		{NodeID: "b", Model: "gpt-missing"},
		{NodeID: "c", Model: "gpt-6.1-sol", Effort: "medium"},
		{NodeID: "d", Model: "codex-auto-review", Effort: "low", Tools: []string{"write(docs)", "network", "web_search"}},
	})
	var out, diag bytes.Buffer
	if code := run(append(fakeArgs(t), adapter.CheckFlag), bytes.NewReader(in), &out, &diag); code != 0 {
		t.Fatalf("exit %d: %s", code, diag.String())
	}
	problems, err := adapter.ParseCheckReply(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range problems {
		got = append(got, p.NodeID+" "+p.Field)
	}
	want := []string{"plan tools", "a effort", "b model", "c effort"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("problems %v (%+v), want %v", got, problems, want)
	}
	if problems[0].Item == nil || *problems[0].Item != 1 || problems[2].Warning {
		t.Errorf("problems %+v", problems)
	}

	// Without a catalogue a model is only unverified.
	t.Setenv(envNoCatalog, "all")
	out.Reset()
	run(append(fakeArgs(t), adapter.CheckFlag), bytes.NewReader(adapter.EncodeCheck([]adapter.Step{{NodeID: "b", Model: "gpt-missing"}})), &out, io.Discard)
	problems, _ = adapter.ParseCheckReply(out.Bytes())
	if len(problems) != 1 || !problems[0].Warning || !strings.Contains(problems[0].Message, "not verified") {
		t.Errorf("problems %+v", problems)
	}
}

// An effort the model does not offer is rejected before the client runs.
func TestEffort(t *testing.T) {
	cases := []struct{ model, effort, problem string }{
		{"gpt-5.6-luna", "max", ""},
		{"gpt-6.1-sol", "medium", "is not offered by model"},
		{"gpt-6.1-sol", "ultra", "delegates to sub-agents"},
		{"gpt-unlisted", "low", ""},
		{"", "low", ""},
		{"gpt-5.6-luna", "Very High", "not a reasoning effort name"},
	}
	for _, c := range cases {
		t.Run(c.model+" "+c.effort, func(t *testing.T) {
			rec := record(t)
			st := testStep(t.TempDir())
			st.Model, st.Effort = c.model, c.effort
			f := final(stepRun(t, "ok", st))
			if c.problem == "" {
				if f.Type != adapter.TypeResult {
					t.Fatalf("final %+v", f)
				}
				return
			}
			if f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, c.problem) {
				t.Fatalf("final %+v, want a permanent error with %q", f, c.problem)
			}
			if len(runs(t, rec)) > 0 {
				t.Error("the client was run")
			}
		})
	}
}

// The profile of a step that writes: the working folder and the writable
// folders write, the protected paths read; a writable folder inside a
// protected one stays writable.
func TestSandboxProfile(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "flow", ".hetman")
	work := filepath.Join(state, "campaigns", "2", "work")
	os.MkdirAll(work, 0o755)
	code := filepath.Join(root, "code")
	os.MkdirAll(code, 0o755)
	st := adapter.Step{Workdir: code, Protect: []string{state, filepath.Join(root, "flow", "orders.yaml")}, Writable: []string{work, filepath.Join(root, "gone")}}
	ts, _ := parseTools([]string{"write"})
	sb, err := sandboxFor(st, ts)
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{}
	for _, e := range sb.entries {
		modes[e.path] = e.mode
	}
	for p, want := range map[string]string{code: "write", work: "write", state: "read", filepath.Join(root, "flow", "orders.yaml"): "read"} {
		if modes[p] != want {
			t.Errorf("%s: mode %q, want %q (entries %+v)", p, modes[p], want, sb.entries)
		}
	}
	if sb.extends != ":workspace" || len(sb.notes) != 1 || !strings.Contains(sb.notes[0], "does not exist") {
		t.Errorf("sandbox %+v", sb)
	}

	ts, _ = parseTools([]string{"read"})
	sb, _ = sandboxFor(st, ts)
	if sb.extends != ":read-only" || len(sb.entries) != 0 || len(sb.notes) != 1 {
		t.Errorf("read-only sandbox %+v", sb)
	}
}

func TestTomlString(t *testing.T) {
	if got := tomlString(`C:\Users\a "b"` + "\n"); got != `"C:\\Users\\a \"b\"\u000A"` {
		t.Errorf("tomlString = %s", got)
	}
}
