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
	"hetman/internal/ask"
)

// The tests run the adapter against a fake client: the test binary started
// again with envFake naming a scenario. The fake replays files from
// testdata. Recorded from the real client (codex-cli 0.167.0) and
// sanitized, with thread ids, paths and token counts kept in shape:
// ok, normal (a refused write in the sandbox and an applied patch; the
// commands shortened), resume, patch_refused (the client's error log line
// for a refused patch), badschema, badmodel, offline and resume_missing.
// Written by hand after the client's event schema and the messages in its
// binary: plain, limit, limit429, noauth, garbage, empty, questions and
// models.json (a subset of "debug models").
const (
	envSelf      = "HETMAN_TEST_ADAPTER_SELF" // the test binary runs the adapter
	envFake      = "FAKE_CLIENT_SCENARIO"     // the test binary acts as the client
	envData      = "FAKE_CLIENT_DATA"         // folder of the replayed files
	envRecord    = "FAKE_CLIENT_RECORD"       // file the fake appends its invocation to
	envNoCatalog = "FAKE_CLIENT_NO_CATALOG"   // "refresh": only the bundled catalogue; "all": none
	envReal      = "HETMAN_CODEX_REAL"        // set to 1 to run the tests that call the real client
	envModel     = "HETMAN_CODEX_MODEL"       // the model of those tests
)

// newThread is the thread id the fake reports for a new conversation.
const newThread = "00000000-0000-7000-8000-0000000000aa"

