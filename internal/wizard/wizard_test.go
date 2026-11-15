package wizard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/orders"
)

// input joins answers, one per line; "" takes the suggestion.
func input(answers ...string) *strings.Reader {
	return strings.NewReader(strings.Join(answers, "\n") + "\n")
}

const fourSteps = `version: 0
flow: toy
workdir: .
start: prepare

checks:
  lint: {run: [sh, checks/lint.sh], soft: true}
  tests: {run: [sh, checks/tests.sh], timeout: 1m}

nodes:
  prepare:
    type: script
    run: [sh, steps/prepare.sh]
    on: {ok: build, fail: failed}
  build:
    type: script
    run: [sh, steps/build.sh]
    timeout: 5m
    on: {ok: test, fail: failed, timeout: failed}
  test:
    type: script
    run: [sh, steps/test.sh]
    on: {ok: ready, fail: build}
  ready:
    type: gate
    checks: [lint, tests]
    on: {pass: publish, blocked: failed}
  publish:
    type: script
    run: [sh, steps/publish.sh]
    on: {ok: done, fail: failed}
`

// The answers that build fourSteps from nothing.
var buildFourSteps = []string{
	"toy",                      // flow name
	"",                         // no language: keep English
	"1", "prepare", "", "", "", // a script step: the suggested command, no limit, fail -> failed
	"",                                         // write it
	"1", "", "build", "", "", "", "5m", "", "", // add a step after prepare, with a limit
	"1", "script", "test", "after a step", "build", "", "", "build", "", // its failure goes back to build
	"2", "ready", "", "", // add a gate after test
	"lint", "", "2", "", "y", // a soft check
	"tests", "", "", "1m", "n", // a required check with a limit
	"", "", // blocked -> failed; write it
	"1", "", "publish", "", "", "", "", "", "",
	"11", // quit
}

func TestFourStepsAndGateFromNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "toy")
	os.Mkdir(dir, 0o755)
	path := filepath.Join(dir, orders.DefaultFile)
	var out bytes.Buffer
	if err := Run(path, input(buildFourSteps...), &out, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if string(data) != fourSteps {
		t.Fatalf("got:\n%s\ntranscript:\n%s", data, out.String())
	}
	if _, err := orders.Load(path); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), "Wrote orders.yaml."); n != 5 {
		t.Fatalf("%d writes, want 5:\n%s", n, out.String())
	}
}

// TestChangesAndRefusal changes an existing flow and shows that a change
// the check refuses writes nothing.
func TestChangesAndRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), orders.DefaultFile)
	if err := os.WriteFile(path, []byte(fourSteps), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	answers := []string{
		"4", "build", "compile", "", // rename build
		"5", "ready", "blocked", "compile", "", // the gate sends a block back
		"6", "test", "2m", "", // a limit on test
		"6", "compile", "none", "", // no limit on compile
		"5", "publish", "fail", "nowhere", "failed", "n", // a change not confirmed
		"6", "ready", "1m", // a gate with checks takes no timeout: refused by the check
		"3", "prepare", "", // remove prepare: compile becomes the start
		"10", "ready", "notes", "Are the release notes written?", "", // a question at the gate
		"quit",
	}
	if err := Run(path, input(answers...), &out, true); err != nil {
		t.Fatal(err)
	}
	o, err := orders.Load(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	switch {
	case o.Start != "compile" || o.Nodes["build"] != nil || o.Nodes["prepare"] != nil:
		t.Fatalf("start %s, nodes %v", o.Start, o.Order)
	case o.Nodes["ready"].On["blocked"] != "compile" || o.Nodes["test"].On["fail"] != "compile":
		t.Fatalf("transitions %v %v", o.Nodes["ready"].On, o.Nodes["test"].On)
	case o.Nodes["test"].Timeout.String() != "2m0s" || o.Nodes["compile"].Timeout != 0 || o.Nodes["compile"].On["timeout"] != "":
		t.Fatal("timeouts not changed")
	case o.Nodes["publish"].On["fail"] != "failed":
		t.Fatal("a change was written without confirmation")
	case len(o.Nodes["ready"].Ask) != 1:
		t.Fatal("question not added")
	}
	if !strings.Contains(out.String(), "Pick a number from 1 to") {
		t.Fatalf("an unknown choice was not refused:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "has no timeout of its own") || o.Nodes["ready"].Timeout != 0 {
		t.Fatalf("a change the check refuses was written:\n%s", out.String())
	}
}

// TestNewFlowLanguage picks the language of a new flow by its name, after
// answers that match no language or more than one.
func TestNewFlowLanguage(t *testing.T) {
	path := filepath.Join(t.TempDir(), orders.DefaultFile)
	var out bytes.Buffer
	answers := []string{"toy", "klingon", "7", "ish", "ukrainian", "1", "prepare", "", "", "", "", "quit"}
	if err := Run(path, input(answers...), &out, true); err != nil {
		t.Fatal(err)
	}
	o, err := orders.Load(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if o.Language != "uk" {
		t.Fatalf("language %q:\n%s", o.Language, out.String())
	}
	for _, s := range []string{"0) skip, keep English", "6) zh  Mandarin Chinese (普通话)", `No language in the list is called "klingon"`,
		"Pick a number from 0 to 6.", "More than one language matches: en English, es Spanish.",
		"write what the flow addresses to a person in Ukrainian (українська), code uk", "+language: uk"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("transcript lacks %q:\n%s", s, out.String())
		}
	}
	for _, pick := range []string{"5", "uk", "UK", "українська"} {
		path := filepath.Join(t.TempDir(), orders.DefaultFile)
		if err := Run(path, input("toy", pick, "1", "prepare", "", "", "", "", "quit"), &bytes.Buffer{}, true); err != nil {
			t.Fatal(err)
		}
		if o, err := orders.Load(path); err != nil || o.Language != "uk" {
			t.Errorf("pick %q: %v", pick, err)
		}
	}
}
