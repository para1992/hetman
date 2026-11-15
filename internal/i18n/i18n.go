// Package i18n holds the texts of Hetman's own interface in a catalogue per
// language and picks them for the person reading.
//
// A catalogue is a YAML file named after the language code, embedded in
// the binary. Each entry maps a key to a message, or, when the message
// depends on a number, to its plural forms by CLDR category (zero, one,
// two, few, many, other). A message names its parameters in braces, as in
// "campaign {id} started". English is the source: it has every key, and
// every key or language without a translation falls back to it.
package i18n

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"hetman/internal/language"
)

// Source is the language every catalogue is translated from.
const Source = "en"

//go:embed catalogue/*.yaml
var files embed.FS

// Msg is one message: a text, or plural forms by category.
type Msg struct {
	Text   string
	Plural map[string]string
}

// Catalogue is the messages of one language by key.
type Catalogue map[string]Msg

var (
	loadOnce   sync.Once
	catalogues map[string]Catalogue
	loadErr    error
)

func load() {
	catalogues = map[string]Catalogue{}
	entries, err := fs.ReadDir(files, "catalogue")
	if err != nil {
		loadErr = err
		return
	}
	for _, e := range entries {
		code, ok := strings.CutSuffix(e.Name(), ".yaml")
		if !ok {
			continue
		}
		data, err := files.ReadFile("catalogue/" + e.Name())
		if err != nil {
			loadErr = err
			return
		}
		c, err := Parse(data)
		if err != nil {
			loadErr = fmt.Errorf("catalogue %s: %v", e.Name(), err)
			return
		}
		catalogues[code] = c
	}
	if catalogues[Source] == nil {
		loadErr = errors.New("there is no English catalogue")
	}
}

// Catalogues returns every embedded catalogue by language code. A broken
// catalogue is a build mistake caught by the tests, so it panics.
func Catalogues() map[string]Catalogue {
	loadOnce.Do(load)
	if loadErr != nil {
		panic("i18n: " + loadErr.Error())
	}
	return catalogues
}

// Parse reads a catalogue file.
func Parse(data []byte) (Catalogue, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	c := Catalogue{}
	if len(doc.Content) == 0 {
		return c, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("the file must be a mapping of keys to messages")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if _, dup := c[k.Value]; dup {
			return nil, fmt.Errorf("line %d: key %s is given twice", k.Line, k.Value)
		}
		switch v.Kind {
		case yaml.ScalarNode:
			c[k.Value] = Msg{Text: v.Value}
		case yaml.MappingNode:
			m := Msg{Plural: map[string]string{}}
			for j := 0; j+1 < len(v.Content); j += 2 {
				cat, form := v.Content[j], v.Content[j+1]
				if form.Kind != yaml.ScalarNode {
					return nil, fmt.Errorf("line %d: plural form %s of %s must be text", form.Line, cat.Value, k.Value)
				}
				switch cat.Value {
				case Zero, One, Two, Few, Many, Other:
				default:
					return nil, fmt.Errorf("line %d: %s is not a plural category (zero, one, two, few, many, other)", cat.Line, cat.Value)
				}
				m.Plural[cat.Value] = form.Value
			}
			c[k.Value] = m
		default:
			return nil, fmt.Errorf("line %d: %s must be a text or plural forms", v.Line, k.Value)
		}
	}
	return c, nil
}

var paramRe = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

