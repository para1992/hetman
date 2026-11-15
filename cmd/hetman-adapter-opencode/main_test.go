package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/adapter/adaptertest"
)

// The tests run the adapter against a fake client: the test binary started
// again with envFake naming a scenario. The fake replays files from
// testdata. Recorded from the real client and sanitized: limit_plan,
// limit_free, badmodel, offline, resume_missing and badargs (output and
// standard error as the client printed them, with ids, request ids and
// local paths replaced). Written by hand after the client's event schema:
// normal, resume, ok, questions, garbage, noauth, limit_go, denied,
// denied_empty, continued and models.txt.
const (
	envSelf   = "HETMAN_TEST_ADAPTER_SELF" // the test binary runs the adapter
	envFake   = "FAKE_CLIENT_SCENARIO"     // the test binary acts as the client
	envData   = "FAKE_CLIENT_DATA"         // folder of the replayed files
	envRecord = "FAKE_CLIENT_RECORD"       // file the fake appends its invocation to
	envReal   = "HETMAN_OPENCODE_REAL"     // set to 1 to run the tests that call the real client
	envModel  = "HETMAN_OPENCODE_MODEL"    // the model of those tests
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
	Config string   `json:"config"`
	PWD    string   `json:"pwd"`
}

func fakeClient(scenario string) int {
	args := os.Args[1:]
	var prompt []byte
	if len(args) > 0 && args[0] == "run" {
		prompt, _ = io.ReadAll(os.Stdin)
	}
	if rec := os.Getenv(envRecord); rec != "" {
		var names []string
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			names = append(names, k)
		}
		data, _ := json.Marshal(invocation{Args: args, Prompt: string(prompt), Env: names,
			Config: os.Getenv("OPENCODE_CONFIG_CONTENT"), PWD: os.Getenv("PWD")})
		f, err := os.OpenFile(rec, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			f.Write(append(data, '\n'))
			f.Close()
		}
	}
	if len(args) > 0 && args[0] == "models" {
		data, _ := os.ReadFile(filepath.Join(os.Getenv(envData), "models.txt"))
		os.Stdout.Write(data)
		return 0
	}
	session := ""
	for i, a := range args {
		if a == "--session" && i+1 < len(args) {
			session = args[i+1]
		}
	}
	switch scenario {
	case "normal", "garbage", "denied":
		return replay(scenario, 0)
	case "resume":
		if session == "" {
			fmt.Fprintln(os.Stderr, "fake: expected --session")
			return 3
		}
		return replay("resume", 0)
	case "denied-then-answer":
		// The turn ends empty after a denial; continued, it answers.
		if session != "" {
			return replay("continued", 0)
		}
		return replay("denied_empty", 0)
	case "resume-missing":
		if session != "" {
			return replay("resume_missing", 1)
		}
		return replay("normal", 0)
	case "limit-plan", "limit-go", "badmodel", "offline", "noauth":
		return replay(strings.ReplaceAll(scenario, "-", "_"), 1)
	case "limit-free":
		// The client keeps retrying without printing anything else.
		replay("limit_free", 0)
		time.Sleep(10 * time.Minute)
		return 0
	case "badargs":
		replay("badargs", 1)
		return 1
	case "hang":
		replayLines("normal", 1)
		time.Sleep(10 * time.Minute)
		return 0
	case "conformance":
		if strings.Contains(string(prompt), "Count from 1 to 2000") {
			replayLines("ok", 1)
			time.Sleep(10 * time.Minute)
			return 0
		}
		if strings.Contains(string(prompt), `"outcome": "questions"`) {
			return replay("questions", 0)
		}
		return replay("ok", 0)
	}
	fmt.Fprintf(os.Stderr, "fake: unknown scenario %q\n", scenario)
	return 2
}

// replay writes a recorded output, when there is one, and its standard
// error, when there is one, and returns code.
func replay(name string, code int) int {
	if _, err := os.Stat(filepath.Join(os.Getenv(envData), name+".jsonl")); err == nil {
		replayLines(name, -1)
	}
	if data, err := os.ReadFile(filepath.Join(os.Getenv(envData), name+".stderr")); err == nil {
		os.Stderr.Write(data)
	}
	return code
}