func TestMain(m *testing.M) {
	if os.Getenv(envSelf) == "1" && len(os.Args) > 1 && os.Args[1] == "adapter" {
		os.Exit(serve(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
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
	Schema string   `json:"schema"`
	Dir    string   `json:"dir"`
}

func (inv invocation) resume() bool {
	return len(inv.Args) > 1 && inv.Args[0] == "exec" && inv.Args[1] == "resume"
}

// session returns the session a resume continues.
func (inv invocation) session() string {
	if !inv.resume() || len(inv.Args) < 2 {
		return ""
	}
	return inv.Args[len(inv.Args)-2]
}

func (inv invocation) value(flag string) string {
	for i, a := range inv.Args {
		if a == flag && i+1 < len(inv.Args) {
			return inv.Args[i+1]
		}
	}
	return ""
}

func fakeClient(scenario string) int {
	args := os.Args[1:]
	if len(args) >= 2 && args[0] == "debug" && args[1] == "models" {
		bundled := len(args) > 2 && args[2] == "--bundled"
		switch os.Getenv(envNoCatalog) {
		case "all":
			fmt.Fprintln(os.Stderr, "Error: failed to load models")
			return 1
		case "refresh":
			if !bundled {
				fmt.Fprintln(os.Stderr, "Error: failed to refresh available models")
				return 1
			}
		}
		data, _ := os.ReadFile(filepath.Join(os.Getenv(envData), "models.json"))
		os.Stdout.Write(data)
		return 0
	}
	var prompt []byte
	if len(args) > 0 && args[0] == "exec" {
		prompt, _ = io.ReadAll(os.Stdin)
	}
	inv := invocation{Args: args, Prompt: string(prompt)}
	if f := inv.value("--output-schema"); f != "" {
		data, _ := os.ReadFile(f)
		inv.Schema = string(data)
	}
	inv.Dir, _ = os.Getwd()
	if rec := os.Getenv(envRecord); rec != "" {
		for _, kv := range os.Environ() {
			k, _, _ := strings.Cut(kv, "=")
			inv.Env = append(inv.Env, k)
		}
		data, _ := json.Marshal(inv)
		f, err := os.OpenFile(rec, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			f.Write(append(data, '\n'))
			f.Close()
		}
	}
	thread := newThread
	if inv.resume() {
		thread = inv.session()
	}
	needSession := func() bool {
		if !inv.resume() {
			fmt.Fprintln(os.Stderr, "fake: expected exec resume")
			return false
		}
		return true
	}
	switch scenario {
	case "normal", "ok", "garbage", "questions", "patch_refused", "plain":
		return replay(scenario, thread, 0)
	case "badmodel", "offline", "limit", "limit429", "noauth":
		return replay(scenario, thread, 1)
	case "resume":
		if !needSession() {
			return 3
		}
		return replay("resume", thread, 0)
	case "resume-missing":
		if inv.resume() {
			return replay("resume_missing", thread, 1)
		}
		return replay("ok", thread, 0)
	case "badschema":
		if inv.Schema != "" {
			return replay("badschema", thread, 1)
		}
		return replay("plain", thread, 0)
	case "empty-then-answer":
		if inv.resume() {
			return replay("ok", thread, 0)
		}
		return replay("empty", thread, 0)
	case "empty-in-session":
		if !needSession() {
			return 3
		}
		return replay("empty", thread, 0)
	case "newthread":
		return replay("ok", newThread, 0)
	case "hang":
		replayLines("ok", thread, 2)
		time.Sleep(10 * time.Minute)
		return 0
	case "conformance":
		if strings.Contains(string(prompt), "Count from 1 to 2000") {
			replayLines("ok", thread, 2)
			time.Sleep(10 * time.Minute)
			return 0
		}
		if strings.Contains(string(prompt), `"outcome": "questions"`) {
			return replay("questions", thread, 0)
		}
		return replay("ok", thread, 0)
	}
	if play, ok := extraScenarios[scenario]; ok {
		return play(thread)
	}
	fmt.Fprintf(os.Stderr, "fake: unknown scenario %q\n", scenario)
	return 2
}

// extraScenarios are scenarios of the fake client that only some systems
// have, by name, each given the thread id.
var extraScenarios = map[string]func(thread string) int{}

// replay writes a recorded output, when there is one, and its standard
// error, when there is one, and returns code.
func replay(name, thread string, code int) int {
	if _, err := os.Stat(filepath.Join(os.Getenv(envData), name+".jsonl")); err == nil {
		replayLines(name, thread, -1)
	}
	if data, err := os.ReadFile(filepath.Join(os.Getenv(envData), name+".stderr")); err == nil {
		os.Stderr.WriteString(fill(string(data), thread))
	}
	return code
}

// fill replaces $THREAD, $WORKDIR and $RESET (an hour ahead, as the client
// words a reset time) in a recorded text.
func fill(text, thread string) string {
	wd, _ := os.Getwd()
	quoted, _ := json.Marshal(wd)
	reset := time.Now().Add(time.Hour).Format("Jan 2th, 2006 3:04 PM")
	return strings.NewReplacer("$THREAD", thread, "$WORKDIR", string(quoted[1:len(quoted)-1]), "$RESET", reset).Replace(text)
}

// replayLines writes the first n lines of a recorded output (all for n < 0).
func replayLines(name, thread string, n int) {
	data, err := os.ReadFile(filepath.Join(os.Getenv(envData), name+".jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake:", err)
		os.Exit(2)
	}
	lines := strings.SplitAfter(fill(string(data), thread), "\n")
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
	return []string{"--client", self(t), "--env", envFake, "--env", envData, "--env", envRecord, "--env", envNoCatalog}
}

const testSchema = `{"type":"object","properties":{"outcome":{"type":"string","enum":["ok","failed"]},"note":{"type":"string"}},"required":["outcome"]}`

func testStep(dir string) adapter.Step {
	return adapter.Step{RunID: "7", NodeID: "draft", Attempt: 1, Prompt: "write the note", Workdir: dir, Schema: json.RawMessage(testSchema)}
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

// runs returns the recorded invocations of "codex exec".
func runs(t *testing.T, path string) []invocation {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var list []invocation
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var inv invocation
		if err := json.Unmarshal([]byte(line), &inv); err != nil {
			t.Fatal(err)
		}
		if len(inv.Args) > 0 && inv.Args[0] == "exec" {
			list = append(list, inv)
		}
	}
	return list
}

func record(t *testing.T) string {
	rec := filepath.Join(t.TempDir(), "record.jsonl")
	t.Setenv(envRecord, rec)
	return rec
}

func sameDir(a, b string) bool {
	r, ok := adapter.Rel(a, b)
	return ok && r == "."
}

func contains(list []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(list); i++ {
		ok := true
		for j := range seq {
			ok = ok && list[i+j] == seq[j]
		}
		if ok {
			return true
		}
	}
	return false
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
	for _, f := range []string{adapter.FeatureResume, adapter.FeatureProtect, adapter.FeatureCheck, adapter.FeatureProgress, adapter.FeatureCatalog, adapter.FeatureRepair} {
		if !d.Has(f) {
			t.Errorf("description %+v lacks %s", d, f)
		}
	}
	if d.Name != "codex" || d.Has(adapter.FeatureUsage) {
		t.Fatalf("description %+v", d)
	}
}

// The command line keeps the user's setup out, defines the step's
// permission profile, carries model and effort, gives the client the strict
// form of the schema, reads the prompt from standard input, and the client
// gets only the environment it needs.
func TestCommandLineAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	rec := record(t)
	work := filepath.Join(dir, ".hetman", "campaigns", "2", "work")
	os.MkdirAll(work, 0o755)
	t.Setenv("OPENAI_API_KEY", "not-a-key")
	t.Setenv("CODEX_API_KEY", "not-a-key")
	t.Setenv("CODEX_HOME", filepath.Join(dir, "home"))
	t.Setenv("HETMAN_RUN_ID", "1")
	st := testStep(dir)
	st.Model, st.Effort = "gpt-5.6-luna", "low"
	st.Tools = []string{"write", "network"}
	st.Protect = []string{filepath.Join(dir, ".hetman"), filepath.Join(dir, "orders.yaml")}
	st.Writable = []string{work}
	stepRun(t, "ok", st)
	inv := runs(t, rec)
	if len(inv) != 1 {
		t.Fatalf("invocations %+v", inv)
	}
	a := inv[0].Args
	if a[0] != "exec" || a[1] == "resume" || a[len(a)-1] != "-" {
		t.Errorf("arguments %q", a)
	}
	for _, seq := range [][]string{
		{"--json", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check"},
		{"-c", `approval_policy="never"`}, {"-c", `approvals_reviewer="user"`}, {"-c", `forced_login_method="chatgpt"`},
		{"-c", "allow_login_shell=false"}, {"-c", `history.persistence="none"`},
		{"--disable", "multi_agent"}, {"--disable", "plugins"}, {"--disable", "apps"}, {"--disable", "hooks"},
		{"-c", `default_permissions="hetman"`}, {"-c", `permissions.hetman.extends=":workspace"`},
		{"-c", "permissions.hetman.network.enabled=true"}, {"-c", `web_search="disabled"`},
		{"-C", dir}, {"-m", "gpt-5.6-luna"}, {"-c", `model_reasoning_effort="low"`},
	} {
		if !contains(a, seq...) {
			t.Errorf("arguments lack %q:\n%q", seq, a)
		}
	}
	for _, bad := range []string{"--dangerously-bypass-approvals-and-sandbox", "danger-full-access", "shell_tool"} {
		if strings.Contains(strings.Join(a, " "), bad) {
			t.Errorf("arguments hold %s", bad)
		}
	}
	fs := ""
	for i, x := range a {
		if i > 0 && a[i-1] == "-c" && strings.HasPrefix(x, "permissions.hetman.filesystem=") {
			fs = x
		}
	}
	q := func(p string) string { return tomlString(p) }
	for _, want := range []string{q(dir) + `="write"`, q(work) + `="write"`, q(filepath.Join(dir, ".hetman")) + `="read"`, q(filepath.Join(dir, "orders.yaml")) + `="read"`} {
		if !strings.Contains(fs, want) {
			t.Errorf("profile %s lacks %s", fs, want)
		}
	}
	if inv[0].Schema == "" || !strings.Contains(inv[0].Schema, `"additionalProperties":false`) || !strings.Contains(inv[0].Schema, `"required":["note","outcome"]`) {
		t.Errorf("schema given to the client: %s", inv[0].Schema)
	}
	p := inv[0].Prompt
	if !strings.HasPrefix(p, "write the note\n\n---\nWhat this step can do") || !strings.Contains(p, "you work through shell commands") || !strings.Contains(p, "only in "+dir+", "+work) ||
		!strings.Contains(p, "never write in "+filepath.Join(dir, ".hetman")) || !strings.Contains(p, "commands can reach the network") ||
		!strings.Contains(p, "\n\n---\nAnswer format:") || !strings.Contains(p, testSchema) {
		t.Errorf("prompt on standard input:\n%s", p)
	}
	if r, ok := adapter.Rel(dir, inv[0].Dir); !ok || r != "." {
		t.Errorf("client ran in %s, want %s", inv[0].Dir, dir)
	}
	env := " " + strings.Join(inv[0].Env, " ") + " "
	for _, name := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "HETMAN_RUN_ID", envSelf} {
		if strings.Contains(env, " "+name+" ") {
			t.Errorf("the client got %s", name)
		}
	}
	for _, name := range []string{envFake, "CODEX_HOME"} {
		if !strings.Contains(env, " "+name+" ") {
			t.Errorf("the client did not get %s", name)
		}
	}
}

// Without tools the shell is switched off and the profile is read-only;
// without a model or effort the client's defaults apply.
func TestNoTools(t *testing.T) {
	rec := record(t)
	st := testStep(t.TempDir())
	st.Protect = []string{filepath.Join(st.Workdir, ".hetman")}
	stepRun(t, "ok", st)
	a := runs(t, rec)[0].Args
	for _, seq := range [][]string{{"--disable", "shell_tool"}, {"--disable", "unified_exec"}, {"-c", `permissions.hetman.extends=":read-only"`}} {
		if !contains(a, seq...) {
			t.Errorf("arguments lack %q: %q", seq, a)
		}
	}
	joined := strings.Join(a, " ")
	for _, bad := range []string{"filesystem=", "-m ", "model_reasoning_effort", "network.enabled"} {
		if strings.Contains(joined, bad) {
			t.Errorf("arguments hold %s: %s", bad, joined)
		}
	}
	if p := runs(t, rec)[0].Prompt; !strings.Contains(p, "- no shell commands and no file edits") {
		t.Errorf("prompt:\n%s", p)
	}
}

// read gives the shell in a read-only profile, write(FOLDER) a profile that
// writes only there and in the temporary folders, web_search the search tool.
func TestReadAndWriteFolder(t *testing.T) {
	rec := record(t)
	st := testStep(t.TempDir())
	st.Tools = []string{"read", "web_search"}
	stepRun(t, "ok", st)
	a := runs(t, rec)[0].Args
	if !contains(a, "-c", `permissions.hetman.extends=":read-only"`) || !contains(a, "-c", `web_search="live"`) || contains(a, "--disable", "shell_tool") {
		t.Errorf("read: %q", a)
	}

	os.Remove(rec)
	os.Mkdir(filepath.Join(st.Workdir, "docs"), 0o755)
	st.Tools = []string{"write(docs/**)"}
	stepRun(t, "ok", st)
	a = runs(t, rec)[0].Args
	// A folder behind a symbolic link (the temporary folder on macOS, a
	// short name on Windows) is written in both spellings.
	want := "permissions.hetman.filesystem={"
	for _, p := range adapter.Spellings(filepath.Join(st.Workdir, "docs")) {
		want += tomlString(p) + `="write",`
	}
	want += `":slash_tmp"="write",":tmpdir"="write"}`
	if !contains(a, "-c", `permissions.hetman.extends=":read-only"`) || !contains(a, "-c", want) {
		t.Errorf("write(docs): %q", a)
	}

	os.Remove(rec)
	st.Tools = []string{"write(missing)"}
	f := final(stepRun(t, "ok", st))
	if f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, "not an existing folder") {
		t.Errorf("missing folder: %+v", f)
	}
	if len(runs(t, rec)) > 0 {
		t.Error("the client ran for a folder that does not exist")
	}
}

// A run with tool use: the session comes first, the log follows the work, a
// write the sandbox refused is reported as denied, and a null the strict
// schema allowed for an optional property is dropped from the result.
func TestNormalRun(t *testing.T) {
	msgs := stepRun(t, "normal", testStep(t.TempDir()))
	if msgs[0].Type != adapter.TypeSession || msgs[0].ID != newThread {
		t.Errorf("first message %+v, want the session", msgs[0])
	}
	log := logText(msgs)
	for _, want := range []string{"session " + newThread + ", model the client's default, tools: none",
		"> shell echo ok > ok2.txt\n  < (empty)", "> shell echo x > .hetman/y\ndenied: shell echo x > .hetman/y: zsh:1: read-only file system: .hetman/y\n",
		"> apply_patch add note.txt\n  < applied", "finished after 4 model steps in "} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if !strings.Contains(log, ", 3 tool calls, 24113 tokens in, 285 out") || strings.Contains(log, `{"type"`) || strings.Contains(log, "$WORKDIR") {
		t.Errorf("log:\n%s", log)
	}
	f := final(msgs)
	if f.Type != adapter.TypeResult || string(f.Output) != `{"outcome":"ok"}` || len(f.Denied) != 1 {
		t.Errorf("final %+v", f)
	}
}

func TestResume(t *testing.T) {
	rec := record(t)
	st := testStep(t.TempDir())
	id := "00000000-0000-7000-8000-000000000011"
	st.Session = &id
	msgs := stepRun(t, "resume", st)
	inv := runs(t, rec)[0]
	if !inv.resume() || inv.session() != id || contains(inv.Args, "-C", st.Workdir) || !sameDir(st.Workdir, inv.Dir) {
		t.Errorf("arguments %q in %s do not continue the session", inv.Args, inv.Dir)
	}
	if got := sessions(msgs); len(got) != 1 || got[0] != id {
		t.Errorf("sessions %v", got)
	}
	if f := final(msgs); f.Type != adapter.TypeResult || string(f.Output) != `{"note":"ok.txt created.","outcome":"ok"}` {
		t.Errorf("final %+v", f)
	}
}

// A thread the client cannot find gives a new conversation; so does a
// session that is not a thread id, without asking the client, which would
// take it for a thread name.
func TestResumeMissingSession(t *testing.T) {
	rec := record(t)
	st := testStep(t.TempDir())
	id := "00000000-0000-7000-8000-000000000000"
	st.Session = &id
	msgs := stepRun(t, "resume-missing", st)
	inv := runs(t, rec)
	if len(inv) != 2 || !inv[0].resume() || inv[1].resume() {
		t.Fatalf("invocations %+v, want a resume and then a new conversation", inv)
	}
	if !strings.Contains(logText(msgs), "starting a new conversation") || final(msgs).Type != adapter.TypeResult {
		t.Errorf("messages %+v", msgs)
	}

	os.Remove(rec)
	name := "some-name"
	st.Session = &name
	msgs = stepRun(t, "ok", st)
	if inv := runs(t, rec); len(inv) != 1 || inv[0].resume() {
		t.Errorf("invocations %+v", inv)
	}
	if !strings.Contains(logText(msgs), "not a thread id") {
		t.Errorf("log:\n%s", logText(msgs))
	}
}

// A repair round continues the session with the engine's message and the
// answer format, not the sandbox note of a first message, and a turn that
// ends without the answer again is the engine's to judge.
func TestRepair(t *testing.T) {
	rec := record(t)
	st := testStep(t.TempDir())
	id := "00000000-0000-7000-8000-000000000011"
	st.Session, st.Repair, st.Tools = &id, 2, []string{"read"}
	st.Prompt = "Your answer was not accepted. Answer again with only the corrected JSON object."
	msgs := stepRun(t, "resume", st)
	inv := runs(t, rec)
	if len(inv) != 1 || inv[0].session() != id {
		t.Fatalf("invocations %+v", inv)
	}
	if p := inv[0].Prompt; p != st.Prompt+answerFormat(st) {
		t.Errorf("prompt %q", p)
	}
	if !strings.Contains(logText(msgs), "repair 2: asking session "+id) || final(msgs).Type != adapter.TypeResult {
		t.Errorf("messages %+v", msgs)
	}

	os.Remove(rec)
	if f := final(stepRun(t, "empty-in-session", st)); f.Type != adapter.TypeError {
		t.Errorf("empty turn: %+v", f)
	}
	if inv := runs(t, rec); len(inv) != 1 {
		t.Errorf("invocations %+v, want no continuation of a repair round", inv)
	}
}

// A repair round never starts a new conversation: without a session, with
// one that is not a thread id, with one the client cannot find, and when
// the client reports another thread, it fails.
func TestRepairNeedsItsSession(t *testing.T) {
	rec := record(t)
	st := testStep(t.TempDir())
	st.Repair = 1
	if f := final(stepRun(t, "resume", st)); f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, "needs the session") {
		t.Errorf("without a session: %+v", f)
	}
	name := adaptertest.MissingSession
	st.Session = &name
	if f := final(stepRun(t, "resume", st)); f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, "not a thread id") {
		t.Errorf("not a thread id: %+v", f)
	}
	if inv := runs(t, rec); len(inv) > 0 {
		t.Errorf("the client ran: %+v", inv)
	}
	id := "00000000-0000-7000-8000-000000000000"
	st.Session = &id
	if f := final(stepRun(t, "resume-missing", st)); f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, "cannot be corrected") {
		t.Errorf("missing session: %+v", f)
	}
	if inv := runs(t, rec); len(inv) != 1 {
		t.Errorf("invocations %+v, want only the one that continues the session", inv)
	}
	f := final(stepRun(t, "newthread", st))
	if f.Kind != adapter.ErrPermanent || !strings.Contains(f.Message, "cannot be corrected") {
		t.Errorf("another thread: %+v", f)
	}
}

