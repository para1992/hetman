package orders

import (
	"errors"
	"strings"
	"testing"
)

func TestLanguage(t *testing.T) {
	head := "version: 0\nstart: a\n"
	tail := "nodes:\n  a: {type: script, run: [x], on: {ok: done, fail: failed}}\n"
	o, err := Parse("/x/orders.yaml", []byte(head+"language: hi\n"+tail))
	if err != nil || o.Language != "hi" {
		t.Fatalf("language %q, %v", o.Language, err)
	}
	if o, err = Parse("/x/orders.yaml", []byte(head+tail)); err != nil || o.Language != "" {
		t.Fatalf("without the key: language %q, %v", o.Language, err)
	}
	for _, c := range []struct {
		value, msg string
	}{
		{"Hindi", `unknown language code "Hindi"; did you mean hi (Hindi)?`},
		{"HI", `did you mean hi (Hindi)?`},
		{"xx", `unknown language code "xx" (known: ar, en, es, hi, uk, zh)`},
		{"[de]", `language must be a single value`},
	} {
		_, err := Parse("/x/orders.yaml", []byte(head+"language: "+c.value+"\n"+tail))
		var ps Problems
		if !errors.As(err, &ps) || len(ps) != 1 || ps[0].Line != 3 || ps[0].Col != 11 || !strings.Contains(ps[0].Msg, c.msg) {
			t.Errorf("language %s: %v", c.value, err)
		}
	}
}

func TestLanguagePlaceholder(t *testing.T) {
	o := &Orders{Nodes: map[string]*Node{}}
	for name, want := range map[string]Ref{
		"language":        {Kind: RefLanguage},
		"language.native": {Kind: RefLanguage, Path: []string{LanguageNative}},
		"language.code":   {Kind: RefLanguage, Path: []string{LanguageCode}},
	} {
		r, err := o.Ref(name)
		if err != nil || r.Kind != want.Kind || strings.Join(r.Path, ".") != strings.Join(want.Path, ".") {
			t.Errorf("%s: %+v %v", name, r, err)
		}
	}
	for _, name := range []string{"language.english", "language.code.x", "language."} {
		if _, err := o.Ref(name); err == nil || !strings.Contains(err.Error(), "{{language.native}}") {
			t.Errorf("%s: %v", name, err)
		}
	}
	path := agentFiles(t, agentOrders, map[string]string{"prompts/write.md": "Write {{input.task}} for a reader of {{language}}.\n  {{language.nativ}}\n"})
	_, err := Load(path)
	var ps Problems
	if !errors.As(err, &ps) || len(ps) != 1 || ps[0].Line != 2 || ps[0].Col != 3 || !strings.Contains(ps[0].Msg, "{{language.nativ}}") {
		t.Fatalf("%v", err)
	}
}
