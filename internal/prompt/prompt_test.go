package prompt

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseAndRender(t *testing.T) {
	text := "Task: {{input.task}}\n  ünï {{ output.review.notes }} and {{rework}}{{rework}}\n" +
		"Code stays: style={{color: red}} {{ }} {{.x}} {x} {{a b}}\nend {{artifact.build.out/r.txt}}"
	tpl := Parse(text)
	want := []Placeholder{
		{"input.task", 1, 7},
		{"output.review.notes", 2, 7},
		{"rework", 2, 37},
		{"rework", 2, 47},
		{"artifact.build.out/r.txt", 4, 5},
	}
	if got := tpl.Placeholders(); !reflect.DeepEqual(got, want) {
		t.Fatalf("placeholders\n got %+v\nwant %+v", got, want)
	}
	out, err := tpl.Render(func(p Placeholder) (string, error) { return "<" + p.Name + ">", nil })
	if err != nil {
		t.Fatal(err)
	}
	wantText := "Task: <input.task>\n  ünï <output.review.notes> and <rework><rework>\n" +
		"Code stays: style={{color: red}} {{ }} {{.x}} {x} {{a b}}\nend <artifact.build.out/r.txt>"
	if out != wantText {
		t.Fatalf("rendered %q", out)
	}
	if _, err := tpl.Render(func(p Placeholder) (string, error) { return "", errors.New("no " + p.Name) }); err == nil || !strings.Contains(err.Error(), "no input.task") {
		t.Fatalf("error %v", err)
	}
	if out, _ := Parse("no placeholders").Render(nil); out != "no placeholders" {
		t.Fatalf("plain text %q", out)
	}
}
