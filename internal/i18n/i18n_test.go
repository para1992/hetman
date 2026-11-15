package i18n

import (
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestPluralRules(t *testing.T) {
	cases := map[string]map[int64]string{
		"zh": {0: Other, 1: Other, 2: Other, 5: Other, 21: Other, 100: Other},
		"en": {0: Other, 1: One, 2: Other, 5: Other, 11: Other, 21: Other, 101: Other},
		"es": {0: Other, 1: One, 2: Other, 5: Other, 21: Other, 1000000: Other},
		"hi": {0: One, 1: One, 2: Other, 5: Other, 11: Other, 21: Other},
		"uk": {
			0: Many, 1: One, 2: Few, 3: Few, 4: Few, 5: Many, 9: Many, 10: Many,
			11: Many, 12: Many, 13: Many, 14: Many, 15: Many, 20: Many,
			21: One, 22: Few, 24: Few, 25: Many, 101: One, 111: Many, 112: Many, 122: Few, 1001: One,
		},
		"ar": {
			0: Zero, 1: One, 2: Two, 3: Few, 10: Few, 11: Many, 26: Many, 99: Many,
			100: Other, 101: Other, 102: Other, 103: Few, 110: Few, 111: Many, 199: Many, 200: Other, 1000: Other,
		},
	}
	for lang, want := range cases {
		for n, cat := range want {
			if got := PluralCategory(lang, n); got != cat {
				t.Errorf("%s %d: %s, want %s", lang, n, got, cat)
			}
		}
	}
	counts := map[string]int{"zh": 1, "en": 2, "es": 2, "hi": 2, "uk": 3, "ar": 6}
	for lang, n := range counts {
		if got := len(PluralCategories(lang)); got != n {
			t.Errorf("%s has %d plural categories, want %d", lang, got, n)
		}
	}
	if PluralCategory("xx", 1) != One || PluralCategory("xx", 3) != Other {
		t.Error("a language without a rule does not use the English rule")
	}
}

func TestParamsAndPlurals(t *testing.T) {
	c, err := Parse([]byte("a: \"{x} and {y}, {x}\"\nb:\n  one: \"{n} file\"\n  other: \"{n} files\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c["a"].Params(); !slices.Equal(got, []string{"x", "y"}) {
		t.Errorf("params %v", got)
	}
	if got := c["b"].form("en", 1); got != "{n} file" {
		t.Errorf("one: %q", got)
	}
	if got := c["b"].form("uk", 3); got != "{n} files" {
		t.Errorf("a missing form must fall back to other: %q", got)
	}
	for _, bad := range []string{"a: x\na: y\n", "a:\n  several: x\n", "- a\n", "a: [x]\n"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestRender(t *testing.T) {
	en := English()
	if got := en.T("cli.halt_done", "id", 7, "node", "build"); got != "campaign 7 halted at build" {
		t.Errorf("substitution: %q", got)
	}
	if got := en.T("explain.once"); got != "once" {
		t.Errorf("plural one: %q", got)
	}
	if got := en.T("explain.times", "n", 3); got != "3 times" {
		t.Errorf("plural other: %q", got)
	}
	if got := en.T("cli.halt_done", "id", 7); got != "campaign 7 halted at {node}" {
		t.Errorf("a missing parameter must stay visible: %q", got)
	}
	if got := en.T("no.such.key"); got != "no.such.key" {
		t.Errorf("missing key: %q", got)
	}
	var nilPrinter *Printer
	if nilPrinter.Lang() != "en" || nilPrinter.T("cli.halt_already", "id", 1) != "campaign 1 is already halted" {
		t.Error("a nil printer does not speak English")
	}
	// A parameter value is not read as a template.
	if got := en.T("cli.halt_done", "id", "{node}", "node", "x"); got != "campaign {node} halted at x" {
		t.Errorf("value read as a template: %q", got)
	}
}

func TestFallback(t *testing.T) {
	for _, code := range []string{"hi", "zh", "es", "ar", "xx", ""} {
		p := For(code)
		if got := p.T("cli.halt_already", "id", 2); got != "campaign 2 is already halted" {
			t.Errorf("%q: no English fallback: %q", code, got)
		}
		// English plural forms keep the English rule in another language.
		if got := p.T("explain.times", "n", 1); got != "1 time" {
			t.Errorf("%q: %q", code, got)
		}
	}
	if For("xx").Lang() != "en" {
		t.Error("an unknown code must give English")
	}
	if Resolve("", "uk") != "uk" || Resolve("zh", "uk") != "zh" || Resolve("xx", "") != "en" || Resolve() != "en" {
		t.Error("resolve")
	}
	uk := For("uk")
	for n, want := range map[int]string{1: "1 спроба", 2: "2 спроби", 5: "5 спроб", 11: "11 спроб", 21: "21 спроба", 24: "24 спроби"} {
		if got := uk.T("page.attempts", "n", n); got != want {
			t.Errorf("uk %d: %q, want %q", n, got, want)
		}
	}
	tx := Texts("hi", "page.")
	if tx["page.brand"] != "Watchtower" {
		t.Errorf("texts of a language without a catalogue: %v", tx["page.brand"])
	}
	if f, ok := Texts("uk", "page.")["page.attempts"].(Forms); !ok || f.Lang != "uk" || len(f.Forms) != 3 {
		t.Errorf("uk plural texts: %#v", f)
	}
	for k := range tx {
		if !strings.HasPrefix(k, "page.") {
			t.Errorf("texts outside the prefix: %s", k)
		}
	}
}

func TestYesNo(t *testing.T) {
	uk := For("uk")
	for _, s := range []string{"y", "Yes", "т", "Так"} {
		if !IsYes(uk, s) {
			t.Errorf("uk yes %q", s)
		}
	}
	for _, s := range []string{"n", "NO", "н", "ні"} {
		if !IsNo(uk, s) {
			t.Errorf("uk no %q", s)
		}
	}
	if IsYes(English(), "т") || IsYes(English(), "") {
		t.Error("English takes a Ukrainian yes")
	}
	if Upper("додати") != "Додати" || Upper("add") != "Add" || Upper("") != "" {
		t.Error("upper")
	}
}

// requiredComplete lists the catalogues that must translate every key.
var requiredComplete = []string{"uk"}

// TestCatalogues checks every translation against English: no key English
// lacks, no parameter the English message does not have, every plural
// form the language needs. It reports the keys a catalogue does not
// translate yet; a catalogue in requiredComplete must have them all.
func TestCatalogues(t *testing.T) {
	all := Catalogues()
	en := all[Source]
	for key, m := range en {
		if m.Plural != nil {
			for _, cat := range PluralCategories(Source) {
				if _, ok := m.Plural[cat]; !ok {
					t.Errorf("en: %s has no %s form", key, cat)
				}
			}
		}
	}
	var langs []string
	for code := range all {
		langs = append(langs, code)
	}
	sort.Strings(langs)
	for _, code := range langs {
		if code == Source {
			continue
		}
		if _, known := rules[code]; !known {
			t.Errorf("catalogue %s: not a language of the built-in list", code)
			continue
		}
		c := all[code]
		var missing []string
		for key := range en {
			if _, ok := c[key]; !ok {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
		for key, m := range c {
			src, ok := en[key]
			if !ok {
				t.Errorf("%s: %s is not an English key", code, key)
				continue
			}
			allowed := src.Params()
			for _, p := range m.Params() {
				if !slices.Contains(allowed, p) {
					t.Errorf("%s: %s uses {%s}, which the English message does not have", code, key, p)
				}
			}
			switch {
			case src.Plural != nil && m.Plural == nil:
				t.Errorf("%s: %s must have plural forms, as in English", code, key)
			case m.Plural != nil && !slices.Contains(allowed, "n"):
				t.Errorf("%s: %s has plural forms, but the English message has no {n}", code, key)
			}
			if m.Plural != nil {
				need := PluralCategories(code)
				for _, cat := range need {
					if _, ok := m.Plural[cat]; !ok {
						t.Errorf("%s: %s has no %s form", code, key, cat)
					}
				}
				for cat := range m.Plural {
					if !slices.Contains(need, cat) {
						t.Errorf("%s: %s has a %s form, which %s does not use", code, key, cat, code)
					}
				}
			}
		}
		t.Logf("translation %s: %d of %d keys translated, %d not yet", code, len(en)-len(missing), len(en), len(missing))
		for _, key := range missing {
			t.Logf("translation %s: not translated: %s", code, key)
		}
		if slices.Contains(requiredComplete, code) && len(missing) > 0 {
			t.Errorf("%s must be complete; %d keys are missing, such as %s", code, len(missing), missing[0])
		}
	}
	for _, code := range requiredComplete {
		if all[code] == nil {
			t.Errorf("there is no %s catalogue", code)
		}
	}
}