// Params lists the parameter names a message uses, in every form.
func (m Msg) Params() []string {
	seen := map[string]bool{}
	add := func(s string) {
		for _, p := range paramRe.FindAllStringSubmatch(s, -1) {
			seen[p[1]] = true
		}
	}
	add(m.Text)
	for _, f := range m.Plural {
		add(f)
	}
	var out []string
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a language has a catalogue of its own.
func Has(code string) bool {
	_, ok := Catalogues()[code]
	return ok
}

// Resolve picks the interface language: the first of the given codes that
// is in the built-in language list, else English. Callers pass the
// person's override before the orders' language.
func Resolve(codes ...string) string {
	for _, c := range codes {
		if _, ok := language.Lookup(c); ok {
			return c
		}
	}
	return Source
}

// Printer renders messages in one language. A nil Printer renders English.
type Printer struct {
	lang string
}

var printers sync.Map

// For returns the printer of a language; an unknown code gives English.
func For(code string) *Printer {
	if _, ok := language.Lookup(code); !ok {
		code = Source
	}
	if p, ok := printers.Load(code); ok {
		return p.(*Printer)
	}
	p, _ := printers.LoadOrStore(code, &Printer{lang: code})
	return p.(*Printer)
}

// English is the printer of the source language.
func English() *Printer { return For(Source) }

// Lang is the language code of the printer.
func (p *Printer) Lang() string {
	if p == nil {
		return Source
	}
	return p.lang
}

// lookup finds a message in the printer's language, else in English.
func (p *Printer) lookup(key string) (Msg, string, bool) {
	all := Catalogues()
	lang := p.Lang()
	if c := all[lang]; c != nil {
		if m, ok := c[key]; ok {
			return m, lang, true
		}
	}
	m, ok := all[Source][key]
	return m, Source, ok
}

// T renders the message of key with its parameters, given as name and
// value pairs. The value of a parameter named n chooses the plural form. A
// key missing even in English renders as the key itself.
func (p *Printer) T(key string, args ...any) string {
	m, lang, ok := p.lookup(key)
	if !ok {
		return key
	}
	values := map[string]string{}
	var n int64
	for i := 0; i+1 < len(args); i += 2 {
		name, _ := args[i].(string)
		values[name] = fmt.Sprint(args[i+1])
		if name == "n" {
			n = toInt(args[i+1])
		}
	}
	text := m.Text
	if m.Plural != nil {
		text = m.form(lang, n)
	}
	return paramRe.ReplaceAllStringFunc(text, func(s string) string {
		if v, ok := values[s[1:len(s)-1]]; ok {
			return v
		}
		return s
	})
}

// form picks a plural form, falling back to other and then to any form.
func (m Msg) form(lang string, n int64) string {
	if f, ok := m.Plural[PluralCategory(lang, n)]; ok {
		return f
	}
	if f, ok := m.Plural[Other]; ok {
		return f
	}
	for _, c := range []string{Many, Few, One, Two, Zero} {
		if f, ok := m.Plural[c]; ok {
			return f
		}
	}
	return ""
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case int32:
		return int64(x)
	case uint:
		return int64(x)
	case uint64:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

// Message is a message with its parameters, kept so that it can be
// rendered later in the language of whoever reads it.
type Message struct {
	Key  string
	Args []any
}

// M makes a Message.
func M(key string, args ...any) Message { return Message{Key: key, Args: args} }

// In renders the message.
func (m Message) In(p *Printer) string { return p.T(m.Key, m.Args...) }

// String renders the message in English.
func (m Message) String() string { return m.In(nil) }

// Localized is an error that can say itself in another language; its
// Error method says it in English.
type Localized interface {
	error
	Localize(p *Printer) string
}

// Error renders an error for a person: in the printer's language when the
// error, or one it wraps, can say itself in it, else as it is.
func (p *Printer) Error(err error) string {
	if err == nil {
		return ""
	}
	var l Localized
	if errors.As(err, &l) {
		return l.Localize(p)
	}
	return err.Error()
}

// Forms are the plural forms of a message as sent to the web page, with
// the language whose plural rule picks among them.
type Forms struct {
	Lang  string            `json:"lang"`
	Forms map[string]string `json:"forms"`
}

// Texts are the messages under a key prefix in a language, each falling
// back to English, as sent to the web page: a message is a text or Forms.
// Keys keep the prefix.
func Texts(code, prefix string) map[string]any {
	out := map[string]any{}
	all := Catalogues()
	for _, lang := range []string{Source, code} {
		for k, m := range all[lang] {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			if m.Plural != nil {
				out[k] = Forms{Lang: lang, Forms: m.Plural}
			} else {
				out[k] = m.Text
			}
		}
	}
	return out
}

// Upper makes the first letter of s a capital, rune-aware.
func Upper(s string) string {
	for i, r := range s {
		return strings.ToUpper(string(r)) + s[i+len(string(r)):]
	}
	return s
}

// IsYes reports whether s is a yes: a word of the yes.words message in the
// printer's language or in English, ignoring case.
func IsYes(p *Printer, s string) bool { return isWord(p, "yes.words", s) }

// IsNo reports whether s is a no, by the no.words message.
func IsNo(p *Printer, s string) bool { return isWord(p, "no.words", s) }

func isWord(p *Printer, key, s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return false
	}
	for _, list := range []string{p.T(key), English().T(key)} {
		for _, w := range strings.Split(list, ",") {
			if strings.ToLower(strings.TrimSpace(w)) == s {
				return true
			}
		}
	}
	return false
}
