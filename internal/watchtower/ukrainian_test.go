package watchtower_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"hetman/internal/watchtower"
)

func oneStep() string {
	return "version: 0\nflow: f\nstart: a\nnodes:\n  a: {type: script, run: " + arg("exit", "0") + ", on: {ok: done, fail: failed}}\n"
}

// The page gets its texts from the catalogue of the language it asks for,
// starting in the server's language; a language without a catalogue gets
// English texts.
func TestPageTextsInUkrainian(t *testing.T) {
	w := newWorld(t, oneStep())
	var tx struct {
		Lang      string
		Default   string
		Languages []watchtower.LanguageInfo
		Texts     map[string]any
	}
	if code := w.get("/api/texts?lang=uk", &tx); code != http.StatusOK {
		t.Fatalf("texts: %d", code)
	}
	if tx.Lang != "uk" || tx.Default != "en" || tx.Texts["page.brand"] != "Вежа" || tx.Texts["page.tab.chronicle"] != "Літопис" {
		t.Fatalf("uk texts: %s %s %v %v", tx.Lang, tx.Default, tx.Texts["page.brand"], tx.Texts["page.tab.chronicle"])
	}
	forms, _ := tx.Texts["page.attempts"].(map[string]any)
	if f, _ := forms["forms"].(map[string]any); forms["lang"] != "uk" || f["one"] != "{n} спроба" || f["few"] != "{n} спроби" || f["many"] != "{n} спроб" {
		t.Fatalf("plural forms: %v", forms)
	}
	if len(tx.Languages) != 6 {
		t.Fatalf("languages: %v", tx.Languages)
	}
	for _, l := range tx.Languages {
		if l.Translated != (l.Code == "en" || l.Code == "uk") {
			t.Errorf("%s translated: %v", l.Code, l.Translated)
		}
	}
	for k := range tx.Texts {
		if !strings.HasPrefix(k, "page.") {
			t.Errorf("not a page text: %s", k)
		}
	}
	if w.get("/api/texts?lang=zh", &tx); tx.Lang != "zh" || tx.Texts["page.brand"] != "Watchtower" {
		t.Fatalf("zh falls back to English: %s %v", tx.Lang, tx.Texts["page.brand"])
	}

	// The server's language is where the page starts.
	w.srv.Lang = "uk"
	if w.get("/api/texts", &tx); tx.Lang != "uk" || tx.Default != "uk" {
		t.Fatalf("default: %s %s", tx.Lang, tx.Default)
	}
	resp, err := http.Get(w.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), `<html lang="uk">`) || !strings.Contains(string(page), "<title>Вежа</title>") {
		t.Fatalf("page:\n%s", page)
	}

	// The campaign in words follows the language asked for.
	if _, err := w.chron.NewCampaign("f", w.orders, "a", nil, ""); err != nil {
		t.Fatal(err)
	}
	var c watchtower.Campaign
	if w.get("/api/campaigns/1?lang=uk", &c); len(c.Words) == 0 || c.Words[0].Title != "Де похід зараз" ||
		!strings.HasPrefix(c.Words[0].Lines[0], "Похід 1 потоку f розпочато ") {
		t.Fatalf("words: %+v", c.Words)
	}
	if w.get("/api/campaigns/1?lang=en", &c); c.Words[0].Title != "Where it is" {
		t.Fatalf("words in English: %+v", c.Words)
	}
}
