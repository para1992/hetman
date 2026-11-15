package wizard

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"hetman/internal/i18n"
	"hetman/internal/orders"
)

const formOrders = `version: 0
start: a
input:
  ticket: {kind: text, label: {en: Ticket, uk: Квиток}, hint: {en: such as T-1, uk: наприклад T-1}, required: true}
  size: {kind: choice, options: [small, large], default: small}
  budget: {kind: number, required: true}
  urgent: {kind: yesno}
  notes: {kind: longtext}
  tags: {kind: list, default: [a, b]}
  team: {kind: text, default: core, ask: false}
nodes:
  a: {type: script, run: [x], on: {ok: done, fail: failed}}
`

func formSpec(t *testing.T) *orders.Orders {
	t.Helper()
	o, err := orders.Parse("/x/orders.yaml", []byte(formOrders))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// Every kind of field is asked in the person's language, with its default
// offered and wrong answers asked again; the result is checked input with
// the fixed value filled in.
func TestFormAsksEveryKind(t *testing.T) {
	o := formSpec(t)
	var out bytes.Buffer
	data, err := Form(i18n.For("uk"), o.Input, input(
		"",         // ticket is required
		"T-7",      // ticket
		"2",        // size: large
		"a lot",    // budget: not a number
		"12.5",     // budget
		"може",     // urgent: neither yes nor no
		"т",        // urgent
		"first",    // notes
		"second",   //
		"",         // end of notes
		"",         // tags: keep a, b
		"2",        // change a field
		"5",        // notes
		"only one", //
		"",         //
		"1",        // use it
	), &out, true)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	want := `{"ticket":"T-7","size":"large","budget":12.5,"urgent":true,"notes":"only one","tags":["a","b"],"team":"core"}`
	if string(data) != want {
		t.Fatalf("got  %s\nwant %s\n%s", data, want, out.String())
	}
	got := out.String()
	for _, w := range []string{"(наприклад T-1)", "Квиток: ", "Тут потрібна відповідь.", "size (необов'язково)", "Вибір [small]",
		"Введіть число", "Відповідайте «т» або «н».", "Зараз: a; b", "team (незмінне): core", "urgent: так"} {
		if !strings.Contains(got, w) {
			t.Errorf("transcript lacks %q", w)
		}
	}
}

func TestFormCancelled(t *testing.T) {
	o := formSpec(t)
	for _, answers := range [][]string{
		{"T-1", "", "3", "", "", "", "3"}, // cancel at the end
		{"T-1"},                           // the input ends
	} {
		var out bytes.Buffer
		if _, err := Form(nil, o.Input, input(answers...), &out, true); !errors.Is(err, ErrCancelled) {
			t.Fatalf("%v\n%s", err, out.String())
		}
	}
}
