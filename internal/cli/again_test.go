package cli

import (
	"os"
	"strings"
	"testing"

	"hetman/internal/chronicle"
	"hetman/internal/teststep"
)

func TestRetryAndRewindCommands(t *testing.T) {
	w := newWorld(t, "  a: {type: script, run: "+arg("exit", "0")+", on: {ok: b, fail: failed}}\n"+
		"  b: {type: script, run: "+arg("exit", "1")+", on: {ok: done, fail: failed}}\n")
	w.call(exitFailed, "run")
	// Nothing visited at x; the refusal says why and records nothing.
	before := len(w.events())
	if out := w.call(exitFailed, "rewind", "-to", "x"); !strings.Contains(out, "campaign 1 has never been at x") {
		t.Fatalf("rewind to x:\n%s", out)
	}
	if out := w.call(exitUsage, "rewind"); !strings.Contains(out, "-to NODE") {
		t.Fatalf("rewind without -to:\n%s", out)
	}
	if n := len(w.events()); n != before {
		t.Fatalf("refusals recorded %d events", n-before)
	}
	// The cause is fixed, then the failed step runs again.
	text, _ := os.ReadFile(w.orders)
	os.WriteFile(w.orders, []byte(strings.Replace(string(text), "'exit', '1'", "'exit', '0'", 1)), 0o644)
	if out := w.call(exitDone, "retry", "-by", "ann"); !strings.Contains(out, "campaign 1 runs b again with a fresh attempt budget") {
		t.Fatalf("retry:\n%s", out)
	}
	if out := w.call(exitFailed, "retry"); !strings.Contains(out, "campaign 1 has nothing to retry") {
		t.Fatalf("second retry:\n%s", out)
	}
	if out := w.call(exitDone, "rewind", "-c", "1", "-to", "a"); !strings.Contains(out, "campaign 1 is sent back to a") {
		t.Fatalf("rewind:\n%s", out)
	}
	if got := strings.Join(teststep.Trace(w.work()), " "); got != "a#1 b#1 b#2 a#2 b#3" {
		t.Fatalf("trace %s", got)
	}
	var kinds []string
	for _, e := range w.events() {
		if e.Kind == chronicle.Retried || e.Kind == chronicle.Rewound {
			kinds = append(kinds, e.Kind+" "+e.Node+" "+e.By+" "+e.Via)
		}
	}
	if len(kinds) != 2 || kinds[0] != "retried b ann command" || !strings.HasPrefix(kinds[1], "rewound a ") {
		t.Fatalf("events %q", kinds)
	}
}
