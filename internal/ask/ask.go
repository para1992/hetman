// Package ask holds the questions Hetman puts to a person and the answers it
// records: their shapes, how answers are checked, and how they are written
// out for a prompt or a terminal.
//
// Questions come from the orders (a gate's or a human node's ask list) or
// from an agent step that asks instead of answering (a dialogue step).
package ask

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Question kinds.
const (
	YesNo  = "yesno"  // the answer is yes or no
	Choice = "choice" // the answer is one of Options
	Text   = "text"   // the answer is free text
)

// Kinds lists the question kinds.
var Kinds = []string{YesNo, Choice, Text}

// Yes and No are the two answers to a yes/no question.
const (
	Yes = "yes"
	No  = "no"
)

// Outcome is the outcome an agent step of a dialogue reports when it asks
// the person questions instead of answering; Field holds the questions.
const (
	Outcome = "questions"
	Field   = "questions"
)

// MaxQuestions is the most questions an agent step may ask in one round.
const MaxQuestions = 20

// Question is one question for a person.
type Question struct {
	ID       string   `json:"id"`
	Question string   `json:"question"`
	Kind     string   `json:"kind"`
	Options  []string `json:"options,omitempty"`
}

// Answer is a person's answer to one question.
type Answer struct {
	ID      string `json:"id"`
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

// IDPattern is what a question id looks like.
var IDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// Expect describes the kind of a question as a person types it.
func (q Question) Expect() string {
	switch q.Kind {
	case Choice:
		return strings.Join(q.Options, "/")
	case Text:
		return "text"
	}
	return "yes/no"
}

// ErrInvalid wraps every problem with a set of answers.
var ErrInvalid = errors.New("invalid answers")

// Given is what a person answered, before it is checked.
type Given struct {
	Values   map[string]string // question id to answer
	Comments map[string]string // question id to comment
	Outcome  string            // the chosen outcome, when outcomes are offered
}

// Check checks given answers against the questions and the outcomes a
// person may choose from (none for a gate or a dialogue step). Every
// question needs an answer; a yes/no answer may be written y or n in any
// case; a choice must be one of its options. A single outcome is chosen
// without asking. It returns the answers in question order and the outcome.
func Check(qs []Question, outcomes []string, g Given) ([]Answer, string, error) {
	var problems []string
	known := map[string]bool{}
	var out []Answer
	for _, q := range qs {
		known[q.ID] = true
		v, ok := g.Values[q.ID]
		v = strings.TrimSpace(v)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("question %s has no answer", q.ID))
			continue
		case q.Kind == YesNo:
			switch strings.ToLower(v) {
			case "yes", "y":
				v = Yes
			case "no", "n":
				v = No
			default:
				problems = append(problems, fmt.Sprintf("question %s: answer yes or no, not %q", q.ID, v))
				continue
			}
		case q.Kind == Choice:
			found := false
			for _, o := range q.Options {
				found = found || o == v
			}
			if !found {
				problems = append(problems, fmt.Sprintf("question %s: choose one of %s, not %q", q.ID, strings.Join(q.Options, ", "), v))
				continue
			}
		}
		out = append(out, Answer{ID: q.ID, Value: v, Comment: strings.TrimSpace(g.Comments[q.ID])})
	}
	for id := range g.Values {
		if !known[id] {
			problems = append(problems, fmt.Sprintf("there is no question %s", id))
		}
	}
	for id := range g.Comments {
		if !known[id] {
			problems = append(problems, fmt.Sprintf("comment for %s: there is no question %s", id, id))
		}
	}
	outcome := strings.TrimSpace(g.Outcome)
	switch {
	case len(outcomes) == 0 && outcome != "":
		problems = append(problems, "this step takes no outcome; it follows from the answers")
	case len(outcomes) == 1 && outcome == "":
		outcome = outcomes[0]
	case len(outcomes) > 0 && outcome == "":
		problems = append(problems, "choose an outcome: "+strings.Join(outcomes, ", "))
	case len(outcomes) > 0:
		found := false
		for _, o := range outcomes {
			found = found || o == outcome
		}
		if !found {
			problems = append(problems, fmt.Sprintf("outcome %q is not one of %s", outcome, strings.Join(outcomes, ", ")))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, "", fmt.Errorf("%w: %s", ErrInvalid, strings.Join(problems, "; "))
	}
	return out, outcome, nil
}

