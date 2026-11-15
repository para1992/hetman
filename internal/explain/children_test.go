package explain

import (
	"strings"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/i18n"
)

func TestForEachChildrenAndLocks(t *testing.T) {
	var j journal
	j.start("each")
	j.add(chronicle.Event{Kind: chronicle.LockWaiting, Node: "each", Attempt: 1, Details: chronicle.Details{Lock: "stand", Holder: "campaign 9 at test"}})
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "each", Attempt: 1})
	j.add(chronicle.Event{Kind: chronicle.ChildStarted, Node: "each", Attempt: 1, Details: chronicle.Details{Child: 4, Item: "a"}})
	j.add(chronicle.Event{Kind: chronicle.ChildStarted, Node: "each", Attempt: 1, Details: chronicle.Details{Child: 5, Item: "b"}})
	j.add(chronicle.Event{Kind: chronicle.ItemFinished, Node: "each", Attempt: 1, Outcome: "failed", Details: chronicle.Details{Child: 4, Item: "a"}})
	j.add(chronicle.Event{Kind: chronicle.ItemNotRun, Node: "each", Attempt: 1, Outcome: "failed", Details: chronicle.Details{Item: "c", Needs: "a"}})
	got := explainAll(j)
	for _, want := range []string{
		"each waited for the lock stand, held by campaign 9 at test.",
		"each ran item a as campaign 4.",
		"each: item a (campaign 4) ended failed.",
		"each: item c was not run: it needs a, which failed.",
		"Its child campaign campaign 5 (item b) has not finished yet.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("explanation lacks %q:\n%s", want, got)
		}
	}
	uk := Text(In(i18n.For("uk"), chronicle.Campaign{ID: 3, Flow: "release", Started: t0}, j, Summary, "", time.UTC))
	if !strings.Contains(uk, "Ще не завершився 1 дочірній похід: похід 5 (елемент b).") || !strings.Contains(uk, "елемент c не запускався") {
		t.Fatalf("Ukrainian explanation:\n%s", uk)
	}
}

func TestWaitingForALock(t *testing.T) {
	var j journal
	j.start("a")
	j.add(chronicle.Event{Kind: chronicle.LockWaiting, Node: "a", Attempt: 1, Details: chronicle.Details{Lock: "stand", Holder: "campaign 2 at a"}})
	if got := explainAll(j); !strings.Contains(got, "Since 2026-03-02 09:00:01 a waits for the lock stand, held by campaign 2 at a.") {
		t.Fatalf("explanation:\n%s", got)
	}
}
