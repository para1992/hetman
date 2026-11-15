// Package stubadapter is an executor adapter that runs no model. It speaks
// protocol version 0 and does what a plan file tells it, so that flows with
// agent nodes can be tried and tested without any real client.
//
// The adapter is started as "PROGRAM [PLAN]". The environment variable
// STUB_PLAN, when set, names the plan instead. A plan is a JSON object:
//
//	{
//	  "name": "stub",                 name to describe itself with (default "stub")
//	  "features": ["resume"],          features to declare (default ["resume"], and
//	                                   "usage" when the plan has usage)
//	  "protocol": [0],                 protocol versions to declare (default [0])
//	  "trace": "stub-trace.jsonl",     file to append every start message's step to
//	  "reject_tools": ["Write(x)"],    tools entries a check (--check) reports as problems
//	  "usage": {                       what --usage reports, per window asked for;
//	    "five_hour": {"used": 0.4, "resets_in": "2h"},   a window not listed is
//	    "weekly": {"used": 0.9, "resets_at": "RFC 3339"} left out
//	  },
//	  "when": [                        moves chosen by the prompt, before "nodes":
//	    {"prompt": "text", "moves": [[MOVE, ...], ...]}  the first entry whose text is
//	  ],                               part of the prompt is used, by attempt as below
//	  "nodes": {
//	    "review": [[MOVE, ...], ...],  moves per attempt: the first list for attempt 1,
//	    "*": [[MOVE, ...]]             the second for attempt 2, the last for any later one;
//	  },                               "*" serves nodes not listed
//	  "repairs": {                     moves for repair rounds (a start message with
//	    "review": [[MOVE, ...], ...]   "repair" set), by round as "nodes" is by attempt;
//	  }                                with a "repairs" section the stub declares the
//	}                                  "repair" feature
//
// A repair round needs the session it continues; without one, or with no
// repair moves for the node, the stub sends a permanent error, as an
// adapter does that cannot continue the conversation.
//
// Relative paths are taken from the step's working directory. A move is an
// object with one of these keys:
//
//	{"log": "text"}                      send a log message
//	{"session": "id"}                    send a session message
//	{"stderr": "text"}                   write a line to standard error
//	{"progress": true}                   send a progress message
//	{"sleep": "2s"}                      wait (a cancel message ends the wait)
//	{"touch": "path"}                    create an empty file, for tests to wait on
//	{"spawn": ["program", "arg", ...], "await": "path"}
//	                                     start a program and leave it running, as a
//	                                     client's tool may; with "await", wait up to 10s
//	                                     for the file at path to appear first
//	{"write": "path", "text": "..."}     write a file, as an agent working in the folder would
//	{"require_session": "id"}            go on only if the start message continues session id;
//	                                     otherwise send a permanent error and stop
//	{"result": {...}}                    send a result with this output; "denied": ["..."]
//	                                     in the same move reports tool calls the client denied
//	{"error": "KIND", "message": "...", "retry_in": "30s", "retry_at": "RFC 3339 time"}
//	                                     send an error; retry_in or retry_at for a limit
//
// The plan is read again for every start, --describe, --usage and --catalog, so a test
// can change what the adapter does between steps by rewriting the file.
//
// A dialogue step asks with a result whose outcome is "questions", such as
// {"result": {"outcome": "questions", "questions": [{"id": "q", "question": "?", "kind": "yesno"}]}};
// the next attempt is the same conversation continued with the answers.
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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	Reject   []string            `json:"reject_tools"`
	When     []When              `json:"when"`
	Nodes    map[string][][]Move `json:"nodes"`
	Repairs  map[string][][]Move `json:"repairs"`
	Usage    map[string]Window   `json:"usage"`
	Catalog  json.RawMessage     `json:"catalog"`
}

// FixedCatalog is the catalogue the stub reports for "catalog": true: two
// groups, a model with no effort setting and one whose efforts it cannot
// tell.
var FixedCatalog = adapter.Catalog{Complete: true, Models: []adapter.CatalogModel{
	{ID: "alpha/small", Label: "Alpha Small", Group: "alpha", Efforts: []string{}},
	{ID: "alpha/large", Label: "Alpha Large", Group: "alpha", Efforts: []string{"low", "medium", "high"}},
	{ID: "beta/deep", Label: "Beta Deep", Group: "beta", Efforts: []string{"high", "max"}},
	{ID: "beta/plain", Label: "Beta Plain", Group: "beta"},
}}

