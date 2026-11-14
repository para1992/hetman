package watchtower_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/cli"
	"hetman/internal/engine"
	"hetman/internal/orders"
	"hetman/internal/teststep"
	"hetman/internal/watchtower"
)

// The test binary doubles as the hetman program ("cli ...") and as step
// programs ("step ..."), so a campaign can be driven by a real separate
// process while the server under test watches it.
func TestMain(m *testing.M) {
	if os.Getenv(teststep.EnvHelper) == "1" && len(os.Args) > 1 {
		switch os.Args[1] {
		case "cli":
			os.Exit(cli.Main(os.Args[2:], os.Stdout, os.Stderr))
		case "step":
			os.Exit(teststep.Main(os.Args[2:]))
		}
	}
	os.Setenv(teststep.EnvHelper, "1")
	os.Exit(m.Run())
}

var arg = teststep.Argv

type world struct {
	t      *testing.T
	dir    string
	orders string
	chron  *chronicle.Chronicle
	url    string
	srv    *watchtower.Server
}

// newWorld writes orders whose text may refer to the folders SYNC1 and
// SYNC2 for block steps, opens the chronicle and starts a server.
func newWorld(t *testing.T, config string) *world {
	dir, err := os.MkdirTemp("", "hetman-watchtower-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 50 && os.RemoveAll(dir) != nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
	})
	w := &world{t: t, dir: dir, orders: filepath.Join(dir, orders.DefaultFile)}
	for _, s := range []string{"SYNC1", "SYNC2"} {
		p := filepath.Join(dir, strings.ToLower(s))
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		config = strings.ReplaceAll(config, s, strings.ReplaceAll(p, "'", "''"))
	}
	if err := os.WriteFile(w.orders, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := orders.Load(w.orders)
	if err != nil {
		t.Fatalf("orders rejected: %v\n%s", err, config)
	}
	if err := os.MkdirAll(engine.StateDir(o), 0o755); err != nil {
		t.Fatal(err)
	}
	w.chron, err = chronicle.Open(engine.ChroniclePath(o))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.chron.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	w.srv = watchtower.New(ctx, o, w.chron)
	w.srv.Poll = 50 * time.Millisecond
	ts := httptest.NewServer(w.srv.Handler())
	w.url = ts.URL
	t.Cleanup(func() {
		cancel()
		w.srv.Wait()
		ts.Close()
	})
	return w
}

func (w *world) sync(n int) string { return filepath.Join(w.dir, "sync"+string(rune('0'+n))) }

