// Package explain tells what a campaign did and where it stands, in plain
// sentences built from its chronicle events by fixed templates.
//
// Every sentence states only what the events record: node names, outcomes,
// times, reasons, answers. Nothing is guessed, and the flow config is not
// consulted, so the explanation stays true even after the orders changed.
package explain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
)

// Query kinds: the questions an explanation answers.
const (
	Summary  = "summary"  // all of the below
	Where    = "where"    // where the campaign is, what it waits for and since when
	History  = "history"  // what ran, with what outcome
	Rework   = "rework"   // why the flow went back, and how many times
	Failures = "failures" // what failed and the recorded reason
	Warnings = "warnings" // which soft checks warned
)

// Kinds lists the query kinds.
var Kinds = []string{Summary, Where, History, Rework, Failures, Warnings}

// Section is one part of an explanation.
type Section struct {
	Kind  string   `json:"kind"`
	Title string   `json:"title"`
	Lines []string `json:"lines"`
}

var titles = map[string]string{
	Where:    "Where it is",
	History:  "What ran",
	Rework:   "Rework",
	Failures: "What failed",
	Warnings: "Warnings",
}

// Campaign explains a campaign. kind is one of Kinds ("" for the summary);
// node, when given, limits history, rework, failures and warnings to that
// node. Times are shown in loc.
func Campaign(k chronicle.Campaign, events []chronicle.Event, kind, node string, loc *time.Location) []Section {
	x := &explainer{k: k, events: events, node: node, loc: loc}
	if loc == nil {
		x.loc = time.Local
	}
	var kinds []string
	if kind == "" || kind == Summary {
		kinds = Kinds[1:]
	} else {
		kinds = []string{kind}
	}
	var out []Section
	for _, kd := range kinds {
		var lines []string
		switch kd {
		case Where:
			lines = x.where()
		case History:
			lines = x.history()
		case Rework:
			lines = x.rework()
		case Failures:
			lines = x.failures()
		case Warnings:
			lines = x.warnings()
		default:
			continue
		}
		out = append(out, Section{Kind: kd, Title: titles[kd], Lines: lines})
	}
	return out
}

