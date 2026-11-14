package watchtower_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/watchtower"
)

// answer posts an answer the way the page does and returns the status and
// the result or error.
func (w *world) answer(id int, body string, header map[string]string) (int, string) {
	w.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, w.url+"/api/campaigns/"+string(rune('0'+id))+"/answer", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var res struct{ Result, Error string }
	json.Unmarshal(data, &res)
	if res.Result != "" {
		return resp.StatusCode, res.Result
	}
	if res.Error != "" {
		return resp.StatusCode, res.Error
	}
	return resp.StatusCode, strings.TrimSpace(string(data))
}

// waitAsking waits for the first campaign to exist and ask a person.
func (w *world) waitAsking() watchtower.Campaign {
	w.t.Helper()
	for i := 0; i < 1500 && w.get("/api/campaigns/1", nil) != http.StatusOK; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	return w.waitState(1, watchtower.StateAsking)
}

func (w *world) eventCount() int {
	events, _ := w.chron.Events(1)
	return len(events)
}

const askingOrders = `version: 0
start: a
nodes:
  a: {type: script, run: STEP, on: {ok: g, fail: failed}}
  g:
    type: gate
    ask:
      - {id: scope, question: "Is it all there?"}
    show: [notes.md, missing.md]
    on: {pass: h, blocked: failed}
  h:
    type: human
    ask:
      - {id: channel, question: "Which channel?", kind: choice, options: [stable, beta]}
      - {id: notes, question: "Anything else?", kind: text}
    on: {ship: done, stop: failed}
`

