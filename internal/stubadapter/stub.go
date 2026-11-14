// Package stubadapter is an executor adapter that runs no model. It speaks
// protocol version 0 and does what a plan file tells it, so that flows with
// agent nodes can be tried and tested without any real client.
//
// The adapter is started as "PROGRAM [PLAN]". The environment variable
// STUB_PLAN, when set, names the plan instead. A plan is a JSON object:
//
//	{
//	  "name": "stub",                 name to describe itself with (default "stub")
//	  "features": ["resume"],          features to declare (default ["resume"])
//	  "protocol": [0],                 protocol versions to declare (default [0])
//	  "trace": "stub-trace.jsonl",     file to append every start message's step to
//	  "nodes": {
//	    "review": [[MOVE, ...], ...],  moves per attempt: the first list for attempt 1,
//	    "*": [[MOVE, ...]]             the second for attempt 2, the last for any later one;
//	  }                                "*" serves nodes not listed
//	}
//
// Relative paths are taken from the step's working directory. A move is an
// object with one of these keys:
//
//	{"log": "text"}                      send a log message
//	{"session": "id"}                    send a session message
//	{"stderr": "text"}                   write a line to standard error
//	{"sleep": "2s"}                      wait (a cancel message ends the wait)
//	{"touch": "path"}                    create an empty file, for tests to wait on
//	{"result": {...}}                    send a result with this output
//	{"error": "KIND", "message": "...", "retry_in": "30s", "retry_at": "RFC 3339 time"}
//	                                     send an error; retry_in or retry_at for a limit
//
// A result or an error ends the moves; the adapter then exits with the code
// given as "code" in the same move (default 0).
//
//	{"raw": "text"}                      write a line that is not a protocol message
//	{"exit": N}                          exit with code N at once
//	{"hang": true}                       wait until cancelled or killed
//
// With no plan, or no moves for a node, the adapter sends a session id and a
// result whose outcome is the first one the step's schema allows. On a cancel message it
// sends a transient error and exits. When the moves run out without a result
// or an error, it exits with code 0, which the engine counts as a crash.
package stubadapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hetman/internal/adapter"
)

// EnvPlan names a plan file and overrides the plan given as an argument.
const EnvPlan = "STUB_PLAN"

// Plan scripts the adapter.
type Plan struct {
	Name     string              `json:"name"`
	Features *[]string           `json:"features"`
	Protocol []int               `json:"protocol"`
	Trace    string              `json:"trace"`
	Nodes    map[string][][]Move `json:"nodes"`
}

