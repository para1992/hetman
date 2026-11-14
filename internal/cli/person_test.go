package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

const askingNodes = `  a: {type: script, run: STEP, on: {ok: g, fail: failed}}
  g:
    type: gate
    ask:
      - {id: scope, question: "Is it all there?"}
      - {id: risk, question: "Can it be undone?"}
    show: [notes.md]
    on: {pass: h, blocked: a}
  h:
    type: human
    ask:
      - {id: channel, question: "Which channel?", kind: choice, options: [stable, beta]}
    on: {ship: done, stop: failed}
`

func newAskingWorld(t *testing.T) *world {
	w := newWorld(t, strings.Replace(askingNodes, "STEP", arg("exit", "0"), 1))
	os.WriteFile(filepath.Join(w.dir, "notes.md"), []byte("notes"), 0o644)
	return w
}

func (w *world) waitAsked(after int) chronicle.Event {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(w.dir, ".hetman", "chronicle.db")); err == nil {
			var asked []chronicle.Event
			for _, e := range w.events() {
				if e.Kind == chronicle.Asked {
					asked = append(asked, e)
				}
			}
			if len(asked) > after {
				return asked[after]
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for question %d\n%s", after+1, w.diagnose())
	return chronicle.Event{}
}

func TestWaitingForAnAnswerSurvivesKilledEngine(t *testing.T) {
	w := newAskingWorld(t)
	engineProc, out := w.process("run")
	w.waitAsked(0)
	status := w.call(0, "status")
	for _, want := range []string{"state: waiting for a person at g: hetman answer (the running hetman process continues once answered)",
		"> g     gate    asking", "gate g asks (question #", "look at: " + filepath.Join(w.dir, "notes.md"),
		"1. Is it all there?  [scope: yes/no]", "-a scope=yes -a risk=yes"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}
	if err := engineProc.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	engineProc.Wait()
	killed := out.String()
	if status := w.call(0, "status"); !strings.Contains(status, "no hetman process drives it: after answering, hetman resume continues it") {
		t.Fatalf("status after kill:\n%s", status)
	}
	// A wrong answer changes nothing; a right one is recorded and waits for
	// resume, since nothing drives the campaign.
	if out := w.call(exitUsage, "answer", "-a", "scope=yes", "-a", "risk=perhaps"); !strings.Contains(out, "answer yes or no") || !strings.Contains(out, "nothing was recorded") {
		t.Fatalf("bad answer output:\n%s", out)
	}
	answered := w.call(0, "answer", "-a", "scope=yes", "-a", "risk=no", "-comment", "risk=drops a column", "-by", "alice")
	if !strings.Contains(answered, "no hetman process drives campaign 1; continue it with: hetman resume -f") {
		t.Fatalf("answer output:\n%s", answered)
	}
	if out := w.call(exitFailed, "answer", "-a", "scope=yes", "-a", "risk=yes"); !strings.Contains(out, "already answered") {
		t.Fatalf("second answer output:\n%s", out)
	}
	// Resume goes on: the gate is blocked, the flow returns to a and the
	// gate asks again.
	resumed, rout := w.process("resume")
	w.waitAsked(1)
	w.call(0, "answer", "-a", "scope=yes", "-a", "risk=yes")
	w.waitAsked(2)
	w.call(0, "answer", "-a", "channel=beta", "-outcome", "ship")
	resumed.Wait()
	if code := resumed.ProcessState.ExitCode(); code != exitDone {
		t.Fatalf("resume exit %d\n%s", code, w.diagnose(killed, rout.String()))
	}
	status = w.call(0, "status", "-events")
	for _, want := range []string{"state: done", "answers at g by alice (command)", "  risk: no (drops a column)", "  outcome: ship",
		"answered g attempt=1", `answers=[{"id":"scope","value":"yes"},{"id":"risk","value":"no","comment":"drops a column"}]`} {
		if !strings.Contains(status, want) {
			t.Fatalf("final status lacks %q:\n%s", want, status)
		}
	}
}

func TestInteractiveAnswerContinuesTheRunningCampaign(t *testing.T) {
	w := newAskingWorld(t)
	engineProc, out := w.process("run")
	w.waitAsked(0)
	defer func() { Stdin = os.Stdin }()
	// A reply typed wrong is asked again; numbers pick an option.
	Stdin = strings.NewReader("maybe\ny\n\nyes\nlooks fine\n")
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"answer", "-f", w.orders}, &stdout, &stderr); code != exitDone {
		t.Fatalf("answer exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	if s := stdout.String(); !strings.Contains(s, "answer yes or no, not \"maybe\"") || !strings.Contains(s, "the hetman process that drives campaign 1 continues it") {
		t.Fatalf("answer output:\n%s", s)
	}
	w.waitAsked(1)
	Stdin = strings.NewReader("2\n\nstop\n")
	stdout.Reset()
	if code := Main([]string{"answer", "-f", w.orders}, &stdout, &stderr); code != exitDone {
		t.Fatalf("answer exit %d\n%s%s", code, stdout.String(), stderr.String())
	}
	engineProc.Wait()
	if code := engineProc.ProcessState.ExitCode(); code != exitFailed {
		t.Fatalf("engine exit %d, want %d (outcome stop)\n%s", code, exitFailed, out)
	}
	var h chronicle.Event
	for _, e := range w.events() {
		if e.Kind == chronicle.Answered && e.Node == "h" {
			h = e
		}
	}
	if h.Outcome != "stop" || h.Answers[0].Value != "beta" || h.Via != "command" {
		t.Fatalf("answer of h %+v", h)
	}
	// Nothing is asked any more.
	stderr.Reset()
	if code := Main([]string{"answer", "-f", w.orders, "-a", "x=1"}, &stdout, &stderr); code != exitFailed || !strings.Contains(stderr.String(), "has ended") {
		t.Fatalf("answer after the end: exit %d\n%s", code, stderr.String())
	}
}