// A turn that ends without the answer after a refused write is continued in
// the same session, with the refused call and the sandbox named.
func TestContinuation(t *testing.T) {
	rec := record(t)
	st := testStep(t.TempDir())
	st.Tools = []string{"read"}
	msgs := stepRun(t, "empty-then-answer", st)
	inv := runs(t, rec)
	if len(inv) != 2 || inv[1].session() != newThread {
		t.Fatalf("invocations %+v", inv)
	}
	p := inv[1].Prompt
	if !strings.Contains(p, "ended without an answer") || !strings.Contains(p, "- shell touch made.txt: touch: cannot touch 'made.txt': Read-only file system") ||
		!strings.Contains(p, "write nothing") || !strings.Contains(p, "you work through shell commands") || !strings.Contains(p, "Answer format:") {
		t.Errorf("continuation:\n%s", p)
	}
	f := final(msgs)
	if f.Type != adapter.TypeResult || len(f.Denied) != 1 {
		t.Errorf("final %+v", f)
	}
}

// When the server refuses the schema, the turn runs again without one and
// the answer's last JSON object is taken; a schema with no strict form is
// never given to the client.
func TestSchemaFallback(t *testing.T) {
	rec := record(t)
	st := testStep(t.TempDir())
	msgs := stepRun(t, "badschema", st)
	inv := runs(t, rec)
	if len(inv) != 2 || inv[0].Schema == "" || inv[1].Schema != "" || inv[1].value("--output-schema") != "" {
		t.Fatalf("invocations %+v", inv)
	}
	if f := final(msgs); f.Type != adapter.TypeResult || string(f.Output) != `{"outcome":"ok","extra":{"any":1}}` {
		t.Errorf("final %+v", f)
	}

	os.Remove(rec)
	st.Schema = json.RawMessage(`{"type":"object","properties":{"outcome":{"type":"string"}},"additionalProperties":true}`)
	stepRun(t, "plain", st)
	if inv := runs(t, rec); len(inv) != 1 || inv[0].value("--output-schema") != "" {
		t.Errorf("invocations %+v", inv)
	}
}

