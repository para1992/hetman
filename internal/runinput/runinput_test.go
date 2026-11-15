package runinput

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"hetman/internal/i18n"
)

// spec is the description of a list of tasks: a fixed project and tasks
// with their own fields.
func spec() *Spec {
	return &Spec{Fields: []*Field{
		{Name: "project", Kind: Text, Default: "demo", Ask: false},
		{Name: "tasks", Kind: Objects, Required: true, Ask: true,
			Label: Words{ByLang: map[string]string{"en": "Tasks", "uk": "Завдання"}, Order: []string{"en", "uk"}},
			Fields: []*Field{
				{Name: "id", Kind: Text, Required: true, Ask: true},
				{Name: "needs", Kind: List, Ask: true},
				{Name: "title", Kind: Text, Required: true, Ask: true},
				{Name: "description", Kind: LongText, Ask: true},
				{Name: "criteria", Kind: List, Required: true, Ask: true},
			}},
		{Name: "size", Kind: Choice, Options: []string{"small", "large"}, Default: "small", Ask: true},
		{Name: "budget", Kind: Number, Ask: true},
		{Name: "urgent", Kind: YesNo, Ask: true},
	}}
}

func TestCheckFillsDefaultsAndKeepsFieldOrder(t *testing.T) {
	in := `{"urgent": false, "tasks": [{"criteria": ["a", "b"], "title": "Shelf", "id": "TASK-6", "needs": ["TASK-5"],
		"description": "line one\nline two"}], "budget": 2.5}`
	out, err := Check(spec(), json.RawMessage(in))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"project":"demo","tasks":[{"id":"TASK-6","needs":["TASK-5"],"title":"Shelf","description":"line one\nline two","criteria":["a","b"]}],"size":"small","budget":2.5,"urgent":false}`
	if string(out) != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
	// A given value wins over the default, also for a field that is not
	// asked.
	out, err = Check(spec(), json.RawMessage(`{"project": "other", "tasks": [{"id": "x", "title": "t", "criteria": ["c"]}]}`))
	if err != nil || !strings.HasPrefix(string(out), `{"project":"other",`) {
		t.Fatalf("%s %v", out, err)
	}
}

func TestCheckNamesTheField(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{``, "tasks is required"},
		{`[]`, "must be one JSON object"},
		{`{"tasks": []}`, "tasks needs at least one item"},
		{`{"tasks": [{"id": "x", "title": " ", "criteria": ["a"]}]}`, "tasks[1].title must not be empty"},
		{`{"tasks": [{"id": "x", "title": "t", "criteria": []}]}`, "tasks[1].criteria needs at least one item"},
		{`{"tasks": [{"id": "x", "title": "t", "criteria": ["a"]}, {"id": "y", "title": "u"}]}`, "tasks[2].criteria is required"},
		{`{"tasks": [{"id": "x", "title": "t", "criteria": ["a", 3]}]}`, "tasks[1].criteria[2] must be a text"},
		{`{"tasks": [{"id": "x", "title": "t", "criteria": ["a"], "owner": "me"}]}`, "tasks[1].owner is not a field"},
		{`{"tasks": [5]}`, "tasks[1] must be an object"},
		{`{"tasks": [{"id": "x\ny", "title": "t", "criteria": ["a"]}]}`, "tasks[1].id must be one line"},
		{`{"tasks": [{"id": "x", "title": "t", "criteria": ["a"]}], "size": "huge"}`, "size must be one of small, large"},
		{`{"tasks": [{"id": "x", "title": "t", "criteria": ["a"]}], "budget": "a lot"}`, "budget must be a number"},
		{`{"tasks": [{"id": "x", "title": "t", "criteria": ["a"]}], "urgent": "yes"}`, "urgent must be true or false"},
		{`{"tasks": [{"id": "x", "title": "t", "criteria": ["a"]}], "ticket": "T-1"}`, "ticket is not a field of the run input (its fields: project, tasks, size, budget, urgent)"},
	} {
		_, err := Check(spec(), json.RawMessage(c.in))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.in, err, c.want)
		}
	}
	_, err := Check(spec(), json.RawMessage(`{}`))
	var inv *Invalid
	if !errors.As(err, &inv) || !strings.Contains(inv.Localize(i18n.For("uk")), "tasks — обов'язкове поле") {
		t.Fatalf("in Ukrainian: %v", err)
	}
}

func TestWordsPickTheLanguage(t *testing.T) {
	w := Words{ByLang: map[string]string{"uk": "Завдання", "es": "Tareas"}, Order: []string{"uk", "es"}}
	if w.In("es") != "Tareas" || w.In("hi") != "Завдання" {
		t.Fatalf("%q %q", w.In("es"), w.In("hi"))
	}
	w.ByLang["en"] = "Tasks"
	if w.In("hi") != "Tasks" || (Words{Plain: "Tasks"}).In("uk") != "Tasks" {
		t.Fatal("English is the fallback; a plain text serves every language")
	}
	f := &Field{Name: "tasks"}
	if f.LabelIn("en") != "tasks" {
		t.Fatal("no label: the name")
	}
}

func TestWriteIsReadable(t *testing.T) {
	out, _ := Check(spec(), json.RawMessage(`{"tasks": [{"id": "TASK-6", "title": "Shelf", "criteria": ["a", "b"], "description": "one\ntwo"}], "urgent": true}`))
	got := Write(i18n.For("uk"), spec(), out)
	for _, want := range []string{"project (незмінне): demo", "Завдання:\n  Завдання 1\n    id: TASK-6", "    description:\n        one\n        two", "      - b", "urgent: так"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
}

func TestParseDefault(t *testing.T) {
	for _, c := range []struct {
		kind, in string
		want     string
	}{
		{Number, "3", "3"}, {YesNo, "yes", "true"}, {List, "a, b,,c", `["a","b","c"]`}, {Choice, "small", `"small"`}, {Text, "x", `"x"`},
	} {
		v, err := ParseDefault(&Field{Kind: c.kind, Options: []string{"small", "large"}}, c.in)
		data, _ := json.Marshal(v)
		if err != nil || string(data) != c.want {
			t.Errorf("%s %q: %s %v", c.kind, c.in, data, err)
		}
	}
	for _, c := range []struct{ kind, in string }{{Number, "many"}, {YesNo, "maybe"}, {Choice, "huge"}, {Objects, "x"}} {
		if _, err := ParseDefault(&Field{Kind: c.kind, Options: []string{"small", "large"}}, c.in); err == nil {
			t.Errorf("%s %q accepted", c.kind, c.in)
		}
	}
}
