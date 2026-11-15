package watchtower_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hetman/internal/dispatch"
	"hetman/internal/watchtower"
)

func TestTasksView(t *testing.T) {
	w := newWorld(t, `version: 0
flow: quick
start: a
nodes:
  a: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	var none []dispatch.View
	if code := w.get("/api/tasks", &none); code != http.StatusOK || none == nil || len(none) != 0 {
		t.Fatalf("tasks without a queue: %d %v", code, none)
	}
	list := w.stream("/api/stream")
	list.until("list", func(d string) bool { return d == "[]" })

	// A second flow in the same folder, and routes to both.
	remark := `version: 0
flow: remark
start: decide
nodes:
  decide:
    type: human
    ask:
      - id: how
        question: What should be done?
        kind: text
    on: {fix: a, decline: done}
  a: {type: script, run: ` + arg("exit", "0") + `, on: {ok: done, fail: failed}}
`
	if err := os.WriteFile(filepath.Join(w.dir, "remark.yaml"), []byte(remark), 0o644); err != nil {
		t.Fatal(err)
	}
	routesFile := filepath.Join(w.dir, dispatch.DefaultRoutesFile)
	if err := os.WriteFile(routesFile, []byte("version: 0\nroutes:\n  finding: {orders: orders.yaml}\n  remark: {orders: remark.yaml}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	routes, _, err := dispatch.LoadRoutes(routesFile)
	if err != nil {
		t.Fatal(err)
	}
	q, err := dispatch.OpenQueue(dispatch.QueuePath(w.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	finding, _, _ := q.Add("finding", []byte(`{}`), "lint:1", false)
	rem, _, _ := q.Add("remark", []byte(`{}`), "", false)
	queued, _, _ := q.Add("finding", []byte(`{}`), "", false)
	if _, err := q.Cancel(queued.ID); err != nil {
		t.Fatal(err)
	}
	// The page shows the queue as soon as it exists.
	list.until("tasks", func(d string) bool {
		var v []dispatch.View
		json.Unmarshal([]byte(d), &v)
		return len(v) == 3 && v[0].State == dispatch.StateCancelled && v[2].State == dispatch.StateQueued
	})

	d := &dispatch.Dispatcher{Routes: routes, Queue: q, Chron: w.chron, Once: true, Poll: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Run(ctx); err != nil {
		t.Fatal(err)
	}
	data := list.until("tasks", func(d string) bool {
		var v []dispatch.View
		json.Unmarshal([]byte(d), &v)
		return len(v) == 3 && v[1].State == dispatch.StateWaiting && v[2].State == dispatch.StateDone
	})
	var views []dispatch.View
	json.Unmarshal([]byte(data), &views)
	byID := map[int64]dispatch.View{}
	for _, v := range views {
		byID[v.ID] = v
	}
	f, r := byID[finding.ID], byID[rem.ID]
	if f.Campaign != 1 || f.Flow != "quick" || f.Key != "lint:1" || f.Kind != "finding" || f.Added.IsZero() || f.Ended == nil {
		t.Fatalf("finding task: %+v", f)
	}
	if r.Campaign != 2 || r.Flow != "remark" || r.Waits != "a person to decide at decide (fix, decline)" || r.Driven || r.Input != nil {
		t.Fatalf("remark task: %+v", r)
	}

	// The campaign of the other flow is shown and answered with its own
	// orders, and the server drives it on.
	c := w.campaign(2)
	if c.State != watchtower.StateAsking || find(c.Columns, "decide") == nil {
		t.Fatalf("campaign 2 is %s with columns %+v", c.State, c.Columns)
	}
	if code, res := w.answer(2, `{"values": {"how": "fix it"}, "outcome": "fix"}`, pageHeaders); code != http.StatusOK || res != watchtower.ResumeDriving {
		t.Fatalf("answer: %d %s", code, res)
	}
	list.until("tasks", func(d string) bool {
		var v []dispatch.View
		json.Unmarshal([]byte(d), &v)
		return len(v) == 3 && v[1].State == dispatch.StateDone
	})
	var final []dispatch.View
	w.get("/api/tasks", &final)
	if len(final) != 3 || final[1].State != dispatch.StateDone || final[1].Campaign != 2 {
		t.Fatalf("tasks at the end: %+v", final)
	}
}
