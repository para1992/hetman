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

// The client's stream-json output: one JSON event per line. Only the fields
// the adapter uses are declared.

type event struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	// system init
	Model string   `json:"model"`
	Tools []string `json:"tools"`
	// assistant and user
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	// assistant messages that wrap an API error, and system api_retry
	Error          string `json:"error"`
	APIError       string `json:"api_error"`
	APIErrorStatus *int   `json:"api_error_status"`
	APIErrorParams *struct {
		RateLimitInfo *rateLimitInfo `json:"rate_limit_info"`
	} `json:"api_error_params"`
	IsAPIErrorMessage bool `json:"is_api_error_message"`
	Attempt           int  `json:"attempt"`
	MaxRetries        int  `json:"max_retries"`
	// rate_limit_event
	RateLimitInfo *rateLimitInfo `json:"rate_limit_info"`
	// result
	IsError           bool            `json:"is_error"`
	Result            string          `json:"result"`
	StructuredOutput  json.RawMessage `json:"structured_output"`
	Errors            []string        `json:"errors"`
	NumTurns          int             `json:"num_turns"`
	DurationMs        int64           `json:"duration_ms"`
	PermissionDenials []struct {
		ToolName  string          `json:"tool_name"`
		ToolUseID string          `json:"tool_use_id"`
		ToolInput json.RawMessage `json:"tool_input"`
	} `json:"permission_denials"`
}

