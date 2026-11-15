package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/teststep"
)

// A dispatcher killed between creating a campaign and recording it in the
// queue leaves the task starting. The next one links the campaign instead
// of creating a second one, and queues again a task whose campaign was
// never created.
func TestStartingTaskIsRecovered(t *testing.T) {
	dir := t.TempDir()
	flow := "version: 0\nflow: fix\nstart: a\nnodes:\n  a:\n    type: script\n    run: " + teststep.Argv("exit", "0") +
		"\n    on: {ok: done, fail: failed}\n"
	if err := os.WriteFile(filepath.Join(dir, "fix.yaml"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	routesFile := filepath.Join(dir, DefaultRoutesFile)
	if err := os.WriteFile(routesFile, []byte("version: 0\nroutes:\n  finding: {orders: fix.yaml}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	routes, _, err := LoadRoutes(routesFile)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, ".hetman"), 0o755)
	q, err := OpenQueue(QueuePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ch, err := chronicle.Open(filepath.Join(dir, ".hetman", "chronicle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()

	created, _, _ := q.Add("finding", []byte(`{}`), "", false)
	never, _, _ := q.Add("finding", []byte(`{}`), "", false)
	for _, id := range []int64{created.ID, never.ID} {
		if ok, err := q.claim(id); !ok || err != nil {
			t.Fatalf("claim %d: %v %v", id, ok, err)
		}
	}
	o, err := orders.Load(filepath.Join(dir, "fix.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	created.State = Starting
	input, _ := created.RunInput()
	if _, err := ch.NewCampaign(o.Flow, o.Path, o.Start, input, ""); err != nil {
		t.Fatal(err)
	}

	d := &Dispatcher{Routes: routes, Queue: q, Chron: ch, Once: true, Poll: 20 * time.Millisecond, Limit: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Run(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ := ch.Campaigns(10)
	if len(list) != 2 {
		t.Fatalf("%d campaigns, want 2", len(list))
	}
	a, _ := q.Get(created.ID)
	b, _ := q.Get(never.ID)
	if a.State != Done || a.Campaign != 1 || b.State != Done || b.Campaign != 2 {
		t.Fatalf("tasks after recovery: %+v %+v", a, b)
	}
}
