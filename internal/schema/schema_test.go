package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

const review = `{
	"$schema": "https://json-schema.org/draft/2020-12/schema",
	"title": "Review",
	"type": "object",
	"properties": {
		"outcome": {"type": "string", "enum": ["ok", "needs_rework"], "description": "verdict"},
		"score": {"type": "integer"},
		"ratio": {"type": "number"},
		"files": {"type": "array", "items": {"type": "string"}},
		"meta": {"type": "object", "additionalProperties": {"type": "boolean"}},
		"level": {"enum": [1, 2.5, "high", null, true]}
	},
	"required": ["outcome", "files"],
	"additionalProperties": false
}`

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestValidate(t *testing.T) {
	s, err := Parse([]byte(review))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := s.Properties["outcome"].Strings(); strings.Join(out, ",") != "ok,needs_rework" || strings.Join(s.Order, ",") != "outcome,score,ratio,files,meta,level" {
		t.Fatalf("parsed %+v", s)
	}
	cases := map[string]string{
		`{"outcome": "ok", "files": []}`: "",
		`{"outcome": "ok", "files": ["a"], "score": 3, "ratio": 3, "meta": {"x": true}, "level": 2.5}`: "",
		`{"outcome": "ok", "files": [], "level": null}`:                                                "",
		`{"outcome": "maybe", "files": []}`:                                                            `$.outcome: must be one of "ok", "needs_rework", got "maybe"`,
		`{"files": []}`:                                                                                `$: missing required property "outcome"`,
		`{"outcome": "ok", "files": [], "extra": 1}`:                                                   `$: property "extra" is not allowed`,
		`{"outcome": "ok", "files": [1]}`:                                                              `$.files[0]: must be of type string, got integer`,
		`{"outcome": "ok", "files": [], "score": 1.5}`:                                                 `$.score: must be of type integer, got number`,
		`{"outcome": "ok", "files": [], "meta": {"x": "yes"}}`:                                         `$.meta.x: must be of type boolean, got string`,
		`{"outcome": "ok", "files": [], "level": "low"}`:                                               `$.level: must be one of 1, 2.5, "high", null, true, got "low"`,
		`["outcome"]`: `$: must be of type object, got array`,
	}
	for in, want := range cases {
		got := strings.Join(s.Validate(decode(t, in)), "; ")
		if got != want {
			t.Errorf("%s:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestValidateLimitsProblems(t *testing.T) {
	s, _ := Parse([]byte(`{"type": "array", "items": {"type": "string"}}`))
	got := s.Validate(decode(t, `[1,2,3,4,5,6,7,8,9,10,11,12]`))
	if len(got) != maxProblems+1 || got[maxProblems] != "and 2 more problems" {
		t.Fatalf("%v", got)
	}
}

func TestParseProblems(t *testing.T) {
	cases := []struct {
		in        string
		line, col int
		msg       string
	}{
		{"{\n  \"type\": \"object\",\n  \"type\" \"x\"\n}", 3, 10, "not valid JSON"},
		{"{\n  \"type\": \"object\",\n  \"oneOf\": []\n}", 3, 3, `keyword "oneOf" is not supported`},
		{"{\n  \"type\": \"text\"\n}", 2, 11, "type must be one of"},
		{"{\"type\": \"object\", \"properties\": {\"a\": {\"minLength\": 1}}}", 1, 41, `property "a": keyword "minLength" is not supported`},
		{"{\"type\": \"string\", \"properties\": {}}", 1, 1, "properties applies only to type object"},
		{"{\"type\": \"string\", \"items\": {}}", 1, 1, "items applies only to type array"},
		{"{\"enum\": []}", 1, 10, "enum must be a non-empty list"},
		{"{\"type\": \"integer\", \"enum\": [1, 1.5]}", 1, 1, "enum value 1.5 is not of type integer"},
		{"{\"type\": \"object\", \"required\": [\"a\"], \"properties\": {}, \"additionalProperties\": false}", 1, 32, `required property "a" is not listed`},
		{"{\"additionalProperties\": 1}", 1, 26, "additionalProperties must be true, false or a schema"},
		{"{\"type\": \"object\", \"type\": \"array\"}", 1, 20, `duplicate key "type"`},
		{"[]", 1, 1, "schema must be an object"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.in))
		ps, ok := err.(Problems)
		if !ok || len(ps) == 0 {
			t.Errorf("%s: want problems, got %v", c.in, err)
			continue
		}
		if ps[0].Line != c.line || ps[0].Col != c.col || !strings.Contains(ps[0].Msg, c.msg) {
			t.Errorf("%s:\n got %d:%d %s\nwant %d:%d %s", c.in, ps[0].Line, ps[0].Col, ps[0].Msg, c.line, c.col, c.msg)
		}
	}
}
