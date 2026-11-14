package orders

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const valid = `version: 0
flow: toy
start: build
checks:
  lint:  {run: [./checks/lint.sh], soft: true}
  tests: {run: [./checks/test.sh], timeout: 20m}
nodes:
  build:
    type: script
    run: [./steps/build.sh, --fast]
    timeout: 5m
    on: {ok: ready, fail: failed, timeout: failed}
  ready:
    type: gate
    checks: [lint, tests]
    on: {pass: publish, blocked: build}
  publish:
    type: gate
    run: [./steps/publish.sh]
    on: {pass: done, blocked: failed, retry: publish}
`

func TestValid(t *testing.T) {
	o, err := Parse("/x/orders.yaml", []byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if o.Flow != "toy" || o.Start != "build" || strings.Join(o.Order, ",") != "build,ready,publish" {
		t.Fatalf("parsed %+v", o)
	}
	b := o.Nodes["build"]
	if b.Timeout != 5*time.Minute || strings.Join(b.Run, " ") != "./steps/build.sh --fast" || b.On["ok"] != "ready" {
		t.Fatalf("build %+v", b)
	}
	if !o.Checks["lint"].Soft || o.Checks["tests"].Soft || o.Checks["tests"].Timeout != 20*time.Minute {
		t.Fatalf("checks %+v %+v", o.Checks["lint"], o.Checks["tests"])
	}
	if o.Nodes["publish"].On["retry"] != "publish" {
		t.Fatal("extra outcome from a result file must be kept")
	}
}

func TestExampleOrders(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "toy", DefaultFile)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestRejected(t *testing.T) {
	cases := []struct {
		name, config string
		line         int
		msg          string
	}{
		{"unknown node type", `version: 0
start: a
nodes:
  a:
    type: agent
    on: {ok: done}
`, 5, `unknown node type "agent"`},
		{"transition to missing node", `version: 0
start: a
nodes:
  a:
    type: script
    run: [x]
    on: {ok: b, fail: failed}
`, 7, `transition ok -> "b": no such node`},
		{"outcome without transition", `version: 0
start: a
nodes:
  a:
    type: script
    run: [x]
    on:
      ok: done
`, 8, `outcome "fail" has no transition`},
		{"timeout outcome without transition", `version: 0
start: a
nodes:
  a:
    type: script
    run: [x]
    timeout: 1m
    on: {ok: done, fail: failed}
`, 8, `outcome "timeout" has no transition`},
		{"missing start", `version: 0
nodes:
  a: {type: script, run: [x], on: {ok: done, fail: failed}}
`, 1, `missing key "start"`},
		{"start node does not exist", `version: 0
start: nope
nodes:
  a: {type: script, run: [x], on: {ok: done, fail: failed}}
`, 2, `start node "nope" does not exist`},
		{"undeclared check", `version: 0
start: g
checks:
  lint: {run: [x]}
nodes:
  g:
    type: gate
    checks: [lint, tests]
    on: {pass: done, blocked: failed}
`, 8, `check "tests" is not declared`},
		{"unknown top-level key", `version: 0
start: a
stages: []
nodes:
  a: {type: script, run: [x], on: {ok: done, fail: failed}}
`, 3, `unknown key "stages"`},
		{"unknown node key", `version: 0
start: a
nodes:
  a:
    type: script
    run: [x]
    tmeout: 5m
    on: {ok: done, fail: failed}
`, 7, `unknown key "tmeout"`},
		{"unknown check key", `version: 0
start: g
checks:
  lint: {run: [x], weak: true}
nodes:
  g: {type: gate, checks: [lint], on: {pass: done, blocked: failed}}
`, 4, `unknown key "weak"`},
		{"run as a string", `version: 0
start: a
nodes:
  a:
    type: script
    run: ./build.sh --fast
    on: {ok: done, fail: failed}
`, 6, `must be a list`},
		{"gate with run and checks", `version: 0
start: g
checks:
  lint: {run: [x]}
nodes:
  g:
    type: gate
    run: [x]
    checks: [lint]
    on: {pass: done, blocked: failed}
`, 9, `either run or checks`},
		{"checks gate with foreign outcome", `version: 0
start: g
checks:
  lint: {run: [x]}
nodes:
  g:
    type: gate
    checks: [lint]
    on: {pass: done, blocked: failed, ok: done}
`, 9, `only produces pass or blocked`},
		{"bad duration", `version: 0
start: a
nodes:
  a:
    type: script
    run: [x]
    timeout: 5 minutes
    on: {ok: done, fail: failed, timeout: failed}
`, 7, `positive duration`},
		{"duplicate key", `version: 0
start: a
nodes:
  a: {type: script, run: [x], on: {ok: done, fail: failed}}
  a: {type: script, run: [y], on: {ok: done, fail: failed}}
`, 5, `duplicate key "a"`},
		{"reserved node id", `version: 0
start: done
nodes:
  done: {type: script, run: [x], on: {ok: done, fail: failed}}
`, 4, `reserved`},
		{"wrong version", `version: 2
start: a
nodes:
  a: {type: script, run: [x], on: {ok: done, fail: failed}}
`, 0, `unsupported version`},
		{"yaml syntax", `version: 0
start: a
nodes:
  a: {type: script
  b: x
`, 3, `did not find expected`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse("orders.yaml", []byte(c.config))
			var ps Problems
			if !errors.As(err, &ps) {
				t.Fatalf("want problems, got %v", err)
			}
			for _, p := range ps {
				if strings.Contains(p.Msg, c.msg) && (c.line == 0 || p.Line == c.line) {
					return
				}
			}
			t.Fatalf("want line %d %q, got:\n%v", c.line, c.msg, err)
		})
	}
}

func TestProblemFormat(t *testing.T) {
	_, err := Parse("dir/orders.yaml", []byte("version: 0\nstart: a\nnodes:\n  a: {type: agent}\n"))
	if err == nil || !strings.HasPrefix(err.Error(), "dir/orders.yaml:4:") {
		t.Fatalf("got %v", err)
	}
}
