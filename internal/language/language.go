// Package language lists the languages a flow can address a person in.
//
// The list holds the five most spoken languages of the world by total
// number of speakers (first and second language), as ranked by the
// Ethnologue 200, 2025 edition, and Ukrainian. Each language has its BCP 47
// code, its English name and its own name written in its own script. The
// list is sorted by code.
package language

import (
	"fmt"
	"strings"
)

// Language is one language of the list.
type Language struct {
	Code    string // BCP 47 language tag
	English string // name in English
	Native  string // own name, in its own script
}

// Label names a language by its English name and its own name.
func (l Language) Label() string { return l.English + " (" + l.Native + ")" }

// List is every language, sorted by code.
var List = []Language{
	{"ar", "Arabic", "العربية"},
	{"en", "English", "English"},
	{"es", "Spanish", "español"},
	{"hi", "Hindi", "हिन्दी"},
	{"uk", "Ukrainian", "українська"},
	{"zh", "Mandarin Chinese", "普通话"},
}

// Default is the language placeholders name when a flow sets none. No
// instruction about the language is added to prompts then.
const Default = "en"

// Lookup finds a language by its code, written exactly as in the list.
func Lookup(code string) (Language, bool) {
	for _, l := range List {
		if l.Code == code {
			return l, true
		}
	}
	return Language{}, false
}

// Codes lists every code, in the order of the list.
func Codes() []string {
	out := make([]string, len(List))
	for i, l := range List {
		out[i] = l.Code
	}
	return out
}

// Find looks a language up by what a person types: its code, its English
// name or its own name, ignoring case. Without such a match it returns the
// languages whose English name contains s.
func Find(s string) []Language {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, l := range List {
		if strings.EqualFold(s, l.Code) || strings.EqualFold(s, l.English) || strings.EqualFold(s, l.Native) {
			return []Language{l}
		}
	}
	var out []Language
	for _, l := range List {
		if strings.Contains(strings.ToLower(l.English), strings.ToLower(s)) {
			out = append(out, l)
		}
	}
	return out
}

// Unknown explains that a code is not in the list, suggesting the code a
// person probably meant.
func Unknown(code string) string {
	msg := fmt.Sprintf("unknown language code %q", code)
	if m := Find(code); len(m) == 1 {
		msg += "; did you mean " + m[0].Code + " (" + m[0].English + ")?"
	}
	return msg + " (known: " + strings.Join(Codes(), ", ") + ")"
}
