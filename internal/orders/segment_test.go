package orders

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const segmentFile = `version: 0
flow: item
start: work
nodes:
  work: {type: script, run: [./work.sh], locks: [z-stand], on: {ok: done, fail: failed}}
`

// segmentDir writes files into a new folder and returns it.
func segmentDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSegmentAndForEach(t *testing.T) {
	dir := segmentDir(t, map[string]string{"segs/item.yaml": segmentFile, "orders.yaml": `version: 0
start: plan
executors:
  x: {run: [./x]}
checks:
  unit: {run: [./unit.sh], locks: [stand]}
nodes:
  plan: {type: agent, executor: x, prompt: p.md, schema: s.json, on: {ok: one, fail: failed}}
  one:
    type: segment
    segment: segs/item.yaml
    input: output.plan.task
    locks: [a-writer]
    on: {done: each, failed: failed}
  each:
    type: foreach
    segment: segs/item.yaml
    items: output.plan.items
    parallel: 3
    on: {done: g, failed: plan}
  g: {type: gate, checks: [unit], locks: [copy], on: {pass: done, blocked: failed}}
workdir: .
`, "p.md": "plan", "s.json": `{"type":"object","properties":{"outcome":{"type":"string","enum":["ok"]}},"required":["outcome"]}`})
	o, err := Load(filepath.Join(dir, "orders.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	one, each := o.Nodes["one"], o.Nodes["each"]
	if one.Segment == nil || one.Segment.Flow != "item" || one.InputRef.Kind != RefOutput || one.InputRef.Node != "plan" ||
		!reflect.DeepEqual(one.Locks, []string{"a-writer"}) || !reflect.DeepEqual(one.Outcomes(), []string{Done, Failed}) {
		t.Fatalf("segment node %+v", one)
	}
	if each.Parallel != 3 || each.ItemsRef.Kind != RefOutput || !reflect.DeepEqual(each.ItemsRef.Path, []string{"items"}) ||
		each.SegmentPath != filepath.Join(dir, "segs", "item.yaml") {
		t.Fatalf("foreach node %+v", each)
	}
	if got := o.LocksOf(o.Nodes["g"]); !reflect.DeepEqual(got, []string{"copy", "stand"}) {
		t.Fatalf("locks of the gate %v", got)
	}
	if got := o.AllLocks(); !reflect.DeepEqual(got, []string{"a-writer", "copy", "stand", "z-stand"}) {
		t.Fatalf("all locks %v", got)
	}
	// The programs of a segment are looked for too, from its own folder.
	errs, _ := o.CheckPrograms()
	found := false
	for _, p := range errs {
		found = found || (p.File == filepath.Join(dir, "segs", "item.yaml") && strings.Contains(p.Msg, "./work.sh"))
	}
	if !found {
		t.Fatalf("program of the segment not checked: %v", errs)
	}
}

func TestSegmentRejected(t *testing.T) {
	head := "version: 0\nstart: a\nnodes:\n"
	cases := []struct {
		name  string
		nodes string
		files map[string]string
		file  string // the file the problem is in; empty for the orders
		line  int
		msg   string
	}{
		{"runs itself", "  a: {type: segment, segment: b.yaml, on: {done: done, failed: failed}}\n",
			map[string]string{"b.yaml": "version: 0\nstart: x\nnodes:\n  x: {type: segment, segment: orders.yaml, on: {done: done, failed: failed}}\n"},
			"b.yaml", 4, "runs itself: orders.yaml -> b.yaml -> orders.yaml"},
		{"missing file", "  a: {type: segment, segment: none.yaml, on: {done: done, failed: failed}}\n", nil,
			"", 4, `segment file "none.yaml" does not exist`},
		{"problem in the segment", "  a: {type: segment, segment: b.yaml, on: {done: done, failed: failed}}\n",
			map[string]string{"b.yaml": "version: 0\nstart: x\nnodes:\n  x: {type: script, on: {ok: done, fail: failed}}\n"},
			"b.yaml", 4, `missing key "run"`},
		{"problem in the segment named at the node", "  a: {type: segment, segment: b.yaml, on: {done: done, failed: failed}}\n",
			map[string]string{"b.yaml": "version: 0\nstart: x\nnodes:\n  x: {type: script, on: {ok: done, fail: failed}}\n"},
			"", 4, `segment "b.yaml" is rejected: 1 problem(s)`},
		{"missing segment", "  a: {type: segment, on: {done: done, failed: failed}}\n", nil, "", 4, `missing key "segment"`},
		{"items on a segment", "  a: {type: segment, segment: s.yaml, items: input.x, on: {done: done, failed: failed}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 4, "items is allowed only on a foreach node"},
		{"input on a foreach", "  a: {type: foreach, segment: s.yaml, items: input.x, input: input, on: {done: done, failed: failed}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 4, "input is allowed only on a segment node"},
		{"foreach without items", "  a: {type: foreach, segment: s.yaml, on: {done: done, failed: failed}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 4, `missing key "items"`},
		{"parallel out of range", "  a: {type: foreach, segment: s.yaml, items: input.x, parallel: 0, on: {done: done, failed: failed}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 4, "parallel must be a whole number from 1 to 64"},
		{"run on a segment", "  a: {type: segment, segment: s.yaml, run: [x], on: {done: done, failed: failed}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 4, "a segment node has no run"},
		{"other outcome", "  a: {type: segment, segment: s.yaml, on: {done: done, failed: failed, ok: done}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 4, `outcome "ok" can never happen`},
		{"missing failed", "  a: {type: segment, segment: s.yaml, on: {done: done}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 4, `outcome "failed" has no transition`},
		{"items of a script output", "  b: {type: script, run: [x], on: {ok: a, fail: failed}}\n  a: {type: foreach, segment: s.yaml, items: output.b.list, on: {done: done, failed: failed}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 5, "only agent nodes have an output"},
		{"input of a kind that is no value", "  a: {type: segment, segment: s.yaml, input: rework, on: {done: done, failed: failed}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 4, "input must be input[.FIELD], output.NODE[.FIELD] or artifact.NODE.FILE"},
		{"segment uses a lock its node holds", "  a: {type: segment, segment: s.yaml, locks: [z-stand], on: {done: done, failed: failed}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 4, `its segment takes the lock "z-stand" while this node holds "z-stand"`},
		{"segment uses a lock that sorts first", "  a: {type: segment, segment: s.yaml, locks: [zz], on: {done: done, failed: failed}}\n",
			map[string]string{"s.yaml": segmentFile}, "", 4, "must sort after the locks of the node that runs it"},
		{"lock listed twice", "  a: {type: script, run: [x], locks: [s, s], on: {ok: done, fail: failed}}\n", nil, "", 4, `lock "s" is listed twice`},
		{"lock name", "  a: {type: script, run: [x], locks: [a b], on: {ok: done, fail: failed}}\n", nil, "", 4, "use letters, digits"},
		{"empty locks", "  a: {type: script, run: [x], locks: [], on: {ok: done, fail: failed}}\n", nil, "", 4, "locks must be a non-empty list"},
		{"segment key on a script", "  a: {type: script, run: [x], segment: s.yaml, on: {ok: done, fail: failed}}\n", nil, "", 4,
			"segment is allowed only on a segment or a foreach node"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"orders.yaml": head + c.nodes}
			for k, v := range c.files {
				files[k] = v
			}
			dir := segmentDir(t, files)
			_, err := Load(filepath.Join(dir, "orders.yaml"))
			var ps Problems
			if !errors.As(err, &ps) {
				t.Fatalf("want problems, got %v", err)
			}
			want := filepath.Join(dir, "orders.yaml")
			if c.file != "" {
				want = filepath.Join(dir, c.file)
			}
			for _, p := range ps {
				if strings.Contains(p.Msg, c.msg) && p.Line == c.line && p.File == want {
					return
				}
			}
			t.Fatalf("want %s:%d %q, got:\n%v", want, c.line, c.msg, err)
		})
	}
}

func TestCheckLocks(t *testing.T) {
	_, err := Parse("orders.yaml", []byte(`version: 0
start: g
checks:
  unit: {run: [x], locks: [stand, stand]}
nodes:
  g: {type: gate, checks: [unit], on: {pass: done, blocked: failed}}
`))
	if err == nil || !strings.Contains(err.Error(), `orders.yaml:4:`) || !strings.Contains(err.Error(), `lock "stand" is listed twice`) {
		t.Fatalf("got %v", err)
	}
}
