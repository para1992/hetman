package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"hetman/internal/adapter"
)

// clientArgs builds the client's command line for a step. The prompt is not
// on it: it goes to standard input.
//
//   - --restricted ignores the user, project and local settings files, so no
//     permission rule, hook or server from them applies, and keeps the file
//     tools inside the working folder; --strict-mcp-config with no
//     --mcp-config loads no tool servers.
//   - --tools makes only the built-in tools named by the step available (none
//     when the step names none), --allowedTools pre-approves each entry as
//     written, so "Bash(go test *)" makes Bash available but approves only
//     that pattern.
//   - --permission-mode dontAsk and --permission-prompts none deny every
//     request that is not pre-approved instead of asking anybody.
//   - --disallowedTools and --add-dir keep the file tools off the step's
//     protected paths and let them reach its writable folders (fenceArgs).
func clientArgs(st adapter.Step, session string) []string {
	a := []string{"-p", "--output-format", "stream-json", "--verbose",
		"--restricted", "--strict-mcp-config",
		"--permission-mode", "dontAsk", "--permission-prompts", "none",
		"--tools=" + strings.Join(toolNames(st.Tools), ",")}
	for _, t := range st.Tools {
		a = append(a, "--allowedTools="+strings.TrimSpace(t))
	}
	a = append(a, fenceArgs(st)...)
	if st.Model != "" {
		a = append(a, "--model", st.Model)
	}
	if st.Effort != "" {
		a = append(a, "--effort", st.Effort)
	}
	if s := schemaArg(st.Schema); s != "" {
		a = append(a, "--json-schema", s)
	}
	if session != "" {
		a = append(a, "--resume", session)
	}
	return a
}

// toolNames returns the tool names of permission entries such as
// "Bash(git status)", each once, in order. An Edit rule with a path also
// makes Write available: the rule approves creating files there too, and
// Write is the tool that creates them.
func toolNames(tools []string) []string {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, t := range tools {
		name, _, call, _ := splitTool(t)
		add(name)
		if name == "Edit" && call {
			add("Write")
		}
	}
	return names
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
// certificate settings, and where the client keeps its login. Everything
// else is left out, among it the variables of a client session the engine
// may itself run inside, and API keys, which would switch the client from
// the subscription to paid API usage.
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
	// the client: its configuration folder, a subscription token made with
	// "claude setup-token", and the shell it uses on Windows
	"CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_GIT_BASH_PATH",
}

// refusedEnv reports whether a variable must never reach the client.
func refusedEnv(name string) bool {
	n := strings.ToUpper(name)
	return n == "ANTHROPIC_API_KEY" || n == "ANTHROPIC_AUTH_TOKEN"
}

// clientEnv picks the client's environment out of environ.
func clientEnv(environ, extra []string) []string {
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
	return env
}

// attemptResult is how one run of the client ended.
type attemptResult struct {
	final          adapter.Message
	missingSession bool // the client found no conversation with the id to continue
}

// attempt runs the client once and turns what it printed into a result or
// error message, sending log and session messages on the way.
func attempt(o options, st adapter.Step, session string, out *sender, diag io.Writer, cancel <-chan struct{}) attemptResult {
	cmd := exec.Command(o.client, clientArgs(st, session)...)
	cmd.Dir = st.Workdir
	cmd.Env = clientEnv(os.Environ(), o.env)
	cmd.Stdin = strings.NewReader(st.Prompt)
	tail := &tailBuffer{max: 8 << 10}
	cmd.Stderr = io.MultiWriter(diag, tail)
	// A process the client started must not keep the adapter waiting for
	// output that will never come.
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
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
	s := newStream(st.Workdir, out)
	s.allowed = st.Tools
	for done := false; !done; {
		select {
		case line, ok := <-lines:
			if !ok {
				done = true
				continue
			}
			s.line(line)
		case <-cancel:
			exited := make(chan struct{})
			go func() { cmd.Wait(); close(exited) }()
			stop(cmd.Process, exited)
			return attemptResult{final: failure(adapter.ErrTransient, "cancelled")}
		}
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
