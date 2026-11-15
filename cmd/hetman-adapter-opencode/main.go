// Command hetman-adapter-opencode is an executor adapter that runs one agent
// step through the OpenCode command-line client in non-interactive mode
// ("opencode run"). It speaks protocol version 0 as defined in package
// hetman/internal/adapter.
//
// Usage:
//
//	hetman-adapter-opencode [--client PROGRAM] [--env NAME]... [--describe | --check | --catalog]
//
// --client names the client program, "opencode" on the path by default.
// --env passes one more variable of the adapter's own environment to the
// client; by default the client gets only what it needs to run, to reach the
// network and to find its stored logins (see clientEnv).
// --catalog prints the models and variants the client lists (see
// catalog.go).
//
// The step's prompt, followed by the answer format, goes to the client on
// standard input. The client runs in the step's working folder as an agent
// defined for this run only, whose permissions deny every tool except the
// step's tools. Its JSON events become log messages, its session id a
// session message, and the JSON object at the end of its answer the result.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"hetman/internal/adapter"
)

// adapterName is the name the adapter describes itself with.
const adapterName = "opencode"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// options are the adapter's command-line options.
type options struct {
	client string   // the client program
	env    []string // names of extra variables to pass to the client
}

func parseArgs(args []string) (o options, describe bool, err error) {
	o.client = "opencode"
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
			return o, false, fmt.Errorf("--env %s: the adapter sets this variable itself to restrict the client", value)
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
			Features: []string{adapter.FeatureResume, adapter.FeatureProtect, adapter.FeatureCheck, adapter.FeatureCatalog}})
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}
	if catalog {
		return serveCatalog(o, stdout, stderr)
	}
	if check {
		if err := adapter.ServeCheck(stdin, stdout, func(st adapter.Step) []adapter.CheckProblem {
			return append(stepProblems(o, st, stderr), modelProblems(o, st)...)
		}); err != nil {
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
// cannot find (it lives in another data folder or on another computer) is
// not worth failing the step for: the prompt holds the whole task, so the
// step starts a new conversation instead.
func step(o options, st adapter.Step, out *sender, diag io.Writer, cancel <-chan struct{}) adapter.Message {
	perms, err := permissions(st.Tools)
	if err != nil {
		return failure(adapter.ErrPermanent, err.Error())
	}
	perms = fence(st, perms)
	if err := checkEffort(o, st, diag); err != nil {
		return failure(adapter.ErrPermanent, err.Error())
	}
	session := ""
	if st.Session != nil {
		session = *st.Session
	}
	r := attempt(o, st, perms, session, out, diag, cancel)
	if r.missingSession && session != "" {
		out.log(fmt.Sprintf("session %s cannot be continued here; starting a new conversation", session))
		r = attempt(o, st, perms, "", out, diag, cancel)
	}
	return r.final
}