func TestStrictSchema(t *testing.T) {
	dialogue, err := ask.DialogueSchema([]byte(adaptertest.Schema))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := strictSchema(dialogue)
	if !ok {
		t.Fatalf("no strict form of %s", dialogue)
	}
	var v map[string]any
	json.Unmarshal(s, &v)
	if req := fmt.Sprint(v["required"]); req != "[outcome questions]" || v["additionalProperties"] != false {
		t.Errorf("strict form %s", s)
	}
	if !strings.Contains(string(s), `"questions":{"anyOf":[{`) || !strings.Contains(string(s), `{"type":"null"}`) {
		t.Errorf("an optional property is not nullable: %s", s)
	}
	for _, bad := range []string{`{"type":"object","properties":{"a":{"type":"object"}}}`, `{"type":"object","properties":{"a":{"$ref":"#/x"}}}`,
		`{"type":"object","additionalProperties":{"type":"string"},"properties":{}}`, `{"type":"array"}`} {
		if _, ok := strictSchema(json.RawMessage(bad)); ok {
			t.Errorf("strict form of %s", bad)
		}
	}
	got := dropNulls(json.RawMessage(`{"outcome":"ok","questions":null,"x":{"a":null}}`), dialogue)
	if string(got) != `{"outcome":"ok","x":{"a":null}}` {
		t.Errorf("dropNulls = %s", got)
	}
}

