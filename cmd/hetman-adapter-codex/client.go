package main

import (
	"bufio"
	"bytes"
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

// isolation are the arguments that keep the user's own setup out of a step
// and keep the client from asking anybody:
//
//   - --ignore-user-config does not load the user's config.toml: its model,
//     approval and sandbox settings, tool servers (MCP), skills list,
//     project trust entries, hooks and profiles do not apply. The login
//     still comes from the client's home folder. --ignore-rules does not
//     load the user's or the project's command rules files.
//   - --skip-git-repo-check runs in a working folder that is not a git
//     repository, as the step's folder may be.
//   - approval_policy "never" returns a refused command or patch to the model
//     instead of asking; approvals_reviewer "user" keeps an automatic
//     reviewer from approving anything; forced_login_method "chatgpt"
//     refuses a login by API key, which would be paid usage.
//   - allow_login_shell false runs commands without the user's login shell
//     profile; history.persistence "none" adds nothing to the user's
//     history file; check_for_update_on_startup false makes no update check.
//   - skills.* false gives the model no skills list; the disabled features
//     remove apps, plugins, hooks, memories, sub-agents, the browser,
//     computer use, image generation, goals and tool suggestions.
//
// What cannot be switched off this way: the instruction file in the
// client's home folder (AGENTS.md there) is still given to the model, as is
// an AGENTS.md of the project.
var isolation = []string{
	"--json", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check",
	"-c", `approval_policy="never"`, "-c", `approvals_reviewer="user"`, "-c", `forced_login_method="chatgpt"`,
	"-c", "allow_login_shell=false", "-c", `history.persistence="none"`, "-c", "check_for_update_on_startup=false",
	"-c", "skills.include_instructions=false", "-c", "skills.bundled.enabled=false",
	"--disable", "apps", "--disable", "plugins", "--disable", "hooks", "--disable", "memories",
	"--disable", "multi_agent", "--disable", "browser_use", "--disable", "computer_use",
	"--disable", "image_generation", "--disable", "goals", "--disable", "tool_suggest",
}

// clientArgs builds the client's command line for one run. The prompt is
// not on it: "-" makes the client read it from standard input. A new
// conversation is "exec ... -C WORKDIR -", a continued one "exec resume ...
// SESSION -", which takes no -C and runs in the working folder it is
// started in. The sandbox is chosen with the step's permission profile,
// which both forms accept as configuration overrides.
func clientArgs(st adapter.Step, sb sandbox, session, schemaFile string) []string {
	a := []string{"exec"}
	if session != "" {
		a = append(a, "resume")
	}
	a = append(a, isolation...)
	a = append(a, profileArgs(sb)...)
	if session == "" {
		a = append(a, "-C", st.Workdir)
	}
	if st.Model != "" {
		a = append(a, "-m", st.Model)
	}
	if st.Effort != "" {
		a = append(a, "-c", "model_reasoning_effort="+tomlString(st.Effort))
	}
	if schemaFile != "" {
		a = append(a, "--output-schema", schemaFile)
	}
	if session != "" {
		a = append(a, session)
	}
	return append(a, "-")
}

// threadID is the form of the client's thread ids. The client takes any
// other text as a thread name, and when no thread has that name it starts
// a new conversation instead of failing.
var threadID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isThreadID(s string) bool { return threadID.MatchString(s) }

// fullPrompt is the step's prompt followed by what the sandbox allows and
// the answer format.
func fullPrompt(st adapter.Step, sb sandbox) string {
	return strings.TrimRight(st.Prompt, "\n") + strings.TrimRight(scopeNote(st, sb), "\n") + answerFormat(st)
}

// continuation is the message that continues a session whose turn ended
// without the answer: which calls failed in the sandbox, what the sandbox
// allows, and the answer format again.
func continuation(st adapter.Step, sb sandbox, r attemptResult) string {
	var b strings.Builder
	if r.empty {
		b.WriteString("Your turn ended without an answer, and the task is not finished.")
	} else {
		b.WriteString("Your reply did not end with the required JSON object.")
	}
	if len(r.denied) > 0 {
		b.WriteString(" The sandbox refused these calls:\n")
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
		b.WriteString("Such calls stay refused however often they are repeated, so do not make them again; what the sandbox allows still works.")
	}
	b.WriteString(strings.TrimRight(scopeNote(st, sb), "\n"))
	b.WriteString("\nGo on with the task using what is allowed. When you are done, or when the task cannot be finished this way, end your reply with the JSON object; when it cannot be finished, say so in it as far as the schema allows.")
	b.WriteString(answerFormat(st))
	return b.String()
}

// answerFormat says how the reply must end. The client holds the final
// message to the schema when it gets one, but not every schema can be given
// to it (see schema.go), so the prompt always says it too.
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
// certificate settings, and the client's home folder, where its login is.
// Everything else is left out, among it API keys, which would switch the
// client to paid API usage, and the variables of a client session the
// engine may itself run inside. Shell commands the client runs get this
// environment too.
var passEnv = []string{
	// Unix
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TZ", "LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
	// Windows (names compare without case there)
	"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP", "USERPROFILE", "USERNAME",
	"USERDOMAIN", "APPDATA", "LOCALAPPDATA", "PROGRAMDATA", "PROGRAMFILES", "PROGRAMFILES(X86)",
	"COMMONPROGRAMFILES", "HOMEDRIVE", "HOMEPATH", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE", "OS",
	// network
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy", "ALL_PROXY", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	// the client: its home folder
	"CODEX_HOME",
}

// refusedEnv reports whether a variable must never reach the client: an API
// key would be paid usage, a base URL another server.
func refusedEnv(name string) bool {
	switch strings.ToUpper(name) {
	case "OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL", "CODEX_ACCESS_TOKEN":
		return true
	}
	return false
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
	missingSession bool // the client found no thread with the id to continue
	badSchema      bool // the server refused the schema given to the client
	// unanswered: the run ended normally, but the answer holds no JSON
	// object. The session can then be continued with a reminder.
	unanswered bool
	empty      bool     // and the answer holds no text at all
	session    string   // the session of the run
	denied     []string // the calls refused so far in the step's attempt
}

// runInput is what one run of the client is given.
type runInput struct {
	session string // the session to continue, or "" for a new one
	prompt  string
	// continued: the run continues the session of the run before it in the
	// same attempt, which named it already.
	continued bool
	// strictSession: the run must continue session and nothing else; a run
	// that reports another thread is stopped.
	strictSession bool
	plain         bool     // give the client no schema
	denied        []string // the calls refused in earlier runs of the same attempt
}

// progressEvery is the least time between two progress messages.
var progressEvery = 10 * time.Second

// attempt runs the client once and turns what it printed into a result or
// error message, sending log, session and progress messages on the way.
func attempt(o options, st adapter.Step, sb sandbox, in runInput, out *sender, diag io.Writer, cancel <-chan struct{}) attemptResult {
	schemaFile, strict := "", false
	if !in.plain {
		if s, ok := strictSchema(st.Schema); ok {
			f, err := os.CreateTemp("", "hetman-codex-schema-*.json")
			if err == nil {
				f.Write(s)
				f.Close()
				schemaFile, strict = f.Name(), true
				defer os.Remove(schemaFile)
			}
		}
	}
	cmd := exec.Command(o.client, clientArgs(st, sb, in.session, schemaFile)...)
	cmd.Dir = st.Workdir
	cmd.Env = clientEnv(os.Environ(), o.env)
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
	// The client's standard error carries its error log, among it the
	// patches it refused, which no event reports. Its lines go to the
	// adapter's standard error and are kept for classifying a failure.
	tail := &tailBuffer{max: 16 << 10}
	errLines := make(chan string, 64)
	go func() {
		defer close(errLines)
		br := bufio.NewReaderSize(stderr, 64<<10)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				io.WriteString(diag, line)
				tail.Write([]byte(line))
				errLines <- line
			}
			if err != nil {
				return
			}
		}
	}()
	s := newStream(st, out)
	s.strict = strict
	s.denied = append(s.denied, in.denied...)
	s.want, s.only = in.session, in.strictSession
	if in.continued {
		s.session = in.session
	}
	stopClient := func() {
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		stop(cmd.Process, exited)
	}
	tick := time.NewTicker(progressEvery)
	defer tick.Stop()
	// Once the events end, standard error gets a moment to end too; a
	// process the client left behind may hold it open.
	var grace <-chan time.Time
	for lines != nil || errLines != nil {
		select {
		case line, ok := <-lines:
			if !ok {
				lines = nil
				grace = time.After(2 * time.Second)
				continue
			}
			s.line(line)
			if in.strictSession && s.session != "" && s.session != in.session {
				stopClient()
				return attemptResult{missingSession: true, final: failure(adapter.ErrPermanent, "the client started thread "+s.session+" instead of continuing "+in.session)}
			}
		case line, ok := <-errLines:
			if !ok {
				errLines = nil
				continue
			}
			s.errLine(line)
		case <-tick.C:
			s.tick()
		case <-grace:
			errLines = nil
		case <-cancel:
			stopClient()
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
