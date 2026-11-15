// Package adapter defines the protocol between the engine and an executor
// adapter, version 0.
//
// An adapter is a separate program that runs one agent step. The engine
// starts one adapter process per step and exchanges JSON messages with it,
// one message per line: engine to adapter on standard input, adapter to
// engine on standard output. Standard error is the adapter's own diagnostic
// log and is never parsed.
//
// Before a step the engine runs the adapter with the single extra argument
// --describe; the adapter prints one Description and exits with code 0.
//
// An adapter that declares the usage feature also answers the argument
// --usage followed by the names of usage windows (five_hour, weekly): it
// prints one Usage line with its client's current usage of those windows,
// makes no model request, and exits with code 0.
//
// For a step the engine writes one Start message, then the adapter writes
// any number of log and session messages followed by exactly one result or
// error message, and exits with code 0. The engine may write a cancel
// message at any time; the adapter then stops as soon as it can and exits.
package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Version is the protocol version this package speaks.
const Version = 0

// DescribeFlag is the argument that asks an adapter to describe itself.
const DescribeFlag = "--describe"

// Features an adapter may declare.
const (
	// FeatureResume: the adapter can continue an earlier conversation when
	// the start message carries the session id it reported then.
	FeatureResume = "resume"
	// FeatureProtect: the adapter keeps the client's file tools from writing
	// the paths in the start message's protect list (see Step).
	FeatureProtect = "protect"
	// FeatureCheck: the adapter checks the model, effort and tools of steps
	// without running them when started with CheckFlag (see check.go).
	FeatureCheck = "check"
	// FeatureUsage: the adapter answers UsageFlag with its client's usage
	// of the named windows, measured by the client, without a model
	// request.
	FeatureUsage = "usage"
)

// UsageFlag is the argument that asks an adapter for its client's usage; the
// names of the windows follow it as further arguments.
const UsageFlag = "--usage"

// Usage windows an adapter may be asked about.
const (
	WindowFiveHour = "five_hour" // the client's five-hour window
	WindowWeekly   = "weekly"    // the client's weekly window; a client that counts by month reports that share here
)

// UsageWindows lists the usage windows of the protocol.
var UsageWindows = []string{WindowFiveHour, WindowWeekly}

// Usage is what an adapter prints for UsageFlag. A window the client does
// not report is left out.
type Usage struct {
	Windows []UsageWindow `json:"windows"`
}

// UsageWindow is the usage of one window: the share of the allowance used
// (0 for none, 1 for all of it) and when the window resets.
type UsageWindow struct {
	Name     string  `json:"name"`
	Used     float64 `json:"used"`
	ResetsAt string  `json:"resets_at,omitempty"` // RFC 3339 time
}

// ResetTime returns when the window resets, or the zero time when the
// adapter did not say.
func (w UsageWindow) ResetTime() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, w.ResetsAt)
	return t
}

// ParseUsage reads the output of UsageFlag and checks it against the
// windows that were asked for.
func ParseUsage(out []byte, asked []string) (Usage, error) {
	var u Usage
	text := strings.TrimSpace(string(out))
	if text == "" {
		return u, errors.New("printed nothing for " + UsageFlag)
	}
	if strings.Contains(text, "\n") {
		return u, errors.New("printed more than one line for " + UsageFlag)
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&u); err != nil {
		return u, fmt.Errorf("usage is not valid: %v", err)
	}
	seen := map[string]bool{}
	for _, w := range u.Windows {
		known := false
		for _, a := range asked {
			known = known || a == w.Name
		}
		switch {
		case !known:
			return u, fmt.Errorf("usage reports window %q, which was not asked for (asked: %s)", w.Name, strings.Join(asked, ", "))
		case seen[w.Name]:
			return u, fmt.Errorf("usage reports window %q twice", w.Name)
		case w.Used < 0:
			return u, fmt.Errorf("usage of window %q is negative", w.Name)
		case w.ResetsAt != "" && w.ResetTime().IsZero():
			return u, fmt.Errorf("resets_at %q of window %q is not an RFC 3339 time", w.ResetsAt, w.Name)
		}
		seen[w.Name] = true
	}
	return u, nil
}

// Message types.
const (
	TypeStart   = "start"
	TypeCancel  = "cancel"
	TypeLog     = "log"
	TypeSession = "session"
	TypeResult  = "result"
	TypeError   = "error"
)

// Error kinds an adapter reports.
const (
	ErrTransient = "transient" // worth retrying; consumes an attempt
	ErrPermanent = "permanent" // retrying cannot help; the step fails at once
	ErrLimit     = "limit"     // the subscription limit is reached; does not consume an attempt
	ErrOffline   = "offline"   // no connectivity; does not consume an attempt
)

// ErrorKinds lists the error kinds in the protocol.
var ErrorKinds = []string{ErrTransient, ErrPermanent, ErrLimit, ErrOffline}

// Description is what an adapter prints for --describe.
type Description struct {
	Protocol []int    `json:"protocol"`
	Name     string   `json:"name"`
	Features []string `json:"features,omitempty"`
}

