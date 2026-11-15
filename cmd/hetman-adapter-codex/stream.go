package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"hetman/internal/adapter"
)

// The client's --json output: one event per line. Events are
// thread.started (with the thread id, which is the session), turn.started,
// item.started, item.updated and item.completed (with an item), turn.completed
// (with token usage), turn.failed (with an error) and error (a message,
// also for a retry the client is making). Items are agent_message, reasoning,
// command_execution, file_change, mcp_tool_call, web_search, todo_list,
// collab_tool_call and error (a warning). Only the fields the adapter uses
// are declared.
type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     *item  `json:"item"`
	Usage    *struct {
		Input  int64 `json:"input_tokens"`
		Cached int64 `json:"cached_input_tokens"`
		Output int64 `json:"output_tokens"`
	} `json:"usage"`
	Message string `json:"message"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type item struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`
	Message string `json:"message"`
	// command_execution
	Command  string `json:"command"`
	Output   string `json:"aggregated_output"`
	ExitCode *int   `json:"exit_code"`
	Status   string `json:"status"`
	// file_change
	Changes []struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	} `json:"changes"`
	// mcp_tool_call
	Server string `json:"server"`
	Tool   string `json:"tool"`
	// web_search
	Query string `json:"query"`
}

// toolItems are the items that stand for a tool call.
var toolItems = map[string]bool{"command_execution": true, "file_change": true, "mcp_tool_call": true, "web_search": true, "collab_tool_call": true}

var (
	// A command the sandbox kept from writing or from what it needed.
	sandboxDenial = regexp.MustCompile(`(?i)read-only file system|operation not permitted|permission denied`)
	// A patch the client refused, in its error log.
	patchRefused = regexp.MustCompile(`error=patch rejected: (.*)$`)
	// A retry the client makes after a failed request: "Reconnecting... 2/5
	// (reason)".
	retryText = regexp.MustCompile(`^Reconnecting\.\.\. (\d+)/(\d+)(?: \((.*)\))?$`)
	// The shell wrapper the client reports a command in.
	shellWrap = regexp.MustCompile(`^(?:\S*/)?(?:ba|z|da|k|fi)?sh(?:\.exe)? -l?c '(.*)'$`)
)

// stream follows the client's events.
type stream struct {
	st      adapter.Step
	workdir string
	out     *sender
	session string
	want    string // the session the run was asked to continue
	// only: the run may continue want and nothing else; another thread is
	// not reported as the step's session.
	only   bool
	strict bool // the client was given the strict form of the schema
	began  time.Time
	// inTurn: a turn is under way; open counts the tool calls started and
	// not yet completed. Progress is sent only in a turn without an open
	// call, that is while the model works, so a command that hangs is still
	// taken for a silent step.
	inTurn       bool
	open         map[string]bool
	lastProgress time.Time
	messages     []string // the agent messages of the run, in order
	failed       string   // the error of a failed turn
	errors       []string // error events that were not retries
	steps        int      // model steps, as told apart by the items
	afterTool    bool     // the last item was a completed tool call
	tools        int
	completed    bool
	tokIn        int64
	tokOut       int64
	denied       []string // calls the sandbox or the approval policy refused
}

func newStream(st adapter.Step, out *sender) *stream {
	return &stream{st: st, workdir: st.Workdir, out: out, open: map[string]bool{}, afterTool: true, began: time.Now()}
}

// line handles one line of the client's standard output.
func (s *stream) line(raw []byte) {
	raw = bytes.TrimSpace(raw)
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Type == "" {
		s.out.log("client: " + clip(oneLine(string(raw)), 300))
		return
	}
	switch ev.Type {
	case "thread.started":
		s.found(ev.ThreadID)
	case "turn.started":
		s.inTurn = true
		s.afterTool = true
	case "turn.completed":
		s.inTurn = false
		s.completed = true
		if ev.Usage != nil {
			s.tokIn += ev.Usage.Input
			s.tokOut += ev.Usage.Output
		}
	case "turn.failed":
		s.inTurn = false
		if ev.Error != nil {
			s.failed = ev.Error.Message
		}
	case "error":
		if g := retryText.FindStringSubmatch(ev.Message); g != nil {
			s.out.log(fmt.Sprintf("request failed (%s); retry %s of %s", g[3], g[1], g[2]))
			return
		}
		s.errors = append(s.errors, ev.Message)
		s.out.log("client error: " + clip(oneLine(apiErr(ev.Message).text()), 500))
	case "item.started", "item.updated", "item.completed":
		if ev.Item != nil {
			s.item(ev.Type, *ev.Item)
		}
	}
	s.alive()
}