func TestAnswerFromThePage(t *testing.T) {
	w := newWorld(t, strings.Replace(askingOrders, "STEP", arg("exit", "0"), 1))
	os.WriteFile(filepath.Join(w.dir, "notes.md"), []byte("# Notes\n"), 0o644)
	runner, out := w.process("run")
	c := w.waitAsking()
	q := c.Ask
	if q == nil || q.Node != "g" || q.Reason != chronicle.ReasonGate || !c.Driven || !strings.Contains(q.Command, "hetman answer -f ") ||
		!strings.HasSuffix(q.Example, "-ask "+itoa(q.Seq)+" -a scope=yes") {
		t.Fatalf("open question %+v", q)
	}
	if g := find(c.Columns, "g"); g.Status != watchtower.StatusAsking || c.Nodes["g"][0].Status != watchtower.StatusAsking || c.Nodes["g"][0].Ask.Seq != q.Seq {
		t.Fatalf("column g %+v, attempt %+v", g, c.Nodes["g"][0])
	}
	// The files to look at are readable, and nothing else is.
	resp, err := http.Get(w.url + "/api/campaigns/1/show?ask=" + itoa(q.Seq) + "&i=0")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(data) != "# Notes\n" || resp.Header.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
		t.Fatalf("show: %d %q", resp.StatusCode, data)
	}
	for _, bad := range []string{"&i=1", "&i=2", "&i=-1"} {
		if code := w.get("/api/campaigns/1/show?ask="+itoa(q.Seq)+bad, nil); code != http.StatusNotFound {
			t.Fatalf("show %s: %d", bad, code)
		}
	}
	if code := w.get("/api/campaigns/1/show?ask=1&i=0", nil); code != http.StatusNotFound {
		t.Fatalf("show of an event that is not a question: %d", code)
	}

	// Answers that do not fit are refused and change nothing.
	before := w.eventCount()
	for body, want := range map[string]string{
		`{"ask": ` + itoa(q.Seq) + `, "values": {}}`:                       "question scope has no answer",
		`{"ask": ` + itoa(q.Seq) + `, "values": {"scope": "perhaps"}}`:     "answer yes or no",
		`{"ask": ` + itoa(q.Seq+50) + `, "values": {"scope": "yes"}}`:      "another question is open now",
		`{"ask": ` + itoa(q.Seq) + `, "values": {"scope": "yes"}, "x": 1}`: "the answer must be a JSON object",
		`not json`: "the answer must be a JSON object",
	} {
		code, msg := w.answer(1, body, pageHeaders)
		if (code != http.StatusBadRequest && code != http.StatusConflict) || !strings.Contains(msg, want) {
			t.Fatalf("answer %s: %d %q, want %q", body, code, msg, want)
		}
	}
	if n := w.eventCount(); n != before {
		t.Fatalf("a refused answer changed the chronicle: %d events, was %d", n, before)
	}

	// The process that runs the campaign continues it.
	if code, res := w.answer(1, `{"ask": `+itoa(q.Seq)+`, "values": {"scope": "yes"}, "comments": {"scope": "checked"}, "by": "pat"}`, pageHeaders); code != 200 || res != watchtower.ResumeElsewhere {
		t.Fatalf("answer: %d %s", code, res)
	}
	c = w.waitState(1, watchtower.StateAsking)
	for c.Ask == nil || c.Ask.Node != "h" {
		c = w.waitState(1, watchtower.StateAsking)
	}
	if a := c.Nodes["g"][0].Ask; a.By != "pat" || a.Via != "page" || a.AnsweredAt == nil || a.Answers[0].Comment != "checked" || c.Nodes["g"][0].Outcome != "pass" {
		t.Fatalf("answered gate %+v", c.Nodes["g"][0])
	}
	if o := c.Ask.Outcomes; len(o) != 2 || o[0].Name != "ship" || o[0].To != "done" {
		t.Fatalf("outcomes %+v", o)
	}

	// With no process left, the server drives the campaign after the answer.
	if err := runner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	runner.Wait()
	for c = w.campaign(1); c.Driven; c = w.campaign(1) {
	}
	if c.State != watchtower.StateAsking {
		t.Fatalf("after the kill: %s\n%s", c.State, out)
	}
	if code, msg := w.answer(1, `{"values": {"channel": "beta", "notes": "thanks"}}`, pageHeaders); code != http.StatusBadRequest || !strings.Contains(msg, "choose an outcome") {
		t.Fatalf("answer without an outcome: %d %s", code, msg)
	}
	if code, res := w.answer(1, `{"values": {"channel": "beta", "notes": "thanks"}, "outcome": "ship"}`, pageHeaders); code != 200 || res != watchtower.ResumeDriving {
		t.Fatalf("answer: %d %s", code, res)
	}
	c = w.waitState(1, watchtower.StateDone)
	if a := c.Nodes["h"][0]; a.Outcome != "ship" || a.Ask.Outcome != "ship" || a.Ask.Answers[1].Value != "thanks" {
		t.Fatalf("answered human node %+v", a)
	}
	if code, msg := w.answer(1, `{"values": {}}`, pageHeaders); code != http.StatusConflict {
		t.Fatalf("answer after the end: %d %s", code, msg)
	}
}

func TestCrossSiteAnswerRefused(t *testing.T) {
	w := newWorld(t, strings.Replace(askingOrders, "STEP", arg("exit", "0"), 1))
	runner, _ := w.process("run")
	q := w.waitAsking().Ask
	before := w.eventCount()
	body := `{"ask": ` + itoa(q.Seq) + `, "values": {"scope": "yes"}}`
	for name, hd := range map[string]map[string]string{
		"cross-site form":            {"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site", "Content-Type": "text/plain"},
		"cross-site with header":     {"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site", watchtower.ControlHeader: "1"},
		"foreign origin, no headers": {"Origin": "http://evil.example", watchtower.ControlHeader: "1"},
		"no control header":          {"Sec-Fetch-Site": "same-origin"},
	} {
		if code, _ := w.answer(1, body, hd); code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, code)
		}
	}
	if n := w.eventCount(); n != before {
		t.Fatalf("a refused answer changed the chronicle: %d events, was %d", n, before)
	}
	if c := w.campaign(1); c.State != watchtower.StateAsking || c.Ask.Seq != q.Seq {
		t.Fatalf("state after refused answers: %s", c.State)
	}
	runner.Process.Kill()
	runner.Wait()
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
