package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/adapter"
)

// The adapter rejects at once, as a permanent error naming the entry, a
// tools entry the client would not honour as written.
func TestToolsTheClientWouldNotHonour(t *testing.T) {
	for _, c := range []struct{ tool, want string }{
		{"write", "governs writing files with the edit permission"},
		{"write(plan.md)", "governs writing files with the edit permission"},
		{"webfetch(https://example.com/*)", "takes no pattern for webfetch"},
		{"external_directory", "set by the adapter"},
		{"bash(make", "must end at the closing parenthesis"},
		{"task", "refused"},
	} {
		st := testStep(t.TempDir())
		st.Tools = []string{"read", c.tool}
		f := final(stepRun(t, "normal", st))
		if f.Type != adapter.TypeError || f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, `tools entry "`+c.tool+`"`) || !strings.Contains(f.Message, c.want) {
			t.Errorf("%s: %+v", c.tool, f)
		}
	}
}

// With the check flag the adapter checks steps without running a model.
func TestCheckFlag(t *testing.T) {
	in := adapter.EncodeCheck([]adapter.Step{{NodeID: "plan", Tools: []string{"read", "write"}}})
	var out, diag bytes.Buffer
	if code := run([]string{"--client", "/nonexistent", adapter.CheckFlag}, bytes.NewReader(in), &out, &diag); code != 0 {
		t.Fatalf("exit %d: %s", code, diag.String())
	}
	problems, err := adapter.ParseCheckReply(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || problems[0].NodeID != "plan" || problems[0].Item == nil || *problems[0].Item != 1 {
		t.Fatalf("problems %+v", problems)
	}
	var d bytes.Buffer
	run([]string{adapter.DescribeFlag}, strings.NewReader(""), &d, io.Discard)
	desc, _ := adapter.ParseDescription(d.Bytes())
	if !desc.Has(adapter.FeatureCheck) || !desc.Has(adapter.FeatureProtect) {
		t.Fatalf("description %+v", desc)
	}
}

// pathForms writes a path relative to the working folder and to every
// folder above it, as the client may write it relative to its worktree.
func TestPathForms(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "proj")
	p := filepath.Join(root, "proj", "flow", ".hetman")
	got := strings.Join(pathForms(work, p), " ")
	for _, want := range []string{"flow/.hetman flow/.hetman/*", "proj/flow/.hetman proj/flow/.hetman/*"} {
		if !strings.Contains(got, want) {
			t.Errorf("forms %s lack %s", got, want)
		}
	}
}

// When the step may edit everywhere, the protected paths are denied after
// its rules and the writable folder allowed again after that; with edit
// patterns only, the protected paths are denied entry by entry around the
// writable folder. A writable folder outside the working folder is an
// allowed external directory.
func TestFenceConfiguration(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "flow", ".hetman")
	work := filepath.Join(state, "campaigns", "2", "work")
	os.MkdirAll(work, 0o755)
	os.MkdirAll(filepath.Join(root, "code"), 0o755)
	os.WriteFile(filepath.Join(state, "chronicle.db"), nil, 0o644)
	st := adapter.Step{Workdir: filepath.Join(root, "code"), Protect: []string{state}, Writable: []string{work}}
	rules, _ := permissions([]string{"read", "edit"})
	cfg := agentConfig("a", fence(st, rules))
	var v map[string]any
	if err := json.Unmarshal([]byte(cfg), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, cfg)
	}
	rel := func(p string) string { r, _ := filepath.Rel(st.Workdir, p); return filepath.ToSlash(r) }
	deny, allow := `"`+rel(state)+`/*":"deny"`, `"`+rel(work)+`/*":"allow"`
	if !strings.Contains(cfg, `"edit":{"*":"allow",`) || !strings.Contains(cfg, deny) || strings.Index(cfg, allow) < strings.Index(cfg, deny) {
		t.Errorf("edit everywhere:\n%s", cfg)
	}
	if !strings.Contains(cfg, `"external_directory":{"`+filepath.ToSlash(work)+`":"allow","`+filepath.ToSlash(work)+`/*":"allow"}`) {
		t.Errorf("external directory:\n%s", cfg)
	}
	rules, _ = permissions([]string{"edit(src/*)"})
	cfg = agentConfig("a", fence(st, rules))
	if !strings.Contains(cfg, `"src/*":"allow"`) || !strings.Contains(cfg, `"`+rel(filepath.Join(state, "chronicle.db"))+`":"deny"`) ||
		strings.Contains(cfg, `"`+rel(state)+`/*":"deny"`) || strings.Contains(cfg, rel(work)+`/*":"allow"`) {
		t.Errorf("edit patterns:\n%s", cfg)
	}
}

// A working folder reached through a symbolic link: paths the client
// reports resolved are shown relative to it, and the edit rules name the
// protected paths in every spelling the client may use.
func TestWorkdirBehindALink(t *testing.T) {
	dir := t.TempDir()
	real, link := filepath.Join(dir, "real"), filepath.Join(dir, "link")
	os.MkdirAll(filepath.Join(real, "flow", ".hetman"), 0o755)
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	resolved, _ := filepath.EvalSymlinks(real)
	s := newStream(adapter.Step{Workdir: link}, nil)
	if got := s.rel(filepath.Join(resolved, "src", "a.go")); got != filepath.Join("src", "a.go") {
		t.Errorf("rel %q", got)
	}
	if got := s.short("wrote " + filepath.Join(resolved, "a.go")); got != "wrote a.go" {
		t.Errorf("short %q", got)
	}
	forms := strings.Join(pathForms(link, filepath.Join(link, "flow", ".hetman")), " ")
	rel, _ := filepath.Rel("/", filepath.Join(resolved, "flow", ".hetman"))
	for _, want := range []string{"flow/.hetman/*", filepath.ToSlash(rel) + "/*"} {
		if !strings.Contains(" "+forms+" ", " "+want+" ") {
			t.Errorf("forms lack %s: %s", want, forms)
		}
	}
}

// A denied tool call is logged with the tool, its target and the client's
// reason; an answer without JSON after a denial is permanent.
func TestDeniedCall(t *testing.T) {
	msgs := stepRun(t, "denied", testStep(t.TempDir()))
	log := logText(msgs)
	if !strings.Contains(log, "denied: write plan.md: The user has specified a rule which prevents you from using this specific tool call.") ||
		strings.Contains(log, "relevant rules") {
		t.Errorf("log:\n%s", log)
	}
	if f := final(msgs); f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, "the client denied 1 tool call(s)") || len(f.Denied) != 1 {
		t.Errorf("final %+v", f)
	}
}
