package orders

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const reviewSchema = `{
  "type": "object",
  "properties": {
    "outcome": {"type": "string", "enum": ["ok", "needs_rework"]},
    "notes": {"type": "string"}
  },
  "required": ["outcome"],
  "additionalProperties": false
}
`

const writeSchema = `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}, "summary": {"type": "string"}}, "required": ["outcome"]}`

// agentFiles writes the orders and the files they refer to into a fresh
// folder and returns the orders path. files maps a relative path to its text.
func agentFiles(t *testing.T, config string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{
		"prompts/write.md":    "Write {{input.task}}.\n{{rework}}\n",
		"prompts/review.md":   "Review this:\n{{output.write.summary}}\nBuild report: {{artifact.build.report.txt}}\n",
		"schemas/write.json":  writeSchema,
		"schemas/review.json": reviewSchema,
	}
	for k, v := range files {
		all[k] = v
	}
	for rel, text := range all {
		if text == "" {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, DefaultFile)
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const agentOrders = `version: 0
start: build
executors:
  stub: {run: [./adapters/stub, --quiet]}
nodes:
  build: {type: script, run: [./build.sh], on: {ok: write, fail: failed}}
  write:
    type: agent
    executor: stub
    prompt: prompts/write.md
    schema: schemas/write.json
    model: small
    effort: 3
    tools: [read, edit]
    timeout: 10m
    idle_timeout: 1m
    on: {ok: review, fail: failed}
  review:
    type: agent
    executor: stub
    prompt: prompts/review.md
    schema: schemas/review.json
    max_attempts: 5
    on: {ok: done, needs_rework: write, fail: failed}
`

func TestAgentNode(t *testing.T) {
	path := agentFiles(t, agentOrders, nil)
	o, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if x := o.Executors["stub"]; x == nil || !reflect.DeepEqual(x.Run, []string{"./adapters/stub", "--quiet"}) {
		t.Fatalf("executor %+v", x)
	}
	w := o.Nodes["write"]
	if w.Type != Agent || w.Executor != "stub" || w.Model != "small" || w.Effort != "3" ||
		!reflect.DeepEqual(w.Tools, []string{"read", "edit"}) || w.Timeout != 10*time.Minute ||
		w.IdleTimeout != time.Minute || w.MaxAttempts != DefaultMaxAttempts {
		t.Fatalf("write %+v", w)
	}
	if w.Prompt != filepath.Join(o.Dir, "prompts", "write.md") || len(w.Template.Placeholders()) != 2 {
		t.Fatalf("prompt %q %+v", w.Prompt, w.Template.Placeholders())
	}
	if string(w.SchemaJSON) != strings.ReplaceAll(writeSchema, " ", "") {
		t.Fatalf("schema json %s", w.SchemaJSON)
	}
	r := o.Nodes["review"]
	if r.MaxAttempts != 5 || !reflect.DeepEqual(r.Outcomes(), []string{"ok", "needs_rework", "fail"}) {
		t.Fatalf("review %+v outcomes %v", r, r.Outcomes())
	}
	errs, _ := o.CheckPrograms()
	if len(errs) != 2 || !strings.Contains(errs[0].Msg, "./adapters/stub") || errs[0].Line != 4 {
		t.Fatalf("executor program must be looked for: %v", errs)
	}
}

func TestRefs(t *testing.T) {
	o, err := Load(agentFiles(t, agentOrders, nil))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]Ref{
		"input":                     {Kind: RefInput},
		"input.a.b":                 {Kind: RefInput, Path: []string{"a", "b"}},
		"output.review":             {Kind: RefOutput, Node: "review"},
		"output.review.notes":       {Kind: RefOutput, Node: "review", Path: []string{"notes"}},
		"artifact.build.out/r.json": {Kind: RefArtifact, Node: "build", File: "out/r.json"},
		"rework":                    {Kind: RefRework},
	}
	for name, want := range cases {
		got, err := o.Ref(name)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Ref(%q) = %+v, %v; want %+v", name, got, err, want)
		}
	}
	for _, bad := range []string{"inputs", "input.", "output", "output.nope", "output.build", "output.review.extra",
		"artifact.review.x", "artifact.build", "artifact.build.../x", "rework.x", "env.HOME"} {
		if _, err := o.Ref(bad); err == nil {
			t.Errorf("Ref(%q) accepted", bad)
		}
	}
}