// Move is one thing the adapter does.
type Move struct {
	Log     *string         `json:"log"`
	Session string          `json:"session"`
	Stderr  string          `json:"stderr"`
	Sleep   string          `json:"sleep"`
	Touch   string          `json:"touch"`
	Result  json.RawMessage `json:"result"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
	RetryIn string          `json:"retry_in"`
	RetryAt string          `json:"retry_at"`
	Raw     *string         `json:"raw"`
	Exit    *int            `json:"exit"`
	Code    int             `json:"code"`
	Hang    bool            `json:"hang"`
}

type stub struct {
	out    *json.Encoder
	stdout io.Writer
	stderr io.Writer
	cancel chan struct{}
}

// Main runs the adapter and returns its exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	describeOnly := false
	if n := len(args); n > 0 && args[n-1] == adapter.DescribeFlag {
		describeOnly, args = true, args[:n-1]
	}
	planPath := os.Getenv(EnvPlan)
	if planPath == "" && len(args) > 0 {
		planPath = args[0]
	}
	var plan Plan
	if planPath != "" {
		data, err := os.ReadFile(planPath)
		if err == nil {
			err = json.Unmarshal(data, &plan)
		}
		if err != nil {
			fmt.Fprintf(stderr, "stub: plan %s: %v\n", planPath, err)
			return 2
		}
	}
	if describeOnly {
		d := adapter.Description{Protocol: plan.Protocol, Name: plan.Name, Features: []string{adapter.FeatureResume}}
		if d.Protocol == nil {
			d.Protocol = []int{adapter.Version}
		}
		if d.Name == "" {
			d.Name = "stub"
		}
		if plan.Features != nil {
			d.Features = *plan.Features
		}
		data, _ := json.Marshal(d)
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}

	in := bufio.NewReader(stdin)
	line, err := in.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		fmt.Fprintln(stderr, "stub: no start message")
		return 2
	}
	var start adapter.Start
	if err := json.Unmarshal(line, &start); err != nil || start.Type != adapter.TypeStart || start.V != adapter.Version {
		fmt.Fprintf(stderr, "stub: expected a start message, got %q\n", strings.TrimSpace(string(line)))
		return 2
	}
	s := &stub{out: json.NewEncoder(stdout), stdout: stdout, stderr: stderr, cancel: make(chan struct{})}
	go func() {
		for {
			line, err := in.ReadBytes('\n')
			var m struct{ Type string }
			if json.Unmarshal(line, &m) == nil && m.Type == adapter.TypeCancel {
				close(s.cancel)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	step := start.Step
	if plan.Trace != "" {
		trace(local(step.Workdir, plan.Trace), step)
	}
	moves, planned := pick(plan, step)
	if !planned {
		fmt.Fprintf(stderr, "stub: no plan for %s attempt %d; reporting the first outcome the schema allows\n", step.NodeID, step.Attempt)
		moves = []Move{{Session: fmt.Sprintf("stub-%s-%s-%d", step.RunID, step.NodeID, step.Attempt)}, {Result: firstOutcome(step.Schema)}}
	}
	if step.Session != nil {
		fmt.Fprintf(stderr, "stub: continuing session %s\n", *step.Session)
	}
	for _, m := range moves {
		if code, done := s.do(m, step); done {
			return code
		}
	}
	return 0
}

// pick returns the moves for the attempt of a step.
func pick(plan Plan, step adapter.Step) ([]Move, bool) {
	list, ok := plan.Nodes[step.NodeID]
	if !ok {
		list = plan.Nodes["*"]
	}
	if len(list) == 0 {
		return nil, false
	}
	i := step.Attempt - 1
	if i >= len(list) {
		i = len(list) - 1
	}
	if i < 0 {
		i = 0
	}
	return list[i], true
}

// firstOutcome builds an output with the first outcome the schema allows.
func firstOutcome(schema json.RawMessage) json.RawMessage {
	var sc struct {
		Properties map[string]struct {
			Enum []any `json:"enum"`
		} `json:"properties"`
	}
	outcome := any("ok")
	if json.Unmarshal(schema, &sc) == nil {
		if e := sc.Properties["outcome"].Enum; len(e) > 0 {
			outcome = e[0]
		}
	}
	data, _ := json.Marshal(map[string]any{"outcome": outcome})
	return data
}

func local(workdir, p string) string {
	if filepath.IsAbs(p) || workdir == "" {
		return p
	}
	return filepath.Join(workdir, filepath.FromSlash(p))
}

func trace(path string, step adapter.Step) {
	data, _ := json.Marshal(step)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(data, '\n'))
}

func (s *stub) send(m adapter.Message) {
	m.V = adapter.Version
	s.out.Encode(m)
}

// cancelled sends the reply to a cancel message.
func (s *stub) cancelled() (int, bool) {
	s.send(adapter.Message{Type: adapter.TypeError, Kind: adapter.ErrTransient, Message: "cancelled"})
	return 0, true
}

// do performs one move and reports whether the adapter is finished, with
// its exit code.
func (s *stub) do(m Move, step adapter.Step) (int, bool) {
	switch {
	case m.Log != nil:
		s.send(adapter.Message{Type: adapter.TypeLog, Text: *m.Log})
	case m.Session != "":
		s.send(adapter.Message{Type: adapter.TypeSession, ID: m.Session})
	case m.Stderr != "":
		fmt.Fprintln(s.stderr, m.Stderr)
	case m.Sleep != "":
		d, err := time.ParseDuration(m.Sleep)
		if err != nil {
			fmt.Fprintf(s.stderr, "stub: bad sleep %q\n", m.Sleep)
			return 2, true
		}
		select {
		case <-time.After(d):
		case <-s.cancel:
			return s.cancelled()
		}
	case m.Touch != "":
		os.WriteFile(local(step.Workdir, m.Touch), nil, 0o644)
	case len(m.Result) > 0:
		s.send(adapter.Message{Type: adapter.TypeResult, Output: m.Result})
		return m.Code, true
	case m.Error != "":
		msg := adapter.Message{Type: adapter.TypeError, Kind: m.Error, Message: m.Message, RetryAt: m.RetryAt}
		if m.RetryIn != "" {
			d, err := time.ParseDuration(m.RetryIn)
			if err != nil {
				fmt.Fprintf(s.stderr, "stub: bad retry_in %q\n", m.RetryIn)
				return 2, true
			}
			msg.RetryAt = time.Now().Add(d).UTC().Format(time.RFC3339Nano)
		}
		s.send(msg)
		return m.Code, true
	case m.Raw != nil:
		fmt.Fprintln(s.stdout, *m.Raw)
	case m.Exit != nil:
		return *m.Exit, true
	case m.Hang:
		<-s.cancel
		return s.cancelled()
	}
	return 0, false
}
