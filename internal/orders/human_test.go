package orders

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"hetman/internal/ask"
)

const humanOrders = `version: 0
start: write
notify: {run: [./notify.sh, --quiet], timeout: 10s}
executors:
  stub: {run: [./adapters/stub]}
checks:
  format: {run: [./format.sh]}
nodes:
  write:
    type: agent
    executor: stub
    prompt: prompts/write.md
    schema: schemas/write.json
    dialogue: 3
    on: {ok: approved, fail: failed}
  approved:
    type: gate
    checks: [format]
    ask:
      - id: scope
        question: Does it cover everything?
      - id: risk
        question: Can it be undone?
        kind: yesno
    show: [plan.md, docs/notes.md]
    on: {pass: release, blocked: write}
  release:
    type: human
    ask:
      - {id: channel, question: "Which channel?", kind: choice, options: [stable, beta]}
      - {id: notes, question: "Anything else?", kind: text}
    on: {ship: review, rework: write, stop: failed}
  review:
    type: agent
    executor: stub
    prompt: prompts/review.md
    schema: schemas/review.json
    on: {ok: done, needs_rework: write, fail: failed}
workdir: .
`

func TestHumanNodesAndQuestions(t *testing.T) {
	path := agentFiles(t, humanOrders, map[string]string{
		"prompts/review.md": "{{answers.release}} {{answers.release.channel}} {{answers.approved.risk.comment}} {{output.write.summary}}"})
	o, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if o.Notify == nil || !reflect.DeepEqual(o.Notify.Run, []string{"./notify.sh", "--quiet"}) || o.Notify.Timeout != 10*time.Second {
		t.Fatalf("notify %+v", o.Notify)
	}
	if w := o.Nodes["write"]; w.Dialogue != 3 {
		t.Fatalf("dialogue %d", w.Dialogue)
	}
	g := o.Nodes["approved"]
	if len(g.Ask) != 2 || g.Ask[0].Kind != ask.YesNo || g.Ask[1].ID != "risk" || !reflect.DeepEqual(g.Show, []string{"plan.md", "docs/notes.md"}) ||
		!reflect.DeepEqual(g.Outcomes(), []string{Pass, Blocked}) {
		t.Fatalf("gate %+v", g)
	}
	h := o.Nodes["release"]
	if !reflect.DeepEqual(h.Decisions, []string{"ship", "rework", "stop"}) || !reflect.DeepEqual(h.Outcomes(), h.Decisions) ||
		h.Ask[0].Kind != ask.Choice || !reflect.DeepEqual(h.Ask[0].Options, []string{"stable", "beta"}) || h.Ask[1].Kind != ask.Text {
		t.Fatalf("human %+v", h)
	}
	if r, err := o.Ref("answers.release.channel"); err != nil || r.Kind != RefAnswers || r.Node != "release" || r.Path[0] != "channel" {
		t.Fatalf("ref %+v %v", r, err)
	}
	errs, _ := o.CheckPrograms()
	if len(errs) == 0 || !strings.Contains(errs[0].Msg, "./notify.sh") || errs[0].Line != 3 {
		t.Fatalf("the hook program must be looked for: %v", errs)
	}
	// A gate may ask without checks.
	only := strings.Replace(humanOrders, "    checks: [format]\n", "", 1)
	if _, err := Load(agentFiles(t, only, map[string]string{"prompts/review.md": "Review."})); err != nil {
		t.Fatal(err)
	}
}

