package explain

import (
	"encoding/json"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/i18n"
)

// The campaign of TestReworkDone, sent back for rework twice, told in
// Ukrainian; a second one waits for a person after a gate went back.
func TestUkrainianRework(t *testing.T) {
	var j journal
	j.start("build")
	j.step("build", 1, "ok", "ready", 0)
	j.gate("ready", 1, "blocked", "build", "lint?=fail", "tests=fail")
	j.step("build", 2, "ok", "ready", 0)
	j.gate("ready", 2, "pass", "review", "lint?=ok", "tests=ok")
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "review", Attempt: 1, Details: chronicle.Details{Executor: "stub", Model: "m"}})
	j.add(chronicle.Event{Kind: chronicle.StepFinished, Node: "review", Attempt: 1, Outcome: "needs_rework",
		Details: chronicle.Details{Millis: 2000, Output: json.RawMessage(`{"outcome":"needs_rework","notes":["name the log files"]}`)}})
	j.add(chronicle.Event{Kind: chronicle.Transition, Node: "review", Outcome: "needs_rework", Details: chronicle.Details{To: "build"}})
	j.step("build", 3, "ok", "ready", 0)
	j.gate("ready", 3, "pass", "review", "lint?=ok", "tests=ok")
	j.add(chronicle.Event{Kind: chronicle.StepStarted, Node: "review", Attempt: 2})
	j.add(chronicle.Event{Kind: chronicle.StepFinished, Node: "review", Attempt: 2, Outcome: "approved", Details: chronicle.Details{Millis: 1500}})
	j.add(chronicle.Event{Kind: chronicle.Transition, Node: "review", Outcome: "approved", Details: chronicle.Details{To: "done"}})

	uk := i18n.For("uk")
	got := Text(In(uk, chronicle.Campaign{ID: 3, Flow: "release", Started: t0}, j, Summary, "", time.UTC))
	same(t, got, `Де похід зараз:
  Похід 3 потоку release розпочато 2026-03-02 09:00:00 з кроку build.
  Похід завершено: 2026-03-02 09:00:30 крок review дав підсумок approved, а він веде до done.

Що виконано:
  2026-03-02 09:00:02  Крок build, спроба 1, завершився з підсумком ok за 1.2s. Потік перейшов до ready.
  2026-03-02 09:00:07  Крок ready, спроба 1, завершився з підсумком blocked за 600ms; перевірки: lint fail (м'яка, лише попередження), tests fail. Потік перейшов до build.
  2026-03-02 09:00:10  Крок build, спроба 2, завершився з підсумком ok за 1.2s. Потік перейшов до ready.
  2026-03-02 09:00:15  Крок ready, спроба 2, завершився з підсумком pass за 600ms; перевірки: lint ok, tests ok. Потік перейшов до review.
  2026-03-02 09:00:18  Крок review, спроба 1, завершився з підсумком needs_rework за 2s. Потік перейшов до build.
  2026-03-02 09:00:21  Крок build, спроба 3, завершився з підсумком ok за 1.2s. Потік перейшов до ready.
  2026-03-02 09:00:26  Крок ready, спроба 3, завершився з підсумком pass за 600ms; перевірки: lint ok, tests ok. Потік перейшов до review.
  2026-03-02 09:00:29  Крок review, спроба 2, завершився з підсумком approved за 1.5s. Потік перейшов до done.

Повернення на доопрацювання:
  Потік повертався до build двічі.
  2026-03-02 09:00:08  Крок ready дав підсумок blocked і повернув потік до build (обов'язкова перевірка tests повідомила fail).
  2026-03-02 09:00:19  Крок review дав підсумок needs_rework і повернув потік до build (його вихід: {"notes":["name the log files"]}).

Що не вдалося:
  2026-03-02 09:00:07  Крок ready, спроба 1, завершився з підсумком blocked: обов'язкова перевірка tests повідомила fail.

Попередження:
  2026-03-02 09:00:05  М'яка перевірка lint кроку ready повідомила fail у спробі 1; це лише попередження.
`)
	for n, want := range map[int]string{1: "один раз", 3: "3 рази", 5: "5 разів", 21: "21 раз"} {
		if got := (&explainer{p: uk}).times(n); got != want {
			t.Errorf("times %d: %q, want %q", n, got, want)
		}
	}
}
