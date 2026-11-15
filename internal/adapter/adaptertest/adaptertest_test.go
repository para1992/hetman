package adaptertest_test

import (
	"os"
	"path/filepath"
	"testing"

	"hetman/internal/adapter/adaptertest"
	"hetman/internal/stubadapter"
	"hetman/internal/teststep"
)

func TestMain(m *testing.M) {
	if os.Getenv(teststep.EnvHelper) == "1" && len(os.Args) > 1 && os.Args[1] == "adapter" {
		os.Exit(stubadapter.Main(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Setenv(teststep.EnvHelper, "1")
	os.Exit(m.Run())
}

func self(t *testing.T) string {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// The stub with no plan answers every step with the first outcome its
// schema allows.
func TestStubWithoutPlan(t *testing.T) {
	adaptertest.Run(t, adaptertest.Config{Command: []string{self(t), "adapter"}})
}

// The stub with a plan that reports a session, logs and, for the step that
// is cancelled, works until it is told to stop.
func TestStubWithPlan(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(plan, []byte(`{
  "name": "scripted",
  "usage": {"five_hour": {"used": 0.25, "resets_in": "3h"}, "weekly": {"used": 0.5}},
  "nodes": {
    "conformance": [[{"log": "thinking"}, {"session": "s-1"}, {"result": {"outcome": "ok"}}]],
    "conformance-resume": [[{"session": "s-1"}, {"result": {"outcome": "ok"}}]],
    "conformance-dialogue": [
      [{"session": "s-2"}, {"result": {"outcome": "questions", "questions": [{"id": "color", "question": "Which color?", "kind": "choice", "options": ["red", "blue"]}]}}],
      [{"require_session": "s-2"}, {"session": "s-2"}, {"result": {"outcome": "ok"}}]
    ],
    "conformance-cancel": [[{"log": "counting"}, {"hang": true}]]
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	adaptertest.Run(t, adaptertest.Config{Command: []string{self(t), "adapter", plan}, Dir: dir})
}

// TestExternal runs the suite against the adapter named by HETMAN_ADAPTER.
func TestExternal(t *testing.T) {
	argv, ok, err := adaptertest.Command()
	switch {
	case !ok:
		t.Skip("set " + adaptertest.EnvAdapter + ` to a JSON list such as ["/path/to/adapter"] to check an adapter`)
	case err != nil || len(argv) == 0:
		t.Fatalf("%s must be a non-empty JSON list of strings: %v", adaptertest.EnvAdapter, err)
	}
	adaptertest.Run(t, adaptertest.Config{Command: argv, Model: os.Getenv("HETMAN_ADAPTER_MODEL"), Effort: os.Getenv("HETMAN_ADAPTER_EFFORT")})
}
