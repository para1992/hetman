// Package adaptertest is the conformance suite for executor adapters: the
// checks any adapter must pass to be driven by the engine.
//
// Run it from a test with the adapter's command:
//
//	adaptertest.Run(t, adaptertest.Config{Command: []string{"/path/to/adapter", "--flag"}})
//
// The suite in this package's own tests runs it against the adapter named
// by the environment variable HETMAN_ADAPTER, a JSON list such as
// ["/path/to/adapter"], and is skipped when the variable is not set:
//
//	HETMAN_ADAPTER='["/path/to/adapter"]' go test ./internal/adapter/adaptertest -run External -v
//
// The steps it sends use the node ids "conformance", "conformance-resume",
// "conformance-dialogue" and "conformance-cancel" and ask for a trivial
// answer, so an adapter for a real client makes real but tiny requests.
//
// Resume, dialogue, usage and catalog are optional capabilities: the resume
// and dialogue checks run only for an adapter that declares the resume
// feature, the usage and catalog checks only for one that declares that
// feature. Neither of these two makes a model request: the usage check asks
// the adapter for its client's usage of every window of the protocol, the
// catalog check for the models and efforts the client offers, and each
// checks the line printed. The dialogue check is what a dialogue step needs:
// a step that answers with questions, then the same conversation continued
// with the answers as the next message, ending in a final result.
package adaptertest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/ask"
	"hetman/internal/schema"
)

// EnvAdapter names the adapter command for the external conformance run.
const EnvAdapter = "HETMAN_ADAPTER"

// Config describes the adapter under test.
type Config struct {
	Command []string      // the program and its arguments, as in executors.NAME.run
	Dir     string        // working directory and step workdir; a temporary folder when empty
	Env     []string      // environment; the test process's own when nil
	Model   string        // passed through in start messages
	Effort  string        // passed through in start messages
	Tools   []string      // passed through in start messages
	Timeout time.Duration // limit for one step; 5 minutes when zero
	// CancelWithin is how soon the adapter must exit after a cancel
	// message; 30 seconds when zero.
	CancelWithin time.Duration
}

// Schema is the output schema of the conformance steps.
const Schema = `{"type":"object","properties":{"outcome":{"type":"string","enum":["ok"]}},"required":["outcome"]}`

// Prompt is the prompt of the conformance steps.
const Prompt = `This is a protocol check. Do not use any tools and do not change any files. Reply with exactly this JSON object and nothing else: {"outcome": "ok"}`

// DialoguePrompt is the prompt of the first step of the dialogue check.
const DialoguePrompt = `This is a protocol check. Do not use any tools and do not change any files. Reply with exactly this JSON object and nothing else: {"outcome": "questions", "questions": [{"id": "color", "question": "Which color?", "kind": "choice", "options": ["red", "blue"]}]}`

// DialogueAnswer is the message that continues the dialogue check.
const DialogueAnswer = `The person answered your questions: color = blue. Now reply with exactly this JSON object and nothing else: {"outcome": "ok"}`

// CancelPrompt is the prompt of the step that is cancelled.
const CancelPrompt = `This is a protocol check. Do not use any tools and do not change any files. Count from 1 to 2000 in words, one number per line, then reply with {"outcome": "ok"}.`

