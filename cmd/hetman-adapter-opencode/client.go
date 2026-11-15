package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"hetman/internal/adapter"
)

// rule allows one tool: everywhere, or only for the listed patterns; then
// denies the patterns in deny, and allows those in reallow again.
type rule struct {
	tool     string
	all      bool
	patterns []string
	deny     []string
	reallow  []string
}

var (
	toolName = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
	toolRule = regexp.MustCompile(`^([^()]*)\((.*)\)$`)
)

// permissions turns the step's tools into permission rules. An entry is a
// client permission name ("read", "edit", "bash", ...) or a name with a
// pattern, "bash(git status)", which allows that tool only for what matches
// the pattern. "task" is refused: the subagents it starts do not inherit the
// step's agent's permissions.
func permissions(tools []string) ([]rule, error) {
	var rules []rule
	index := map[string]int{}
	for _, t := range tools {
		if msg := toolProblem(t); msg != "" {
			return nil, fmt.Errorf("tools entry %q: %s", strings.TrimSpace(t), msg)
		}
		t = strings.TrimSpace(t)
		name, pattern, hasPattern := t, "", false
		if g := toolRule.FindStringSubmatch(t); g != nil {
			name, pattern, hasPattern = strings.TrimSpace(g[1]), strings.TrimSpace(g[2]), true
		}
		i, seen := index[name]
		if !seen {
			i = len(rules)
			index[name] = i
			rules = append(rules, rule{tool: name})
		}
		if hasPattern {
			rules[i].patterns = append(rules[i].patterns, pattern)
		} else {
			rules[i].all = true
		}
	}
	return rules, nil
}

// agentConfig is the client configuration the adapter adds for one run: an
// agent of its own whose permissions deny everything ("*" first, as the
// client applies the last rule that matches) and then allow the step's
// tools. A denied tool is not offered to the model at all, a tool allowed
// only for some patterns is denied for everything else, and the client
// never asks anybody in this mode. Sharing and automatic updates are off.
// Keys are written in order on purpose: the client gives their order
// meaning.
func agentConfig(agent string, rules []rule) string {
	var b strings.Builder
	q := func(s string) string {
		data, _ := json.Marshal(s)
		return string(data)
	}
	b.WriteString(`{"$schema":"https://opencode.ai/config.json","share":"disabled","autoupdate":false,"agent":{`)
	b.WriteString(q(agent))
	b.WriteString(`:{"mode":"primary","description":"One step of a Hetman flow.","permission":{"*":"deny"`)
	for _, r := range rules {
		b.WriteString("," + q(r.tool) + ":")
		if r.all && len(r.deny) == 0 {
			b.WriteString(`"allow"`)
			continue
		}
		var entries []string
		if r.all {
			entries = append(entries, `"*":"allow"`)
		}
		for _, p := range r.patterns {
			entries = append(entries, q(p)+`:"allow"`)
		}
		for _, p := range r.deny {
			entries = append(entries, q(p)+`:"deny"`)
		}
		for _, p := range r.reallow {
			entries = append(entries, q(p)+`:"allow"`)
		}
		b.WriteString("{" + strings.Join(entries, ",") + "}")
	}
	b.WriteString(`}}}}`)
	return b.String()
}

// newAgentName returns a fresh agent name, so that no agent of the same
// name in the user's or the project's configuration can add rules to it.
func newAgentName() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "hetman-" + hex.EncodeToString(b)
}

// clientArgs builds the client's command line for a step. The prompt is not
// on it: it goes to standard input.
//
//   - run --format json prints one JSON event per line.
//   - --pure loads no external plugins.
//   - --print-logs --log-level INFO prints the client's log on standard
//     error: it names the session as soon as the client creates or loads
//     it, before any model output, and it is the only place where some
//     failures are named (an unknown model, a usage limit the client keeps
//     retrying).
//   - --thinking adds the model's reasoning to the events, which the
//     client leaves out otherwise; it shows in the step's log what a model
//     was about when its turn ended without an answer.
//   - --agent selects the adapter's own agent (see agentConfig).
//   - --title gives a new session a title, so the client does not spend a
//     model request on making one.
func clientArgs(st adapter.Step, agent, session string) []string {
	a := []string{"run", "--format", "json", "--pure", "--print-logs", "--log-level", "INFO", "--thinking", "--agent", agent}
	if st.Model != "" {
		a = append(a, "--model", st.Model)
	}
	if st.Effort != "" {
		a = append(a, "--variant", st.Effort)
	}
	if session != "" {
		a = append(a, "--session", session)
	} else {
		a = append(a, "--title", title(st))
	}
	return a
}

func title(st adapter.Step) string {
	t := "hetman"
	if st.NodeID != "" {
		t += " " + st.NodeID
	}
	if st.RunID != "" {
		t += " (run " + st.RunID + ")"
	}
	return t
}