// catalog returns what the plan says --catalog prints, as one line; empty
// when the plan has no catalogue. An object is printed as written, valid or
// not, so that tests can give a bad one.
func (p Plan) catalog() []byte {
	text := strings.TrimSpace(string(p.Catalog))
	switch text {
	case "", "null", "false":
		return nil
	case "true":
		data, _ := json.Marshal(FixedCatalog)
		return data
	}
	var b bytes.Buffer
	if json.Compact(&b, []byte(text)) != nil {
		return []byte(text)
	}
	return b.Bytes()
}

// Window is the usage the adapter reports for one window.
type Window struct {
	Used     float64 `json:"used"`
	ResetsIn string  `json:"resets_in"` // a duration from now
	ResetsAt string  `json:"resets_at"` // an RFC 3339 time
}

// When chooses moves by the text of the prompt.
type When struct {
	Prompt string   `json:"prompt"`
	Moves  [][]Move `json:"moves"`
}

// Move is one thing the adapter does.
type Move struct {
	Log      *string         `json:"log"`
	Session  string          `json:"session"`
	Stderr   string          `json:"stderr"`
	Sleep    string          `json:"sleep"`
	Touch    string          `json:"touch"`
	Spawn    []string        `json:"spawn"`
	Await    string          `json:"await"`
	Result   json.RawMessage `json:"result"`
	Error    string          `json:"error"`
	Message  string          `json:"message"`
	RetryIn  string          `json:"retry_in"`
	RetryAt  string          `json:"retry_at"`
	Raw      *string         `json:"raw"`
	Exit     *int            `json:"exit"`
	Code     int             `json:"code"`
	Hang     bool            `json:"hang"`
	Require  string          `json:"require_session"`
	Write    string          `json:"write"`
	Denied   []string        `json:"denied"`
	Progress bool            `json:"progress"`
	Text     string          `json:"text"`
}

type stub struct {
	out    *json.Encoder
	stdout io.Writer
	stderr io.Writer
	cancel chan struct{}
}