func TestAgentNodeWithDottedIDs(t *testing.T) {
	config := strings.NewReplacer("prompts/write.md", "prompts/write.md", "schemas/write.json", "schemas/write.json", "write", "w.x").Replace(agentOrders)
	o, err := Load(agentFiles(t, config, map[string]string{"prompts/review.md": "{{output.w.x.summary}}"}))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := o.Ref("output.w.x.summary"); err != nil || r.Node != "w.x" || r.Path[0] != "summary" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestAgentRejected(t *testing.T) {
	cases := []struct {
		name   string
		config string // replacements applied to agentOrders, old=>new per line
		files  map[string]string
		file   string // file the problem is in, relative; empty for the orders
		line   int
		msg    string
	}{
		{"unknown executor", "executor: stub\n    prompt: prompts/write.md=>executor: other\n    prompt: prompts/write.md", nil, "", 9, `executor "other" is not declared`},
		{"missing prompt file", "prompts/write.md=>prompts/missing.md", nil, "", 10, `file "prompts/missing.md" does not exist`},
		{"missing schema file", "schemas/review.json=>schemas/missing.json", nil, "", 22, `file "schemas/missing.json" does not exist`},
		{"missing executor key", "    executor: stub\n    prompt: prompts/review.md=>    prompt: prompts/review.md", nil, "", 18, `missing key "executor"`},
		{"schema not json", "", map[string]string{"schemas/review.json": "{\n  \"type\": \"object\",\n  oops\n}"}, "schemas/review.json", 3, "not valid JSON"},
		{"unsupported keyword", "", map[string]string{"schemas/review.json": "{\n  \"type\": \"object\",\n  \"oneOf\": []\n}"}, "schemas/review.json", 3, `keyword "oneOf" is not supported`},
		{"bad type", "", map[string]string{"schemas/review.json": "{\"type\": \"object\", \"properties\": {\"outcome\": {\"type\": \"text\"}}}"}, "schemas/review.json", 1, "type must be one of"},
		{"not an object", "", map[string]string{"schemas/review.json": `{"type": "array"}`}, "schemas/review.json", 1, "must be an object"},
		{"no outcome enum", "", map[string]string{"schemas/review.json": `{"type": "object", "properties": {"outcome": {"type": "string"}}, "required": ["outcome"]}`}, "schemas/review.json", 1, "must have an enum"},
		{"outcome not required", "", map[string]string{"schemas/review.json": `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}}`}, "schemas/review.json", 1, "must be listed in required"},
		{"undeclared outcome", "", map[string]string{"schemas/review.json": strings.Replace(reviewSchema, `"needs_rework"]`, `"needs_rework", "blocked"]`, 1)}, "", 24, `outcome "blocked" has no transition, but the schema allows`},
		{"outcome the schema cannot produce", "needs_rework: write=>needs_rework: write, skipped: done", nil, "", 24, `outcome "skipped" can never happen`},
		{"fail transition required", "{ok: review, fail: failed}=>{ok: review}", nil, "", 17, `outcome "fail" has no transition`},
		{"unknown placeholder", "", map[string]string{"prompts/write.md": "Do it.\n  Now {{inptu.task}} please"}, "prompts/write.md", 2, `{{inptu.task}} is not a known placeholder`},
		{"output of a script", "", map[string]string{"prompts/write.md": "{{output.build}}"}, "prompts/write.md", 1, `only agent nodes have an output`},
		{"output field the schema forbids", "", map[string]string{"prompts/write.md": "{{output.review.score}}"}, "prompts/write.md", 1, `has no field "score"`},
		{"agent key on a script", "run: [./build.sh],=>run: [./build.sh], model: x,", nil, "", 6, `model is allowed only on an agent node`},
		{"run on an agent", "    executor: stub\n    prompt: prompts/write.md=>    executor: stub\n    run: [x]\n    prompt: prompts/write.md", nil, "", 10, `an agent node has no run`},
		{"max attempts zero", "max_attempts: 5=>max_attempts: 0", nil, "", 23, `max_attempts must be a whole number`},
		{"idle timeout", "idle_timeout: 1m=>idle_timeout: soon", nil, "", 16, `idle_timeout must be a positive duration`},
		{"executor without run", "stub: {run: [./adapters/stub, --quiet]}=>stub: {}", nil, "", 4, `missing key "run"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config := agentOrders
			if c.config != "" {
				old, repl, _ := strings.Cut(c.config, "=>")
				if !strings.Contains(config, old) {
					t.Fatalf("replacement %q not found", old)
				}
				config = strings.Replace(config, old, repl, 1)
			}
			path := agentFiles(t, config, c.files)
			_, err := Load(path)
			var ps Problems
			if !errors.As(err, &ps) {
				t.Fatalf("want problems, got %v", err)
			}
			want := path
			if c.file != "" {
				want = filepath.Join(filepath.Dir(path), filepath.FromSlash(c.file))
			}
			for _, p := range ps {
				if p.File == want && p.Line == c.line && strings.Contains(p.Msg, c.msg) {
					return
				}
			}
			t.Fatalf("want %s:%d %q, got:\n%v", c.file, c.line, c.msg, err)
		})
	}
}

func TestScribeSetting(t *testing.T) {
	base := "version: 0\nstart: a\nexecutors:\n  x: {run: [adapter]}\nnodes:\n  a: {type: script, run: [s], on: {ok: done, fail: failed}}\n"
	o, err := Parse("/x/orders.yaml", []byte(base+"scribe: {executor: x, model: m, effort: low, timeout: 2m}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s := o.Scribe; s == nil || s.Executor != "x" || s.Model != "m" || s.Effort != "low" || s.Timeout != 2*time.Minute {
		t.Fatalf("scribe %+v", o.Scribe)
	}
	if o, _ := Parse("/x/orders.yaml", []byte(base)); o.Scribe != nil {
		t.Fatal("a scribe setting without the key")
	}
	for config, want := range map[string]string{
		"scribe: {executor: y, model: m}\n": `scribe: executor "y" is not declared`,
		"scribe: {executor: x}\n":           `scribe: missing key "model" (the setup assistant has no default model)`,
		"scribe: {model: m, tools: [a]}\n":  `scribe: unknown key "tools"`,
	} {
		if _, err := Parse("/x/orders.yaml", []byte(base+config)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", config, err, want)
		}
	}
}

func TestParseOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultFile)
	config := "version: 0\nstart: a\nexecutors:\n  x: {run: [adapter]}\nnodes:\n  a: {type: agent, executor: x, prompt: p.md, schema: s.json, on: {ok: done, fail: failed}}\n"
	if _, err := Parse(path, []byte(config)); err == nil {
		t.Fatal("missing files accepted")
	}
	files := map[string][]byte{
		filepath.Join(dir, "p.md"):   []byte("Do it.\n"),
		filepath.Join(dir, "s.json"): []byte(`{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}}, "required": ["outcome"]}`),
	}
	o, err := ParseOverlay(path, []byte(config), files)
	if err != nil {
		t.Fatal(err)
	}
	if o.Nodes["a"].Prompt != filepath.Join(dir, "p.md") {
		t.Fatal(o.Nodes["a"].Prompt)
	}
}