// fullPrompt is the step's prompt followed by the answer format. The
// client's non-interactive mode has no structured-output option, so the
// model is asked to end its answer with the JSON object.
func fullPrompt(st adapter.Step) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(st.Prompt, "\n"))
	var entries []adapter.ScopeEntry
	for _, t := range st.Tools {
		t = strings.TrimSpace(t)
		e := adapter.ScopeEntry{Tool: t}
		if g := toolRule.FindStringSubmatch(t); g != nil {
			e = adapter.ScopeEntry{Tool: strings.TrimSpace(g[1]), Pattern: strings.TrimSpace(g[2])}
		}
		entries = append(entries, e)
	}
	b.WriteString(strings.TrimRight(adapter.ScopeNote(entries, "bash"), "\n"))
	b.WriteString(answerFormat(st))
	return b.String()
}

// continuation is the message that continues a session whose turn ended
// without the answer: which calls the client denied and that they stay
// denied, which tools and folders the step has, and the answer format
// again. The client does not let the adapter change the denial the model
// sees, which names the matching rules but not the folders, so they are
// named here.
func continuation(st adapter.Step, r attemptResult) string {
	var b strings.Builder
	if r.empty {
		b.WriteString("Your turn ended without an answer, and the task is not finished.")
	} else {
		b.WriteString("Your reply did not end with the required JSON object.")
	}
	if len(r.denied) > 0 {
		b.WriteString(" The client denied these tool calls:\n")
		seen := map[string]bool{}
		n := 0
		for _, d := range r.denied {
			if seen[d] {
				continue
			}
			seen[d] = true
			if n++; n > 10 {
				b.WriteString("- and more\n")
				break
			}
			b.WriteString("- " + d + "\n")
		}
		b.WriteString("Such calls stay denied however often they are repeated, so do not make them again; a call that the step's tools allow still works.")
	}
	b.WriteString("\nThe tools of this step: " + toolsText(st.Tools) + ".")
	if st.Workdir != "" {
		folders := []string{st.Workdir}
		for _, w := range st.Writable {
			if _, in := adapter.Rel(st.Workdir, w); !in {
				folders = append(folders, w)
			}
		}
		b.WriteString(" File tools reach " + strings.Join(folders, " and ") + "; a path outside may be denied.")
	}
	b.WriteString("\nGo on with the task using what is allowed. When you are done, or when the task cannot be finished with these tools, end your reply with the JSON object; when it cannot be finished, say so in it as far as the schema allows.")
	b.WriteString(answerFormat(st))
	return b.String()
}

// answerFormat says how the reply must end.
func answerFormat(st adapter.Step) string {
	var b strings.Builder
	b.WriteString("\n\n---\nAnswer format: when you are done, end your reply with exactly one JSON object")
	if s := schemaArg(st.Schema); s != "" {
		b.WriteString(" that matches this JSON Schema:\n\n")
		b.WriteString(s)
		b.WriteString("\n\n")
	} else {
		b.WriteString(". ")
	}
	b.WriteString("Write nothing after the object and do not save it to a file.\n")
	return b.String()
}

// schemaArg returns the step's schema as one compact line, or "" when it is
// not a JSON object.
func schemaArg(schema json.RawMessage) string {
	var v map[string]any
	if json.Unmarshal(schema, &v) != nil || v == nil {
		return ""
	}
	var b bytes.Buffer
	if json.Compact(&b, schema) != nil {
		return ""
	}
	return b.String()
}

// passEnv lists the variables the client gets from the adapter's
// environment: what a program needs to run on each system, proxy and
// certificate settings, and the folders where the client keeps its
// configuration, its stored logins and its sessions. Everything else is
// left out, among it provider API keys, the variables of a client session
// the engine may itself run inside, and the client's own variables that
// change its configuration or permissions.
var passEnv = []string{
	// Unix
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TZ", "LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
	// Windows (names compare without case there)
	"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP", "USERPROFILE", "USERNAME",
	"USERDOMAIN", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "PROGRAMFILES", "PROGRAMFILES(X86)",
	"COMMONPROGRAMFILES", "HOMEDRIVE", "HOMEPATH", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "OS",
	// network
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR",
	// the client: the shell it uses on Windows
	"OPENCODE_GIT_BASH_PATH",
}

// ownEnv are the variables the adapter sets for the client itself.
var ownEnv = []string{"OPENCODE_CONFIG_CONTENT", "OPENCODE_PERMISSION", "OPENCODE_DISABLE_AUTOUPDATE",
	"OPENCODE_DISABLE_LSP_DOWNLOAD", "PWD"}

