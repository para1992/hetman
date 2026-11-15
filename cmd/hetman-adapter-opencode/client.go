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

// rule allows one tool: everywhere, or only for the listed patterns.
type rule struct {
	tool     string
	all      bool
	patterns []string
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
		t = strings.TrimSpace(t)
		name, pattern, hasPattern := t, "", false
		if g := toolRule.FindStringSubmatch(t); g != nil {
			name, pattern, hasPattern = strings.TrimSpace(g[1]), strings.TrimSpace(g[2]), true
			if pattern == "" {
				return nil, fmt.Errorf("tool %q has an empty pattern", t)
			}
		}
		switch {
		case t == "":
			return nil, fmt.Errorf("tools contains an empty name")
		case !toolName.MatchString(name):
			return nil, fmt.Errorf("tool %q is not a client permission name such as read, edit, glob, grep, list, bash, webfetch or websearch, optionally with a pattern as in bash(git status)", t)
		case name == "task":
			return nil, fmt.Errorf("tool task is refused: the subagents it starts run with the client's default permissions, not the step's tools")
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
		if r.all {
			b.WriteString(`"allow"`)
			continue
		}
		b.WriteString("{")
		for i, p := range r.patterns {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(q(p) + `:"allow"`)
		}
		b.WriteString("}")
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
//   - --agent selects the adapter's own agent (see agentConfig).
//   - --title gives a new session a title, so the client does not spend a
//     model request on making one.
func clientArgs(st adapter.Step, agent, session string) []string {
	a := []string{"run", "--format", "json", "--pure", "--print-logs", "--log-level", "INFO", "--agent", agent}
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
}

// attempt runs the client once and turns what it printed into a result or
// error message, sending log and session messages on the way.
func attempt(o options, st adapter.Step, rules []rule, session string, out *sender, diag io.Writer, cancel <-chan struct{}) attemptResult {
	agent := newAgentName()
	cmd := exec.Command(o.client, clientArgs(st, agent, session)...)
	cmd.Dir = st.Workdir
	cmd.Env = clientEnv(os.Environ(), o.env, agentConfig(agent, rules), st.Workdir)
	cmd.Stdin = strings.NewReader(fullPrompt(st))
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
