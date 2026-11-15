package orders

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const reachOrders = `version: 0
workdir: ..
start: write
executors:
  stub: {run: [./adapters/stub]}
checks:
  unit: {run: [./unit.sh]}
nodes:
  write:
    type: agent
    executor: stub
    prompt: prompts/write.md
    schema: schemas/write.json
    on: {ok: tests, fail: failed}
  tests:
    type: gate
    checks: [unit]
    on: {pass: review, blocked: {to: write, max: 3, then: stuck}, stuck: failed}
  review:
    type: agent
    executor: stub
    prompt: prompts/review.md
    schema: schemas/review.json
    workdir: .
    on: {ok: done, needs_rework: write, fail: failed, cannot_proceed: failed}
  ask:
    type: human
    show: [notes.md, "{{work}}/plan.md"]
    on: {go: done}
`

func loadReach(t *testing.T, config string, files map[string]string) (*Orders, error) {
	t.Helper()
	return Load(agentFiles(t, config, files))
}

func TestWorkdirLimitsShowAndUnable(t *testing.T) {
	o, err := loadReach(t, reachOrders, map[string]string{"prompts/review.md": "Read {{work}}/plan.md.\n{{rework}}"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Workdir != filepath.Dir(o.Dir) || o.Nodes["write"].Workdir != filepath.Dir(o.Dir) || o.Nodes["review"].Workdir != o.Dir {
		t.Fatalf("workdirs %q %q %q", o.Workdir, o.Nodes["write"].Workdir, o.Nodes["review"].Workdir)
	}
	if lim := o.Nodes["tests"].Limits["blocked"]; lim.Max != 3 || lim.Then != "stuck" || o.Nodes["tests"].On["blocked"] != "write" {
		t.Fatalf("limit %+v on %v", lim, o.Nodes["tests"].On)
	}
	if got := strings.Join(o.Nodes["ask"].Show, " "); got != "notes.md {{work}}/plan.md" {
		t.Fatalf("show %q", got)
	}
	files := strings.Join(o.Files(), "\n")
	for _, want := range []string{o.Path, filepath.Join(o.Dir, "prompts", "write.md"), filepath.Join(o.Dir, "schemas", "review.json"),
		filepath.Join(o.Dir, "unit.sh"), filepath.Join(o.Dir, "adapters", "stub")} {
		if !strings.Contains(files, want) {
			t.Errorf("files lack %s:\n%s", want, files)
		}
	}
	if line, _ := Position(o.Path, "review", "workdir", -1); line != 24 {
		t.Errorf("position of review's workdir: line %d", line)
	}
}

func TestReachRejected(t *testing.T) {
	cases := []struct {
		name, old, new string
		files          map[string]string
		msg            string
	}{
		{"no workdir anywhere", "workdir: ..\n", "", nil, `node "write": missing key "workdir"`},
		{"workdir in the state folder", "workdir: ..\n", "workdir: .hetman/x\n", nil, "closed to agents"},
		{"workdir on a gate", "    checks: [unit]\n", "    checks: [unit]\n    workdir: .\n", nil, "workdir is allowed only on an agent node"},
		{"unbalanced tools", "    workdir: .\n", "    workdir: .\n    tools: [Read, \"Bash(make\", \"-C\", \"*)\"]\n", nil, `tools item "Bash(make": an opening parenthesis without a closing one`},
		{"closing without opening", "    workdir: .\n", "    workdir: .\n    tools: [\"*)\"]\n", nil, "a closing parenthesis without an opening one"},
		{"max out of range", "max: 3", "max: 0", nil, "max must be a whole number from 1 to 100"},
		{"then without transition", ", stuck: failed}", "}", nil, `then names "stuck", which has no transition`},
		{"limit without then", ", then: stuck}", "}", nil, `missing key "then"`},
		{"then is the outcome itself", "then: stuck}", "then: blocked}", nil, "then must name another outcome"},
		{"limits in a circle", "stuck: failed}", "stuck: {to: failed, max: 1, then: blocked}}", nil, "name each other in a circle"},
		{"show leaving the work folder", `"{{work}}/plan.md"`, `"{{work}}/../plan.md"`, nil, "must name a file inside the campaign's work folder"},
		{"show with another placeholder", `"{{work}}/plan.md"`, `"{{input}}/plan.md"`, nil, "must be a relative path inside the orders folder"},
		{"reserved outcome in a schema", "", "", map[string]string{"schemas/write.json": strings.Replace(writeSchema, `["ok"]`, `["ok", "cannot_proceed"]`, 1)}, `outcome "cannot_proceed" is reserved`},
		{"work placeholder with a field", "", "", map[string]string{"prompts/write.md": "{{work.plan}}"}, "work takes no fields"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			config := reachOrders
			if c.old != "" {
				if !strings.Contains(config, c.old) {
					t.Fatalf("%q not found", c.old)
				}
				config = strings.Replace(config, c.old, c.new, 1)
			}
			_, err := loadReach(t, config, c.files)
			var ps Problems
			if !errors.As(err, &ps) || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("want %q, got %v", c.msg, err)
			}
		})
	}
}

func TestToolProblem(t *testing.T) {
	for _, ok := range []string{"read", "Bash(go test *)", "Bash(make (x) *)", "Edit(src/**)"} {
		if msg := ToolProblem(ok); msg != "" {
			t.Errorf("%s: %s", ok, msg)
		}
	}
	for _, bad := range []string{"", " ", "Bash(make", "*)"} {
		if ToolProblem(bad) == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}