// replayLines writes the first n lines of a recorded output (all for n < 0)
// with $WORKDIR replaced by the current folder.
func replayLines(name string, n int) {
	data, err := os.ReadFile(filepath.Join(os.Getenv(envData), name+".jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake:", err)
		os.Exit(2)
	}
	wd, _ := os.Getwd()
	quoted, _ := json.Marshal(wd)
	text := strings.ReplaceAll(string(data), "$WORKDIR", string(quoted[1:len(quoted)-1]))
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
	return adapter.Step{RunID: "7", NodeID: "draft", Attempt: 1, Prompt: "write the note", Workdir: dir,
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

// runs returns the recorded invocations of "opencode run".
func runs(t *testing.T, path string) []invocation {
	var list []invocation
	for _, inv := range recorded(t, path) {
		if len(inv.Args) > 0 && inv.Args[0] == "run" {
			list = append(list, inv)
		}
	}
	return list
}

func agentArg(args []string) string {
	for i, a := range args {
		if a == "--agent" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
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
	if d.Name != "opencode" || !d.Has(adapter.FeatureResume) {
		t.Fatalf("description %+v", d)
	}
}

// The command line carries the step's model and effort, the configuration
// restricts the agent to the step's tools, the prompt ends with the answer
// format, and the client gets only the environment it needs.
func TestCommandLineAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "record.jsonl")
	t.Setenv(envRecord, rec)
	t.Setenv("OPENAI_API_KEY", "not-a-key")
	t.Setenv("OPENCODE_PERMISSION", `{"*":"allow"}`)
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permission":"allow"}`)
	t.Setenv("HETMAN_RUN_ID", "1")
	t.Setenv("PWD", "/somewhere/else")
	st := testStep(dir)
	st.Model, st.Effort = "example/fast", "low"
	st.Tools = []string{"read", "edit", "bash(go test *)", "bash(git status)"}
	stepRun(t, "normal", st)
	all := recorded(t, rec)
	if len(all) != 2 || all[0].Args[0] != "models" {
		t.Fatalf("invocations %+v, want the model catalogue and then the run", all)
	}
	if got := strings.Join(all[0].Args, " "); got != "models example --verbose --pure" {
		t.Errorf("catalogue arguments %q", got)
	}
	inv := all[1]
	agent := agentArg(inv.Args)
	if !strings.HasPrefix(agent, "hetman-") || len(agent) != len("hetman-")+8 {
		t.Errorf("agent %q", agent)
	}
	want := []string{"run", "--format", "json", "--pure", "--print-logs", "--log-level", "INFO", "--thinking", "--agent", agent,
		"--model", "example/fast", "--variant", "low", "--title", "hetman draft (run 7)"}
	if strings.Join(inv.Args, "\n") != strings.Join(want, "\n") {
		t.Errorf("arguments\n got %q\nwant %q", inv.Args, want)
	}
	wantConfig := `{"$schema":"https://opencode.ai/config.json","share":"disabled","autoupdate":false,"agent":{"` + agent +
		`":{"mode":"primary","description":"One step of a Hetman flow.","permission":{"*":"deny","read":"allow","edit":"allow","bash":{"go test *":"allow","git status":"allow"}}}}}`
	if inv.Config != wantConfig {
		t.Errorf("configuration\n got %s\nwant %s", inv.Config, wantConfig)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(inv.Config), &cfg); err != nil {
		t.Errorf("configuration is not JSON: %v", err)
	}
	if !strings.HasPrefix(inv.Prompt, "write the note\n\n---\nThe tools of this step") || !strings.Contains(inv.Prompt, "- bash: only a command line that matches one of `go test *`, `git status`\n") ||
		!strings.Contains(inv.Prompt, "\n\n---\nAnswer format:") ||
		!strings.Contains(inv.Prompt, `{"type":"object","properties":{"outcome":{"type":"string","enum":["ok"]}},"required":["outcome"]}`) {
		t.Errorf("prompt on standard input:\n%s", inv.Prompt)
	}
	if inv.PWD != dir {
		t.Errorf("PWD %q, want the step's folder %q", inv.PWD, dir)
	}
	env := " " + strings.Join(inv.Env, " ") + " "
	for _, name := range []string{"OPENAI_API_KEY", "OPENCODE_PERMISSION", "HETMAN_RUN_ID", envSelf} {
		if strings.Contains(env, " "+name+" ") {
			t.Errorf("the client got %s", name)
		}
	}
	for _, name := range []string{envFake, "OPENCODE_DISABLE_AUTOUPDATE", "OPENCODE_CONFIG_CONTENT"} {
		if !strings.Contains(env, " "+name+" ") {
			t.Errorf("the client did not get %s: %s", name, env)
		}
	}
}

// Without tools the agent may use none, and without a model or effort the
// client's defaults apply.
func TestNoTools(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "record.jsonl")
	t.Setenv(envRecord, rec)
	stepRun(t, "normal", testStep(dir))
	inv := recorded(t, rec)
	if len(inv) != 1 {
		t.Fatalf("invocations %+v", inv)
	}
	args := strings.Join(inv[0].Args, " ")
	if strings.Contains(args, "--model") || strings.Contains(args, "--variant") || strings.Contains(args, "--session") {
		t.Errorf("arguments %s", args)
	}
	if !strings.HasSuffix(inv[0].Config, `"permission":{"*":"deny"}}}}`) {
		t.Errorf("configuration %s", inv[0].Config)
	}
}

