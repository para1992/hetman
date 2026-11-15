package main

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hetman/internal/adapter"
)

// --catalog lists every model of the client's listing with its variants as
// the efforts, in one request that needs no model.
func TestCatalog(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "record.jsonl")
	t.Setenv(envRecord, rec)
	t.Setenv(envFake, "normal")
	var out, diag bytes.Buffer
	if code := run(append(fakeArgs(t), adapter.CatalogFlag), strings.NewReader(""), &out, &diag); code != 0 {
		t.Fatalf("exit %d: %s", code, diag.String())
	}
	c, err := adapter.ParseCatalog(out.Bytes())
	if err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	want := []adapter.CatalogModel{
		{ID: "example/fast", Label: "Fast", Group: "example", Efforts: []string{"low", "high"}},
		{ID: "example/plain", Label: "Plain", Group: "example", Efforts: []string{}},
	}
	if !c.Complete || !reflect.DeepEqual(c.Models, want) {
		t.Fatalf("catalogue %+v", c)
	}
	all := recorded(t, rec)
	if len(all) != 1 || strings.Join(all[0].Args, " ") != "models --verbose --pure" {
		t.Fatalf("invocations %+v", all)
	}
}

// A client that cannot list its models gives no catalogue and a reason.
func TestCatalogWithoutClient(t *testing.T) {
	var out, diag bytes.Buffer
	if code := run([]string{"--client", filepath.Join(t.TempDir(), "missing"), adapter.CatalogFlag}, strings.NewReader(""), &out, &diag); code == 0 || out.Len() > 0 {
		t.Fatalf("exit %d, output %q", code, out.String())
	}
	if !strings.Contains(diag.String(), "cannot be listed") {
		t.Fatalf("diagnostics %q", diag.String())
	}
}

func TestDescribeDeclaresCatalog(t *testing.T) {
	var out bytes.Buffer
	run([]string{adapter.DescribeFlag}, strings.NewReader(""), &out, &bytes.Buffer{})
	d, err := adapter.ParseDescription(out.Bytes())
	if err != nil || !d.Has(adapter.FeatureCatalog) {
		t.Fatalf("%v %+v", err, d)
	}
}