func (w *world) release(n int) {
	if err := os.WriteFile(filepath.Join(w.sync(n), "release"), nil, 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) waitFile(path string) {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting for %s\n%s", path, w.dump())
}

// process starts hetman as a separate process.
func (w *world) process(args ...string) (*exec.Cmd, *bytes.Buffer) {
	self, err := os.Executable()
	if err != nil {
		w.t.Fatal(err)
	}
	cmd := exec.Command(self, append([]string{"cli"}, append(args, "-f", w.orders)...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return cmd, &out
}

func (w *world) dump() string {
	var b strings.Builder
	events, _ := w.chron.Events(1)
	for _, e := range events {
		b.WriteString(e.Kind + " " + e.Node + " " + e.Check + " " + e.Outcome + " " + e.Error + "\n")
	}
	return b.String()
}

func (w *world) get(path string, v any) int {
	w.t.Helper()
	resp, err := http.Get(w.url + path)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if v != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, v); err != nil {
			w.t.Fatalf("GET %s: %v\n%s", path, err, data)
		}
	}
	return resp.StatusCode
}

func (w *world) campaign(id int) watchtower.Campaign {
	w.t.Helper()
	var c watchtower.Campaign
	if code := w.get("/api/campaigns/"+string(rune('0'+id)), &c); code != http.StatusOK {
		w.t.Fatalf("campaign %d: status %d", id, code)
	}
	return c
}

// control sends a halt or resume the way the page does.
func (w *world) control(id int, action string, header map[string]string) (int, string) {
	w.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, w.url+"/api/campaigns/"+string(rune('0'+id))+"/"+action, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var body struct{ Result string }
	json.Unmarshal(data, &body)
	if body.Result == "" {
		body.Result = strings.TrimSpace(string(data))
	}
	return resp.StatusCode, body.Result
}

var pageHeaders = map[string]string{watchtower.ControlHeader: "1", "Sec-Fetch-Site": "same-origin"}

func (w *world) waitState(id int, state string) watchtower.Campaign {
	w.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		c := w.campaign(id)
		if c.State == state {
			return c
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("campaign %d is %s, want %s\n%s", id, c.State, state, w.dump())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func find(cols []watchtower.Column, node string) *watchtower.Column {
	for i := range cols {
		if cols[i].Node == node {
			return &cols[i]
		}
	}
	return nil
}

func TestListCampaignAndFiles(t *testing.T) {
	w := newWorld(t, `version: 0
flow: sample
start: a
checks:
  style: {run: `+arg("exit", "1")+`, soft: true}
  unit: {run: `+arg("exit", "0")+`}
nodes:
  a: {type: script, run: `+arg("artifact")+`, on: {ok: g, fail: failed}}
  g: {type: gate, checks: [style, unit], on: {pass: done, blocked: failed}}
`)
	var stdout, stderr bytes.Buffer
	if code := cli.Main([]string{"run", "-f", w.orders}, &stdout, &stderr); code != 0 {
		t.Fatalf("run: exit %d\n%s%s", code, &stdout, &stderr)
	}

	var list []watchtower.Summary
	if code := w.get("/api/campaigns", &list); code != http.StatusOK || len(list) != 1 {
		t.Fatalf("list: status %d, %+v", code, list)
	}
	k := list[0]
	if k.ID != 1 || k.Flow != "sample" || k.State != watchtower.StateDone || k.Ended == nil || k.Started.IsZero() {
		t.Fatalf("summary %+v", k)
	}
	var marks []string
	for _, m := range k.Marks {
		marks = append(marks, m.Node+"="+m.Status)
	}
	if want := []string{"a=passed", "g=warning"}; !reflect.DeepEqual(marks, want) {
		t.Fatalf("marks %v, want %v", marks, want)
	}

	c := w.campaign(1)
	if c.End != orders.Done || len(c.Columns) != 2 || c.Columns[0].Node != "a" || c.Columns[1].Node != "g" {
		t.Fatalf("campaign %+v", c)
	}
	g := c.Columns[1]
	if len(g.Tiles) != 2 || g.Tiles[0].Check != "style" || g.Tiles[0].Status != watchtower.StatusWarning ||
		!g.Tiles[0].Soft || g.Tiles[1].Check != "unit" || g.Tiles[1].Status != watchtower.StatusPassed {
		t.Fatalf("gate tiles %+v", g.Tiles)
	}
	a := c.Nodes["a"]
	if len(a) != 1 || a[0].Status != watchtower.StatusPassed || a[0].Log != "logs/a.1.log" ||
		a[0].Result != "results/a.1.json" || !reflect.DeepEqual(a[0].Artifacts, []string{"out.txt"}) || a[0].Transition != "g" {
		t.Fatalf("attempts of a %+v", a)
	}
	if checks := c.Nodes["g"][0].Checks; len(checks) != 2 || checks[0].Log != "logs/g.style.1.log" || checks[0].Exit == nil || *checks[0].Exit != 1 {
		t.Fatalf("checks of g %+v", checks)
	}

	var events []watchtower.Event
	if code := w.get("/api/campaigns/1/events", &events); code != http.StatusOK || events[0].Kind != chronicle.CampaignStarted ||
		events[len(events)-1].To != orders.Done {
		t.Fatalf("events: status %d, %+v", code, events)
	}

	read := func(path string) (int, string, http.Header) {
		resp, err := http.Get(w.url + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data), resp.Header
	}
	if code, body, hd := read("/api/campaigns/1/file?path=logs/a.1.log"); code != 200 || !strings.Contains(body, "teststep artifact") ||
		!strings.HasPrefix(hd.Get("Content-Type"), "text/plain") {
		t.Fatalf("log: %d %q %v", code, body, hd)
	}
	if code, body, _ := read("/api/campaigns/1/file?path=work/out.txt"); code != 200 || body != "built\n" {
		t.Fatalf("artifact: %d %q", code, body)
	}
	for _, bad := range []string{"../../chronicle.db", "lock", "work/../../../orders.yaml", "/etc/hosts", ""} {
		if code, _, _ := read("/api/campaigns/1/file?path=" + bad); code != http.StatusBadRequest {
			t.Errorf("path %q: status %d, want 400", bad, code)
		}
	}
	if code := w.get("/api/campaigns/7", nil); code != http.StatusNotFound {
		t.Errorf("missing campaign: status %d", code)
	}

	for path, typ := range map[string]string{"/": "text/html", "/assets/app.js": "javascript", "/assets/app.css": "text/css", "/assets/favicon.svg": "image/svg+xml"} {
		code, body, hd := read(path)
		if code != 200 || len(body) == 0 || !strings.Contains(hd.Get("Content-Type"), typ) || !strings.Contains(hd.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Errorf("asset %s: %d, %d bytes, %v", path, code, len(body), hd)
		}
	}
}

// stream reads server-sent events into a channel.
type stream struct {
	t      *testing.T
	events chan sseEvent
	body   io.Closer
}

type sseEvent struct {
	kind string
	data string
}

func (w *world) stream(path string) *stream {
	w.t.Helper()
	resp, err := http.Get(w.url + path)
	if err != nil {
		w.t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		w.t.Fatalf("stream %s: %d %v", path, resp.StatusCode, resp.Header)
	}
	s := &stream{t: w.t, events: make(chan sseEvent, 100), body: resp.Body}
	w.t.Cleanup(func() { resp.Body.Close() })
	go func() {
		defer close(s.events)
		r := bufio.NewReader(resp.Body)
		var ev sseEvent
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if ev.kind != "" {
					s.events <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "event: "):
				ev.kind = line[7:]
			case strings.HasPrefix(line, "data: "):
				ev.data += line[6:]
			}
		}
	}()
	return s
}

// until returns the first event of kind for which match is true.
func (s *stream) until(kind string, match func(data string) bool) string {
	s.t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				s.t.Fatalf("stream ended while waiting for %s", kind)
			}
			if ev.kind == kind && match(ev.data) {
				return ev.data
			}
		case <-timeout:
			s.t.Fatalf("timed out waiting for a %s event", kind)
		}
	}
}

