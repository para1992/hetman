package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/adapter/adaptertest"
)

// The tests run the adapter against a fake client: the test binary started
// again with envFake naming a scenario. The fake replays an output file from
// testdata. Files recorded from the real client and sanitized: normal,
// resume, badmodel, noauth, offline and resume_missing (the .stderr files
// too). Files written by hand after the client's own message schema: the two
// limit files, garbage, textjson, ok, denied and thinking.
const (
	envSelf   = "HETMAN_TEST_ADAPTER_SELF" // the test binary runs the adapter
	envFake   = "FAKE_CLIENT_SCENARIO"     // the test binary acts as the client
	envData   = "FAKE_CLIENT_DATA"         // folder of the replayed files
	envRecord = "FAKE_CLIENT_RECORD"       // file the fake appends its invocation to
	envReal   = "HETMAN_CLAUDE_REAL"       // set to 1 to run the tests that call the real client
)

func TestMain(m *testing.M) {
	if os.Getenv(envSelf) == "1" && len(os.Args) > 1 && os.Args[1] == "adapter" {
		os.Exit(run(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if sc := os.Getenv(envFake); sc != "" {
		os.Exit(fakeClient(sc))
	}
	data, err := filepath.Abs("testdata")
	if err != nil {
		panic(err)
	}
	os.Setenv(envData, data)
	os.Setenv(envSelf, "1")
	os.Exit(m.Run())
}

// invocation is what the fake client records about how it was started.
type invocation struct {
	Args   []string `json:"args"`
	Prompt string   `json:"prompt"`
	Env    []string `json:"env"` // names only
}

func fakeClient(scenario string) int {
	prompt, _ := io.ReadAll(os.Stdin)
	args := os.Args[1:]
	if rec := os.Getenv(envRecord); rec != "" {
		var names []string
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			names = append(names, k)
		}
		data, _ := json.Marshal(invocation{Args: args, Prompt: string(prompt), Env: names})
		f, err := os.OpenFile(rec, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			f.Write(append(data, '\n'))
			f.Close()
		}
	}
	resume := ""
	for i, a := range args {
		if a == "--resume" && i+1 < len(args) {
			resume = args[i+1]
		}
	}
	switch scenario {
	case "normal", "garbage", "textjson", "denied", "thinking":
		return replay(scenario, 0)
	case "resume":
		if resume == "" {
			fmt.Fprintln(os.Stderr, "fake: expected --resume")
			return 3
		}
		return replay("resume", 0)
	case "resume-missing":
		if resume != "" {
			return replay("resume_missing", 1)
		}
		return replay("normal", 0)
	case "limit-reset":
		return replay("limit_reset", 1)
	case "limit-noreset":
		return replay("limit_noreset", 1)
	case "noauth", "offline", "badmodel":
		return replay(scenario, 1)
	case "badargs":
		fmt.Fprintln(os.Stderr, "error: unknown option '--bogus'")
		return 1
	case "hang":
		replayLines("normal", 1)
		time.Sleep(10 * time.Minute)
		return 0
	case "conformance":
		if strings.Contains(string(prompt), "Count from 1 to 2000") {
			replayLines("normal", 1)
			time.Sleep(10 * time.Minute)
			return 0
		}
		return replay("ok", 0)
	}
	fmt.Fprintf(os.Stderr, "fake: unknown scenario %q\n", scenario)
	return 2
}

// replay writes a recorded output, and its standard error when there is
// one, and returns code.
func replay(name string, code int) int {
	replayLines(name, -1)
	if data, err := os.ReadFile(filepath.Join(os.Getenv(envData), name+".stderr")); err == nil {
		os.Stderr.Write(data)
	}
	return code
}

// replayLines writes the first n lines of a recorded output (all for n < 0)
// with the placeholders filled in: $WORKDIR is the current folder and
// $RESETS_AT a time an hour ahead in Unix seconds.
func replayLines(name string, n int) {
	data, err := os.ReadFile(filepath.Join(os.Getenv(envData), name+".jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake:", err)
		os.Exit(2)
	}
	wd, _ := os.Getwd()
	quoted, _ := json.Marshal(wd)
	text := strings.ReplaceAll(string(data), "$WORKDIR", string(quoted[1:len(quoted)-1]))
	text = strings.ReplaceAll(text, "$RESETS_AT", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	lines := strings.SplitAfter(text, "\n")
	if n >= 0 && n < len(lines) {
		lines = lines[:n]
	}
	os.Stdout.WriteString(strings.Join(lines, ""))
}

func self(t *testing.T) string {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// fakeArgs are the adapter arguments that make it start the fake client.
func fakeArgs(t *testing.T) []string {
	return []string{"--client", self(t), "--env", envFake, "--env", envData, "--env", envRecord}
}

func testStep(dir string) adapter.Step {
	return adapter.Step{RunID: "1", NodeID: "draft", Attempt: 1, Prompt: "write the note", Workdir: dir,
		Schema: json.RawMessage(`{"type":"object","properties":{"outcome":{"type":"string","enum":["ok"]}},"required":["outcome"]}`)}
}

// stepRun runs the adapter in this process on one step against a scenario
// and returns the messages it wrote, each checked against the protocol.
func stepRun(t *testing.T, scenario string, st adapter.Step) []adapter.Message {
	t.Helper()
	t.Setenv(envFake, scenario)
	var out, diag bytes.Buffer
	code := run(fakeArgs(t), bytes.NewReader(adapter.EncodeStart(st)), &out, &diag)
	if code != 0 {
		t.Fatalf("exit code %d; standard error:\n%s", code, diag.String())
	}
	if diag.Len() > 0 {
		t.Logf("adapter standard error:\n%s", diag.String())
	}
	return parse(t, out.Bytes())
}

func parse(t *testing.T, out []byte) []adapter.Message {
	t.Helper()
	var msgs []adapter.Message
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		m, err := adapter.ParseMessage(line)
		if err != nil {
			t.Fatalf("not a protocol message: %v: %s", err, line)
		}
		msgs = append(msgs, m)
	}
	if len(msgs) == 0 || !msgs[len(msgs)-1].Terminal() {
		t.Fatalf("the last message is not a result or an error: %+v", msgs)
	}
	for _, m := range msgs[:len(msgs)-1] {
		if m.Terminal() {
			t.Fatalf("a %s before the last message", m.Type)
		}
	}
	return msgs
}

func final(msgs []adapter.Message) adapter.Message { return msgs[len(msgs)-1] }

func logText(msgs []adapter.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Type == adapter.TypeLog {
			b.WriteString(m.Text + "\n")
		}
	}
	return b.String()
}

func sessions(msgs []adapter.Message) []string {
	var ids []string
	for _, m := range msgs {
		if m.Type == adapter.TypeSession {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

func recorded(t *testing.T, path string) []invocation {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list []invocation
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var inv invocation
		if err := json.Unmarshal([]byte(line), &inv); err != nil {
			t.Fatal(err)
		}
		list = append(list, inv)
	}
	return list
}

func TestDescribe(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{adapter.DescribeFlag}, strings.NewReader(""), &out, io.Discard); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	d, err := adapter.ParseDescription(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "claude" || !d.Has(adapter.FeatureResume) {
		t.Fatalf("description %+v", d)
	}
}

// The command line carries the step's model, effort, tools and schema, and
// the client gets only the environment it needs.
func TestCommandLineAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "record.jsonl")
	t.Setenv(envRecord, rec)
	t.Setenv("ANTHROPIC_API_KEY", "not-a-key")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("HETMAN_RUN_ID", "1")
	st := testStep(dir)
	st.Model, st.Effort = "haiku", "low"
	st.Tools = []string{"Read", "Write", "Bash(go test *)", "Bash(git status)"}
	stepRun(t, "normal", st)
	inv := recorded(t, rec)[0]
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--restricted", "--strict-mcp-config",
		"--permission-mode", "dontAsk", "--permission-prompts", "none", "--tools=Read,Write,Bash",
		"--allowedTools=Read", "--allowedTools=Write", "--allowedTools=Bash(go test *)", "--allowedTools=Bash(git status)",
		"--model", "haiku", "--effort", "low", "--json-schema", `{"type":"object","properties":{"outcome":{"type":"string","enum":["ok"]}},"required":["outcome"]}`}
	if strings.Join(inv.Args, "\n") != strings.Join(want, "\n") {
		t.Errorf("arguments\n got %q\nwant %q", inv.Args, want)
	}
	// The prompt, then which entries of each tool the step allows.
	if inv.Prompt != st.Prompt+scopeNote(st.Tools) || !strings.Contains(inv.Prompt, "- Bash: only a command line that matches one of `go test *`, `git status`") {
		t.Errorf("prompt on standard input %q", inv.Prompt)
	}
	env := strings.Join(inv.Env, " ")
	for _, name := range []string{"ANTHROPIC_API_KEY", "CLAUDECODE", "HETMAN_RUN_ID", envSelf} {
		if strings.Contains(" "+env+" ", " "+name+" ") {
			t.Errorf("the client got %s", name)
		}
	}
	if !strings.Contains(" "+env+" ", " "+envFake+" ") {
		t.Errorf("a variable named with --env did not reach the client: %s", env)
	}
}

func TestNoTools(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "record.jsonl")
	t.Setenv(envRecord, rec)
	stepRun(t, "normal", testStep(dir))
	args := strings.Join(recorded(t, rec)[0].Args, " ")
	if !strings.Contains(args, "--tools= ") || strings.Contains(args, "--allowedTools") || strings.Contains(args, "--model") || strings.Contains(args, "--effort") {
		t.Errorf("arguments %s", args)
	}
}