// item handles one item event.
func (s *stream) item(kind string, it item) {
	done := kind == "item.completed"
	if kind == "item.started" || done && !s.open[it.ID] {
		if s.afterTool && (toolItems[it.Type] || it.Type == "agent_message" || it.Type == "reasoning") {
			s.steps++
			s.afterTool = false
		}
	}
	if toolItems[it.Type] {
		if kind == "item.started" {
			s.open[it.ID] = true
			s.tools++
			s.out.log("> " + s.describe(it))
			return
		}
		if !done {
			return
		}
		if !s.open[it.ID] {
			s.tools++
			s.out.log("> " + s.describe(it))
		}
		delete(s.open, it.ID)
		s.afterTool = true
		s.result(it)
		return
	}
	if !done {
		return
	}
	switch it.Type {
	case "agent_message":
		s.messages = append(s.messages, it.Text)
		if t := strings.TrimSpace(it.Text); t != "" {
			s.out.log(s.short(t))
		}
	case "reasoning":
		if t := strings.TrimSpace(it.Text); t != "" {
			s.out.log("(thinking) " + s.short(firstLine(t, 300)))
		}
	case "todo_list":
		s.out.log("(plan updated)")
	case "error":
		s.out.log("client: " + clip(oneLine(it.Message), 500))
	}
}

// result logs how a tool call ended.
func (s *stream) result(it item) {
	switch it.Type {
	case "command_execution":
		code := 0
		if it.ExitCode != nil {
			code = *it.ExitCode
		}
		if it.Status == "failed" || code != 0 {
			if m := sandboxDenial.FindString(it.Output); m != "" {
				s.deny(s.describe(it), s.short(oneLine(lineWith(it.Output, m))), strings.ToLower(m))
				return
			}
			s.out.log(fmt.Sprintf("  < exit %d: %s", code, s.short(firstLine(it.Output, 200))))
			return
		}
		s.out.log("  < " + s.short(firstLine(it.Output, 200)))
	case "file_change":
		if it.Status == "failed" {
			s.out.log("  < failed")
			return
		}
		s.out.log("  < applied")
	default:
		if it.Status == "failed" {
			s.out.log("  < failed")
		}
	}
}

// errLine handles one line of the client's standard error.
func (s *stream) errLine(line string) {
	if g := patchRefused.FindStringSubmatch(strings.TrimSpace(line)); g != nil {
		s.deny("apply_patch", s.short(oneLine(g[1])), "")
	}
}

// maxDenied is the most characters of one refused call's description.
const maxDenied = 300

// deny records a call the sandbox refused: what was called, the line of
// the refusal and its kind (the words that told it apart). A build tool
// may be refused the same way for every file it writes, and a model may
// repeat a refused command, so a call already recorded with the same text,
// or the same call with the same kind of refusal, is not recorded again;
// the description is clipped.
func (s *stream) deny(call, reason, kind string) {
	text := clip(call+": "+reason, maxDenied)
	for _, d := range s.denied {
		if d == text || kind != "" && strings.HasPrefix(d, call+": ") && strings.Contains(strings.ToLower(d[len(call)+2:]), kind) {
			return
		}
	}
	s.denied = append(s.denied, text)
	s.out.log("denied: " + text)
}

// alive sends a progress message, at most every progressEvery, while the
// model works: in a turn, with no tool call open.
func (s *stream) alive() {
	if !s.inTurn || len(s.open) > 0 {
		return
	}
	if now := time.Now(); now.Sub(s.lastProgress) >= progressEvery {
		s.lastProgress = now
		s.out.progress()
	}
}

// tick is called every progressEvery while the client runs. The client's
// JSON output has no event while the model thinks or writes; the client
// itself ends a model stream that stays silent and retries it, reporting
// each retry, so a turn without an open tool call is the model at work.
func (s *stream) tick() { s.alive() }

// found reports the session id the first time it is known.
func (s *stream) found(id string) {
	if id == "" || s.session == id {
		return
	}
	if s.session != "" {
		s.out.log(fmt.Sprintf("the client started thread %s instead of continuing %s", id, s.session))
		s.session = id
		s.out.session(id)
		return
	}
	s.session = id
	if s.only && id != s.want {
		return
	}
	s.out.session(id)
	model := s.st.Model
	if model == "" {
		model = "the client's default"
	}
	if s.want != "" && s.want != id {
		s.out.log(fmt.Sprintf("the client started thread %s instead of continuing %s", id, s.want))
	}
	s.out.log(fmt.Sprintf("session %s, model %s, tools: %s", id, model, toolsText(s.st.Tools)))
}