// AllYes reports whether every yes/no question was answered yes; it decides
// a gate.
func AllYes(qs []Question, as []Answer) bool {
	byID := map[string]string{}
	for _, a := range as {
		byID[a.ID] = a.Value
	}
	for _, q := range qs {
		if q.Kind == YesNo && byID[q.ID] != Yes {
			return false
		}
	}
	return true
}

// Write renders questions with their answers as text, one question per
// item: the question, the answer and the comment.
func Write(qs []Question, as []Answer) string {
	byID := map[string]Answer{}
	for _, a := range as {
		byID[a.ID] = a
	}
	var b strings.Builder
	for _, q := range qs {
		a := byID[q.ID]
		fmt.Fprintf(&b, "- %s (%s)\n  Answer: %s\n", q.Question, q.ID, a.Value)
		if a.Comment != "" {
			fmt.Fprintf(&b, "  Comment: %s\n", a.Comment)
		}
	}
	return b.String()
}

// Parse reads the questions of an agent step's output and checks them.
func Parse(raw json.RawMessage) ([]Question, error) {
	var qs []Question
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&qs); err != nil {
		return nil, fmt.Errorf("%s: %v", Field, err)
	}
	var problems []string
	if len(qs) == 0 || len(qs) > MaxQuestions {
		problems = append(problems, fmt.Sprintf("ask from 1 to %d questions, not %d", MaxQuestions, len(qs)))
	}
	seen := map[string]bool{}
	for i, q := range qs {
		at := fmt.Sprintf("question %d", i+1)
		if !IDPattern.MatchString(q.ID) {
			problems = append(problems, fmt.Sprintf("%s: id %q must use letters, digits, '_', '-' and '.' only", at, q.ID))
		} else if seen[q.ID] {
			problems = append(problems, fmt.Sprintf("%s: id %q is used twice", at, q.ID))
		}
		seen[q.ID] = true
		if strings.TrimSpace(q.Question) == "" {
			problems = append(problems, at+": the question is empty")
		}
		switch q.Kind {
		case Choice:
			if msg := CheckOptions(q.Options); msg != "" {
				problems = append(problems, at+": "+msg)
			}
		case YesNo, Text:
			if len(q.Options) > 0 {
				problems = append(problems, at+": options are allowed only for kind choice")
			}
		default:
			problems = append(problems, fmt.Sprintf("%s: kind must be one of %s, not %q", at, strings.Join(Kinds, ", "), q.Kind))
		}
	}
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return qs, nil
}

// CheckOptions checks the options of a choice question and returns what is
// wrong with them, or "".
func CheckOptions(opts []string) string {
	if len(opts) < 2 {
		return "a choice needs at least two options"
	}
	seen := map[string]bool{}
	for _, o := range opts {
		if strings.TrimSpace(o) == "" || o != strings.TrimSpace(o) {
			return fmt.Sprintf("option %q must be non-empty text without spaces around it", o)
		}
		if seen[o] {
			return fmt.Sprintf("option %q is listed twice", o)
		}
		seen[o] = true
	}
	return ""
}

// questionsSchema is the schema of the questions of a dialogue step.
const questionsSchema = `{"type":"array","items":{"type":"object","properties":{` +
	`"id":{"type":"string"},"question":{"type":"string"},` +
	`"kind":{"type":"string","enum":["yesno","choice","text"]},` +
	`"options":{"type":"array","items":{"type":"string"}}},` +
	`"required":["id","question","kind"],"additionalProperties":false}}`

// DialogueSchema turns the schema of an agent node into the schema given to
// the adapter while the step may still ask questions: "questions" is added
// to the outcomes and as a property, and only "outcome" stays required, so
// that both a questions answer and a final answer fit. The engine still
// checks a final answer against the node's own schema.
func DialogueSchema(nodeSchema []byte) ([]byte, error) {
	var s map[string]any
	if err := json.Unmarshal(nodeSchema, &s); err != nil {
		return nil, err
	}
	props, _ := s["properties"].(map[string]any)
	out, _ := props["outcome"].(map[string]any)
	enum, _ := out["enum"].([]any)
	if props == nil || out == nil {
		return nil, errors.New("the schema has no outcome property")
	}
	out["enum"] = append(append([]any{}, enum...), Outcome)
	props[Field] = json.RawMessage(questionsSchema)
	s["required"] = []string{"outcome"}
	return json.Marshal(s)
}