// A run with tool use: the session comes first, the log follows the work,
// the structured output is the result.
func TestNormalRun(t *testing.T) {
	msgs := stepRun(t, "normal", testStep(t.TempDir()))
	if msgs[0].Type != adapter.TypeSession || msgs[0].ID != "00000001-0000-4000-8000-000000000001" {
		t.Errorf("first message %+v, want the session", msgs[0])
	}
	if got := sessions(msgs); len(got) != 1 {
		t.Errorf("sessions %v, want one", got)
	}
	log := logText(msgs)
	for _, want := range []string{"model claude-haiku-5-5, tools: Read, StructuredOutput, Write", "> Write ", "note.txt", "> Read ",
		"  < 1\talpha (+2 lines)", `> answer {"outcome":"ok","lines":2}`, "finished after 4 turns"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "{\"type\"") {
		t.Errorf("log holds raw events:\n%s", log)
	}
	if f := final(msgs); f.Type != adapter.TypeResult || string(f.Output) != `{"outcome":"ok","lines":2}` {
		t.Errorf("final %+v", f)
	}
}

func TestResume(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "record.jsonl")
	t.Setenv(envRecord, rec)
	st := testStep(dir)
	id := "00000001-0000-4000-8000-000000000001"
	st.Session = &id
	msgs := stepRun(t, "resume", st)
	args := recorded(t, rec)[0].Args
	if n := len(args); n < 2 || args[n-2] != "--resume" || args[n-1] != id {
		t.Errorf("arguments %q do not continue the session", args)
	}
	if got := sessions(msgs); len(got) != 1 || got[0] != id {
		t.Errorf("sessions %v", got)
	}
	if f := final(msgs); f.Type != adapter.TypeResult || string(f.Output) != `{"outcome":"ok","words":["alpha","beta"]}` {
		t.Errorf("final %+v", f)
	}
}

