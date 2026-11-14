package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// runIn runs one command in this process.
func runIn(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// startLanguage is the language a campaign recorded when it started.
func (w *world) startLanguage(id int64) string {
	w.t.Helper()
	ch, err := chronicle.Open(w.dir + "/.hetman/chronicle.db")
	if err != nil {
		w.t.Fatal(err)
	}
	defer ch.Close()
	events, err := ch.Events(id)
	if err != nil || len(events) == 0 || events[0].Kind != chronicle.CampaignStarted {
		w.t.Fatalf("campaign %d: %v %v", id, events, err)
	}
	return events[0].Language
}

func TestLanguageOverrideAndStatus(t *testing.T) {
	w := newWorld(t, `  a: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	plain := readFile(t, w.orders)
	// Without the key: nothing about a language anywhere.
	code, out, errOut := runIn(t, "run", "-f", w.orders)
	if code != exitDone || strings.Contains(out+errOut, "language") || w.startLanguage(1) != "" {
		t.Fatalf("run without a language: %d\n%s%s", code, out, errOut)
	}
	if _, out, _ = runIn(t, "status", "-f", w.orders, "-events"); strings.Contains(out, "language") {
		t.Fatalf("status without a language:\n%s", out)
	}

	if err := os.WriteFile(w.orders, []byte(strings.Replace(plain, "start: a\n", "start: a\nlanguage: hi\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ = runIn(t, "check", "-f", w.orders); code != exitDone || !strings.Contains(out, ", language hi Hindi (हिन्दी))") {
		t.Fatalf("check: %d %s", code, out)
	}
	if code, out, _ = runIn(t, "run", "-f", w.orders); code != exitDone || !strings.Contains(out, "campaign 2 started (flow test, language hi Hindi (हिन्दी))") {
		t.Fatalf("run: %d %s", code, out)
	}
	_, out, _ = runIn(t, "status", "-f", w.orders, "-events")
	if !strings.Contains(out, "language for the person: hi Hindi (हिन्दी)\n") || !strings.Contains(out, "campaign_started a language=hi") {
		t.Fatalf("status:\n%s", out)
	}

	// The environment variable wins over the file.
	t.Setenv(languageEnv, "es")
	if code, out, _ = runIn(t, "run", "-f", w.orders); code != exitDone || w.startLanguage(3) != "es" || !strings.Contains(out, "language es Spanish (español)") {
		t.Fatalf("override: %d %s", code, out)
	}
	if code, out, _ = runIn(t, "check", "-f", w.orders); code != exitDone || !strings.Contains(out, "language es Spanish") {
		t.Fatalf("check with override: %d %s", code, out)
	}
	// An unknown code is refused, and no campaign starts.
	t.Setenv(languageEnv, "xx")
	for _, cmd := range []string{"run", "check"} {
		code, out, errOut = runIn(t, cmd, "-f", w.orders)
		if code != exitUsage || !strings.Contains(errOut, `hetman `+cmd+`: HETMAN_LANGUAGE: unknown language code "xx" (known: ar, en, es, hi, uk, zh)`) {
			t.Fatalf("%s with an unknown override: %d\n%s%s", cmd, code, out, errOut)
		}
	}
	if _, out, _ = runIn(t, "status", "-f", w.orders); !strings.Contains(out, "campaign 3 ") {
		t.Fatalf("a campaign started with an unknown override:\n%s", out)
	}
	// An empty variable is no override.
	t.Setenv(languageEnv, "")
	if code, _, _ = runIn(t, "run", "-f", w.orders); code != exitDone || w.startLanguage(4) != "hi" {
		t.Fatalf("empty override: %d", code)
	}
}

func TestScribeLanguageCommands(t *testing.T) {
	w := newWorld(t, `  a: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	before := readFile(t, w.orders)
	code, out, errOut := scribeCall(t, "", "scribe", "language", "Ukrainian", "-f", w.orders)
	if code != exitUsage || !strings.Contains(errOut, `unknown language code "Ukrainian"; did you mean uk (Ukrainian)?`) || readFile(t, w.orders) != before {
		t.Fatalf("a name instead of a code: %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = scribeCall(t, "", "scribe", "language", "uk", "-f", w.orders)
	if code != exitDone || !strings.Contains(out, "Write what the flow addresses to a person in Ukrainian (українська), code uk.") || !strings.Contains(out, "+language: uk") {
		t.Fatalf("language: %d\n%s%s", code, out, errOut)
	}
	if o, err := orders.Load(w.orders); err != nil || o.Language != "uk" {
		t.Fatalf("not written: %v", err)
	}
	if code, out, _ = scribeCall(t, "", "scribe", "language", "uk", "-f", w.orders); code != exitDone || !strings.Contains(out, "nothing to write") {
		t.Fatalf("again: %d %s", code, out)
	}
	if code, out, errOut = scribeCall(t, "", "scribe", "unlanguage", "-f", w.orders); code != exitDone || !strings.Contains(out, "-language: uk") || readFile(t, w.orders) != before {
		t.Fatalf("unlanguage: %d\n%s%s", code, out, errOut)
	}
	if code, _, errOut = scribeCall(t, "", "scribe", "language", "-f", w.orders); code != exitUsage || !strings.Contains(errOut, "usage: hetman scribe language CODE") {
		t.Fatalf("no code: %d %s", code, errOut)
	}
}

func TestScribeSentenceToLanguage(t *testing.T) {
	w := scribeWorld(t, plan("questions should be in Ukrainian", `{"outcome": "operation", "operation": {"op": "language", "language": "uk"}}`))
	code, out, _ := scribeCall(t, "y\n", "scribe", "-f", w.orders, "questions should be in Ukrainian")
	if code != exitDone || !strings.Contains(out, "Scribe proposes: write what the flow addresses to a person in Ukrainian (українська), code uk.") {
		t.Fatalf("%d\n%s", code, out)
	}
	if o, err := orders.Load(w.orders); err != nil || o.Language != "uk" {
		t.Fatalf("not applied: %v", err)
	}
	if p := w.requests()[0]["prompt"].(string); !strings.Contains(p, "  language: write what the flow addresses to a person") || !strings.Contains(p, "language: the code of the language for the person, one of ar, en, es, hi, uk, zh.") {
		t.Fatalf("the prompt does not offer the operation:\n%s", p)
	}
}
