package explain

import (
	"strings"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/i18n"
)

func TestRetryAndRewindInWords(t *testing.T) {
	var j journal
	j.start("build")
	j.step("build", 1, "ok", "test", 0)
	j.step("test", 1, "fail", "failed", 1)
	j.add(chronicle.Event{Kind: chronicle.Retried, Node: "test", Details: chronicle.Details{By: "ann", Via: "page"}})
	text := explainAll(j)
	for _, s := range []string{
		"ann on the page asked at 2026-03-02 09:00:07 for test to run again with a fresh attempt budget; it has not started since.",
		"2026-03-02 09:00:07  ann on the page asked for test to run again with a fresh attempt budget.",
		"The flow went back to test once.",
	} {
		if !strings.Contains(text, s) {
			t.Fatalf("lacks %q:\n%s", s, text)
		}
	}
	j.step("test", 2, "ok", "done", 0)
	j.add(chronicle.Event{Kind: chronicle.Rewound, Node: "build", Details: chronicle.Details{Via: "command"}})
	text = explainAll(j)
	for _, s := range []string{
		"A person from the command line sent it back to build at 2026-03-02 09:00:11; build and every step after it run again.",
		"A person from the command line sent the campaign back to build; the steps after it run again.",
	} {
		if !strings.Contains(text, s) {
			t.Fatalf("lacks %q:\n%s", s, text)
		}
	}
	uk := Text(In(i18n.For("uk"), chronicle.Campaign{ID: 3, Flow: "release", Started: t0}, j, Summary, "", time.UTC))
	if !strings.Contains(uk, "похід повернули з командного рядка (людина) до build") {
		t.Fatalf("Ukrainian:\n%s", uk)
	}
}