// Run runs every conformance check as a subtest.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	if len(cfg.Command) == 0 {
		t.Fatal("adaptertest: no adapter command")
	}
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.CancelWithin == 0 {
		cfg.CancelWithin = 30 * time.Second
	}
	var desc adapter.Description
	ok := t.Run("describe", func(t *testing.T) { desc = describe(t, cfg) })
	if !ok {
		return
	}
	var session string
	t.Run("step", func(t *testing.T) {
		s := run(t, cfg, desc, step(cfg, "conformance", Prompt, nil), false)
		session = s.session
		if s.terminal == nil || s.terminal.Type != adapter.TypeResult {
			t.Fatalf("the step must end with a result; got %+v", s.terminal)
		}
	})
	t.Run("resume", func(t *testing.T) {
		if !desc.Has(adapter.FeatureResume) {
			t.Skip("the adapter does not declare the resume feature")
		}
		if session == "" {
			t.Fatal("the adapter declares resume but reported no session in the step check")
		}
		s := run(t, cfg, desc, step(cfg, "conformance-resume", Prompt, &session), false)
		if s.terminal == nil || s.terminal.Type != adapter.TypeResult {
			t.Fatalf("a resumed step must end with a result; got %+v", s.terminal)
		}
	})
	t.Run("dialogue", func(t *testing.T) {
		if !desc.Has(adapter.FeatureResume) {
			t.Skip("the adapter does not declare the resume feature, which a dialogue needs")
		}
		dialogue, err := ask.DialogueSchema([]byte(Schema))
		if err != nil {
			t.Fatal(err)
		}
		first := step(cfg, "conformance-dialogue", DialoguePrompt, nil)
		first.Schema = dialogue
		s := run(t, cfg, desc, first, false)
		if s.terminal == nil || s.terminal.Type != adapter.TypeResult {
			t.Fatalf("the questions step must end with a result; got %+v", s.terminal)
		}
		if s.session == "" {
			t.Fatal("the questions step reported no session, so the conversation cannot be continued")
		}
		var out struct {
			Outcome   string          `json:"outcome"`
			Questions json.RawMessage `json:"questions"`
		}
		json.Unmarshal(s.terminal.Output, &out)
		if _, err := ask.Parse(out.Questions); out.Outcome != ask.Outcome || err != nil {
			// The answer is the model's; the engine would reject it and retry.
			t.Logf("note: the first answer is not a valid questions answer (%v): %s", err, s.terminal.Output)
		}
		next := step(cfg, "conformance-dialogue", DialogueAnswer, &s.session)
		next.Attempt, next.Schema = 2, dialogue
		s = run(t, cfg, desc, next, false)
		if s.terminal == nil || s.terminal.Type != adapter.TypeResult {
			t.Fatalf("the continued step must end with a result; got %+v", s.terminal)
		}
	})
	t.Run("usage", func(t *testing.T) {
		if !desc.Has(adapter.FeatureUsage) {
			t.Skip("the adapter does not declare the usage feature")
		}
		usage(t, cfg)
	})
	t.Run("catalog", func(t *testing.T) {
		if !desc.Has(adapter.FeatureCatalog) {
			t.Skip("the adapter does not declare the catalog feature")
		}
		catalog(t, cfg)
	})
	t.Run("cancel", func(t *testing.T) {
		run(t, cfg, desc, step(cfg, "conformance-cancel", CancelPrompt, nil), true)
	})
	t.Run("no start message", func(t *testing.T) {
		cmd := command(cfg, nil)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stdin = strings.NewReader("")
		if err := start(t, cmd); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(cfg.CancelWithin):
			cmd.Process.Kill()
			<-exited
			t.Fatalf("the adapter did not exit within %s when standard input closed without a start message", cfg.CancelWithin)
		}
		for _, line := range lines(out.Bytes()) {
			if m, err := adapter.ParseMessage(line); err == nil && m.Type == adapter.TypeResult {
				t.Fatalf("the adapter sent a result without a start message: %s", line)
			}
		}
	})
}

func command(cfg Config, extra []string) *exec.Cmd {
	cmd := exec.Command(cfg.Command[0], append(append([]string{}, cfg.Command[1:]...), extra...)...)
	cmd.Dir = cfg.Dir
	cmd.Env = cfg.Env
	return cmd
}

func start(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	var stderr bytes.Buffer
	if cmd.Stderr == nil {
		cmd.Stderr = &stderr
		t.Cleanup(func() {
			if stderr.Len() > 0 {
				t.Logf("adapter standard error:\n%s", stderr.String())
			}
		})
	}
	return cmd.Start()
}

func lines(data []byte) [][]byte {
	var out [][]byte
	for _, l := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			out = append(out, bytes.TrimRight(l, "\r"))
		}
	}
	return out
}

func describe(t *testing.T, cfg Config) adapter.Description {
	cmd := command(cfg, []string{adapter.DescribeFlag})
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := start(t, cmd); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("%s must exit with code 0: %v", adapter.DescribeFlag, err)
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("%s did not finish within 30s", adapter.DescribeFlag)
	}
	d, err := adapter.ParseDescription(out.Bytes())
	if err != nil {
		t.Fatalf("%s: %v\noutput: %s", adapter.DescribeFlag, err, out.String())
	}
	for _, f := range d.Features {
		if f == "" || strings.ContainsAny(f, " \t\n") {
			t.Errorf("feature %q is not a plain name", f)
		}
	}
	return d
}

// usage asks the adapter for its client's usage and checks what it prints.
func usage(t *testing.T, cfg Config) {
	cmd := command(cfg, append([]string{adapter.UsageFlag}, adapter.UsageWindows...))
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := start(t, cmd); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("%s must exit with code 0: %v", adapter.UsageFlag, err)
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("%s did not finish within 30s", adapter.UsageFlag)
	}
	u, err := adapter.ParseUsage(out.Bytes(), adapter.UsageWindows)
	if err != nil {
		t.Fatalf("%s: %v\noutput: %s", adapter.UsageFlag, err, out.String())
	}
	t.Logf("usage: %+v", u.Windows)
}

// catalog asks the adapter for its client's catalogue and checks what it
// prints: one valid line, quickly, without a model request.
func catalog(t *testing.T, cfg Config) {
	cmd := command(cfg, []string{adapter.CatalogFlag})
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := start(t, cmd); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("%s must exit with code 0: %v", adapter.CatalogFlag, err)
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("%s did not finish within 30s", adapter.CatalogFlag)
	}
	c, err := adapter.ParseCatalog(out.Bytes())
	if err != nil {
		t.Fatalf("%s: %v\noutput: %.500s", adapter.CatalogFlag, err, out.String())
	}
	if len(c.Models) == 0 {
		t.Logf("note: the catalogue lists no models")
	}
	t.Logf("catalogue: %d models, complete %v", len(c.Models), c.Complete)
}

