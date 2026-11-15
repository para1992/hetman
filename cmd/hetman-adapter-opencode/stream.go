package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"hetman/internal/adapter"
)

// The client's JSON output: one event per line, each with a type, a time in
// Unix milliseconds and the session id. Only the fields the adapter uses are
// declared.
type event struct {
	Type      string     `json:"type"`
	Timestamp int64      `json:"timestamp"`
	SessionID string     `json:"sessionID"`
	Part      *part      `json:"part"`
	Error     *clientErr `json:"error"`
}

// part is a piece of an assistant message: "step-start", "text", "tool",
// "reasoning" or "step-finish".
type part struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
	State  *struct {
		Status string         `json:"status"`
		Input  map[string]any `json:"input"`
		Output string         `json:"output"`
		Title  string         `json:"title"`
		Error  string         `json:"error"`
	} `json:"state"`
	Cost   float64 `json:"cost"`
	Tokens *struct {
		Input  int64 `json:"input"`
		Output int64 `json:"output"`
	} `json:"tokens"`
}

// clientErr is the error of an "error" event: a name such as APIError,
// ProviderAuthError or UnknownError, and data with a message and, for an
// API error, the HTTP status and response headers.
type clientErr struct {
	Name string `json:"name"`
	Data struct {
		Message         string            `json:"message"`
		StatusCode      int               `json:"statusCode"`
		ResponseHeaders map[string]string `json:"responseHeaders"`
		ProviderID      string            `json:"providerID"`
	} `json:"data"`
}

// stream follows the client's events.
type stream struct {
	st      adapter.Step
	workdir string
	out     *sender
	session string
	texts   []string // the text parts of the answer, in order
	errors  []clientErr
	steps   int
	first   int64 // time of the first event, Unix milliseconds
	last    int64
	tokIn   int64
	tokOut  int64
	cost    float64
	stopped bool     // a step finished with reason "stop"
	denied  []string // the tool calls the client's permissions denied, with its reason
}

func newStream(st adapter.Step, out *sender) *stream {
	return &stream{st: st, workdir: st.Workdir, out: out}
}

// line handles one line of the client's standard output.
func (s *stream) line(raw []byte) {
	raw = bytes.TrimSpace(raw)
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Type == "" {
		s.out.log("client: " + clip(oneLine(stripANSI(string(raw))), 300))
		return
	}
	s.found(ev.SessionID)
	if ev.Timestamp > 0 {
		if s.first == 0 {
			s.first = ev.Timestamp
		}
		s.last = ev.Timestamp
	}
	p := ev.Part
	switch ev.Type {
	case "text", "reasoning":
		if p == nil {
			return
		}
		t := strings.TrimSpace(p.Text)
		if t == "" {
			return
		}
		if ev.Type == "reasoning" {
			s.out.log("(thinking) " + s.short(firstLine(t, 300)))
			return
		}
		s.texts = append(s.texts, p.Text)
		s.out.log(s.short(t))
	case "tool_use":
		if p == nil || p.State == nil {
			return
		}
		call := s.describeTool(p.Tool, p.State.Title, p.State.Input)
		s.out.log("> " + call)
		if reason, ok := denial(p.State.Error); p.State.Status == "error" && ok {
			text := call + ": " + s.short(reason)
			s.denied = append(s.denied, text)
			s.out.log("denied: " + text)
		} else if p.State.Status == "error" {
			s.out.log("  < error: " + s.short(firstLine(p.State.Error, 300)))
		} else {
			s.out.log("  < " + s.short(firstLine(toolOutput(p.State.Output), 200)))
		}
	case "step_finish":
		if p == nil {
			return
		}
		s.steps++
		s.cost += p.Cost
		if p.Tokens != nil {
			s.tokIn += p.Tokens.Input
			s.tokOut += p.Tokens.Output
		}
		if p.Reason == "stop" {
			s.stopped = true
		}
	case "error":
		if ev.Error != nil {
			s.errors = append(s.errors, *ev.Error)
			s.out.log("client error: " + clip(oneLine(errText(*ev.Error)), 500))
		}
	}
}