func TestToolRules(t *testing.T) {
	rules, err := permissions([]string{"bash(git *)", "bash", "read(*.md)", " grep "})
	if err != nil {
		t.Fatal(err)
	}
	got := agentConfig("a", rules)
	if !strings.HasSuffix(got, `"permission":{"*":"deny","bash":"allow","read":{"*.md":"allow"},"grep":"allow"}}}}`) {
		t.Errorf("configuration %s", got)
	}
	for _, bad := range []string{"", "*", "Read", "task", "bash()", "rm -rf"} {
		if _, err := permissions([]string{bad}); err == nil {
			t.Errorf("tool %q was accepted", bad)
		}
	}
}

// A run with tool use: the session comes first, the log follows the work,
// the last JSON object of the answer is the result.
func TestNormalRun(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "record.jsonl")
	t.Setenv(envRecord, rec)
	msgs := stepRun(t, "normal", testStep(dir))
	// A denial followed by an answer needs no continuation.
	if n := len(runs(t, rec)); n != 1 {
		t.Errorf("%d runs of the client, want 1", n)
	}
	if f := final(msgs); len(f.Denied) != 1 || !strings.HasPrefix(f.Denied[0], "bash rm -rf build: ") {
		t.Errorf("denied %q", f.Denied)
	}
	if msgs[0].Type != adapter.TypeSession || msgs[0].ID != "ses_00000000000000000000000010" {
		t.Errorf("first message %+v, want the session", msgs[0])
	}
	if got := sessions(msgs); len(got) != 1 {
		t.Errorf("sessions %v, want one", got)
	}
	log := logText(msgs)
	for _, want := range []string{"model the client's default, tools: none", "I will write the note first.",
		"> write note.txt\n  < Wrote file successfully.", "> bash rm -rf build\ndenied: bash rm -rf build: The user has specified a rule which prevents you from using this specific tool call.\n",
		"> read note.txt\n  < 1: alpha (+1 lines)", "finished after 3 model steps in 5.4s, 27500 tokens in, 300 out"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, `{"type"`) || strings.Contains(log, "$WORKDIR") {
		t.Errorf("log holds raw events or paths:\n%s", log)
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
	id := "ses_00000000000000000000000011"
	st.Session = &id
	msgs := stepRun(t, "resume", st)
	args := runs(t, rec)[0].Args
	if n := len(args); n < 2 || args[n-2] != "--session" || args[n-1] != id {
		t.Errorf("arguments %q do not continue the session", args)
	}
	if strings.Contains(strings.Join(args, " "), "--title") {
		t.Errorf("arguments %q give a continued session a title", args)
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
	id := "ses_00000000000000000000000000"
	st.Session = &id
	msgs := stepRun(t, "resume-missing", st)
	inv := runs(t, rec)
	if len(inv) != 2 || strings.Contains(strings.Join(inv[1].Args, " "), "--session") {
		t.Fatalf("invocations %+v, want a second one without --session", inv)
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
		{"limit-plan", adapter.ErrLimit, "Token Plan usage limit has been reached", false},
		{"limit-free", adapter.ErrLimit, "Free usage limit reached", false},
		{"limit-go", adapter.ErrLimit, "It will reset in 1 hour", true},
		{"noauth", adapter.ErrPermanent, "No API key found", false},
		{"badmodel", adapter.ErrPermanent, "Model not found: opencode/no-such-model-xyz", false},
		{"offline", adapter.ErrOffline, "Unable to connect", false},
		{"badargs", adapter.ErrPermanent, "rejected its arguments", false},
		{"garbage", adapter.ErrTransient, "no JSON object", false},
	}
	for _, c := range cases {
		t.Run(c.scenario, func(t *testing.T) {
			began := time.Now()
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
			if took := time.Since(began); took > 20*time.Second {
				t.Errorf("took %s", took)
			}
		})
	}
}

// The client's log names the session before any event: here the client is
// stopped at a usage limit before it printed one. Its routine log lines do
// not reach the adapter's standard error.
func TestSessionFromLog(t *testing.T) {
	t.Setenv(envFake, "limit-free")
	var out, diag bytes.Buffer
	run(fakeArgs(t), bytes.NewReader(adapter.EncodeStart(testStep(t.TempDir()))), &out, &diag)
	msgs := parse(t, out.Bytes())
	if got := sessions(msgs); len(got) != 1 || got[0] != "ses_00000000000000000000000002" {
		t.Errorf("sessions %v", got)
	}
	if strings.Contains(diag.String(), "level=INFO") || !strings.Contains(diag.String(), "level=ERROR") {
		t.Errorf("adapter standard error:\n%s", diag.String())
	}
}

func TestGarbageIsLogged(t *testing.T) {
	log := logText(stepRun(t, "garbage", testStep(t.TempDir())))
	if !strings.Contains(log, "client: this line is not JSON") {
		t.Errorf("log:\n%s", log)
	}
}

func TestEffort(t *testing.T) {
	cases := []struct {
		model, effort, problem string
	}{
		{"example/fast", "high", ""},
		{"example/fast", "max", "its variants: low, high"},
		{"example/plain", "low", "has no variants"},
		{"example/unlisted", "low", ""}, // not in the catalogue: passed unchecked
		{"", "low", ""},                 // no model: passed unchecked
		{"example/fast", "Very High", "not a model variant name"},
	}
	for _, c := range cases {
		t.Run(c.model+" "+c.effort, func(t *testing.T) {
			dir := t.TempDir()
			rec := filepath.Join(dir, "record.jsonl")
			t.Setenv(envRecord, rec)
			st := testStep(dir)
			st.Model, st.Effort = c.model, c.effort
			f := final(stepRun(t, "normal", st))
			if c.problem == "" {
				if f.Type != adapter.TypeResult {
					t.Fatalf("final %+v", f)
				}
				return
			}
			if f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, c.problem) {
				t.Fatalf("final %+v, want a permanent error with %q", f, c.problem)
			}
			if _, err := os.Stat(rec); err == nil && len(runs(t, rec)) > 0 {
				t.Error("the client was run")
			}
		})
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
	for _, name := range []string{"OPENCODE_CONFIG_CONTENT", "OPENCODE_PERMISSION", "PWD"} {
		if _, _, err := parseArgs([]string{"--env", name}); err == nil {
			t.Errorf("--env %s was accepted", name)
		}
	}
}

func TestLogError(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "offline.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	if got := lastLogError(string(data)); got != "Cannot connect to API: Unable to connect. Is the computer able to access the url?" {
		t.Errorf("lastLogError = %q", got)
	}
	if got := logError(`level=INFO message=created`); got != "" {
		t.Errorf("logError of an info line = %q", got)
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

// TestRealClient runs the conformance suite against the real client. It
// makes five small model requests (step, resume, two dialogue rounds and the
// cancelled step), so it runs only when HETMAN_OPENCODE_REAL=1, with the
// model in HETMAN_OPENCODE_MODEL in provider/model notation (a free model by
// default):
//
//	HETMAN_OPENCODE_REAL=1 HETMAN_OPENCODE_MODEL=opencode/big-pickle \
//	  go test ./cmd/hetman-adapter-opencode -run RealClient -v
func TestRealClient(t *testing.T) {
	if os.Getenv(envReal) != "1" {
		t.Skip("set " + envReal + "=1 to run the conformance suite against the real client")
	}
	model := os.Getenv(envModel)
	if model == "" {
		model = "opencode/big-pickle"
	}
	adaptertest.Run(t, adaptertest.Config{Command: []string{self(t), "adapter"}, Model: model,
		Timeout: 3 * time.Minute, CancelWithin: 15 * time.Second})
}
