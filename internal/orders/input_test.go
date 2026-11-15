package orders

import (
	"encoding/json"
	"strings"
	"testing"

	"hetman/internal/runinput"
)

const inputHead = "version: 0\nstart: a\nnodes:\n  a: {type: script, run: [x], on: {ok: done, fail: failed}}\n"

// The description of a real task list parses with every property.
func TestInputSection(t *testing.T) {
	o, err := Parse("/x/orders.yaml", []byte(inputHead+`input:
  project: {kind: text, default: shop, ask: false}
  tasks:
    kind: objects
    label: {en: Tasks, uk: Завдання}
    hint: one per piece of work
    required: true
    fields:
      id: {kind: text, label: Task id, required: true}
      needs: {kind: list, default: []}
      title: {kind: text, required: true}
      description: {kind: longtext}
      criteria: {kind: list, required: true}
  size: {kind: choice, options: [small, large], default: large}
  budget: {kind: number, default: 2.5}
  urgent: {kind: yesno, default: false}
`))
	if err != nil {
		t.Fatal(err)
	}
	s := o.Input
	if s == nil || len(s.Fields) != 5 {
		t.Fatalf("input %+v", s)
	}
	project, tasks := s.Field("project"), s.Field("tasks")
	if project.Ask || project.Default != "shop" || tasks.Kind != runinput.Objects || !tasks.Required || len(tasks.Fields) != 5 {
		t.Fatalf("project %+v tasks %+v", project, tasks)
	}
	if tasks.LabelIn("uk") != "Завдання" || tasks.LabelIn("hi") != "Tasks" || tasks.Hint.In("uk") != "one per piece of work" {
		t.Fatalf("labels %+v", tasks.Label)
	}
	if tasks.Fields[0].LabelIn("en") != "Task id" || tasks.Fields[2].LabelIn("en") != "title" {
		t.Fatal("field labels")
	}
	data, _ := json.Marshal([]any{s.Field("budget").Default, s.Field("urgent").Default, s.Field("size").Default, tasks.Fields[1].Default})
	if string(data) != `[2.5,false,"large",[]]` {
		t.Fatalf("defaults %s", data)
	}
	if o, err := Parse("/x/orders.yaml", []byte(inputHead)); err != nil || o.Input != nil {
		t.Fatalf("no section: %v %+v", err, o.Input)
	}
}

func TestInputSectionRejected(t *testing.T) {
	for _, c := range []struct {
		name, section string
		line          int
		msg           string
	}{
		{"not a mapping", "input: [a, b]", 5, "input must be a mapping"},
		{"empty", "input: {}", 5, "input must declare at least one field"},
		{"no kind", "input:\n  a: {label: A}", 6, `field "a": missing key "kind" (text, longtext, number, yesno, choice, list, objects)`},
		{"unknown kind", "input:\n  a: {kind: date}", 6, `unknown kind "date"`},
		{"unknown key", "input:\n  a: {kind: text, question: A}", 6, `unknown key "question"`},
		{"dotted name", "input:\n  a.b: {kind: text}", 6, `field "a.b": use letters, digits`},
		{"reserved name", "input:\n  hetman_task: {kind: text}", 6, "names starting with hetman_ are kept"},
		{"duplicate", "input:\n  a: {kind: text}\n  a: {kind: number}", 7, `duplicate key "a"`},
		{"choice without options", "input:\n  a: {kind: choice}", 6, "a choice needs options"},
		{"options on text", "input:\n  a: {kind: text, options: [x, y]}", 6, "options are allowed only on a field of kind choice"},
		{"one option", "input:\n  a: {kind: choice, options: [x]}", 6, "at least two options"},
		{"objects without fields", "input:\n  a: {kind: objects}", 6, "a list of objects needs fields"},
		{"fields on text", "input:\n  a: {kind: text, fields: {b: {kind: text}}}", 6, "fields are allowed only on a field of kind objects"},
		{"nested objects", "input:\n  a:\n    kind: objects\n    fields:\n      b: {kind: objects, fields: {c: {kind: text}}}", 9, "cannot hold another list of objects (one level of nesting is supported)"},
		{"not asked without default", "input:\n  a: {kind: text, required: true, ask: false}", 6, "a field with ask: false is never asked, so it needs a default"},
		{"required not a bool", "input:\n  a: {kind: text, required: yes please}", 6, "required must be true or false"},
		{"default of objects", "input:\n  a: {kind: objects, default: x, fields: {b: {kind: text}}}", 6, "a list of objects takes no default"},
		{"number default", "input:\n  a: {kind: number, default: many}", 6, "default of a number must be a number"},
		{"yesno default", "input:\n  a: {kind: yesno, default: maybe}", 6, "default of a yes/no field must be true or false"},
		{"choice default", "input:\n  a: {kind: choice, options: [x, y], default: z}", 6, `the default "z" is not one of the options x, y`},
		{"list default", "input:\n  a: {kind: list, default: x}", 6, "default of a list must be a list of texts"},
		{"label language", "input:\n  a: {kind: text, label: {en: A, xx: B}}", 6, `label: unknown language code "xx"`},
		{"empty label", "input:\n  a: {kind: text, label: ''}", 6, "label must not be empty"},
		{"label list", "input:\n  a: {kind: text, label: [A]}", 6, "label must be a text, or a text per language"},
	} {
		_, err := Parse("/x/orders.yaml", []byte(inputHead+c.section+"\n"))
		ps, ok := err.(Problems)
		if !ok {
			t.Errorf("%s: accepted (%v)", c.name, err)
			continue
		}
		found := false
		for _, p := range ps {
			if strings.Contains(p.Msg, c.msg) {
				found = true
				if p.Line != c.line {
					t.Errorf("%s: line %d, want %d (%s)", c.name, p.Line, c.line, p.Msg)
				}
			}
		}
		if !found {
			t.Errorf("%s: %v, want %q", c.name, err, c.msg)
		}
	}
}

// The name of the work a person starts is one text, or one per language,
// short and on one line.
func TestStartLabel(t *testing.T) {
	o, err := Parse("/x/orders.yaml", []byte(inputHead+"start_label: {en: New task, uk: Нове завдання}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if o.StartLabel.In("uk") != "Нове завдання" || o.StartLabel.In("de") != "New task" {
		t.Fatalf("label %+v", o.StartLabel)
	}
	if o, err := Parse("/x/orders.yaml", []byte(inputHead+"start_label: New task\n")); err != nil || o.StartLabel.In("uk") != "New task" {
		t.Fatalf("plain: %v %+v", err, o)
	}
	if o, err := Parse("/x/orders.yaml", []byte(inputHead)); err != nil || !o.StartLabel.Empty() {
		t.Fatalf("none: %v", err)
	}
	for _, c := range []struct{ text, want string }{
		{`start_label: ""`, "must not be empty"},
		{`start_label: {xx: New}`, "xx"},
		{"start_label: \"one\\ntwo\"", "one line"},
		{"start_label: " + strings.Repeat("x", 61), "must be short"},
		{"start_label: [a]", "must be a text"},
	} {
		_, err := Parse("/x/orders.yaml", []byte(inputHead+c.text+"\n"))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.text, err)
		}
	}
}