func TestStreamFollowsCampaignRunByAnotherProcess(t *testing.T) {
	w := newWorld(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("block", "SYNC1")+`, on: {ok: b, fail: failed}}
  b: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	list := w.stream("/api/stream")
	list.until("list", func(d string) bool { return d == "[]" })

	runner, out := w.process("run")
	w.waitFile(filepath.Join(w.sync(1), "started.a.1"))
	list.until("list", func(d string) bool {
		var l []watchtower.Summary
		json.Unmarshal([]byte(d), &l)
		return len(l) == 1 && l[0].State == watchtower.StateRunning
	})

	s := w.stream("/api/stream?campaign=1")
	first := s.until("campaign", func(string) bool { return true })
	var c watchtower.Campaign
	json.Unmarshal([]byte(first), &c)
	if c.State != watchtower.StateRunning || c.Node != "a" || c.Columns[0].Status != watchtower.StatusRunning || !c.Columns[0].Current {
		t.Fatalf("first campaign event: %s", first)
	}
	var seen []watchtower.Event
	snapshot := s.until("chronicle", func(string) bool { return true })
	var batch struct {
		Reset  bool
		Events []watchtower.Event
	}
	json.Unmarshal([]byte(snapshot), &batch)
	if !batch.Reset || len(batch.Events) == 0 {
		t.Fatalf("first chronicle event: %s", snapshot)
	}
	seen = append(seen, batch.Events...)

	logs := w.stream("/api/campaigns/1/log?path=logs/a.1.log")
	logs.until("chunk", func(d string) bool { return strings.Contains(d, "teststep block") })

	w.release(1)
	// The step's end is written by the other process; it must reach the
	// page as new chronicle events and a new view without the page asking.
	ended, done := false, false
	timeout := time.After(30 * time.Second)
	for !ended || !done {
		var ev sseEvent
		select {
		case ev = <-s.events:
		case <-timeout:
			t.Fatalf("timed out; events so far %+v, done view %v", seen, done)
		}
		switch ev.kind {
		case "chronicle":
			json.Unmarshal([]byte(ev.data), &batch)
			if batch.Reset {
				t.Fatalf("second snapshot instead of new events: %s", ev.data)
			}
			seen = append(seen, batch.Events...)
			last := seen[len(seen)-1]
			ended = last.Kind == chronicle.Transition && last.To == orders.Done
		case "campaign":
			json.Unmarshal([]byte(ev.data), &c)
			done = c.State == watchtower.StateDone
		}
	}
	for i := 1; i < len(seen); i++ {
		if seen[i].Seq != seen[i-1].Seq+1 {
			t.Fatalf("events missing or repeated: %d after %d", seen[i].Seq, seen[i-1].Seq)
		}
	}
	if err := runner.Wait(); err != nil {
		t.Fatalf("runner: %v\n%s", err, out)
	}
}

func TestHaltAndResumeOverHTTP(t *testing.T) {
	w := newWorld(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("block", "SYNC1")+`, on: {ok: b, fail: failed}}
  b: {type: script, run: `+arg("block", "SYNC2")+`, on: {ok: c, fail: failed}}
  c: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	// Halt a campaign driven by another process.
	runner, out := w.process("run")
	w.waitFile(filepath.Join(w.sync(1), "started.a.1"))
	if code, res := w.control(1, "halt", pageHeaders); code != 200 || res != engine.HaltPending {
		t.Fatalf("halt: %d %s", code, res)
	}
	if c := w.campaign(1); c.State != watchtower.StateRunning || !c.HaltRequested {
		t.Fatalf("after halt request: %s halt requested %v", c.State, c.HaltRequested)
	}
	w.release(1)
	runner.Wait()
	if code := runner.ProcessState.ExitCode(); code != 3 {
		t.Fatalf("runner exit %d, want 3 (halted)\n%s", code, out)
	}
	if c := w.waitState(1, watchtower.StateHalted); c.Node != "b" || find(c.Columns, "b").Status != watchtower.StatusWaiting {
		t.Fatalf("halted at %s, b is %s", c.Node, find(c.Columns, "b").Status)
	}

	// Resume with no other process alive: the server drives it.
	if code, res := w.control(1, "resume", pageHeaders); code != 200 || res != watchtower.ResumeDriving {
		t.Fatalf("resume: %d %s", code, res)
	}
	w.waitFile(filepath.Join(w.sync(2), "started.b.1"))
	if c := w.campaign(1); c.State != watchtower.StateRunning {
		t.Fatalf("while the server drives: %s", c.State)
	}
	// Halt the campaign this server drives.
	if code, res := w.control(1, "halt", pageHeaders); code != 200 || res != engine.HaltPending {
		t.Fatalf("second halt: %d %s", code, res)
	}
	w.release(2)
	if c := w.waitState(1, watchtower.StateHalted); c.Node != "c" {
		t.Fatalf("halted at %s, want c", c.Node)
	}
	if code, res := w.control(1, "halt", pageHeaders); code != 200 || res != engine.HaltAlready {
		t.Fatalf("halt of a halted campaign: %d %s", code, res)
	}

	if code, res := w.control(1, "resume", pageHeaders); code != 200 || res != watchtower.ResumeDriving {
		t.Fatalf("second resume: %d %s", code, res)
	}
	w.waitState(1, watchtower.StateDone)
	work := filepath.Join(w.dir, ".hetman", "campaigns", "1", "work")
	if got := teststep.Trace(work); !reflect.DeepEqual(got, []string{"a#1", "b#1", "c#1"}) {
		t.Fatalf("trace %v", got)
	}
	if code, _ := w.control(1, "resume", pageHeaders); code != http.StatusConflict {
		t.Fatalf("resume of an ended campaign: %d, want 409", code)
	}
}

