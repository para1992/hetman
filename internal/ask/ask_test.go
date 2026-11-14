package ask

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"hetman/internal/schema"
)

var qs = []Question{
	{ID: "ok", Question: "Fine?", Kind: YesNo},
	{ID: "pick", Question: "Which?", Kind: Choice, Options: []string{"a", "b"}},
	{ID: "say", Question: "Anything?", Kind: Text},
}

func TestCheck(t *testing.T) {
	as, outcome, err := Check(qs, []string{"ship", "stop"}, Given{
		Values:   map[string]string{"ok": " Y ", "pick": "b", "say": ""},
		Comments: map[string]string{"ok": " soon "}, Outcome: "ship"})
	if err != nil || outcome != "ship" || as[0].Value != Yes || as[0].Comment != "soon" || as[1].Value != "b" || as[2].Value != "" {
		t.Fatalf("%+v %q %v", as, outcome, err)
	}
	if _, outcome, err := Check(nil, []string{"only"}, Given{}); err != nil || outcome != "only" {
		t.Fatalf("a single outcome is taken without asking: %q %v", outcome, err)
	}
	_, _, err = Check(qs, nil, Given{Values: map[string]string{"ok": "maybe", "pick": "c", "x": "1"}, Comments: map[string]string{"y": "z"}, Outcome: "go"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	for _, w := range []string{"answer yes or no", "choose one of a, b", "question say has no answer", "there is no question x", "comment for y", "takes no outcome"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("%q lacks %q", err, w)
		}
	}
	if !AllYes(qs, []Answer{{ID: "ok", Value: Yes}, {ID: "pick", Value: "a"}}) || AllYes(qs, []Answer{{ID: "ok", Value: No}}) {
		t.Fatal("AllYes")
	}
}

func TestParse(t *testing.T) {
	if q, err := Parse(json.RawMessage(`[{"id": "a", "question": "Q?", "kind": "choice", "options": ["x", "y"]}]`)); err != nil || q[0].Options[1] != "y" {
		t.Fatalf("%+v %v", q, err)
	}
	for raw, want := range map[string]string{
		`[]`: "ask from 1 to 20 questions",
		`[{"id": "a b", "question": "Q", "kind": "text"}]`:                                             "must use letters",
		`[{"id": "a", "question": "Q", "kind": "yesno", "options": ["x"]}]`:                            "options are allowed only",
		`[{"id": "a", "question": "", "kind": "maybe"}]`:                                               "kind must be one of",
		`[{"id": "a", "question": "Q", "kind": "text", "extra": 1}]`:                                   "unknown field",
		`[{"id": "a", "question": "Q", "kind": "text"}, {"id": "a", "question": "R", "kind": "text"}]`: "used twice",
	} {
		if _, err := Parse(json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
	}
}

func TestDialogueSchema(t *testing.T) {
	node := `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}, "text": {"type": "string"}}, "required": ["outcome", "text"], "additionalProperties": false}`
	out, err := DialogueSchema([]byte(node))
	if err != nil {
		t.Fatal(err)
	}
	s, err := schema.Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, v := range []string{`{"outcome": "questions", "questions": [{"id": "a", "question": "Q", "kind": "yesno"}]}`, `{"outcome": "ok", "text": "t"}`} {
		var x any
		json.Unmarshal([]byte(v), &x)
		if p := s.Validate(x); len(p) > 0 {
			t.Errorf("%s: %v", v, p)
		}
	}
}
