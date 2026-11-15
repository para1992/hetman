package adapter

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCatalog(t *testing.T) {
	c, err := ParseCatalog([]byte(`{"complete":true,"models":[{"id":"a/x","efforts":[]},{"id":"a/y","efforts":["low"]},{"id":"b/z"},{"id":"b/w","efforts":null}]}` + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Model("a/x").EffortsKnown() || len(c.Model("a/x").Efforts) != 0 {
		t.Error("an empty list means no effort setting")
	}
	if c.Model("b/z").EffortsKnown() || c.Model("b/w").EffortsKnown() {
		t.Error("absent or null efforts mean unknown")
	}
	for _, bad := range []string{``, `{"models":[]}` + "\n" + `{}`, `{"models":[{"id":""}]}`, `{"models":[{"id":"a"},{"id":"a"}]}`,
		`{"models":[{"id":"a","efforts":["x","x"]}]}`, `{"models":[],"extra":1}`} {
		if _, err := ParseCatalog([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCatalogCheck(t *testing.T) {
	c := &Catalog{Complete: true, Models: []CatalogModel{
		{ID: "p/small", Efforts: []string{}}, {ID: "p/large", Efforts: []string{"low", "high"}}, {ID: "q/open"}}}
	if f := c.Check("", "high"); f != nil {
		t.Errorf("the default model cannot be checked: %+v", f)
	}
	if f := c.Check("p/large", "high"); f != nil {
		t.Errorf("a valid pair: %+v", f)
	}
	if f := c.Check("q/open", "anything"); f != nil {
		t.Errorf("unknown efforts are not checked: %+v", f)
	}
	f := c.Check("p/larg", "")
	if len(f) != 1 || f[0].Field != FieldModel || f[0].Warning || !reflect.DeepEqual(f[0].Suggest, []string{"p/large"}) {
		t.Errorf("unknown model: %+v", f)
	}
	f = c.Check("p/small", "low")
	if len(f) != 1 || f[0].Field != FieldEffort || !strings.Contains(f[0].Message, "no effort setting") {
		t.Errorf("effort on a model without one: %+v", f)
	}
	f = c.Check("p/large", "hihg")
	if len(f) != 1 || !reflect.DeepEqual(f[0].Suggest, []string{"high"}) {
		t.Errorf("bad effort: %+v", f)
	}
	c.Complete = false
	if f := c.Check("p/other", ""); len(f) != 1 || !f[0].Warning {
		t.Errorf("an incomplete catalogue only warns: %+v", f)
	}
	if got := c.Filter("LARGE"); len(got) != 1 || got[0].ID != "p/large" {
		t.Errorf("filter: %+v", got)
	}
}

func TestSuggest(t *testing.T) {
	ids := []string{"cloud/big-x", "prov/swift-4", "prov/swift-5", "prov/brisk", "other/plain"}
	if got := Suggest("swift", ids, 3); len(got) != 2 {
		t.Errorf("a part of the name: %v", got)
	}
	if got := Suggest("prov/brisc", ids, 3); len(got) == 0 || got[0] != "prov/brisk" {
		t.Errorf("a typo: %v", got)
	}
	if got := Suggest("zzzzzz", ids, 3); got != nil {
		t.Errorf("nothing near: %v", got)
	}
}