func TestFailures(t *testing.T) {
	cases := []struct {
		scenario, kind, message string
		retryAt                 bool
	}{
		{"limit", adapter.ErrLimit, "hit your usage limit", true},
		{"limit429", adapter.ErrLimit, "429 Too Many Requests", false},
		{"noauth", adapter.ErrPermanent, "authentication token has expired", false},
		{"badmodel", adapter.ErrPermanent, "model is not supported", false},
		{"offline", adapter.ErrOffline, "workspace routing discovery failed", false},
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
			if c.retryAt && (at.Before(time.Now().Add(55*time.Minute)) || at.After(time.Now().Add(65*time.Minute))) {
				t.Errorf("retry_at %q, want about an hour ahead", f.RetryAt)
			}
			if !c.retryAt && f.RetryAt != "" {
				t.Errorf("retry_at %q, want none", f.RetryAt)
			}
		})
	}
	log := logText(stepRun(t, "offline", testStep(t.TempDir())))
	if !strings.Contains(log, "request failed (workspace routing discovery failed); retry 2 of 5") {
		t.Errorf("log:\n%s", log)
	}
}

func TestResetAt(t *testing.T) {
	now := time.Date(2026, 11, 15, 13, 0, 0, 0, time.Local)
	for text, want := range map[string]time.Time{
		"try again at Nov 15th, 2026 3:45 PM.": time.Date(2026, 11, 15, 15, 45, 0, 0, time.Local),
		"try again at Nov 16, 2026 9:05 AM":    time.Date(2026, 11, 16, 9, 5, 0, 0, time.Local),
		"or try again at 12:30 PM.":            time.Date(2026, 11, 16, 12, 30, 0, 0, time.Local),
		"try again in 5 minutes":               now.Add(5 * time.Minute),
		"try again at Nov 1st, 2026 3:45 PM":   {},
	} {
		if got := resetAt(text, now); !got.Equal(want) {
			t.Errorf("resetAt(%q) = %v, want %v", text, got, want)
		}
	}
}

