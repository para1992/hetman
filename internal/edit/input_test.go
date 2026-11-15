package edit

import (
	"strings"
	"testing"

	"hetman/internal/i18n"
	"hetman/internal/runinput"
)

// The input operations build the description of a task list from nothing,
// change it and take it apart; every step passes the orders check.
func TestInputOperations(t *testing.T) {
	d := fixture(t)
	for _, op := range []Op{
		{Op: OpInputAdd, Name: "project", Kind: "text", Default: "demo", Ask: no()},
		{Op: OpInputAdd, Name: "id", Kind: "text", Of: "tasks", Label: "Task id", Required: yes()},
		{Op: OpInputAdd, Name: "needs", Kind: "list", Of: "tasks"},
		{Op: OpInputAdd, Name: "criteria", Kind: "list", Of: "tasks", Required: yes(), Hint: "one per line"},
		{Op: OpInputSet, Name: "tasks", Field: "label", Value: "Tasks"},
		{Op: OpInputSet, Name: "tasks", Field: "label", Value: "Завдання", Language: "uk"},
		{Op: OpInputSet, Name: "tasks", Field: "required", Value: "true"},
		{Op: OpInputAdd, Name: "size", Kind: "choice", Options: []string{"small", "large"}, Default: "small"},
		{Op: OpInputSet, Name: "size", Field: "default", Value: "large"},
		{Op: OpInputAdd, Name: "budget", Kind: "number", Label: "Presupuesto", Language: "es"},
		{Op: OpInputSet, Name: "criteria", Of: "tasks", Field: "hint", Value: "одне в рядку", Language: "uk"},
		{Op: OpInputUnset, Name: "criteria", Of: "tasks", Field: "hint", Language: "en"},
	} {
		if err := d.Apply(op); err != nil {
			t.Fatalf("%s: %v", op.Words(), err)
		}
		if _, err := d.Check(); err != nil {
			t.Fatalf("after %s: %v\n%s", op.Words(), err, d.Text())
		}
	}
	o, _ := d.Check()
	s := o.Input
	tasks := s.Field("tasks")
	if s.Field("project").Ask || s.Field("project").Default != "demo" || !tasks.Required || tasks.Kind != runinput.Objects {
		t.Fatalf("input %+v", s.Fields)
	}
	if tasks.LabelIn("uk") != "Завдання" || tasks.LabelIn("en") != "Tasks" || s.Field("size").Default != "large" || s.Field("budget").LabelIn("en") != "Presupuesto" {
		t.Fatalf("labels and defaults:\n%s", d.Text())
	}
	criteria := tasks.Fields[2]
	if criteria.Name != "criteria" || criteria.Hint.In("en") != "одне в рядку" || len(criteria.Hint.ByLang) != 1 {
		t.Fatalf("criteria %+v", criteria)
	}
	text := string(d.Text())
	for _, want := range []string{"input:\n  project: {kind: text, ask: false, default: demo}\n  tasks:\n    kind: objects\n    label: {en: Tasks, uk: Завдання}\n    required: true\n    fields:\n      id: {kind: text, label: Task id, required: true}\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q:\n%s", want, text)
		}
	}

	for _, c := range []struct {
		op   Op
		want string
	}{
		{Op{Op: OpInputAdd, Name: "tasks", Kind: "objects"}, "a list of objects is made by adding its first field"},
		{Op{Op: OpInputAdd, Name: "project", Kind: "text"}, `already has a field "project"`},
		{Op{Op: OpInputAdd, Name: "id", Kind: "text", Of: "tasks"}, `already has a field "id"`},
		{Op{Op: OpInputAdd, Name: "x", Kind: "text", Of: "project"}, `"project" is not a list of objects`},
		{Op{Op: OpInputAdd, Name: "a.b", Kind: "text"}, "use letters, digits"},
		{Op{Op: OpInputAdd, Name: "x", Kind: "date"}, "kind must be one of"},
		{Op{Op: OpInputAdd, Name: "x", Kind: "choice", Options: []string{"a"}}, "at least two options"},
		{Op{Op: OpInputAdd, Name: "x", Kind: "text", Ask: no()}, "needs a default"},
		{Op{Op: OpInputAdd, Name: "x", Kind: "number", Default: "many"}, "must be a number"},
		{Op{Op: OpInputAdd, Name: "x", Kind: "text", Language: "uk"}, "give one of them"},
		{Op{Op: OpInputRm, Name: "id", Of: "tasks"}, ""},
		{Op{Op: OpInputRm, Name: "needs", Of: "tasks"}, ""},
		{Op{Op: OpInputRm, Name: "criteria", Of: "tasks"}, "keeps at least one field"},
		{Op{Op: OpInputRm, Name: "nothing"}, `has no field "nothing" (its fields are: project, tasks, size, budget)`},
		{Op{Op: OpInputSet, Name: "tasks", Field: "label", Value: "Jobs"}, "written per language (en, uk); name the language"},
		{Op{Op: OpInputSet, Name: "tasks", Field: "default", Value: "x"}, "takes no default"},
		{Op{Op: OpInputSet, Name: "size", Field: "default", Value: "huge"}, "not one of the options small, large"},
		{Op{Op: OpInputSet, Name: "size", Field: "kind", Value: "text"}, "unknown field \"kind\""},
		{Op{Op: OpInputSet, Name: "size", Field: "required", Value: "maybe"}, "required must be true or false"},
		{Op{Op: OpInputUnset, Name: "budget", Field: "required"}, "unknown field \"required\""},
	} {
		err := d.Apply(c.op)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.op.Words(), err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.op.Words(), err, c.want)
		}
	}
	// The section goes with its last field.
	for _, name := range []string{"project", "tasks", "size", "budget"} {
		if err := d.Apply(Op{Op: OpInputRm, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if o, err := d.Check(); err != nil || o.Input != nil || strings.Contains(string(d.Text()), "input:") {
		t.Fatalf("%v\n%s", err, d.Text())
	}
}

func TestInputWords(t *testing.T) {
	op := Op{Op: OpInputAdd, Name: "id", Kind: "text", Of: "tasks", Label: "Task id", Required: yes()}
	if got := op.Words(); got != `add the field id of the run input list tasks, of kind text, labelled "Task id", required` {
		t.Fatalf("words: %q", got)
	}
	if got := op.WordsIn(i18n.For("uk")); got != `додати поле id списку вхідних даних tasks виду text, з підписом "Task id", обов'язкове` {
		t.Fatalf("uk words: %q", got)
	}
	set := Op{Op: OpInputSet, Name: "tasks", Field: "label", Value: "Завдання", Language: "uk"}
	if got := set.Words(); got != "set the label of the run input field tasks in the language uk to Завдання" {
		t.Fatalf("set words: %q", got)
	}
}