// refusedEnv reports whether a variable may not be named with --env: the
// adapter sets it, or it would loosen the step's restrictions.
func refusedEnv(name string) bool {
	for _, n := range ownEnv {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// clientEnv picks the client's environment out of environ and adds the
// adapter's own variables.
func clientEnv(environ, extra []string, config, dir string) []string {
	same := func(a, b string) bool { return a == b }
	if runtime.GOOS == "windows" {
		same = strings.EqualFold
	}
	allowed := func(k string) bool {
		if refusedEnv(k) {
			return false
		}
		for _, n := range passEnv {
			if same(k, n) {
				return true
			}
		}
		for _, n := range extra {
			if same(k, n) {
				return true
			}
		}
		return false
	}
	var env []string
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if ok && k != "" && allowed(k) {
			env = append(env, kv)
		}
	}
	// The client takes its working folder from PWD when it is set.
	if dir == "" {
		dir, _ = os.Getwd()
	}
	return append(env, "OPENCODE_CONFIG_CONTENT="+config, "OPENCODE_DISABLE_AUTOUPDATE=1",
		"OPENCODE_DISABLE_LSP_DOWNLOAD=1", "PWD="+dir)
}

// attemptResult is how one run of the client ended.
type attemptResult struct {
	final          adapter.Message
	missingSession bool // the client found no session with the id to continue
	// unanswered: the run ended normally, but the answer holds no JSON
	// object. The session can then be continued with a reminder.
	unanswered bool
	empty      bool     // and the answer holds no text at all
	session    string   // the session of the run
	denied     []string // the calls denied so far in the step's attempt
}

// runInput is what one run of the client is given.
type runInput struct {
	session string // the session to continue, or "" for a new one
	prompt  string
	// continued: the run continues the session of the run before it in the
	// same attempt, which named it already.
	continued bool
	denied    []string // the calls denied in earlier runs of the same attempt
}

// attempt runs the client once and turns what it printed into a result or
// error message, sending log and session messages on the way. The result
// reports the calls denied in earlier runs of the same attempt as well.
func attempt(o options, st adapter.Step, rules []rule, in runInput, out *sender, diag io.Writer, cancel <-chan struct{}) attemptResult {
	agent := newAgentName()
	cmd := exec.Command(o.client, clientArgs(st, agent, in.session)...)
	cmd.Dir = st.Workdir
	cmd.Env = clientEnv(os.Environ(), o.env, agentConfig(agent, rules), st.Workdir)
	cmd.Stdin = strings.NewReader(in.prompt)
	// A process the client started must not keep the adapter waiting for
	// output that will never come.
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	var stderr io.ReadCloser
	if err == nil {
		stderr, err = cmd.StderrPipe()
	}
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		return attemptResult{final: failure(adapter.ErrPermanent, fmt.Sprintf("cannot start %s: %v", o.client, err))}
	}
	lines := make(chan []byte, 64)
	go func() {
		defer close(lines)
		br := bufio.NewReaderSize(stdout, 1<<20)
		for {
			line, err := br.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				lines <- line
			}
			if err != nil {
				return
			}
		}
	}()
	// The client's log gives the session id early. Its warnings, errors and
	// anything else it prints on standard error go to the adapter's
	// standard error and are kept for classifying a failure; its routine
	// lines are dropped. A usage limit ends the run at once: the client
	// would otherwise keep retrying, for hours when the provider says so,
	// without printing anything else.
	tail := &tailBuffer{max: 16 << 10}
	limited := make(chan string, 1)
	found := make(chan string, 1)
	errDone := make(chan struct{})
	go func() {
		defer close(errDone)
		br := bufio.NewReaderSize(stderr, 64<<10)
		for {
			line, err := br.ReadString('\n')
			if id := logSession(line); id != "" {
				select {
				case found <- id:
				default:
				}
			}
			if line != "" && !routineLog(line) {
				io.WriteString(diag, line)
				tail.Write([]byte(line))
				if m := logError(line); m != "" && limitText.MatchString(m) {
					select {
					case limited <- m:
					default:
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	s := newStream(st, out)
	s.denied = append(s.denied, in.denied...)
	if in.continued {
		s.session = in.session
	}
	stopClient := func() {
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		stop(cmd.Process, exited)
		select {
		case <-errDone:
		case <-time.After(2 * time.Second):
		}
	}
	for done := false; !done; {
		select {
		case line, ok := <-lines:
			if !ok {
				done = true
				continue
			}
			s.line(line)
		case id := <-found:
			s.found(id)
		case m := <-limited:
			select {
			case id := <-found:
				s.found(id)
			default:
			}
			out.log("the provider reports a usage limit; stopping the client instead of letting it retry")
			stopClient()
			return attemptResult{final: s.limit(clip(oneLine(m), 500), m+"\n"+tail.String())}
		case <-cancel:
			stopClient()
			return attemptResult{final: failure(adapter.ErrTransient, "cancelled")}
		}
	}
	select {
	case <-errDone:
	case <-time.After(2 * time.Second):
	}
	cmd.Wait()
	return s.finish(cmd.ProcessState.ExitCode(), tail.String())
}

// tailBuffer keeps the last bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