// A patch the client refused shows only in its error log; it is reported
// as denied, and the answer without an object after it is continued.
func TestPatchRefused(t *testing.T) {
	st := testStep(t.TempDir())
	st.Tools = []string{"write"}
	msgs := stepRun(t, "patch_refused", st)
	log := logText(msgs)
	if !strings.Contains(log, "denied: apply_patch: writing outside of the project; rejected by user approval settings") {
		t.Errorf("log:\n%s", log)
	}
	if f := final(msgs); f.Kind != adapter.ErrTransient || len(f.Denied) == 0 {
		t.Errorf("final %+v", f)
	}
}

func TestGarbageIsLogged(t *testing.T) {
	log := logText(stepRun(t, "garbage", testStep(t.TempDir())))
	if !strings.Contains(log, "client: this line is not JSON") {
		t.Errorf("log:\n%s", log)
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
	for _, name := range []string{"OPENAI_API_KEY", "codex_api_key", "OPENAI_BASE_URL"} {
		if _, _, err := parseArgs([]string{"--env", name}); err == nil {
			t.Errorf("--env %s was accepted", name)
		}
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
	if f := final(msgs); f.Type != adapter.TypeError || f.Kind != adapter.ErrTransient || f.Message != "cancelled" {
		t.Fatalf("final %+v", f)
	}
}

// Progress is sent while a turn is under way without an open tool call.
func TestProgress(t *testing.T) {
	old := progressEvery
	progressEvery = 50 * time.Millisecond
	defer func() { progressEvery = old }()
	t.Setenv(envFake, "hang")
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		run(fakeArgs(t), inR, outW, io.Discard)
		outW.Close()
	}()
	inW.Write(adapter.EncodeStart(testStep(t.TempDir())))
	br := bufio.NewReader(outR)
	progress := 0
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			break
		}
		m, _ := adapter.ParseMessage(bytes.TrimSpace(line))
		if m.Type == adapter.TypeProgress {
			if progress++; progress == 3 {
				inW.Write(adapter.EncodeCancel())
			}
		}
	}
	if progress < 3 {
		t.Errorf("%d progress messages", progress)
	}
}

