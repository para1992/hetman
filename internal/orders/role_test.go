package orders

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const roleOrders = `version: 0
start: write
executors:
  first: {run: [./first]}
  second: {run: [./second]}
roles:
  coder:
    select: quota
    clients:
      - {executor: first, model: big, effort: high, allowance: {five_hour: 40, weekly: 300}}
      - executor: second
        model: other
        allowance: {five_hour: 2h, monthly: 120h}
nodes:
  write:
    type: agent
    role: coder
    prompt: prompts/write.md
    schema: schemas/write.json
    on: {ok: done, fail: failed}
`

func TestRoles(t *testing.T) {
	o, err := Load(agentFiles(t, roleOrders, nil))
	if err != nil {
		t.Fatal(err)
	}
	r := o.Roles["coder"]
	if r == nil || r.Select != SelectQuota || len(r.Clients) != 2 {
		t.Fatalf("role %+v", r)
	}
	a, b := r.Clients[0], r.Clients[1]
	if a.Executor != "first" || a.Model != "big" || a.Effort != "high" || a.Allowance[WindowFiveHour].Attempts != 40 || a.Allowance[WindowWeekly].Attempts != 300 {
		t.Fatalf("first client %+v", a)
	}
	w := b.Allowance[WindowWeekly]
	if b.Allowance[WindowFiveHour].Work != 2*time.Hour || !w.Monthly || w.Work.Round(time.Minute) != (27*time.Hour+42*time.Minute) || w.Text != "120h/monthly" {
		t.Fatalf("second client %+v", b)
	}
	if n := o.Nodes["write"]; n.Role != "coder" || n.Executor != "" || n.Model != "" {
		t.Fatalf("node %+v", n)
	}
	if got := a.Label(0); got != "1 (first, big)" {
		t.Fatalf("label %q", got)
	}
}

func TestRolesRejected(t *testing.T) {
	cases := []struct {
		name   string
		config string // old=>new applied to roleOrders
		line   int
		msg    string
	}{
		{"role and executor", "    role: coder\n=>    role: coder\n    executor: first\n", 18, `a node names either a role or an executor, not both`},
		{"role and model", "    role: coder\n=>    role: coder\n    model: big\n", 18, `model is given by each client of role "coder"`},
		{"neither role nor executor", "    role: coder\n=>", 15, `missing key "executor" (or "role"`},
		{"undeclared role", "role: coder=>role: tester", 17, `role "tester" is not declared under the top-level roles`},
		{"role on a script", "    type: agent\n    role: coder\n    prompt: prompts/write.md\n    schema: schemas/write.json\n=>    type: script\n    run: [./x]\n    role: coder\n", 18, `role is allowed only on an agent node`},
		{"missing select", "    select: quota\n=>", 7, `missing key "select"`},
		{"unknown select", "select: quota=>select: cheapest", 8, `unknown select "cheapest" (known: fixed, round_robin, quota)`},
		{"missing clients", "    select: quota\n    clients:\n      - {executor: first, model: big, effort: high, allowance: {five_hour: 40, weekly: 300}}\n      - executor: second\n        model: other\n        allowance: {five_hour: 2h, monthly: 120h}\n=>    select: quota\n", 7, `missing key "clients"`},
		{"empty clients", "    clients:\n      - {executor: first, model: big, effort: high, allowance: {five_hour: 40, weekly: 300}}\n      - executor: second\n        model: other\n        allowance: {five_hour: 2h, monthly: 120h}\n=>    clients: []\n", 9, `clients must be a non-empty list`},
		{"undeclared executor", "{executor: first, model: big=>{executor: third, model: big", 10, `client 1: executor "third" is not declared`},
		{"client without executor", "      - executor: second\n        model: other\n=>      - model: other\n", 11, `client 2: missing key "executor"`},
		{"unknown client key", "effort: high,=>effort: high, tools: [x],", 10, `unknown key "tools"`},
		{"weekly and monthly", "{five_hour: 2h, monthly: 120h}=>{weekly: 10, monthly: 120h}", 13, `give either weekly or monthly`},
		{"bad allowance", "five_hour: 40,=>five_hour: soon,", 10, `five_hour must be a positive number of attempts or a duration`},
		{"zero allowance", "five_hour: 40,=>five_hour: 0,", 10, `five_hour must be a positive number`},
		{"unknown window", "five_hour: 40,=>daily: 40,", 10, `unknown key "daily"`},
		{"repeated client", "      - executor: second\n        model: other\n        allowance: {five_hour: 2h, monthly: 120h}\n=>      - {executor: first, model: big, effort: high}\n", 11, `client 2 repeats client 1`},
		{"bad role name", "  coder:\n=>  'coder x':\n", 7, `role name "coder x"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			old, repl, _ := strings.Cut(c.config, "=>")
			if !strings.Contains(roleOrders, old) {
				t.Fatalf("replacement %q not found", old)
			}
			_, err := Load(agentFiles(t, strings.Replace(roleOrders, old, repl, 1), nil))
			var ps Problems
			if !errors.As(err, &ps) {
				t.Fatalf("want problems, got %v", err)
			}
			for _, p := range ps {
				if p.Line == c.line && strings.Contains(p.Msg, c.msg) {
					return
				}
			}
			t.Fatalf("want line %d %q, got:\n%v", c.line, c.msg, err)
		})
	}
}
