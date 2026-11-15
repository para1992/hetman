package watchtower_test

import (
	"io"
	"net/http"
	"testing"
)

// A file of show in the campaign's work folder is read from the folder of
// the campaign that asks.
func TestShowFromTheWorkFolder(t *testing.T) {
	w := newWorld(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("write", "plan.md", "the plan")+`, on: {ok: g, fail: failed}}
  g:
    type: gate
    ask:
      - {id: ok, question: "Is the plan good?"}
    show: ["{{work}}/plan.md", "{{work}}/missing.md"]
    on: {pass: done, blocked: failed}
`)
	w.process("run")
	q := w.waitAsking().Ask
	resp, err := http.Get(w.url + "/api/campaigns/1/show?ask=" + itoa(q.Seq) + "&i=0")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(data) != "the plan" {
		t.Fatalf("show: %d %q", resp.StatusCode, data)
	}
	if code := w.get("/api/campaigns/1/show?ask="+itoa(q.Seq)+"&i=1", nil); code != http.StatusNotFound {
		t.Fatalf("a missing file: %d", code)
	}
}