func TestExtractObject(t *testing.T) {
	cases := map[string]string{
		`{"outcome":"ok"}`: `{"outcome":"ok"}`,
		"Done.\n```json\n{\"outcome\": \"ok\", \"a\": {\"b\": 1}}\n```": `{"outcome": "ok", "a": {"b": 1}}`,
		`e.g. {"outcome":"x"} but finally {"outcome":"ok"}`:             `{"outcome":"ok"}`,
		`no object here [1,2]`: ``,
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
// makes about seven small model requests (step, resume, two dialogue
// rounds, two of the repair check and the cancelled step), so it runs only
// when HETMAN_CODEX_REAL=1, with the model in HETMAN_CODEX_MODEL (a small
// one by default) at low effort:
//
//	HETMAN_CODEX_REAL=1 go test ./cmd/hetman-adapter-codex -run RealClient -v
func TestRealClient(t *testing.T) {
	if os.Getenv(envReal) != "1" {
		t.Skip("set " + envReal + "=1 to run the conformance suite against the real client")
	}
	model := os.Getenv(envModel)
	if model == "" {
		model = "gpt-5.6-luna"
	}
	adaptertest.Run(t, adaptertest.Config{Command: []string{self(t), "adapter"}, Model: model, Effort: "low",
		Timeout: 3 * time.Minute, CancelWithin: 15 * time.Second})
}
