package language

import (
	"sort"
	"strings"
	"testing"
)

func TestList(t *testing.T) {
	if len(List) != 6 {
		t.Fatalf("%d languages, want 6", len(List))
	}
	seen := map[string]bool{}
	for _, l := range List {
		if l.Code == "" || strings.TrimSpace(l.English) == "" || strings.TrimSpace(l.Native) == "" {
			t.Errorf("incomplete entry %+v", l)
		}
		if l.Code != strings.ToLower(l.Code) || strings.ContainsAny(l.Code, " -_") {
			t.Errorf("code %q: want a lower-case language subtag", l.Code)
		}
		if seen[l.Code] {
			t.Errorf("code %q is listed twice", l.Code)
		}
		seen[l.Code] = true
	}
	if !sort.SliceIsSorted(List, func(i, j int) bool { return List[i].Code < List[j].Code }) {
		t.Error("the list is not sorted by code")
	}
	if _, ok := Lookup(Default); !ok {
		t.Errorf("the default %q is not in the list", Default)
	}
}

func TestFind(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"es", []string{"es"}},
		{"ES", []string{"es"}},
		{"spanish", []string{"es"}},
		{"español", []string{"es"}},
		{"українська", []string{"uk"}},
		{"mandarin", []string{"zh"}},
		{"arab", []string{"ar"}},
		{"ish", []string{"en", "es"}},
		{"klingon", nil},
		{"", nil},
	} {
		var got []string
		for _, l := range Find(c.in) {
			got = append(got, l.Code)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("Find(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if msg := Unknown("Spanish"); !strings.Contains(msg, `"Spanish"`) || !strings.Contains(msg, "did you mean es (Spanish)?") || !strings.Contains(msg, "(known: ar, en, es, hi, uk, zh)") {
		t.Errorf("Unknown: %s", msg)
	}
}
