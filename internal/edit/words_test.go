package edit

import (
	"testing"

	"hetman/internal/i18n"
)

// The summaries a model reads stay as they were; a person reads the same
// summaries from the catalogue, in English or translated.
func TestSummariesFromTheCatalogue(t *testing.T) {
	uk := i18n.For("uk")
	for _, s := range Specs {
		if got := s.SummaryIn(nil); got != s.Summary {
			t.Errorf("%s: catalogue %q, spec %q", s.Op, got, s.Summary)
		}
		if got := s.SummaryIn(uk); got == s.Summary || got == "op.summary."+s.Op {
			t.Errorf("%s: no Ukrainian summary: %q", s.Op, got)
		}
	}
	op := Op{Op: OpGateAdd, Node: "ready", Check: "lint", At: 2}
	if got := op.WordsIn(uk); got != "додати перевірку lint до застави ready на позицію 2" {
		t.Errorf("words: %q", got)
	}
}