// finish turns the end of a run into the step's final message.
func (s *stream) finish(exit int, stderr string) attemptResult {
	if missingSession(stderr) {
		return attemptResult{missingSession: true, final: failure(adapter.ErrPermanent, "no thread with the session id")}
	}
	if s.completed && s.failed == "" && exit == 0 {
		s.out.log(s.summary())
		answer := ""
		if n := len(s.messages); n > 0 {
			answer = s.messages[n-1]
		}
		if s.strict {
			if obj, ok := wholeObject(answer); ok {
				return attemptResult{final: adapter.Message{Type: adapter.TypeResult, Output: dropNulls(obj, s.st.Schema), Denied: s.denied}}
			}
		}
		if obj, ok := extractObject(answer); ok {
			return attemptResult{final: adapter.Message{Type: adapter.TypeResult, Output: obj, Denied: s.denied}}
		}
		// The model ended its turn without the answer. Continuing the
		// session, or another attempt, can still bring it, so the error
		// is transient. A flow that wants a refused call to fail the
		// attempt says so with fail_on_denied, which the engine applies to
		// the calls reported here and with a result.
		r := attemptResult{unanswered: true, session: s.session, denied: s.denied, empty: strings.TrimSpace(answer) == ""}
		switch {
		case len(s.denied) > 0:
			r.final = failure(adapter.ErrTransient, fmt.Sprintf("the sandbox refused %d call(s) (%s) and the answer holds no JSON object: %s",
				len(s.denied), clip(strings.Join(s.denied, "; "), 600), clip(oneLine(answer), 300)), s.denied...)
		case r.empty:
			r.final = failure(adapter.ErrTransient, "the client gave no answer")
		default:
			r.final = failure(adapter.ErrTransient, "the answer holds no JSON object: "+clip(oneLine(answer), 300))
		}
		if r.empty {
			s.out.log("the turn ended with no answer")
		} else {
			s.out.log("the turn ended without a JSON object in the answer")
		}
		return r
	}
	return s.classify(exit, stderr)
}

func (s *stream) summary() string {
	text := fmt.Sprintf("finished after %d model steps in %s", s.steps, time.Since(s.began).Round(100*time.Millisecond))
	if s.tools > 0 {
		text += fmt.Sprintf(", %d tool calls", s.tools)
	}
	if s.tokIn > 0 || s.tokOut > 0 {
		text += fmt.Sprintf(", %d tokens in, %d out", s.tokIn, s.tokOut)
	}
	return text
}

// describe says in a few words which tool is called on what.
func (s *stream) describe(it item) string {
	switch it.Type {
	case "command_execution":
		cmd := it.Command
		if g := shellWrap.FindStringSubmatch(cmd); g != nil {
			cmd = g[1]
		}
		return "shell " + clip(oneLine(s.short(cmd)), 200)
	case "file_change":
		var parts []string
		for _, c := range it.Changes {
			parts = append(parts, c.Kind+" "+s.rel(c.Path))
		}
		return "apply_patch " + clip(strings.Join(parts, ", "), 200)
	case "mcp_tool_call":
		return it.Server + "." + it.Tool
	case "web_search":
		return "web_search " + clip(oneLine(it.Query), 200)
	}
	return it.Type
}

// rel shows a path inside the working folder relative to it. The client
// may report paths with symbolic links resolved, so a working folder
// reached through a link is compared in both spellings.
func (s *stream) rel(p string) string {
	if s.workdir == "" || !filepath.IsAbs(p) {
		return p
	}
	if r, ok := adapter.Rel(s.workdir, p); ok {
		return r
	}
	return p
}

// short drops the working folder from paths in a text and clips it.
func (s *stream) short(text string) string {
	if s.workdir != "" {
		for _, dir := range adapter.Spellings(s.workdir) {
			for _, sep := range []string{"/", string(filepath.Separator)} {
				text = strings.ReplaceAll(text, strings.TrimRight(dir, sep)+sep, "")
			}
		}
	}
	return clip(text, 2000)
}

// lineWith returns the line of text that holds sub.
func lineWith(text, sub string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return sub
}

// firstLine returns the first non-empty line of a text, clipped, and how
// many more lines there are.
func firstLine(text string, max int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	first := clip(strings.TrimSpace(lines[0]), max)
	if first == "" {
		first = "(empty)"
	}
	if n := len(lines) - 1; n > 0 {
		first += fmt.Sprintf(" (+%d lines)", n)
	}
	return first
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func clip(text string, max int) string {
	r := []rune(text)
	if len(r) > max {
		return string(r[:max]) + "..."
	}
	return text
}
