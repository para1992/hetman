// Command hetman-adapter-codex is an executor adapter that runs one agent
// step through the Codex command-line client in its non-interactive mode
// ("codex exec"). It speaks protocol version 0 as defined in package
// hetman/internal/adapter.
//
// Usage:
//
//	hetman-adapter-codex [--client PROGRAM] [--env NAME]... [--describe | --check | --catalog]
//
// --client names the client program, "codex" on the path by default.
// --env passes one more variable of the adapter's own environment to the
// client; by default the client gets only what it needs to run, to reach the
// network and to find its login (see clientEnv).
// --catalog prints the models and reasoning efforts the client lists (see
// catalog.go).
//
// The step's prompt, followed by what the step may do and the answer
// format, goes to the client on standard input. The client runs in the
// step's working folder without the user's configuration file, rules,
// plugins, apps or hooks, under a permission profile made for the step (see
// tools.go): its sandbox, not a list of named tools, decides what shell
// commands and file edits may touch. Its JSON events become log messages,
// its thread id a session message, and its final message, held to the
// step's schema by the client where the schema allows it, the result.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"hetman/internal/adapter"
)

// adapterName is the name the adapter describes itself with.
const adapterName = "codex"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// options are the adapter's command-line options.
type options struct {
	client string   // the client program
	env    []string // names of extra variables to pass to the client
}

func parseArgs(args []string) (o options, describe bool, err error) {
	o.client = "codex"
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
			return o, false, fmt.Errorf("--env %s: this variable would switch the client to paid API usage or another server", value)
		}
		o.env = append(o.env, value)
	}
	return o, describe, nil
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	catalogOnce, catalogCache, catalogErr = sync.Once{}, adapter.Catalog{}, nil
	args, check := cutFlag(args, adapter.CheckFlag)
	args, catalog := cutFlag(args, adapter.CatalogFlag)
	o, describe, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "%s adapter: %v\n", adapterName, err)
		return 2
	}
	if describe {
		data, _ := json.Marshal(adapter.Description{Protocol: []int{adapter.Version}, Name: adapterName,
			Features: []string{adapter.FeatureResume, adapter.FeatureProtect, adapter.FeatureCheck, adapter.FeatureProgress, adapter.FeatureCatalog, adapter.FeatureRepair}})
		fmt.Fprintf(stdout, "%s\n", data)
		return 0
	}
	if catalog {
		return serveCatalog(o, stdout, stderr)
	}
	if check {
		if err := adapter.ServeCheck(stdin, stdout, func(st adapter.Step) []adapter.CheckProblem {
			return stepProblems(o, st, stderr)
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

func (s *sender) progress() {
	s.send(adapter.Message{Type: adapter.TypeProgress})
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
// cannot find (it lives in another home folder or on another computer) is
// not worth failing the step for: the prompt holds the whole task, so the
// step starts a new conversation instead.
func step(o options, st adapter.Step, out *sender, diag io.Writer, cancel <-chan struct{}) adapter.Message {
	if st.Workdir == "" {
		st.Workdir, _ = os.Getwd()
	}
	ts, problems := parseTools(st.Tools)
	if len(problems) > 0 {
		return failure(adapter.ErrPermanent, adapter.ProblemText(st, problems[0]))
	}
	sb, err := sandboxFor(st, ts)
	if err != nil {
		return failure(adapter.ErrPermanent, err.Error())
	}
	for _, note := range sb.notes {
		out.log(note)
	}
	if err := checkEffort(o, st, diag); err != nil {
		return failure(adapter.ErrPermanent, err.Error())
	}
	session := ""
	if st.Session != nil {
		session = *st.Session
	}
	if st.Repair > 0 {
		return repair(o, st, sb, session, out, diag, cancel)
	}
	prompt := fullPrompt(st, sb)
	in := runInput{session: session, prompt: prompt}
	if session != "" && !isThreadID(session) {
		out.log(fmt.Sprintf("session %s is not a thread id of the client; starting a new conversation", session))
		in.session = ""
	}
	r := attempt(o, st, sb, in, out, diag, cancel)
	if r.missingSession && in.session != "" {
		out.log(fmt.Sprintf("session %s cannot be continued here; starting a new conversation", session))
		in.session = ""
		r = attempt(o, st, sb, in, out, diag, cancel)
	}
	if r.badSchema {
		out.log("the client refused the step's schema for its structured output; asking for the JSON object in the answer instead")
		in.plain = true
		r = attempt(o, st, sb, in, out, diag, cancel)
	}
	// A turn that ended without the answer is continued in the same
	// session with a reminder of what the sandbox allows and of the answer
	// format, a few times at most.
	for n := 1; n <= maxContinuations && r.unanswered && r.session != ""; n++ {
		out.log(fmt.Sprintf("continuing session %s (%d of %d): the turn ended without the answer", r.session, n, maxContinuations))
		next := attempt(o, st, sb, runInput{session: r.session, prompt: continuation(st, sb, r), continued: true, denied: r.denied, plain: in.plain}, out, diag, cancel)
		if next.unanswered && n == maxContinuations {
			next.final.Message += fmt.Sprintf(" (also after %d continuation(s) of the session)", n)
		}
		r = next
	}
	return r.final
}

// repair runs a repair round: the engine rejected the answer the session
// gave, and the session is asked to correct only the answer. It continues
// that session or fails; a new conversation would know nothing of the work
// the answer is about. The client starts a new conversation by itself when
// it is given an id that is not one of its thread ids, so such an id is
// refused before the client runs, and a run that reports another thread is
// stopped. A turn that ends without the answer again is the engine's to
// judge, not continued here.
func repair(o options, st adapter.Step, sb sandbox, session string, out *sender, diag io.Writer, cancel <-chan struct{}) adapter.Message {
	if session == "" {
		return failure(adapter.ErrPermanent, "a repair round needs the session to continue")
	}
	if !isThreadID(session) {
		return failure(adapter.ErrPermanent, fmt.Sprintf("session %s is not a thread id of the client, so its answer cannot be corrected", session))
	}
	out.log(fmt.Sprintf("repair %d: asking session %s to correct the answer", st.Repair, session))
	in := runInput{session: session, prompt: strings.TrimRight(st.Prompt, "\n") + answerFormat(st), strictSession: true}
	r := attempt(o, st, sb, in, out, diag, cancel)
	if r.badSchema {
		in.plain = true
		r = attempt(o, st, sb, in, out, diag, cancel)
	}
	if r.missingSession {
		return failure(adapter.ErrPermanent, fmt.Sprintf("session %s cannot be continued here, so its answer cannot be corrected", session))
	}
	return r.final
}

// maxContinuations is how many times one attempt continues a session whose
// turn ended without the answer.
const maxContinuations = 2
