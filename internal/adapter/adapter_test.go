package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseMessage(t *testing.T) {
	good := map[string]Message{
		`{"v":0,"type":"log","text":""}`:      {Type: TypeLog},
		`{"v":0,"type":"session","id":"abc"}`: {Type: TypeSession, ID: "abc"},
		`{"v":0,"type":"error","kind":"limit","message":"m","retry_at":"2030-01-02T03:04:05Z"}`: {Type: TypeError, Kind: ErrLimit, Message: "m", RetryAt: "2030-01-02T03:04:05Z"},
		`{"v":0,"type":"error","kind":"offline"}`:                                               {Type: TypeError, Kind: ErrOffline},
	}
	for line, want := range good {
		got, err := ParseMessage([]byte(line))
		if err != nil || got.Type != want.Type || got.ID != want.ID || got.Kind != want.Kind || got.RetryAt != want.RetryAt {
			t.Errorf("%s: %+v, %v", line, got, err)
		}
	}
	m, err := ParseMessage([]byte(`{"v":0,"type":"result","output":{"outcome":"ok"}}`))
	if err != nil || string(m.Output) != `{"outcome":"ok"}` || !m.Terminal() {
		t.Fatalf("result %+v %v", m, err)
	}
	if m, _ := ParseMessage([]byte(`{"v":0,"type":"error","kind":"limit","retry_at":"2030-01-02T03:04:05+02:00"}`)); m.RetryTime().Hour() != 1 && m.RetryTime().UTC().Hour() != 1 {
		t.Fatalf("retry time %v", m.RetryTime())
	}
	bad := map[string]string{
		`not json`:                              "not a JSON object",
		`{"type":"log","text":"x"}`:             `"v":0`,
		`{"v":1,"type":"log","text":"x"}`:       `"v":0`,
		`{"v":0,"type":"log"}`:                  "no text",
		`{"v":0,"type":"session","id":""}`:      "no id",
		`{"v":0,"type":"result"}`:               "no output",
		`{"v":0,"type":"result","output":null}`: "no output",
		`{"v":0,"type":"error","kind":"oops"}`:  `kind "oops"`,
		`{"v":0,"type":"error","kind":"transient","retry_at":"2030-01-02T03:04:05Z"}`: "only on an error of kind limit",
		`{"v":0,"type":"error","kind":"limit","retry_at":"tomorrow"}`:                 "not an RFC 3339 time",
		`{"v":0,"type":"progress"}`:     `unknown message type "progress"`,
		`{"v":0,"type":"log","text":5}`: "malformed",
	}
	for line, want := range bad {
		if _, err := ParseMessage([]byte(line)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", line, err, want)
		}
	}
}

func TestParseDescription(t *testing.T) {
	d, err := ParseDescription([]byte(`{"protocol": [0, 1], "name": "x", "features": ["resume"]}` + "\n"))
	if err != nil || !d.Has(FeatureResume) || d.Has("tools") {
		t.Fatalf("%+v %v", d, err)
	}
	for in, want := range map[string]string{
		``:                                     "printed nothing",
		`{"protocol":[0],"name":"x"}` + "\n{}": "more than one line",
		`{"protocol":[1],"name":"x"}`:          "not 0",
		`{"protocol":[0]}`:                     "no name",
		`{"protocol":[0],"name":"x","y":1}`:    "not valid",
	} {
		if _, err := ParseDescription([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
	}
}

func TestEncode(t *testing.T) {
	line := EncodeStart(Step{RunID: "1", NodeID: "a", Attempt: 2, Prompt: "p"})
	var s Start
	if err := json.Unmarshal(line, &s); err != nil || s.V != 0 || s.Type != TypeStart || s.Step.Tools == nil || string(s.Step.Schema) != "{}" || s.Step.Session != nil {
		t.Fatalf("%s %+v %v", line, s, err)
	}
	if !strings.Contains(string(line), `"session":null`) || line[len(line)-1] != '\n' {
		t.Fatalf("%s", line)
	}
	if string(EncodeCancel()) != `{"v":0,"type":"cancel"}`+"\n" {
		t.Fatalf("%s", EncodeCancel())
	}
}