// The person example runs its stub adapter, its check and its hook through
// separately built programs and sh; here the test binary stands in for them.
func TestPersonExample(t *testing.T) {
	src := filepath.Join("..", "..", "examples", "person")
	dir := tempDir(t)
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(dir, ".hetman"))
	os.RemoveAll(filepath.Join(dir, "bin"))
	notices := filepath.Join(dir, "notices")
	os.Mkdir(notices, 0o755)
	path := filepath.Join(dir, orders.DefaultFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	config := string(data)
	for old, repl := range map[string]string{
		"[./bin/stub-adapter, plans/happy.json]": teststep.Adapter("plans/happy.json"),
		"[sh, checks/plan-format.sh]":            arg("exit", "0"),
		"[sh, notify.sh]":                        arg("notice", notices),
	} {
		if !strings.Contains(config, old) {
			t.Fatalf("the example no longer runs %s", old)
		}
		config = strings.Replace(config, old, repl, 1)
	}
	os.WriteFile(path, []byte(config), 0o644)
	w := &world{t: t, dir: dir, orders: path, sync: dir}
	run, out := w.process("run", "-input", filepath.Join(dir, "input.json"))
	w.waitAsked(0)
	w.call(0, "answer", "-a", "downtime=yes", "-a", "when=next month")
	w.waitAsked(1)
	w.call(0, "answer", "-a", "keep=the start page")
	w.waitAsked(2)
	w.call(0, "answer", "-a", "scope=yes", "-a", "risk=no", "-comment", "risk=no way back")
	w.waitAsked(3)
	w.call(0, "answer", "-a", "scope=yes", "-a", "risk=yes")
	w.waitAsked(4)
	w.call(0, "answer", "-a", "channel=beta", "-a", "notes=one hour read-only", "-outcome", "ship")
	run.Wait()
	if code := run.ProcessState.ExitCode(); code != exitDone {
		t.Fatalf("run exit %d\n%s", code, w.diagnose(out.String()))
	}
	campaign := filepath.Join(dir, ".hetman", "campaigns", "1")
	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(campaign, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, rel := range []string{"prompts/interview.1.txt", "prompts/interview.3.txt", "prompts/plan.2.txt", "prompts/announce.1.txt"} {
		if p := read(rel); strings.Count(p, "Write all text meant for a person in Spanish (español):") != 1 {
			t.Fatalf("%s lacks the language instruction:\n%s", rel, p)
		}
	}
	if p := read("prompts/interview.3.txt"); !strings.Contains(p, "Answer: the start page") || !strings.Contains(p, "You may not ask any more questions") {
		t.Fatalf("third interview prompt:\n%s", p)
	}
	if p := read("prompts/plan.2.txt"); !strings.Contains(p, "Comment: no way back") || !strings.Contains(p, "keep the address of the start page") {
		t.Fatalf("reworked plan prompt:\n%s", p)
	}
	if p := read("prompts/announce.1.txt"); !strings.Contains(p, "for the beta channel") || !strings.Contains(p, "one hour read-only") {
		t.Fatalf("announce prompt:\n%s", p)
	}
	for _, n := range []string{"notice.dialogue.interview.json", "notice.gate.plan_approved.json", "notice.human.release.json"} {
		if _, err := os.Stat(filepath.Join(notices, n)); err != nil {
			t.Fatalf("hook not called: %v", err)
		}
	}
}