// A session the client cannot find gives a new conversation, not a failure.
func TestResumeMissingSession(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "record.jsonl")
	t.Setenv(envRecord, rec)
	st := testStep(dir)
	id := "00000000-0000-4000-8000-000000000000"
	st.Session = &id
	msgs := stepRun(t, "resume-missing", st)
	inv := recorded(t, rec)
	if len(inv) != 2 || strings.Contains(strings.Join(inv[1].Args, " "), "--resume") {
		t.Fatalf("invocations %+v, want a second one without --resume", inv)
	}
	if !strings.Contains(logText(msgs), "starting a new conversation") {
		t.Errorf("log:\n%s", logText(msgs))
	}
	if f := final(msgs); f.Type != adapter.TypeResult {
		t.Errorf("final %+v", f)
	}
}

func TestFailures(t *testing.T) {
	cases := []struct {
		scenario, kind, message string
		retryAt                 bool
	}{
		{"limit-reset", adapter.ErrLimit, "You've hit your limit", true},
		{"limit-noreset", adapter.ErrLimit, "Rate limited", false},
		{"noauth", adapter.ErrPermanent, "Not logged in", false},
		{"badmodel", adapter.ErrPermanent, "issue with the selected model", false},
		{"offline", adapter.ErrOffline, "ECONNREFUSED", false},
		{"badargs", adapter.ErrPermanent, "unknown option", false},
		{"garbage", adapter.ErrTransient, "no JSON object", false},
	}
	for _, c := range cases {
		t.Run(c.scenario, func(t *testing.T) {
			msgs := stepRun(t, c.scenario, testStep(t.TempDir()))
			f := final(msgs)
			if f.Type != adapter.TypeError || f.Kind != c.kind || !strings.Contains(f.Message, c.message) {
				t.Fatalf("final %+v, want a %s error with %q", f, c.kind, c.message)
			}
			at := f.RetryTime()
			if c.retryAt && (at.Before(time.Now().Add(50*time.Minute)) || at.After(time.Now().Add(70*time.Minute))) {
				t.Errorf("retry_at %q, want about an hour ahead", f.RetryAt)
			}
			if !c.retryAt && f.RetryAt != "" {
				t.Errorf("retry_at %q, want none", f.RetryAt)
			}
		})
	}
}

