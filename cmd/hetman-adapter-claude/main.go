// Command hetman-adapter-claude is an executor adapter that runs one agent
// step through the Claude Code command-line client in print mode. It speaks
// protocol version 0 as defined in package hetman/internal/adapter.
//
// Usage:
//
//	hetman-adapter-claude [--client PROGRAM] [--env NAME]... [--describe | --check | --catalog]
//
// --client names the client program, "claude" on the path by default.
// --env passes one more variable of the adapter's own environment to the
// client; by default the client gets only what it needs to run, to reach the
// network and to find its login (see clientEnv).
// --catalog prints the model aliases and effort levels the adapter knows
// the client accepts (see catalog.go).
//
// The step's prompt goes to the client on standard input. The client runs in
// the step's working folder with only the step's tools, pre-approved, and
// every other tool or permission request denied. Its streamed JSON events
// become log messages, its session id a session message, and its structured
// output the result.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"hetman/internal/adapter"
)

// adapterName is the name the adapter describes itself with.
const adapterName = "claude"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// options are the adapter's command-line options.
type options struct {
	client string   // the client program
	env    []string // names of extra variables to pass to the client
}

func parseArgs(args []string) (o options, describe bool, err error) {
	o.client = "claude"
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, hasValue := strings.Cut(a, "=")
		switch name {
		case adapter.DescribeFlag:
			describe = true
			continue
		case "--client", "--env":
		default:
			return o, false, fmt.Errorf("unknown argument %q", a)
		}
		if !hasValue {
			if i+1 >= len(args) {
				return o, false, fmt.Errorf("%s needs a value", name)
			}
			i++
			value = args[i]
		}
		if value == "" {
			return o, false, fmt.Errorf("%s needs a value", name)
		}
		if name == "--client" {
			o.client = value
			continue
		}
		if refusedEnv(value) {
			return o, false, fmt.Errorf("--env %s: this variable would switch the client to paid API usage", value)
		}
		o.env = append(o.env, value)
	}
	return o, describe, nil
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	args, check := cutFlag(args, adapter.CheckFlag)
	args, catalog := cutFlag(args, adapter.CatalogFlag)
	o, describe, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "%s adapter: %v\n", adapterName, err)
		return 2
	}
	if describe {
		data, _ := json.Marshal(adapter.Description{Protocol: []int{adapter.Version}, Name: adapterName,
			Features: []string{adapter.FeatureResume, adapter.FeatureProtect, adapter.FeatureCheck, adapter.FeatureProgress, adapter.FeatureCatalog}})
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}
	if catalog {
		return serveCatalog(stdout)
	}
	if check {
		if err := adapter.ServeCheck(stdin, stdout, func(st adapter.Step) []adapter.CheckProblem { return stepProblems(o, st, stderr) }); err != nil {
			fmt.Fprintf(stderr, "%s adapter: %v\n", adapterName, err)
			return 2
		}
		return 0
	}
	in := bufio.NewReader(stdin)
	line, _ := in.ReadBytes('\n')
	if len(bytes.TrimSpace(line)) == 0 {
		fmt.Fprintf(stderr, "%s adapter: no start message\n", adapterName)
		return 2
	}
	var start adapter.Start
	if err := json.Unmarshal(line, &start); err != nil || start.Type != adapter.TypeStart || start.V != adapter.Version {
		fmt.Fprintf(stderr, "%s adapter: expected a start message of protocol version %d\n", adapterName, adapter.Version)
		return 2
	}
	cancel := make(chan struct{})
	go func() {
		for {
			line, err := in.ReadBytes('\n')
			var m struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(line, &m) == nil && m.Type == adapter.TypeCancel {
				close(cancel)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	out := newSender(stdout)
	out.send(step(o, start.Step, out, stderr, cancel))
	return 0
}

// sender writes protocol messages.
type sender struct{ enc *json.Encoder }

func newSender(w io.Writer) *sender {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &sender{enc: enc}
}

func (s *sender) send(m adapter.Message) {
	m.V = adapter.Version
	s.enc.Encode(m)
}

func (s *sender) log(text string) {
	if text != "" {
		s.send(adapter.Message{Type: adapter.TypeLog, Text: text})
	}
}

func (s *sender) session(id string) {
	s.send(adapter.Message{Type: adapter.TypeSession, ID: id})
}

// cutFlag removes a flag from args and reports whether it was there.
func cutFlag(args []string, flag string) ([]string, bool) {
	var out []string
	found := false
	for _, a := range args {
		if a == flag {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

// failure is an error message; denied lists the tool calls the client denied
// in the step, when it ends because of them.
func failure(kind, message string, denied ...string) adapter.Message {
	return adapter.Message{Type: adapter.TypeError, Kind: kind, Message: message, Denied: denied}
}

// step runs a step and returns its final message. A session the client
// cannot find (it lives in another folder or on another computer) is not
// worth failing the step for: the prompt holds the whole task, so the step
// starts a new conversation instead.
func step(o options, st adapter.Step, out *sender, diag io.Writer, cancel <-chan struct{}) adapter.Message {
	if err := checkStep(st); err != nil {
		return failure(adapter.ErrPermanent, err.Error())
	}
	session := ""
	if st.Session != nil {
		session = *st.Session
	}
	for _, name := range toolNames(st.Tools) {
		if name == "Bash" {
			out.log(readOnlyNote)
			break
		}
	}
	r := attempt(o, st, session, out, diag, cancel)
	if r.missingSession && session != "" {
		out.log(fmt.Sprintf("session %s cannot be continued here; starting a new conversation", session))
		r = attempt(o, st, "", out, diag, cancel)
	}
	return r.final
}

// efforts are the effort levels the client accepts. It ignores any other
// value with a warning, so the adapter rejects it instead.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// checkStep rejects a step the client would not run as written, naming
// the first problem (see stepProblems).
func checkStep(st adapter.Step) error {
	for _, p := range stepProblems(options{}, st, io.Discard) {
		if !p.Warning {
			return errors.New(adapter.ProblemText(st, p))
		}
	}
	return nil
}
