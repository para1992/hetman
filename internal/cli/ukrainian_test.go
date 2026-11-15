package cli

import (
	"strings"
	"testing"
)

// A campaign that went back for rework and waits for a person, read in
// Ukrainian: the progress lines, the answer, status and the explanation.
// The orders keys, node and outcome names and commands stay as written.
func TestUkrainianCampaign(t *testing.T) {
	t.Setenv(languageEnv, "uk")
	w := newAskingWorld(t)
	engineProc, out := w.process("run")
	defer func() {
		engineProc.Process.Kill()
		engineProc.Wait()
	}()
	w.waitAsked(0)
	code, answered, errOut := runIn(t, "answer", "-f", w.orders, "-a", "scope=no", "-a", "risk=yes", "-by", "olena")
	if code != exitDone || !strings.Contains(answered, "відповіді записано: похід 1, крок g") ||
		!strings.Contains(answered, "процес hetman, що веде похід 1, продовжить його") {
		t.Fatalf("answer: %d\n%s%s", code, answered, errOut)
	}
	w.waitAsked(1)

	code, status, _ := runIn(t, "status", "-f", w.orders)
	for _, want := range []string{
		"похід 1  потік test  розпочато ",
		"мова для людини: uk Ukrainian (українська)",
		"стан: чекає на людину на кроці g: hetman answer (запущений процес hetman продовжить, щойно буде відповідь)",
		"КРОК", "ПІДСУМОК", "СПРОБИ",
		"> g     gate    питає",
		"відповіді на кроці g від olena (командний рядок), ",
		"  scope: no",
		"застава g питає (запитання #",
		"  перегляньте: ",
		"1. Is it all there?  [scope: yes/no]",
		"  відповісти: hetman answer",
		"         або: hetman answer",
	} {
		if code != exitDone || !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}

	code, words, errOut := runIn(t, "scribe", "explain", "-f", w.orders)
	for _, want := range []string{
		"Де похід зараз:",
		"Похід 1 потоку test розпочато ",
		"похід чекає на людину: застава g ставить 2 запитання: \"Is it all there?\"; \"Can it be undone?\".",
		"Повернення на доопрацювання:",
		"Потік повертався до a один раз.",
		"Крок g дав підсумок blocked і повернув потік до a (olena: «ні» на scope).",
		"Кроку g відповіли з командного рядка (olena): scope: no, risk: yes.",
		"Жодна м'яка перевірка не попередила.",
	} {
		if code != exitDone || !strings.Contains(words, want) {
			t.Fatalf("explanation lacks %q:\n%s%s", want, words, errOut)
		}
	}

	// The engine still writes to out; stop it before reading.
	engineProc.Process.Kill()
	engineProc.Wait()
	progress := out.String()
	for _, want := range []string{"похід 1 розпочато (потік test, мова uk Ukrainian (українська))", "[1] a: спроба 1 почалася",
		"[1] a -> g", "[1] g чекає на людину (2 запитання); відповісти: hetman answer"} {
		if !strings.Contains(progress, want) {
			t.Fatalf("progress lacks %q:\n%s", want, progress)
		}
	}
}

// What tools and agents read stays English in a Ukrainian interface: the
// problems of the orders check and the chronicle dump.
func TestUkrainianKeepsMachineOutput(t *testing.T) {
	t.Setenv(languageEnv, "uk")
	w := newWorld(t, "  a: {type: script, run: "+arg("exit", "0")+", on: {ok: nowhere}}\n")
	code, _, errOut := runIn(t, "check", "-f", w.orders)
	if code != exitUsage || !strings.Contains(errOut, "orders.yaml:") || !strings.Contains(errOut, "наказ відхилено: 2 проблеми") || !strings.Contains(errOut, "no such node") {
		t.Fatalf("check: %d\n%s", code, errOut)
	}
	w = newWorld(t, "  a: {type: script, run: "+arg("exit", "0")+", on: {ok: done, fail: failed}}\n")
	if code, _, errOut := runIn(t, "run", "-f", w.orders); code != exitDone {
		t.Fatalf("run: %d %s", code, errOut)
	}
	_, status, _ := runIn(t, "status", "-f", w.orders, "-events")
	if !strings.Contains(status, "chronicle:\n") || !strings.Contains(status, "step_finished a attempt=1 outcome=ok") {
		t.Fatalf("the events dump is not as tools read it:\n%s", status)
	}
}
