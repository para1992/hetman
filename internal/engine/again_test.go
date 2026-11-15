package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/chronicle"
	"hetman/internal/i18n"
)

const againOrders = `version: 0
flow: again
start: a
nodes:
  a: {type: script, run: %A%, on: {ok: b, fail: failed}}
  b: {type: script, run: %B%, on: {ok: c, fail: failed}}
  c: {type: script, run: %C%, on: {ok: done, fail: failed}}
  side: {type: script, run: %C%, on: {ok: done, fail: failed}}
`

func againSetup(t *testing.T, a, b, c string) *fixture {
	r := strings.NewReplacer("%A%", a, "%B%", b, "%C%", c)
	return setup(t, r.Replace(againOrders))
}

func (f *fixture) replayed() chronicle.State {
	return chronicle.Replay(f.events(""))
}

func TestRewindKeepsHistoryAndRunsLaterNodesAgain(t *testing.T) {
	ok := arg("exit", "0")
	f := againSetup(t, ok, ok, arg("artifact"))
	f.drive(Done)
	before := len(f.events(""))
	release, err := Rewind(f.e.Orders, f.chron, f.e.Campaign, "b", "ann", ViaCommand)
	if err != nil {
		t.Fatal(err)
	}
	st := f.replayed()
	if st.End != "" || st.Node != "b" || st.HasOutcome || st.Failures != 0 || st.Attempts["b"] != 1 {
		t.Fatalf("state after rewind %+v", st)
	}
	f.drive(Done)
	release()
	f.trace("a#1", "b#1", "c#1", "b#2", "c#2")
	all := f.events("")
	if len(all) <= before {
		t.Fatal("history shrank")
	}
	rw := f.events(chronicle.Rewound)
	if len(rw) != 1 || rw[0].Node != "b" || rw[0].By != "ann" || rw[0].Via != ViaCommand {
		t.Fatalf("rewound events %+v", rw)
	}
	// Files of the earlier attempts stay; the new attempts have their own.
	dir := CampaignDir(f.e.Orders, f.e.Campaign)
	for _, rel := range []string{LogFile("b", "", 1), LogFile("b", "", 2), LogFile("c", "", 1), LogFile("c", "", 2), "work/out.txt"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	if got := Visited(all); strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("visited %v", got)
	}
}

func TestRetryAfterFailedCampaign(t *testing.T) {
	ok := arg("exit", "0")
	f := againSetup(t, ok, arg("exit", "1"), ok)
	f.drive(Failed)
	if n := RetryNode(f.events("")); n != "b" {
		t.Fatalf("retry node %q", n)
	}
	// The cause is fixed: b passes from now on.
	f.e.Orders.Nodes["b"].Run = []string{f.e.Orders.Nodes["b"].Run[0], "step", "exit", "0"}
	release, node, err := Retry(f.e.Orders, f.chron, f.e.Campaign, "", ViaPage)
	if err != nil || node != "b" {
		t.Fatalf("retry: %q %v", node, err)
	}
	f.drive(Done)
	release()
	f.trace("a#1", "b#1", "b#2", "c#1")
	if r := f.events(chronicle.Retried); len(r) != 1 || r[0].Via != ViaPage {
		t.Fatalf("retried events %+v", r)
	}
	// A done campaign has nothing to retry.
	if _, _, err := Retry(f.e.Orders, f.chron, f.e.Campaign, "", ViaPage); !errors.Is(err, ErrNothingFailed) {
		t.Fatalf("retry of a done campaign: %v", err)
	}
}

func TestRetryAfterErrorStop(t *testing.T) {
	ok := arg("exit", "0")
	f := againSetup(t, arg("badresult"), ok, ok)
	f.drive(Errored)
	if n := RetryNode(f.events("")); n != "a" {
		t.Fatalf("retry node %q", n)
	}
	f.e.Orders.Nodes["a"].Run = []string{f.e.Orders.Nodes["a"].Run[0], "step", "exit", "0"}
	release, _, err := Retry(f.e.Orders, f.chron, f.e.Campaign, "", ViaCommand)
	if err != nil {
		t.Fatal(err)
	}
	f.drive(Done)
	release()
	f.trace("a#1", "a#2", "b#1", "c#1")
}