func step(cfg Config, node, prompt string, session *string) adapter.Step {
	return adapter.Step{RunID: "1", NodeID: node, Attempt: 1, Prompt: prompt, Model: cfg.Model, Effort: cfg.Effort,
		Tools: cfg.Tools, Workdir: cfg.Dir, Schema: json.RawMessage(Schema), Session: session}
}

type stepRun struct {
	session  string
	terminal *adapter.Message
}

// run sends one start message and checks every line the adapter writes.
// With cancel, a cancel message follows as soon as the adapter has written
// its first message or a moment has passed, and the adapter must then exit
// within CancelWithin.
func run(t *testing.T, cfg Config, desc adapter.Description, st adapter.Step, cancel bool) stepRun {
	t.Helper()
	cmd := command(cfg, nil)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := start(t, cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(adapter.EncodeStart(st)); err != nil {
		t.Fatalf("writing the start message: %v", err)
	}
	type line struct {
		data []byte
		err  error
	}
	got := make(chan line, 64)
	go func() {
		br := bufio.NewReader(stdout)
		for {
			data, err := br.ReadBytes('\n')
			got <- line{data, err}
			if err != nil {
				return
			}
		}
	}()
	var r stepRun
	limit := time.NewTimer(cfg.Timeout)
	defer limit.Stop()
	var cancelAt <-chan time.Time
	var cancelled time.Time
	if cancel {
		cancelAt = time.After(500 * time.Millisecond)
	}
	sendCancel := func() {
		if cancelled.IsZero() {
			cancelled = time.Now()
			stdin.Write(adapter.EncodeCancel())
			limit.Reset(cfg.CancelWithin)
		}
	}
	for done := false; !done; {
		select {
		case l := <-got:
			if text := bytes.TrimSpace(l.data); len(text) > 0 {
				if r.terminal != nil {
					t.Errorf("the adapter wrote after its %s: %s", r.terminal.Type, text)
				}
				m, err := adapter.ParseMessage(text)
				if err != nil {
					t.Errorf("not a protocol message: %v: %s", err, text)
				} else {
					check(t, desc, m, &r)
					if cancel {
						sendCancel()
					}
				}
			}
			if l.err != nil {
				if l.err != io.EOF {
					t.Errorf("reading standard output: %v", l.err)
				}
				done = true
			}
		case <-cancelAt:
			sendCancel()
		case <-limit.C:
			cmd.Process.Kill()
			cmd.Wait()
			if cancel {
				t.Fatalf("the adapter did not exit within %s after a cancel message", cfg.CancelWithin)
			}
			t.Fatalf("the step did not end within %s", cfg.Timeout)
		}
	}
	stdin.Close()
	err = cmd.Wait()
	if cancel {
		return r
	}
	if r.terminal == nil {
		t.Fatal("the adapter ended without a result or an error message")
	}
	if err != nil {
		t.Fatalf("the adapter must exit with code 0 after its %s: %v", r.terminal.Type, err)
	}
	return r
}

// check checks one message beyond what ParseMessage already does.
func check(t *testing.T, desc adapter.Description, m adapter.Message, r *stepRun) {
	t.Helper()
	switch m.Type {
	case adapter.TypeProgress:
		if !desc.Has(adapter.FeatureProgress) {
			t.Errorf("the adapter sent a progress message without declaring the progress feature")
		}
	case adapter.TypeSession:
		if !desc.Has(adapter.FeatureResume) {
			t.Errorf("the adapter sent a session id without declaring the resume feature")
		}
		r.session = m.ID
	case adapter.TypeResult:
		r.terminal = &m
		var v any
		if err := json.Unmarshal(m.Output, &v); err != nil {
			t.Errorf("result output is not JSON: %v", err)
			return
		}
		if _, ok := v.(map[string]any); !ok {
			t.Errorf("result output must be a JSON object, got %s", m.Output)
			return
		}
		sc, _ := schema.Parse([]byte(Schema))
		if problems := sc.Validate(v); len(problems) > 0 {
			// The engine rejects such output and retries; it is the model's
			// answer, not a protocol fault, so it is only reported.
			t.Logf("note: the output does not match the step's schema: %v", problems)
		}
	case adapter.TypeError:
		r.terminal = &m
		t.Logf("the adapter reported an error of kind %s: %s", m.Kind, m.Message)
	}
}

// Command reads the adapter command for the external conformance run from
// EnvAdapter; ok is false when it is not set.
func Command() (argv []string, ok bool, err error) {
	v := os.Getenv(EnvAdapter)
	if v == "" {
		return nil, false, nil
	}
	if err := json.Unmarshal([]byte(v), &argv); err != nil || len(argv) == 0 {
		return nil, true, err
	}
	return argv, true, nil
}
