package adapter

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// An adapter that declares FeatureCheck can check steps without running
// them. The engine starts it with the single extra argument CheckFlag and
// writes one check message: the steps of the orders that use the adapter,
// with node_id, model, effort and tools set. The adapter answers with one
// checked message listing the problems it finds, without contacting a
// model, and exits with code 0. A step it would reject at run time with a
// permanent error should be reported here the same way.

// CheckFlag is the argument that asks an adapter to check steps.
const CheckFlag = "--check"

// Message types of a check.
const (
	TypeCheck   = "check"
	TypeChecked = "checked"
)

// Fields of a step a check problem can be about.
const (
	FieldModel  = "model"
	FieldEffort = "effort"
	FieldTools  = "tools"
)

// CheckRequest is the engine's message for a check.
type CheckRequest struct {
	V     int    `json:"v"`
	Type  string `json:"type"`
	Steps []Step `json:"steps"`
}

// CheckProblem is one problem an adapter finds with a step.
type CheckProblem struct {
	NodeID  string `json:"node_id"`
	Field   string `json:"field"`          // model, effort or tools
	Item    *int   `json:"item,omitempty"` // tools: the index of the entry, from 0
	Message string `json:"message"`
}

// CheckReply is the adapter's answer to a check.
type CheckReply struct {
	V        int            `json:"v"`
	Type     string         `json:"type"`
	Problems []CheckProblem `json:"problems"`
}

// EncodeCheck renders a check message as one line.
func EncodeCheck(steps []Step) []byte {
	for i := range steps {
		if steps[i].Tools == nil {
			steps[i].Tools = []string{}
		}
	}
	data, _ := json.Marshal(CheckRequest{V: Version, Type: TypeCheck, Steps: steps})
	return append(data, '\n')
}

// ParseCheckReply reads the output of a check.
func ParseCheckReply(out []byte) ([]CheckProblem, error) {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return nil, errors.New("printed nothing for " + CheckFlag)
	}
	var r CheckReply
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("check reply is not valid: %v", err)
	}
	if r.V != Version || r.Type != TypeChecked {
		return nil, fmt.Errorf("check reply must be {\"v\":%d,\"type\":%q,...}", Version, TypeChecked)
	}
	return r.Problems, nil
}

// ServeCheck reads a check message from in, asks check about each step and
// writes the reply to out. Adapters call it when started with CheckFlag.
func ServeCheck(in io.Reader, out io.Writer, check func(Step) []CheckProblem) error {
	line, err := bufio.NewReader(in).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return errors.New("no check message")
	}
	var req CheckRequest
	if err := json.Unmarshal(line, &req); err != nil || req.Type != TypeCheck || req.V != Version {
		return fmt.Errorf("expected a check message of protocol version %d", Version)
	}
	reply := CheckReply{V: Version, Type: TypeChecked, Problems: []CheckProblem{}}
	for _, st := range req.Steps {
		for _, p := range check(st) {
			p.NodeID = st.NodeID
			reply.Problems = append(reply.Problems, p)
		}
	}
	data, _ := json.Marshal(reply)
	_, err = fmt.Fprintf(out, "%s\n", data)
	return err
}

// ToolProblem is a problem with entry i of a step's tools.
func ToolProblem(i int, message string) CheckProblem {
	return CheckProblem{Field: FieldTools, Item: &i, Message: message}
}

// ProblemText says a check problem in one line, as a permanent error of a
// step says it.
func ProblemText(st Step, p CheckProblem) string {
	if p.Field == FieldTools && p.Item != nil && *p.Item >= 0 && *p.Item < len(st.Tools) {
		return fmt.Sprintf("tools entry %q: %s", st.Tools[*p.Item], p.Message)
	}
	return p.Field + ": " + p.Message
}