type rateLimitInfo struct {
	Status        string `json:"status"`
	ResetsAt      int64  `json:"resetsAt"` // Unix seconds
	RateLimitType string `json:"rateLimitType"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// answerTool is the tool through which the client delivers structured
// output.
const answerTool = "StructuredOutput"

// stream follows the client's events.
// progressEvery is the least time between two progress messages.
var progressEvery = 10 * time.Second

type stream struct {
	workdir      string
	lastProgress time.Time // when the last progress message was sent
	allowed      []string  // the step's tools entries
	out          *sender
	session      string
	tools        map[string]string          // tool name by tool use id
	inputs       map[string]json.RawMessage // tool input by tool use id
	refused      map[string]string          // the error the client returned for a tool call, by tool use id
	denied       []string                   // the denied tool calls, described with the client's reason
	result       *event
	// API errors the client reported in assistant messages
	apiErrors []event
	// the reset time of a limit that was reached, Unix seconds
	resetsAt int64
	lastText string
}

func newStream(workdir string, out *sender) *stream {
	return &stream{workdir: workdir, out: out, tools: map[string]string{}, inputs: map[string]json.RawMessage{}, refused: map[string]string{}}
}

// line handles one line of the client's standard output.
func (s *stream) line(raw []byte) {
	raw = bytes.TrimSpace(raw)
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Type == "" {
		s.out.log("client: " + clip(oneLine(string(raw)), 300))
		return
	}
	if ev.SessionID != "" && s.session == "" {
		s.session = ev.SessionID
		s.out.session(ev.SessionID)
	}
	if ev.Type == "stream_event" {
		// A piece of a message still being written, thinking included:
		// a sign of life, not worth a log line.
		if now := time.Now(); now.Sub(s.lastProgress) >= progressEvery {
			s.lastProgress = now
			s.out.send(adapter.Message{Type: adapter.TypeProgress})
		}
		return
	}
	indent := ""
	if ev.ParentToolUseID != nil && *ev.ParentToolUseID != "" {
		indent = "  | "
	}
	switch ev.Type {
	case "system":
		switch ev.Subtype {
		case "init":
			tools := strings.Join(ev.Tools, ", ")
			if tools == "" {
				tools = "none"
			}
			s.out.log(fmt.Sprintf("session %s, model %s, tools: %s", ev.SessionID, ev.Model, tools))
		case "api_retry":
			s.out.log(fmt.Sprintf("request failed (%s); retry %d of %d", ev.Error, ev.Attempt, ev.MaxRetries))
		}
	case "rate_limit_event":
		if info := ev.RateLimitInfo; info != nil && info.Status != "allowed" {
			s.out.log(fmt.Sprintf("subscription limit %s (%s)%s", info.Status, info.RateLimitType, resetsText(info.ResetsAt)))
			if info.Status == "rejected" && info.ResetsAt > 0 {
				s.resetsAt = info.ResetsAt
			}
		}
	case "assistant":
		if ev.Error != "" || ev.IsAPIErrorMessage {
			s.apiErrors = append(s.apiErrors, ev)
			if p := ev.APIErrorParams; p != nil && p.RateLimitInfo != nil && p.RateLimitInfo.ResetsAt > 0 {
				s.resetsAt = p.RateLimitInfo.ResetsAt
			}
		}
		for _, b := range s.blocks(ev) {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					s.lastText = t
					s.out.log(indent + s.short(t))
				}
			case "tool_use":
				s.tools[b.ID] = b.Name
				s.inputs[b.ID] = b.Input
				s.out.log(indent + "> " + s.describeTool(b.Name, b.Input))
			}
		}
	case "user":
		for _, b := range s.blocks(ev) {
			if b.Type != "tool_result" || s.tools[b.ToolUseID] == answerTool {
				continue
			}
			text := s.short(firstLine(resultText(b.Content), 200))
			if b.IsError {
				s.refused[b.ToolUseID] = s.short(clip(oneLine(resultText(b.Content)), 400))
				s.out.log(indent + "  < error: " + text)
			} else {
				s.out.log(indent + "  < " + text)
			}
		}
	case "result":
		r := ev
		s.result = &r
		for _, d := range ev.PermissionDenials {
			input := d.ToolInput
			if len(input) == 0 {
				input = s.inputs[d.ToolUseID]
			}
			reason := s.refused[d.ToolUseID]
			if reason == "" {
				reason = "the client gave no reason"
			}
			text := s.describeTool(d.ToolName, input) + ": " + s.why(d.ToolName) + "; the client said: " + reason
			s.denied = append(s.denied, text)
			s.out.log("denied: " + text)
		}
		if !ev.IsError && ev.Subtype == "success" {
			s.out.log(fmt.Sprintf("finished after %d turns in %s", ev.NumTurns, (time.Duration(ev.DurationMs) * time.Millisecond).Round(100*time.Millisecond)))
		}
	}
}

// why says why the client denied a call of a tool: the adapter's own deny
// rules name only Edit, so a call of any other tool matched none of the
// step's tools entries for that tool.
func (s *stream) why(tool string) string {
	changes := tool == "Edit" || tool == "Write" || tool == "NotebookEdit" || tool == "MultiEdit"
	var same []string
	for _, t := range s.allowed {
		name, _, _, _ := splitTool(t)
		if name == tool || changes && (name == "Edit" || name == "Write") {
			same = append(same, t)
		}
	}
	switch {
	case len(same) == 0:
		return "not among the step's tools"
	case changes:
		return "outside the paths of " + strings.Join(same, ", ") + ", or on a path hetman protects"
	}
	return "matched none of the step's entries " + strings.Join(same, ", ")
}

func (s *stream) blocks(ev event) []block {
	if ev.Message == nil {
		return nil
	}
	var bs []block
	if json.Unmarshal(ev.Message.Content, &bs) != nil {
		var text string
		if json.Unmarshal(ev.Message.Content, &text) == nil {
			bs = []block{{Type: "text", Text: text}}
		}
	}
	return bs
}

// finish turns the end of a run into the step's final message.
func (s *stream) finish(exit int, stderr string) attemptResult {
	r := s.result
	if r != nil && r.IsError && missingSession(strings.Join(r.Errors, "\n")+"\n"+stderr) {
		return attemptResult{missingSession: true, final: failure(adapter.ErrPermanent, "no conversation with the session id")}
	}
	if r != nil && !r.IsError && r.Subtype == "success" {
		if isObject(r.StructuredOutput) {
			return attemptResult{final: adapter.Message{Type: adapter.TypeResult, Output: r.StructuredOutput, Denied: s.denied}}
		}
		if obj, ok := extractObject(r.Result); ok {
			return attemptResult{final: adapter.Message{Type: adapter.TypeResult, Output: obj, Denied: s.denied}}
		}
		if len(s.denied) > 0 {
			// The same calls are denied again on a retry.
			return attemptResult{final: failure(adapter.ErrPermanent, fmt.Sprintf("the client denied %d tool call(s) (%s) and the answer holds no JSON object: %s",
				len(s.denied), clip(strings.Join(s.denied, "; "), 600), clip(oneLine(r.Result), 300)), s.denied...)}
		}
		return attemptResult{final: failure(adapter.ErrTransient, "the answer holds no JSON object: "+clip(oneLine(r.Result), 300))}
	}
	return attemptResult{final: s.classify(exit, stderr)}
}

func isObject(raw json.RawMessage) bool {
	var v map[string]any
	return json.Unmarshal(raw, &v) == nil && v != nil
}

// describeTool says in a few words which tool is called on what.
func (s *stream) describeTool(name string, input json.RawMessage) string {
	if name == answerTool {
		var b bytes.Buffer
		json.Compact(&b, input)
		return "answer " + clip(b.String(), 400)
	}
	var in map[string]any
	json.Unmarshal(input, &in)
	for _, k := range []string{"file_path", "notebook_path", "path", "command", "pattern", "url", "query", "description", "prompt"} {
		if v, ok := in[k].(string); ok && strings.TrimSpace(v) != "" {
			if strings.HasSuffix(k, "path") {
				v = s.rel(v)
			}
			return name + " " + clip(oneLine(v), 200)
		}
	}
	return name
}

// rel shows a path inside the working folder relative to it.
// The client reports paths with symbolic links resolved, so a working
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

// resultText is the text of a tool result, which is a string or a list of
// content blocks.
func resultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var bs []block
	json.Unmarshal(raw, &bs)
	var parts []string
	for _, b := range bs {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
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

func resetsText(at int64) string {
	if at <= 0 {
		return ""
	}
	return ", resets at " + time.Unix(at, 0).UTC().Format(time.RFC3339)
}