// Text renders sections for a terminal.
func Text(sections []Section) string {
	var b strings.Builder
	for i, s := range sections {
		if i > 0 {
			b.WriteByte('\n')
		}
		if len(sections) > 1 {
			b.WriteString(s.Title + ":\n")
		}
		for _, l := range s.Lines {
			if len(sections) > 1 {
				b.WriteString("  ")
			}
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

type explainer struct {
	k      chronicle.Campaign
	events []chronicle.Event
	node   string
	loc    *time.Location
}

func (x *explainer) at(t time.Time) string { return t.In(x.loc).Format("2006-01-02 15:04:05") }

func (x *explainer) wanted(node string) bool { return x.node == "" || x.node == node }

func dur(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return d.String()
	}
	if d < time.Minute {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// last returns the newest event of a kind, optionally at a node.
func (x *explainer) last(kind, node string) *chronicle.Event {
	for i := len(x.events) - 1; i >= 0; i-- {
		if e := &x.events[i]; e.Kind == kind && (node == "" || e.Node == node) {
			return e
		}
	}
	return nil
}

func who(e *chronicle.Event) string {
	if e.By != "" {
		return e.By
	}
	return "A person"
}

func via(e *chronicle.Event) string {
	switch e.Via {
	case "page":
		return " on the page"
	case "command":
		return " from the command line"
	}
	return ""
}

func questions(qs []ask.Question) string {
	parts := make([]string, len(qs))
	for i, q := range qs {
		parts[i] = fmt.Sprintf("%q", q.Question)
	}
	return strings.Join(parts, "; ")
}

func either(list []string) string {
	switch len(list) {
	case 0:
		return ""
	case 1:
		return list[0]
	}
	return strings.Join(list[:len(list)-1], ", ") + " or " + list[len(list)-1]
}

func (x *explainer) where() []string {
	if len(x.events) == 0 {
		return []string{fmt.Sprintf("Campaign %d has no events.", x.k.ID)}
	}
	st := chronicle.Replay(x.events)
	first := x.events[0]
	lines := []string{fmt.Sprintf("Campaign %d of flow %s started at %s at %s.", x.k.ID, x.k.Flow, x.at(first.At), first.Node)}
	var s string
	switch {
	case st.End != "":
		t := x.last(chronicle.Transition, "")
		s = fmt.Sprintf("It is done: %s finished with %s at %s, which leads to done.", t.Node, t.Outcome, x.at(t.At))
		if st.End == "failed" {
			s = fmt.Sprintf("It failed: %s finished with %s at %s, which leads to failed.", t.Node, t.Outcome, x.at(t.At))
		}
	case st.Error != "":
		e := x.last(chronicle.Stopped, "")
		s = fmt.Sprintf("It stopped in the error state at %s at %s: %s", e.Node, x.at(e.At), strings.TrimSuffix(e.Error, "."))
		s += "."
	case st.Halted:
		e := x.last(chronicle.Halted, "")
		s = fmt.Sprintf("It is halted before %s since %s.", e.Node, x.at(e.At))
	case st.Asking():
		q := st.Ask
		switch q.Reason {
		case chronicle.ReasonHuman:
			s = fmt.Sprintf("It has been waiting for a person since %s: %s asks for a decision between %s", x.at(q.At), q.Node, either(q.Outcomes))
			if len(q.Questions) > 0 {
				s += fmt.Sprintf(" and %d question(s): %s", len(q.Questions), questions(q.Questions))
			}
			s += "."
		case chronicle.ReasonDialogue:
			s = fmt.Sprintf("It has been waiting for a person since %s: the agent step %s asks %d question(s) in round %d: %s.", x.at(q.At), q.Node, len(q.Questions), q.Round, questions(q.Questions))
		default:
			s = fmt.Sprintf("It has been waiting for a person since %s: the gate %s asks %d question(s): %s.", x.at(q.At), q.Node, len(q.Questions), questions(q.Questions))
		}
	case st.Ask != nil && st.Answer != nil:
		s = fmt.Sprintf("%s answered the questions of %s at %s; %s has not finished since.", who(st.Answer), st.Ask.Node, x.at(st.Answer.At), st.Ask.Node)
	case st.Waiting != "":
		f := x.last(chronicle.StepFinished, st.Node)
		switch {
		case st.Waiting == chronicle.WaitLimit && !st.WaitUntil.IsZero():
			s = fmt.Sprintf("It is waiting at %s: attempt %d hit the subscription limit at %s (%s); the step is due to run again at %s. The attempt does not count.",
				st.Node, f.Attempt, x.at(f.At), f.Error, x.at(st.WaitUntil))
		case st.Waiting == chronicle.WaitLimit:
			s = fmt.Sprintf("It has been stopped at %s since %s: attempt %d hit the subscription limit (%s) and no time to retry was given. The attempt does not count; the step runs again when the campaign is resumed.",
				st.Node, x.at(f.At), f.Attempt, f.Error)
		default:
			s = fmt.Sprintf("It has been stopped at %s since %s: attempt %d lost connectivity (%s). The attempt does not count; the step runs again when the campaign is resumed.",
				st.Node, x.at(f.At), f.Attempt, f.Error)
		}
	case st.Running:
		e := x.last(chronicle.StepStarted, st.Node)
		s = fmt.Sprintf("%s attempt %d started at %s and has not finished.", st.Node, e.Attempt, x.at(e.At))
	default:
		if e := x.lastStep(st.Node); e != nil && e.Kind == chronicle.StepInterrupted {
			s = fmt.Sprintf("%s attempt %d was interrupted at %s; the step runs again when the campaign is resumed.", st.Node, e.Attempt, x.at(e.At))
		} else if t := x.last(chronicle.Transition, ""); t != nil {
			s = fmt.Sprintf("It moved to %s at %s; %s has not started since.", st.Node, x.at(t.At), st.Node)
		} else {
			s = fmt.Sprintf("%s has not started yet.", st.Node)
		}
	}
	lines = append(lines, s)
	if st.HaltRequested && !st.Halted && st.End == "" {
		e := x.last(chronicle.HaltRequested, "")
		lines = append(lines, fmt.Sprintf("A halt was requested at %s; it takes effect after the current step.", x.at(e.At)))
	}
	return lines
}

// lastStep is the newest step event at a node.
func (x *explainer) lastStep(node string) *chronicle.Event {
	for i := len(x.events) - 1; i >= 0; i-- {
		e := &x.events[i]
		if e.Node == node && (e.Kind == chronicle.StepStarted || e.Kind == chronicle.StepFinished || e.Kind == chronicle.StepInterrupted) {
			return e
		}
	}
	return nil
}

// transitionAfter is the transition out of a node recorded after event i.
func (x *explainer) transitionAfter(i int, node string) *chronicle.Event {
	for j := i + 1; j < len(x.events); j++ {
		e := &x.events[j]
		if e.Kind == chronicle.StepStarted {
			return nil
		}
		if e.Kind == chronicle.Transition && e.Node == node {
			return e
		}
	}
	return nil
}

// checks lists the checks of a gate attempt with their outcomes.
func (x *explainer) checks(node string, attempt int) []string {
	var out []string
	for _, e := range x.events {
		if e.Kind != chronicle.CheckFinished || e.Node != node || e.Attempt != attempt {
			continue
		}
		s := e.Check + " "
		switch {
		case e.Error != "":
			s += "could not run: " + e.Error
		case e.Soft && e.Outcome != "ok":
			s += e.Outcome + " (soft, a warning)"
		default:
			s += e.Outcome
		}
		out = append(out, s)
	}
	return out
}

func answers(e *chronicle.Event) string {
	var parts []string
	for _, a := range e.Answers {
		s := a.ID + ": " + a.Value
		if a.Comment != "" {
			s += fmt.Sprintf(" (comment %q)", a.Comment)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func (x *explainer) history() []string {
	var lines []string
	asks := map[int64]bool{}
	for i := range x.events {
		e := &x.events[i]
		if !x.wanted(e.Node) && e.Kind != chronicle.Resumed && e.Kind != chronicle.HaltRequested {
			continue
		}
		var s string
		switch e.Kind {
		case chronicle.StepFinished:
			switch {
			case e.Failure == chronicle.FailLimit || e.Failure == chronicle.FailOffline:
				s = fmt.Sprintf("%s attempt %d was cut off (%s): %s; it does not count.", e.Node, e.Attempt, e.Failure, e.Error)
			case e.Failure != "":
				s = fmt.Sprintf("%s attempt %d failed (%s): %s.", e.Node, e.Attempt, e.Failure, strings.TrimSuffix(e.Error, "."))
				if e.Outcome != "" {
					s += fmt.Sprintf(" Its outcome is %s.", e.Outcome)
				}
			case e.Error != "":
				s = fmt.Sprintf("%s attempt %d could not finish: %s.", e.Node, e.Attempt, strings.TrimSuffix(e.Error, "."))
			case e.Outcome == "":
				continue // the step asked a person; the asked event says so
			default:
				s = fmt.Sprintf("%s attempt %d finished with %s after %s.", e.Node, e.Attempt, e.Outcome, dur(e.Millis))
				if cs := x.checks(e.Node, e.Attempt); len(cs) > 0 {
					s = strings.TrimSuffix(s, ".") + "; checks: " + strings.Join(cs, ", ") + "."
				}
			}
			if t := x.transitionAfter(i, e.Node); t != nil && e.Outcome != "" {
				s += fmt.Sprintf(" The flow went to %s.", t.To)
			}
		case chronicle.Asked:
			asks[e.Seq] = true
			switch e.Reason {
			case chronicle.ReasonHuman:
				s = fmt.Sprintf("%s asked a person to choose %s", e.Node, either(e.Outcomes))
				if len(e.Questions) > 0 {
					s += " and to answer " + questions(e.Questions)
				}
				s += "."
			default:
				s = fmt.Sprintf("%s asked a person: %s.", e.Node, questions(e.Questions))
			}
		case chronicle.Answered:
			if !asks[e.Ask] {
				continue // only the first answer to a question counts
			}
			delete(asks, e.Ask)
			s = fmt.Sprintf("%s answered %s%s", who(e), e.Node, via(e))
			if a := answers(e); a != "" {
				s += ": " + a
			}
			if e.Outcome != "" {
				s += "; chose " + e.Outcome
			}
			s += "."
		case chronicle.StepInterrupted:
			s = fmt.Sprintf("%s attempt %d was interrupted.", e.Node, e.Attempt)
		case chronicle.Stopped:
			s = fmt.Sprintf("The campaign stopped in the error state at %s: %s.", e.Node, strings.TrimSuffix(e.Error, "."))
		case chronicle.HaltRequested:
			s = "A halt was requested."
		case chronicle.Halted:
			s = fmt.Sprintf("The campaign halted before %s.", e.Node)
		case chronicle.Resumed:
			s = "The campaign was resumed."
		default:
			continue
		}
		lines = append(lines, x.at(e.At)+"  "+s)
	}
	if len(lines) == 0 {
		if x.node != "" {
			return []string{fmt.Sprintf("Nothing ran at %s.", x.node)}
		}
		return []string{"Nothing has run yet."}
	}
	return lines
}

// clip shortens an agent's output for a sentence.
func clip(raw json.RawMessage) string {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	delete(v, "outcome")
	if len(v) == 0 {
		return ""
	}
	data, _ := json.Marshal(v)
	s := string(data)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func (x *explainer) rework() []string {
	// A move goes back when its target first ran no later than the node
	// it leaves: the order nodes first ran in is the order of the flow as
	// this campaign took it.
	first := map[string]int{}
	type back struct {
		e    *chronicle.Event
		why  string
		into string
	}
	var backs []back
	count := map[string]int{}
	var order []string
	for i := range x.events {
		e := &x.events[i]
		switch e.Kind {
		case chronicle.StepStarted:
			if _, ok := first[e.Node]; !ok {
				first[e.Node] = i
			}
		case chronicle.Transition:
			to, ok := first[e.To]
			if from, ok2 := first[e.Node]; !ok || !ok2 || to > from || !x.wanted(e.To) {
				continue
			}
			if count[e.To] == 0 {
				order = append(order, e.To)
			}
			count[e.To]++
			backs = append(backs, back{e: e, why: x.why(i, e), into: e.To})
		}
	}
	if len(backs) == 0 {
		if x.node != "" {
			return []string{fmt.Sprintf("The flow never went back to %s.", x.node)}
		}
		return []string{"The flow never went back to an earlier step."}
	}
	var lines []string
	for _, to := range order {
		lines = append(lines, fmt.Sprintf("The flow went back to %s %s.", to, times(count[to])))
	}
	for _, b := range backs {
		s := fmt.Sprintf("%s  %s finished with %s and sent the flow back to %s", x.at(b.e.At), b.e.Node, b.e.Outcome, b.into)
		if b.why != "" {
			s += " (" + b.why + ")"
		}
		lines = append(lines, s+".")
	}
	return lines
}

func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

// why gives what the chronicle records about the step that sent the flow
// back: the checks that did not pass, the answers of a person, or the
// output of an agent step.
func (x *explainer) why(i int, t *chronicle.Event) string {
	var fin *chronicle.Event
	for j := i - 1; j >= 0; j-- {
		if e := &x.events[j]; e.Kind == chronicle.StepFinished && e.Node == t.Node && e.Outcome != "" {
			fin = e
			break
		}
	}
	if fin == nil {
		return ""
	}
	var parts []string
	for _, e := range x.events[:i] {
		if e.Kind == chronicle.CheckFinished && e.Node == t.Node && e.Attempt == fin.Attempt && e.Outcome != "ok" && !e.Soft {
			parts = append(parts, fmt.Sprintf("required check %s reported %s", e.Check, e.Outcome))
		}
	}
	for j := i - 1; j >= 0; j-- {
		e := &x.events[j]
		if e.Kind == chronicle.Answered && e.Node == t.Node {
			var no []string
			for _, a := range e.Answers {
				if a.Value == ask.No {
					no = append(no, a.ID)
				}
			}
			switch {
			case len(no) > 0:
				parts = append(parts, fmt.Sprintf("%s answered no to %s", who(e), strings.Join(no, ", ")))
			case e.Outcome != "":
				parts = append(parts, fmt.Sprintf("%s chose %s", who(e), e.Outcome))
			}
			break
		}
		if e.Kind == chronicle.StepStarted && e.Node == t.Node {
			break
		}
	}
	if out := clip(fin.Output); out != "" {
		parts = append(parts, "its output: "+out)
	}
	return strings.Join(parts, "; ")
}

func (x *explainer) failures() []string {
	var lines []string
	for _, e := range x.events {
		if !x.wanted(e.Node) {
			continue
		}
		var s string
		switch {
		case e.Kind == chronicle.StepFinished && (e.Failure == chronicle.FailLimit || e.Failure == chronicle.FailOffline):
			s = fmt.Sprintf("%s attempt %d was cut off (%s): %s; it does not count.", e.Node, e.Attempt, e.Failure, e.Error)
		case e.Kind == chronicle.StepFinished && e.Failure != "":
			s = fmt.Sprintf("%s attempt %d failed (%s): %s.", e.Node, e.Attempt, e.Failure, strings.TrimSuffix(e.Error, "."))
		case e.Kind == chronicle.StepFinished && e.Error != "":
			s = fmt.Sprintf("%s attempt %d could not finish: %s.", e.Node, e.Attempt, strings.TrimSuffix(e.Error, "."))
		case e.Kind == chronicle.StepFinished && (e.Outcome == "fail" || e.Outcome == "timeout" || e.Outcome == "blocked"):
			s = fmt.Sprintf("%s attempt %d finished with %s", e.Node, e.Attempt, e.Outcome)
			if e.Exit != nil {
				s += fmt.Sprintf(" (exit code %d)", *e.Exit)
			}
			var req []string
			for _, c := range x.events {
				if c.Kind == chronicle.CheckFinished && c.Node == e.Node && c.Attempt == e.Attempt && !c.Soft && c.Outcome != "ok" && c.Error == "" {
					req = append(req, fmt.Sprintf("%s reported %s", c.Check, c.Outcome))
				}
			}
			if len(req) > 0 {
				s += ": required check " + strings.Join(req, ", required check ")
			}
			s += "."
		case e.Kind == chronicle.CheckFinished && e.Error != "":
			s = fmt.Sprintf("Check %s of %s attempt %d could not run: %s.", e.Check, e.Node, e.Attempt, strings.TrimSuffix(e.Error, "."))
		case e.Kind == chronicle.Stopped:
			s = fmt.Sprintf("The campaign stopped in the error state at %s: %s.", e.Node, strings.TrimSuffix(e.Error, "."))
		default:
			continue
		}
		lines = append(lines, x.at(e.At)+"  "+s)
	}
	if len(lines) == 0 {
		if x.node != "" {
			return []string{fmt.Sprintf("Nothing failed at %s.", x.node)}
		}
		return []string{"Nothing failed."}
	}
	return lines
}

func (x *explainer) warnings() []string {
	var lines []string
	for _, e := range x.events {
		if e.Kind == chronicle.CheckFinished && e.Soft && e.Outcome != "ok" && e.Error == "" && x.wanted(e.Node) {
			lines = append(lines, fmt.Sprintf("%s  The soft check %s of %s reported %s in attempt %d; it only warned.", x.at(e.At), e.Check, e.Node, e.Outcome, e.Attempt))
		}
	}
	if len(lines) == 0 {
		return []string{"No soft check warned."}
	}
	return lines
}