func TestGarbageIsLogged(t *testing.T) {
	log := logText(stepRun(t, "garbage", testStep(t.TempDir())))
	if !strings.Contains(log, "client: this line is not JSON") {
		t.Errorf("log:\n%s", log)
	}
}

// Without structured output the object is taken from the answer's text.
func TestTextAnswer(t *testing.T) {
	f := final(stepRun(t, "textjson", testStep(t.TempDir())))
	if f.Type != adapter.TypeResult || string(f.Output) != `{"outcome":"ok","note":"a {brace} in text"}` {
		t.Fatalf("final %+v", f)
	}
}

func TestBadEffort(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "record.jsonl")
	t.Setenv(envRecord, rec)
	st := testStep(dir)
	st.Effort = "extreme"
	f := final(stepRun(t, "normal", st))
	if f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, "extreme") {
		t.Fatalf("final %+v", f)
	}
	if _, err := os.Stat(rec); err == nil {
		t.Error("the client was started")
	}
}

func TestMissingClient(t *testing.T) {
	var out bytes.Buffer
	args := []string{"--client", filepath.Join(t.TempDir(), "no-such-client")}
	run(args, bytes.NewReader(adapter.EncodeStart(testStep(t.TempDir()))), &out, io.Discard)
	if f := final(parse(t, out.Bytes())); f.Kind != adapter.ErrPermanent {
		t.Fatalf("final %+v", f)
	}
}

func TestRefusedEnv(t *testing.T) {
	if _, _, err := parseArgs([]string{"--env", "ANTHROPIC_API_KEY"}); err == nil {
		t.Error("--env ANTHROPIC_API_KEY was accepted")
	}
}

// A cancel message stops a client that hangs.
func TestCancel(t *testing.T) {
	t.Setenv(envFake, "hang")
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- run(fakeArgs(t), inR, outW, io.Discard)
		outW.Close()
	}()
	inW.Write(adapter.EncodeStart(testStep(t.TempDir())))
	br := bufio.NewReader(outR)
	var msgs []adapter.Message
	began := time.Now()
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			break
		}
		m, perr := adapter.ParseMessage(bytes.TrimSpace(line))
		if perr != nil {
			t.Fatalf("%v: %s", perr, line)
		}
		msgs = append(msgs, m)
		if m.Type == adapter.TypeSession {
			began = time.Now()
			inW.Write(adapter.EncodeCancel())
		}
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code %d", code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the adapter did not return after a cancel message")
	}
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("stopping took %s", took)
	}
	if len(msgs) == 0 {
		t.Fatal("no messages")
	}
	if f := final(msgs); f.Type != adapter.TypeError || f.Kind != adapter.ErrTransient || f.Message != "cancelled" {
		t.Fatalf("final %+v", f)
	}
}

func TestExtractObject(t *testing.T) {
	cases := map[string]string{
		`{"outcome":"ok"}`: `{"outcome":"ok"}`,
		"Done.\n```json\n{\"outcome\": \"ok\", \"a\": {\"b\": 1}}\n```": `{"outcome": "ok", "a": {"b": 1}}`,
		`e.g. {"outcome":"x"} but finally {"outcome":"ok"}`:             `{"outcome":"ok"}`,
		`{broken {"outcome":"ok"} trailing }`:                           `{"outcome":"ok"}`,
		`no object here [1,2]`:                                          ``,
	}
	for in, want := range cases {
		got, ok := extractObject(in)
		if string(got) != want || ok != (want != "") {
			t.Errorf("extractObject(%q) = %s, %v; want %s", in, got, ok, want)
		}
	}
}

// The conformance suite passes against the adapter with the fake client.
func TestConformance(t *testing.T) {
	t.Setenv(envFake, "conformance")
	adaptertest.Run(t, adaptertest.Config{Command: append([]string{self(t), "adapter"}, fakeArgs(t)...)})
}

// TestRealClient runs the conformance suite against the real client with
// the haiku model. It makes three small requests on the subscription, so it
// runs only when HETMAN_CLAUDE_REAL=1:
//
//	HETMAN_CLAUDE_REAL=1 go test ./cmd/hetman-adapter-claude -run RealClient -v
func TestRealClient(t *testing.T) {
	if os.Getenv(envReal) != "1" {
		t.Skip("set " + envReal + "=1 to run the conformance suite against the real client")
	}
	adaptertest.Run(t, adaptertest.Config{Command: []string{self(t), "adapter"}, Model: "haiku", CancelWithin: 15 * time.Second})
}