func TestHumanRejected(t *testing.T) {
	cases := []struct {
		name, replace string
		files         map[string]string
		file          string
		line          int
		msg           string
	}{
		{"ask on a script", "  review:\n    type: agent=>  extra: {type: script, run: [x], ask: [{id: a, question: b}], on: {ok: done, fail: failed}}\n  review:\n    type: agent", nil, "", 33, "ask is allowed only on a gate or a human node"},
		{"show on an agent", "    dialogue: 3=>    dialogue: 3\n    show: [x.md]", nil, "", 15, "show is allowed only on a gate or a human node"},
		{"gate with ask and run", "    checks: [format]=>    run: [./x]", nil, "", 20, "a gate with ask takes checks, not run"},
		{"gate asks a choice", "        kind: yesno=>        kind: choice", nil, "", 24, "a gate asks yes/no questions only"},
		{"unknown kind", "kind: text}=>kind: essay}", nil, "", 31, `kind must be one of yesno, choice, text, not "essay"`},
		{"choice without options", "kind: choice, options: [stable, beta]}=>kind: choice}", nil, "", 30, "a choice needs options"},
		{"one option", "options: [stable, beta]=>options: [stable]", nil, "", 30, "a choice needs at least two options"},
		{"options on text", "kind: text}=>kind: text, options: [a, b]}", nil, "", 31, "options are allowed only with kind choice"},
		{"duplicate id", "      - id: risk=>      - id: scope", nil, "", 22, `question id "scope" is used twice`},
		{"question without text", "        question: Can it be undone?\n=>", nil, "", 22, "a question needs the text of the question"},
		{"question without id", "      - id: risk\n        question=>      - question", nil, "", 22, "a question needs an id"},
		{"unknown question key", "        kind: yesno=>        kind: yesno\n        default: yes", nil, "", 25, `unknown key "default"`},
		{"show outside the folder", "show: [plan.md, docs/notes.md]=>show: [../secret.txt]", nil, "", 25, "must be a relative path inside the orders folder"},
		{"human with run", "    type: human=>    type: human\n    run: [x]", nil, "", 29, "a human node has no run"},
		{"human with timeout", "    type: human=>    type: human\n    timeout: 5m", nil, "", 29, "a human node has no timeout"},
		{"human without outcomes", "{ship: review, rework: write, stop: failed}=>{}", nil, "", 32, "a human node needs at least one outcome"},
		{"gate with ask and another outcome", "{pass: release, blocked: write}=>{pass: release, blocked: write, maybe: write}", nil, "", 26, "only produces pass or blocked"},
		{"dialogue out of range", "dialogue: 3=>dialogue: 0", nil, "", 14, "dialogue must be a whole number of question rounds from 1 to 100"},
		{"dialogue on a gate", "    checks: [format]=>    checks: [format]\n    dialogue: 2", nil, "", 19, "dialogue is allowed only on an agent node"},
		{"reserved outcome", "", map[string]string{"schemas/write.json": `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok", "questions"]}}, "required": ["outcome"]}`},
			"schemas/write.json", 1, `outcome "questions" is reserved`},
		{"reserved property", "", map[string]string{"schemas/write.json": `{"type": "object", "properties": {"outcome": {"type": "string", "enum": ["ok"]}, "questions": {"type": "string"}}, "required": ["outcome"]}`},
			"schemas/write.json", 1, `property "questions" is reserved`},
		{"notify without run", "notify: {run: [./notify.sh, --quiet], timeout: 10s}=>notify: {timeout: 10s}", nil, "", 3, `notify: missing key "run"`},
		{"notify bad timeout", "timeout: 10s}=>timeout: soon}", nil, "", 3, "notify: timeout must be a positive duration"},
		{"answers of a node that asks nothing", "", map[string]string{"prompts/review.md": "x\n{{answers.write}}"}, "prompts/review.md", 2, `node "write" asks a person nothing`},
		{"answers of an unknown question", "", map[string]string{"prompts/review.md": "{{answers.release.colour}}"}, "prompts/review.md", 1, `node "release" has no question "colour"`},
		{"answers with too many fields", "", map[string]string{"prompts/review.md": "{{answers.release.channel.x}}"}, "prompts/review.md", 1, "use {{answers.release}}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config := humanOrders
			if c.replace != "" {
				old, repl, _ := strings.Cut(c.replace, "=>")
				if !strings.Contains(config, old) {
					t.Fatalf("replacement %q not found", old)
				}
				config = strings.Replace(config, old, repl, 1)
			}
			files := map[string]string{"prompts/review.md": "Review."}
			for k, v := range c.files {
				files[k] = v
			}
			path := agentFiles(t, config, files)
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
