package main

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hetman/internal/adapter"
)

// --catalog lists every model of the client's listing with its efforts,
// without the delegating one, and says the list is complete.
func TestCatalog(t *testing.T) {
	t.Setenv(envFake, "ok")
	var out, diag bytes.Buffer
	if code := run(append(fakeArgs(t), adapter.CatalogFlag), strings.NewReader(""), &out, &diag); code != 0 {
		t.Fatalf("exit %d: %s", code, diag.String())
	}
	c, err := adapter.ParseCatalog(out.Bytes())
	if err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	want := []adapter.CatalogModel{
		{ID: "gpt-5.6-luna", Label: "GPT-5.6-Luna", Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
		{ID: "gpt-6.1-sol", Label: "GPT-6.1-Sol", Efforts: []string{"low", "high"}},
		{ID: "codex-auto-review", Label: "Codex Auto Review", Group: "hidden", Efforts: []string{"low"}},
	}
	if !c.Complete || !reflect.DeepEqual(c.Models, want) {
		t.Fatalf("catalogue %+v", c)
	}
}

// When the listing cannot be refreshed, the bundled one is used and the
// catalogue is not complete; without either there is none, with a reason.
func TestCatalogFallback(t *testing.T) {
	t.Setenv(envFake, "ok")
	t.Setenv(envNoCatalog, "refresh")
	var out bytes.Buffer
	run(append(fakeArgs(t), adapter.CatalogFlag), strings.NewReader(""), &out, &bytes.Buffer{})
	c, err := adapter.ParseCatalog(out.Bytes())
	if err != nil || c.Complete || len(c.Models) != 3 {
		t.Fatalf("%v %+v", err, c)
	}
	var diag bytes.Buffer
	out.Reset()
	if code := run([]string{"--client", filepath.Join(t.TempDir(), "missing"), adapter.CatalogFlag}, strings.NewReader(""), &out, &diag); code == 0 || out.Len() > 0 {
		t.Fatalf("exit %d, output %q", code, out.String())
	}
	if !strings.Contains(diag.String(), "cannot be listed") {
		t.Fatalf("diagnostics %q", diag.String())
	}
}
