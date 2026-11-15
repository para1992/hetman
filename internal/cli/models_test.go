package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hetman/internal/teststep"
)

// catalogWorld is an agent world whose stub has the fixed catalogue, with
// a second executor "bare" whose stub has none.
func catalogWorld(t *testing.T) *world {
	w := newAgentWorld(t, `{"catalog": true}`, "Do it.")
	text, err := os.ReadFile(w.orders)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.dir, "bare.json"), []byte(`{}`), 0o644)
	config := strings.Replace(string(text), "model: m1, effort: e1", "model: alpha/large, effort: high", 1)
	config = strings.Replace(config, "executors:\n", "executors:\n  bare: {run: "+teststep.Adapter(filepath.Join(w.dir, "bare.json"))+"}\n", 1)
	if err := os.WriteFile(w.orders, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return w
}

func call(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestScribeModels(t *testing.T) {
	w := catalogWorld(t)
	code, out, errOut := call("scribe", "models", "-f", w.orders)
	if code != exitDone {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	for _, want := range []string{
		"Executor bare: no catalogue; the adapter of executor bare does not tell which models its client offers.",
		"Executor stub: 4 models",
		"  alpha\n    alpha/small  Alpha Small  no effort setting\n    alpha/large  Alpha Large  effort: low, medium, high\n",
		"    beta/plain   Beta Plain   effort not known\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	code, out, _ = call("scribe", "models", "-f", w.orders, "stub", "DEEP")
	if code != exitDone || !strings.Contains(out, `Executor stub: 1 of 4 models hold "DEEP"`) || !strings.Contains(out, "beta/deep") || strings.Contains(out, "alpha/small") {
		t.Fatalf("filtered: exit %d\n%s", code, out)
	}
	if code, out, _ = call("scribe", "models", "-f", w.orders, "bare"); code != exitFailed || !strings.Contains(out, "no catalogue") {
		t.Fatalf("one executor without a catalogue: exit %d\n%s", code, out)
	}
}

// A node is switched to an executor whose catalogue lacks its model in one
// operation, with the model and effort checked against the new catalogue;
// the plain form is refused with exactly that command, and still works when
// the new catalogue lists the node's model.
func TestScribeSetExecutorWithItsModel(t *testing.T) {
	w := catalogWorld(t)
	os.WriteFile(filepath.Join(w.dir, "other.json"), []byte(`{"catalog": {"complete": true, "models": [{"id": "gamma/one", "efforts": ["low", "high"]}, {"id": "alpha/large", "efforts": ["high"]}]}}`), 0o644)
	text, _ := os.ReadFile(w.orders)
	config := strings.Replace(string(text), "executors:\n", "executors:\n  other: {run: "+teststep.Adapter(filepath.Join(w.dir, "other.json"))+"}\n", 1)
	config = strings.Replace(config, "model: alpha/large, effort: high", "model: alpha/small", 1)
	if err := os.WriteFile(w.orders, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(w.orders)

	code, _, errOut := call("scribe", "set", "a", "executor", "other", "-f", w.orders)
	if code != exitUsage || !strings.Contains(errOut, "hetman scribe set a executor other -model MODEL [-effort EFFORT]") {
		t.Fatalf("plain form: exit %d\n%s", code, errOut)
	}
	if after, _ := os.ReadFile(w.orders); !bytes.Equal(before, after) {
		t.Fatal("a refused change was written")
	}
	code, _, errOut = call("scribe", "set", "a", "executor", "other", "-model", "gamma/one", "-effort", "max", "-f", w.orders)
	if code != exitUsage || !strings.Contains(errOut, `effort "max" is not offered by model "gamma/one"`) {
		t.Fatalf("an effort the new model lacks: exit %d\n%s", code, errOut)
	}
	code, out, errOut := call("scribe", "set", "a", "executor", "other", "-model", "gamma/one", "-effort", "low", "-f", w.orders)
	if code != exitDone || !strings.Contains(out, "Switch a to the client other, model gamma/one, effort low") {
		t.Fatalf("one operation: exit %d\n%s%s", code, out, errOut)
	}
	data, _ := os.ReadFile(w.orders)
	if s := string(data); !strings.Contains(s, "executor: other") || !strings.Contains(s, "model: gamma/one") || !strings.Contains(s, "effort: low") {
		t.Fatalf("orders:\n%s", s)
	}
	if code, _, errOut := call("check", "-f", w.orders); code != exitDone {
		t.Fatalf("check: exit %d\n%s", code, errOut)
	}
	// Back to stub with a model it lists and no effort: the old effort goes.
	if code, out, errOut := call("scribe", "set", "a", "executor", "stub", "-model", "alpha/small", "-f", w.orders); code != exitDone {
		t.Fatalf("model without effort: exit %d\n%s%s", code, out, errOut)
	}
	if data, _ := os.ReadFile(w.orders); strings.Contains(string(data), "effort") {
		t.Fatalf("the old effort stayed:\n%s", data)
	}
	// alpha/large is listed by both; the plain form switches it.
	call("scribe", "set", "a", "model", "alpha/large", "-f", w.orders)
	if code, out, errOut := call("scribe", "set", "a", "executor", "other", "-f", w.orders); code != exitDone {
		t.Fatalf("plain form with a listed model: exit %d\n%s%s", code, out, errOut)
	}
	if code, _, errOut := call("scribe", "set", "a", "model", "alpha/large", "-effort", "high", "-f", w.orders); code != exitUsage || !strings.Contains(errOut, "only with the field executor") {
		t.Fatalf("model and effort with another field: exit %d\n%s", code, errOut)
	}
}

// hetman scribe set refuses a model or effort the catalogue does not
// offer, with the nearest names, unless -unlisted is given.
func TestScribeSetChecksTheCatalogue(t *testing.T) {
	w := catalogWorld(t)
	before, _ := os.ReadFile(w.orders)
	code, _, errOut := call("scribe", "set", "a", "model", "alpha/larg", "-f", w.orders)
	if code != exitUsage || !strings.Contains(errOut, `node "a": model: model "alpha/larg" is not in the catalogue of the executor; nearest: alpha/large`) ||
		!strings.Contains(errOut, "-unlisted") {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	if after, _ := os.ReadFile(w.orders); !bytes.Equal(before, after) {
		t.Fatal("a refused change was written")
	}
	code, _, errOut = call("scribe", "set", "a", "model", "alpha/small", "-f", w.orders)
	if code != exitUsage || !strings.Contains(errOut, `model "alpha/small" has no effort setting; remove effort "high"`) {
		t.Fatalf("a model that does not take the node's effort: exit %d\n%s", code, errOut)
	}
	if code, _, errOut = call("scribe", "set", "a", "effort", "max", "-f", w.orders); code != exitUsage || !strings.Contains(errOut, "which offers low, medium, high") {
		t.Fatalf("an effort the model does not offer: exit %d\n%s", code, errOut)
	}
	if code, out, errOut := call("scribe", "set", "a", "effort", "low", "-f", w.orders); code != exitDone {
		t.Fatalf("a valid effort: exit %d\n%s%s", code, out, errOut)
	}
	code, out, errOut := call("scribe", "set", "a", "model", "gamma/new", "-unlisted", "-f", w.orders)
	if code != exitDone || !strings.Contains(errOut, `warning: orders.yaml:`) || !strings.Contains(errOut, `model "gamma/new" is not in the catalogue`) {
		t.Fatalf("-unlisted: exit %d\n%s%s", code, out, errOut)
	}
	// What was already wrong does not block another change.
	if code, out, errOut := call("scribe", "set", "a", "attempts", "2", "-f", w.orders); code != exitDone {
		t.Fatalf("an unrelated change: exit %d\n%s%s", code, out, errOut)
	}
	if code, _, errOut := call("check", "-f", w.orders); code != exitUsage || !strings.Contains(errOut, `node "a": model: model "gamma/new" is not in the catalogue of the executor (catalogue of executor stub)`) {
		t.Fatalf("check: exit %d\n%s", code, errOut)
	}
	// A role's client is checked the same way.
	code, _, errOut = call("scribe", "role", "coder", "-select", "fixed", "-executor", "stub", "-model", "beta/deep", "-effort", "low", "-f", w.orders, "-unlisted=false")
	if code != exitUsage || !strings.Contains(errOut, `role "coder": effort: effort "low" is not offered by model "beta/deep", which offers high, max`) {
		t.Fatalf("role: exit %d\n%s", code, errOut)
	}
}
