package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/orders"
)

// The list flags of hetman scribe split only where an entry ends, so the
// tools list of the README can be written with the command, and a limited
// transition and the workdir have flags of their own.
func TestScribeListsLimitsAndWorkdir(t *testing.T) {
	w := newAgentWorld(t, `{"trace": "trace.jsonl"}`, "Do it.")
	f := []string{"-f", w.orders}
	code, _, errOut := scribeCall(t, "", append([]string{"scribe", "set", "a", "tools", "Read,Bash(make -C *),Bash(go test *)"}, f...)...)
	if code != exitDone {
		t.Fatalf("set tools: %d %s", code, errOut)
	}
	code, _, errOut = scribeCall(t, "", append([]string{"scribe", "add", "agent", "b", "-after", "a", "-executor", "stub",
		"-tools", "Read, Edit, Write, Grep, Glob, Bash(go test *)", "-prompt", "p.md", "-schema", "s.json"}, f...)...)
	if code != exitDone {
		t.Fatalf("add with -tools: %d %s", code, errOut)
	}
	if code, _, errOut = scribeCall(t, "", append([]string{"scribe", "link", "b", "ok", "a", "-max", "2", "-then", "stuck"}, f...)...); code != exitDone {
		t.Fatalf("link with a limit: %s", errOut)
	}
	if code, _, errOut = scribeCall(t, "", append([]string{"scribe", "workdir", ".."}, f...)...); code != exitDone {
		t.Fatalf("workdir: %s", errOut)
	}
	o, err := orders.Load(w.orders)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(o.Nodes["a"].Tools, "|"); got != "Read|Bash(make -C *)|Bash(go test *)" {
		t.Errorf("tools of a %q", got)
	}
	if got := strings.Join(o.Nodes["b"].Tools, "|"); got != "Read|Edit|Write|Grep|Glob|Bash(go test *)" {
		t.Errorf("tools of b %q", got)
	}
	if lim := o.Nodes["b"].Limits["ok"]; lim.Max != 2 || lim.Then != "stuck" {
		t.Errorf("limit %+v", lim)
	}
	if o.Workdir != filepath.Dir(w.dir) {
		t.Errorf("workdir %q", o.Workdir)
	}
}

// The orders' language key is the flow's, not the interface's: without
// HETMAN_LANGUAGE the command line speaks English.
func TestInterfaceLanguageIsThePersons(t *testing.T) {
	t.Setenv("HETMAN_LANGUAGE", "")
	w := newAgentWorld(t, `{"trace": "trace.jsonl"}`, "Do it.")
	text, _ := os.ReadFile(w.orders)
	if err := os.WriteFile(w.orders, []byte(strings.Replace(string(text), "flow: test\n", "flow: test\nlanguage: uk\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"check", "-f", w.orders}, &stdout, &stderr); code != exitDone || !strings.Contains(stdout.String(), ": ok (flow test,") {
		t.Fatalf("check exit %d:\n%s%s", code, stdout.String(), stderr.String())
	}
	t.Setenv("HETMAN_LANGUAGE", "uk")
	stdout.Reset()
	if Main([]string{"check", "-f", w.orders}, &stdout, &stderr); strings.Contains(stdout.String(), ": ok (flow test,") {
		t.Fatalf("with HETMAN_LANGUAGE=uk:\n%s", stdout.String())
	}
}

// hetman check rejects an entry with unbalanced parentheses at its line,
// and asks an adapter that can check steps about the rest.
func TestCheckAsksTheAdapter(t *testing.T) {
	w := newAgentWorld(t, `{"features": ["resume", "check"], "reject_tools": ["Write(plan.md)"]}`, "Do it.")
	text, err := os.ReadFile(w.orders)
	if err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		if err := os.WriteFile(w.orders, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	config := strings.Replace(string(text), "max_attempts: 1,", "max_attempts: 1, tools: [Read, \"Write(plan.md)\"],", 1)
	write(config)
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"check", "-f", w.orders}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("check exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), `orders.yaml:8:`) || !strings.Contains(stderr.String(), `node "a": tools item "Write(plan.md)": the stub rejects this entry`) {
		t.Fatalf("stderr:\n%s", stderr.String())
	}
	write(strings.Replace(string(text), "max_attempts: 1,", "max_attempts: 1, tools: [Read, \"Bash(make\", \"-C\", \"*)\"],", 1))
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"check", "-f", w.orders}, &stdout, &stderr); code != exitUsage || !strings.Contains(stderr.String(), `tools item "Bash(make": an opening parenthesis without a closing one`) {
		t.Fatalf("check exit %d\n%s", code, stderr.String())
	}
}