// Main runs the adapter and returns its exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	describeOnly, checkOnly := false, false
	if n := len(args); n > 0 && args[n-1] == adapter.DescribeFlag {
		describeOnly, args = true, args[:n-1]
	}
	if n := len(args); n > 0 && args[n-1] == adapter.CheckFlag {
		checkOnly, args = true, args[:n-1]
	}
	catalogOnly := false
	if n := len(args); n > 0 && args[n-1] == adapter.CatalogFlag {
		catalogOnly, args = true, args[:n-1]
	}
	var windows []string
	usageOnly := false
	for i, a := range args {
		if a == adapter.UsageFlag {
			usageOnly, windows, args = true, args[i+1:], args[:i]
			break
		}
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
	if catalogOnly {
		data := plan.catalog()
		if data == nil {
			fmt.Fprintln(stderr, "stub: the plan has no catalogue")
			return 2
		}
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}
	if checkOnly {
		err := adapter.ServeCheck(stdin, stdout, func(st adapter.Step) []adapter.CheckProblem {
			var out []adapter.CheckProblem
			for i, t := range st.Tools {
				for _, r := range plan.Reject {
					if t == r {
						out = append(out, adapter.ToolProblem(i, "the stub rejects this entry"))
					}
				}
			}
			return out
		})
		if err != nil {
			fmt.Fprintln(stderr, "stub:", err)
			return 2
		}
		return 0
	}
	if describeOnly {
		d := adapter.Description{Protocol: plan.Protocol, Name: plan.Name, Features: []string{adapter.FeatureResume}}
		if d.Protocol == nil {
			d.Protocol = []int{adapter.Version}
		}
		if d.Name == "" {
			d.Name = "stub"
		}
		if plan.Usage != nil {
			d.Features = append(d.Features, adapter.FeatureUsage)
		}
		if plan.catalog() != nil {
			d.Features = append(d.Features, adapter.FeatureCatalog)
		}
		if plan.Repairs != nil {
			d.Features = append(d.Features, adapter.FeatureRepair)
		}
		if plan.Features != nil {
			d.Features = *plan.Features
		}
		data, _ := json.Marshal(d)
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}
	if usageOnly {
		return usage(plan, windows, stdout, stderr)
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
	if step.Repair > 0 {
		return s.repair(plan, step)
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

// repair performs the moves of a repair round.
func (s *stub) repair(plan Plan, step adapter.Step) int {
	if step.Session == nil {
		s.send(adapter.Message{Type: adapter.TypeError, Kind: adapter.ErrPermanent, Message: "a repair round needs a session to continue"})
		return 0
	}
	fmt.Fprintf(s.stderr, "stub: repair %d of %s attempt %d in session %s\n", step.Repair, step.NodeID, step.Attempt, *step.Session)
	list, ok := plan.Repairs[step.NodeID]
	if !ok {
		list = plan.Repairs["*"]
	}
	if len(list) == 0 {
		s.send(adapter.Message{Type: adapter.TypeError, Kind: adapter.ErrPermanent, Message: "the plan has no repair moves for " + step.NodeID})
		return 0
	}
	i := step.Repair - 1
	if i >= len(list) {
		i = len(list) - 1
	}
	for _, m := range list[i] {
		if code, done := s.do(m, step); done {
			return code
		}
	}
	return 0
}

// usage prints what the plan says the client has used of each window asked
// for.
func usage(plan Plan, windows []string, stdout, stderr io.Writer) int {
	u := adapter.Usage{Windows: []adapter.UsageWindow{}}
	for _, name := range windows {
		w, ok := plan.Usage[name]
		if !ok {
			continue
		}
		uw := adapter.UsageWindow{Name: name, Used: w.Used, ResetsAt: w.ResetsAt}
		if w.ResetsIn != "" {
			d, err := time.ParseDuration(w.ResetsIn)
			if err != nil {
				fmt.Fprintf(stderr, "stub: bad resets_in %q\n", w.ResetsIn)
				return 2
			}
			uw.ResetsAt = time.Now().Add(d).UTC().Format(time.RFC3339Nano)
		}
		u.Windows = append(u.Windows, uw)
	}
	data, _ := json.Marshal(u)
	fmt.Fprintf(stdout, "%s\n", data)
	return 0
}

// pick returns the moves for the attempt of a step.
func pick(plan Plan, step adapter.Step) ([]Move, bool) {
	var list [][]Move
	matched := false
	for _, w := range plan.When {
		if strings.Contains(step.Prompt, w.Prompt) {
			list, matched = w.Moves, true
			break
		}
	}
	if !matched {
		var ok bool
		if list, ok = plan.Nodes[step.NodeID]; !ok {
			list = plan.Nodes["*"]
		}
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
	case m.Progress:
		s.send(adapter.Message{Type: adapter.TypeProgress})
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
	case m.Require != "":
		if step.Session == nil || *step.Session != m.Require {
			got := "none"
			if step.Session != nil {
				got = *step.Session
			}
			s.send(adapter.Message{Type: adapter.TypeError, Kind: adapter.ErrPermanent,
				Message: fmt.Sprintf("expected to continue session %s, got %s", m.Require, got)})
			return 0, true
		}
	case m.Write != "":
		path := local(step.Workdir, m.Write)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(m.Text), 0o644); err != nil {
			fmt.Fprintf(s.stderr, "stub: %v\n", err)
		}
	case m.Touch != "":
		os.WriteFile(local(step.Workdir, m.Touch), nil, 0o644)
	case len(m.Spawn) > 0:
		cmd := exec.Command(m.Spawn[0], m.Spawn[1:]...)
		cmd.Dir = step.Workdir
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(s.stderr, "stub: cannot spawn: %v\n", err)
			return 2, true
		}
		if m.Await != "" {
			path := local(step.Workdir, m.Await)
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if _, err := os.Stat(path); err == nil {
					break
				}
			}
		}
	case len(m.Result) > 0:
		s.send(adapter.Message{Type: adapter.TypeResult, Output: m.Result, Denied: m.Denied})
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
