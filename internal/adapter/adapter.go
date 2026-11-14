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
)

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