func TestRetryAndRewindRefusals(t *testing.T) {
	ok := arg("exit", "0")
	f := againSetup(t, ok, arg("exit", "1"), ok)
	f.drive(Failed)
	n := len(f.events(""))
	// Being driven: the lock is held by someone else.
	hold, err := Lock(f.e.Orders, f.e.Campaign)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Retry(f.e.Orders, f.chron, f.e.Campaign, "", ViaCommand)
	_, err2 := Rewind(f.e.Orders, f.chron, f.e.Campaign, "a", "", ViaCommand)
	hold()
	if !errors.Is(err, ErrDriven) || !errors.Is(err2, ErrDriven) {
		t.Fatalf("while driven: %v / %v", err, err2)
	}
	uk := i18n.For("uk")
	if msg := uk.Error(err); !strings.Contains(msg, "привал") {
		t.Fatalf("Ukrainian refusal %q", msg)
	}
	// A node never visited, and one that does not exist.
	for _, node := range []string{"side", "nowhere"} {
		if _, err := Rewind(f.e.Orders, f.chron, f.e.Campaign, node, "", ViaCommand); !errors.Is(err, ErrNotVisited) {
			t.Fatalf("rewind to %s: %v", node, err)
		}
	}
	// A visited node that is no longer in the orders.
	delete(f.e.Orders.Nodes, "a")
	if _, err := Rewind(f.e.Orders, f.chron, f.e.Campaign, "a", "", ViaCommand); !errors.Is(err, ErrNotInOrders) {
		t.Fatalf("rewind to a removed node: %v", err)
	}
	if got := len(f.events("")); got != n {
		t.Fatalf("refusals recorded %d events", got-n)
	}
	// The lock is free again after a refusal.
	release, err := Lock(f.e.Orders, f.e.Campaign)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestRewindOfInterruptedStepCleansUpFirst(t *testing.T) {
	ok := arg("exit", "0")
	f := againSetup(t, ok, ok, ok)
	f.record(chronicle.Event{Kind: chronicle.StepStarted, Node: "a", Attempt: 1})
	release, err := Rewind(f.e.Orders, f.chron, f.e.Campaign, "a", "", ViaCommand)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range f.events("")[1:] {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, " "); got != "step_started process_cleanup step_interrupted rewound" {
		t.Fatalf("events %s", got)
	}
	f.drive(Done)
	release()
	f.trace("a#2", "b#1", "c#1")
}

func TestRetryAfterExhaustedAttempts(t *testing.T) {
	f := setupAgent(t, one("    max_attempts: 2"), map[string]string{"p.md": "Do it.{{rework}}", "s.json": okSchema,
		"plan.json": `{"nodes": {"a": [[{"exit": 3}], [{"exit": 3}], [{"result": {"outcome": "ok"}}]]}}`}, "")
	f.drive(Failed)
	if n := RetryNode(f.events("")); n != "a" {
		t.Fatalf("retry node %q", n)
	}
	release, _, err := Retry(f.e.Orders, f.chron, f.e.Campaign, "", ViaCommand)
	if err != nil {
		t.Fatal(err)
	}
	f.drive(Done)
	release()
	want(t, f.finished(), []string{"a#1 /crash", "a#2 fail/crash", "a#3 ok/"})
	dir := CampaignDir(f.e.Orders, f.e.Campaign)
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(PromptFile("a", 3))))
	if err != nil || !strings.Contains(string(data), "a person asked for this step to be retried") {
		t.Fatalf("prompt of the retry: %v\n%s", err, data)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(PromptFile("a", 1)))); err != nil {
		t.Fatalf("prompt of attempt 1: %v", err)
	}
}