// found reports the session id the first time it is known.
func (s *stream) found(id string) {
	if id == "" || s.session != "" {
		return
	}
	s.session = id
	s.out.session(id)
	model := s.st.Model
	if model == "" {
		model = "the client's default"
	}
	s.out.log(fmt.Sprintf("session %s, model %s, tools: %s", id, model, toolsText(s.st.Tools)))
}

// denial reads the error of a tool call that the client's permissions
// refused: its reason, without the list of rules the client appends.
func denial(errText string) (string, bool) {
	t := oneLine(errText)
	for _, mark := range []string{"prevents you from using this specific tool call", "rejected permission to use this specific tool call"} {
		if strings.Contains(t, mark) {
			if i := strings.Index(t, " Here are some of the relevant rules"); i > 0 {
				t = t[:i]
			}
			return clip(t, 300), true
		}
	}
	return "", false
}

func toolsText(tools []string) string {
	if len(tools) == 0 {
		return "none"
	}
	return strings.Join(tools, ", ")
}

func errText(e clientErr) string {
	if e.Data.Message != "" {
		return e.Data.Message
	}
	return e.Name
}

// finish turns the end of a run into the step's final message.
func (s *stream) finish(exit int, stderr string) attemptResult {
	if s.session == "" && missingSession(stderr) {
		return attemptResult{missingSession: true, final: failure(adapter.ErrPermanent, "no session with the id")}
	}
	if len(s.errors) == 0 && exit == 0 {
		s.out.log(s.summary())
		answer := strings.Join(s.texts, "\n")
		if obj, ok := extractObject(answer); ok {
			return attemptResult{final: adapter.Message{Type: adapter.TypeResult, Output: obj, Denied: s.denied}}
		}
		if len(s.denied) > 0 {
			// The same calls are denied again on a retry.
			return attemptResult{final: failure(adapter.ErrPermanent, fmt.Sprintf("the client denied %d tool call(s) (%s) and the answer holds no JSON object: %s",
				len(s.denied), clip(strings.Join(s.denied, "; "), 600), clip(oneLine(answer), 300)), s.denied...)}
		}
		if strings.TrimSpace(answer) == "" {
			return attemptResult{final: failure(adapter.ErrTransient, "the client gave no answer")}
		}
		return attemptResult{final: failure(adapter.ErrTransient, "the answer holds no JSON object: "+clip(oneLine(answer), 300))}
	}
	return attemptResult{final: s.classify(exit, stderr)}
}

func (s *stream) summary() string {
	text := fmt.Sprintf("finished after %d model steps", s.steps)
	if s.last > s.first {
		text += " in " + (time.Duration(s.last-s.first) * time.Millisecond).Round(100*time.Millisecond).String()
	}
	if s.tokIn > 0 || s.tokOut > 0 {
		text += fmt.Sprintf(", %d tokens in, %d out", s.tokIn, s.tokOut)
	}
	if s.cost > 0 {
		text += fmt.Sprintf(", cost %.4f", s.cost)
	}
	return text
}

func isObject(raw json.RawMessage) bool {
	var v map[string]any
	return json.Unmarshal(raw, &v) == nil && v != nil
}

// describeTool says in a few words which tool is called on what.
func (s *stream) describeTool(name, title string, in map[string]any) string {
	for _, k := range []string{"filePath", "path", "command", "pattern", "url", "query", "description"} {
		if v, ok := in[k].(string); ok && strings.TrimSpace(v) != "" {
			if strings.HasSuffix(k, "ath") {
				v = s.rel(v)
			}
			return name + " " + clip(oneLine(v), 200)
		}
	}
	if title != "" {
		return name + " " + clip(oneLine(s.short(title)), 200)
	}
	return name
}

// toolOutput drops the markup the client wraps file contents in.
func toolOutput(out string) string {
	if i := strings.Index(out, "<content>"); i >= 0 {
		out = out[i+len("<content>"):]
		out = strings.TrimSuffix(strings.TrimSpace(out), "</content>")
	}
	return out
}

// rel shows a path inside the working folder relative to it.
// The client may report paths with symbolic links resolved, so a working
// folder reached through a link is compared in both spellings.
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
