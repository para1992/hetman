// Package prompt reads prompt templates: plain text with named placeholders.
//
// A placeholder is "{{", a name, and "}}", with optional spaces inside the
// braces. A name is made of letters, digits and the characters "_", "-",
// ".", and "/". There are no expressions, conditions or loops. Any "{{"
// that is not followed by such a name and "}}" is ordinary text, so code in
// a prompt that uses double braces stays as it is.
package prompt

import (
	"regexp"
	"strings"
)

// Placeholder is one placeholder of a template and where it is written.
type Placeholder struct {
	Name      string
	Line, Col int // from 1; the column counts characters
}

type part struct {
	text        string
	placeholder int // index into placeholders, or -1 for literal text
}

// Template is a parsed prompt template.
type Template struct {
	parts        []part
	placeholders []Placeholder
}

var placeholderPattern = regexp.MustCompile(`\{\{ *([A-Za-z0-9_][A-Za-z0-9_./-]*) *\}\}`)

// Parse reads a template. It never fails: whether each placeholder name
// means something is decided by the caller.
func Parse(text string) *Template {
	t := &Template{}
	last := 0
	for _, m := range placeholderPattern.FindAllStringSubmatchIndex(text, -1) {
		if m[0] > last {
			t.parts = append(t.parts, part{text: text[last:m[0]], placeholder: -1})
		}
		line := strings.Count(text[:m[0]], "\n") + 1
		col := len([]rune(text[strings.LastIndex(text[:m[0]], "\n")+1:m[0]])) + 1
		t.parts = append(t.parts, part{placeholder: len(t.placeholders)})
		t.placeholders = append(t.placeholders, Placeholder{Name: text[m[2]:m[3]], Line: line, Col: col})
		last = m[1]
	}
	if last < len(text) {
		t.parts = append(t.parts, part{text: text[last:], placeholder: -1})
	}
	return t
}

// Placeholders lists the placeholders in the order they are written.
func (t *Template) Placeholders() []Placeholder { return t.placeholders }

// Render replaces every placeholder with what value returns for it. The
// first error stops rendering.
func (t *Template) Render(value func(Placeholder) (string, error)) (string, error) {
	var b strings.Builder
	for _, p := range t.parts {
		if p.placeholder < 0 {
			b.WriteString(p.text)
			continue
		}
		s, err := value(t.placeholders[p.placeholder])
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	return b.String(), nil
}