// Has reports whether the adapter declares a feature.
func (d Description) Has(feature string) bool {
	for _, f := range d.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// ParseDescription reads the output of --describe and checks that the
// adapter speaks this protocol version.
func ParseDescription(out []byte) (Description, error) {
	var d Description
	text := strings.TrimSpace(string(out))
	if text == "" {
		return d, errors.New("printed nothing for " + DescribeFlag)
	}
	if strings.Contains(text, "\n") {
		return d, errors.New("printed more than one line for " + DescribeFlag)
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, fmt.Errorf("description is not valid: %v", err)
	}
	if d.Name == "" {
		return d, errors.New("description has no name")
	}
	for _, v := range d.Protocol {
		if v == Version {
			return d, nil
		}
	}
	return d, fmt.Errorf("adapter %q speaks protocol versions %v, not %d", d.Name, d.Protocol, Version)
}

// Step is the work of one start message.
type Step struct {
	RunID   string          `json:"run_id"`
	NodeID  string          `json:"node_id"`
	Attempt int             `json:"attempt"`
	Prompt  string          `json:"prompt"`
	Model   string          `json:"model"`
	Effort  string          `json:"effort"`
	Tools   []string        `json:"tools"`
	Workdir string          `json:"workdir"`
	Schema  json.RawMessage `json:"schema"`
	Session *string         `json:"session"`
	// Protect lists absolute paths the client's file tools must not write,
	// with everything inside them: the engine's own files and the files of
	// the orders. Writable lists absolute folders the step must be able to
	// read and write although they lie inside a protected path or outside
	// Workdir: the campaign's work folder.
	Protect  []string `json:"protect,omitempty"`
	Writable []string `json:"writable,omitempty"`
}

// Start is the engine's first message to the adapter.
type Start struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	Step Step   `json:"step"`
}

// EncodeStart renders a start message as one line.
func EncodeStart(step Step) []byte {
	if step.Tools == nil {
		step.Tools = []string{}
	}
	if len(step.Schema) == 0 {
		step.Schema = json.RawMessage("{}")
	}
	data, _ := json.Marshal(Start{V: Version, Type: TypeStart, Step: step})
	return append(data, '\n')
}

// EncodeCancel renders a cancel message as one line.
func EncodeCancel() []byte {
	return []byte(fmt.Sprintf(`{"v":%d,"type":"%s"}`+"\n", Version, TypeCancel))
}

// Message is one message from the adapter. Fields that do not apply to its
// type are empty.
type Message struct {
	V       int             `json:"v"`
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`     // log
	ID      string          `json:"id,omitempty"`       // session
	Output  json.RawMessage `json:"output,omitempty"`   // result
	Kind    string          `json:"kind,omitempty"`     // error
	Message string          `json:"message,omitempty"`  // error
	RetryAt string          `json:"retry_at,omitempty"` // error of kind limit: RFC 3339 time
	// Denied lists, on a result or an error, the tool calls the client
	// denied during the step, each as the tool, what it was called on and
	// the reason.
	Denied []string `json:"denied,omitempty"`
}

// Terminal reports whether the message ends the step.
func (m Message) Terminal() bool { return m.Type == TypeResult || m.Type == TypeError }

// RetryTime returns the time a limit error says to retry after, or the zero
// time when it gives none.
func (m Message) RetryTime() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, m.RetryAt)
	return t
}

// ParseMessage reads one line written by an adapter and checks it against
// the protocol.
func ParseMessage(line []byte) (Message, error) {
	var m Message
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return m, fmt.Errorf("not a JSON object: %v", err)
	}
	if err := json.Unmarshal(line, &m); err != nil {
		return m, fmt.Errorf("malformed message: %v", err)
	}
	if v, ok := raw["v"]; !ok || string(v) != fmt.Sprint(Version) {
		return m, fmt.Errorf("message must carry \"v\":%d", Version)
	}
	switch m.Type {
	case TypeLog:
		if _, ok := raw["text"]; !ok {
			return m, errors.New("log message has no text")
		}
	case TypeSession:
		if m.ID == "" {
			return m, errors.New("session message has no id")
		}
	case TypeResult:
		if len(m.Output) == 0 || string(m.Output) == "null" {
			return m, errors.New("result message has no output")
		}
	case TypeError:
		known := false
		for _, k := range ErrorKinds {
			known = known || m.Kind == k
		}
		if !known {
			return m, fmt.Errorf("error message has kind %q; known kinds: %s", m.Kind, strings.Join(ErrorKinds, ", "))
		}
		if m.RetryAt != "" {
			if m.Kind != ErrLimit {
				return m, errors.New("retry_at is allowed only on an error of kind limit")
			}
			if m.RetryTime().IsZero() {
				return m, fmt.Errorf("retry_at %q is not an RFC 3339 time", m.RetryAt)
			}
		}
	default:
		return m, fmt.Errorf("unknown message type %q", m.Type)
	}
	return m, nil
}