func TestResumeOverHTTPAfterRunnerWasKilled(t *testing.T) {
	w := newWorld(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("block", "SYNC1")+`, on: {ok: b, fail: failed}}
  b: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	runner, out := w.process("run")
	w.waitFile(filepath.Join(w.sync(1), "started.a.1"))
	if err := runner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	runner.Wait()
	c := w.waitState(1, watchtower.StateInterrupted)
	if a := find(c.Columns, "a"); a.Status != watchtower.StatusInterrupted {
		t.Fatalf("a after the kill: %s\n%s", a.Status, out)
	}
	// Where the step outlives its engine, let it end by itself.
	w.release(1)
	if code, res := w.control(1, "resume", pageHeaders); code != 200 || res != watchtower.ResumeDriving {
		t.Fatalf("resume: %d %s", code, res)
	}
	c = w.waitState(1, watchtower.StateDone)
	a := c.Nodes["a"]
	if len(a) != 2 || a[0].Status != watchtower.StatusInterrupted || len(a[0].Cleanup) != 1 || a[1].Status != watchtower.StatusPassed {
		t.Fatalf("attempts of a %+v\n%s", a, w.dump())
	}
	if col := find(c.Columns, "a"); col.Attempts != 2 || col.Tiles[0].Attempts != 2 {
		t.Fatalf("column a %+v", col)
	}
}

func TestCrossSiteControlRefused(t *testing.T) {
	w := newWorld(t, `version: 0
start: a
nodes:
  a: {type: script, run: `+arg("exit", "0")+`, on: {ok: done, fail: failed}}
`)
	if _, err := w.chron.NewCampaign("test", w.orders, "a", nil); err != nil {
		t.Fatal(err)
	}
	attacks := map[string]map[string]string{
		// What a form on another site submits: no custom header possible.
		"cross-site form": {"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site",
			"Content-Type": "application/x-www-form-urlencoded"},
		// A script on another site that somehow sends the header.
		"cross-site with header":            {"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site", watchtower.ControlHeader: "1"},
		"foreign origin, no fetch metadata": {"Origin": "http://evil.example", watchtower.ControlHeader: "1"},
		"same-site subdomain":               {"Origin": "http://other.localhost", "Sec-Fetch-Site": "same-site", watchtower.ControlHeader: "1"},
		"no header":                         {"Content-Type": "text/plain"},
	}
	for name, hd := range attacks {
		for _, action := range []string{"halt", "resume"} {
			if code, _ := w.control(1, action, hd); code != http.StatusForbidden {
				t.Errorf("%s %s: status %d, want 403", name, action, code)
			}
		}
	}
	events, _ := w.chron.Events(1)
	for _, e := range events {
		if e.Kind != chronicle.CampaignStarted {
			t.Fatalf("a refused request changed the chronicle: %s", e.Kind)
		}
	}

	// A page served under another host name (DNS rebinding) gets nothing.
	req, _ := http.NewRequest(http.MethodGet, w.url+"/api/campaigns", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("foreign host: status %d", resp.StatusCode)
	}

	// The page itself is allowed.
	origin := map[string]string{"Origin": w.url, "Sec-Fetch-Site": "same-origin", watchtower.ControlHeader: "1"}
	if code, res := w.control(1, "halt", origin); code != 200 || res != engine.HaltNow {
		t.Fatalf("same-origin halt: %d %s", code, res)
	}
}
