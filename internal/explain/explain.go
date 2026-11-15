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
	"strconv"
	"strings"
	"time"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
	"hetman/internal/i18n"
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

// Campaign explains a campaign in English. kind is one of Kinds ("" for
// the summary); node, when given, limits history, rework, failures and
// warnings to that node. Times are shown in loc.
func Campaign(k chronicle.Campaign, events []chronicle.Event, kind, node string, loc *time.Location) []Section {
	return In(nil, k, events, kind, node, loc)
}

// In explains a campaign as Campaign does, in the language of p.
func In(p *i18n.Printer, k chronicle.Campaign, events []chronicle.Event, kind, node string, loc *time.Location) []Section {
	x := &explainer{p: p, k: k, events: events, node: node, loc: loc}
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
		out = append(out, Section{Kind: kd, Title: p.T("explain.title." + kd), Lines: lines})
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
	p      *i18n.Printer
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

func (x *explainer) who(e *chronicle.Event) string {
	if e.By != "" {
		return e.By
	}
	return x.p.T("explain.who")
}

func (x *explainer) via(e *chronicle.Event) string {
	switch e.Via {
	case "page":
		return x.p.T("explain.via.page")
	case "command":
		return x.p.T("explain.via.command")
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

func (x *explainer) either(list []string) string {
	switch len(list) {
	case 0:
		return ""
	case 1:
		return list[0]
	}
	return x.p.T("explain.either", "list", strings.Join(list[:len(list)-1], ", "), "last", list[len(list)-1])
}

func trim(s string) string { return strings.TrimSuffix(s, ".") }

func (x *explainer) where() []string {
	p := x.p
	if len(x.events) == 0 {
		return []string{p.T("explain.where.no_events", "id", x.k.ID)}
	}
	st := chronicle.Replay(x.events)
	first := x.events[0]
	lines := []string{p.T("explain.where.started", "id", x.k.ID, "flow", x.k.Flow, "at", x.at(first.At), "node", first.Node)}
	var s string
	switch {
	case st.End != "":
		t := x.last(chronicle.Transition, "")
		key := "explain.where.done"
		if st.End == "failed" {
			key = "explain.where.failed"
		}
		s = p.T(key, "node", t.Node, "outcome", t.Outcome, "at", x.at(t.At))
	case st.Error != "":
		e := x.last(chronicle.Stopped, "")
		s = p.T("explain.where.error", "node", e.Node, "at", x.at(e.At), "error", trim(e.Error))
	case st.Halted:
		e := x.last(chronicle.Halted, "")
		s = p.T("explain.where.halted", "node", e.Node, "at", x.at(e.At))
	case st.Asking():
		q := st.Ask
		switch q.Reason {
		case chronicle.ReasonHuman:
			if len(q.Questions) > 0 {
				s = p.T("explain.where.human_questions", "at", x.at(q.At), "node", q.Node, "outcomes", x.either(q.Outcomes),
					"n", len(q.Questions), "questions", questions(q.Questions))
			} else {
				s = p.T("explain.where.human", "at", x.at(q.At), "node", q.Node, "outcomes", x.either(q.Outcomes))
			}
		case chronicle.ReasonDialogue:
			s = p.T("explain.where.dialogue", "at", x.at(q.At), "node", q.Node, "n", len(q.Questions), "round", q.Round, "questions", questions(q.Questions))
		default:
			s = p.T("explain.where.gate", "at", x.at(q.At), "node", q.Node, "n", len(q.Questions), "questions", questions(q.Questions))
		}
	case st.Ask != nil && st.Answer != nil:
		s = p.T("explain.where.answered", "who", x.who(st.Answer), "node", st.Ask.Node, "at", x.at(st.Answer.At))
	case st.Waiting != "":
		f := x.last(chronicle.StepFinished, st.Node)
		nc := x.last(chronicle.NoClient, st.Node)
		switch {
		case nc != nil && (f == nil || nc.Seq > f.Seq):
			role, usage := "", ""
			if nc.Choice != nil {
				role, usage = nc.Choice.Role, UsageWords(p, nc.Choice, x.loc)
			}
			if nc.WaitUntil != nil {
				s = p.T("explain.where.no_client_until", "node", st.Node, "at", x.at(nc.At), "role", role, "usage", usage, "until", x.at(st.WaitUntil))
			} else {
				s = p.T("explain.where.no_client", "node", st.Node, "at", x.at(nc.At), "role", role, "usage", usage)
			}
		case st.Waiting == chronicle.WaitLimit && !st.WaitUntil.IsZero():
			s = p.T("explain.where.limit_until", "node", st.Node, "attempt", f.Attempt, "at", x.at(f.At), "error", f.Error, "until", x.at(st.WaitUntil))
		case st.Waiting == chronicle.WaitLimit:
			s = p.T("explain.where.limit", "node", st.Node, "at", x.at(f.At), "attempt", f.Attempt, "error", f.Error)
		default:
			s = p.T("explain.where.offline", "node", st.Node, "at", x.at(f.At), "attempt", f.Attempt, "error", f.Error)
		}
	case st.LockWait != "" && !st.Running:
		e := x.last(chronicle.LockWaiting, st.Node)
		s = p.T("explain.where.lock", "at", x.at(e.At), "node", st.Node, "lock", st.LockWait, "holder", st.LockHolder)
	case st.Running:
		e := x.last(chronicle.StepStarted, st.Node)
		s = p.T("explain.where.running", "node", st.Node, "attempt", e.Attempt, "at", x.at(e.At))
	default:
		if e := x.lastStep(st.Node); e != nil && e.Kind == chronicle.StepInterrupted {
			s = p.T("explain.where.interrupted", "node", st.Node, "attempt", e.Attempt, "at", x.at(e.At))
		} else if b := x.lastSentBack(); b != nil && (x.last(chronicle.Transition, "") == nil || b.Seq > x.last(chronicle.Transition, "").Seq) {
			s = p.T("explain.where."+b.Kind, "who", x.who(b), "via", x.via(b), "node", b.Node, "at", x.at(b.At))
		} else if t := x.last(chronicle.Transition, ""); t != nil {
			s = p.T("explain.where.moved", "node", st.Node, "at", x.at(t.At))
		} else {
			s = p.T("explain.where.not_started", "node", st.Node)
		}
	}
	lines = append(lines, s)
	if st.Running && len(st.Children) > 0 && st.End == "" {
		lines = append(lines, p.T("explain.where.children", "n", len(st.Children), "children", x.children(st)))
	}
	if st.HaltRequested && !st.Halted && st.End == "" {
		e := x.last(chronicle.HaltRequested, "")
		lines = append(lines, p.T("explain.where.halt_requested", "at", x.at(e.At)))
	}
	return lines
}

// children names the unfinished children of the running step, such as
// "campaign 4 (item b)".
func (x *explainer) children(st chronicle.State) string {
	item := map[int64]string{}
	for _, e := range x.events {
		if e.Kind == chronicle.ChildStarted {
			item[e.Child] = e.Item
		}
	}
	var parts []string
	for _, id := range st.Children {
		if item[id] != "" {
			parts = append(parts, x.p.T("explain.child_item", "child", id, "item", item[id]))
		} else {
			parts = append(parts, x.p.T("explain.child", "child", id))
		}
	}
	return strings.Join(parts, ", ")
}

// lastSentBack is the newest retry or rewind.
func (x *explainer) lastSentBack() *chronicle.Event {
	for i := len(x.events) - 1; i >= 0; i-- {
		if e := &x.events[i]; e.Kind == chronicle.Retried || e.Kind == chronicle.Rewound {
			return e
		}
	}
	return nil
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
		switch {
		case e.Error != "":
			out = append(out, x.p.T("explain.check.error", "check", e.Check, "error", e.Error))
		case e.Soft && e.Outcome != "ok":
			out = append(out, x.p.T("explain.check.soft", "check", e.Check, "outcome", e.Outcome))
		default:
			out = append(out, x.p.T("explain.check.ok", "check", e.Check, "outcome", e.Outcome))
		}
	}
	return out
}

func (x *explainer) answers(e *chronicle.Event) string {
	var parts []string
	for _, a := range e.Answers {
		s := a.ID + ": " + a.Value
		if a.Comment != "" {
			s += x.p.T("explain.answer_comment", "comment", strconv.Quote(a.Comment))
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

// cutOff, failed and unfinished say how an attempt ended without an
// outcome of its own.
func (x *explainer) cutOff(e *chronicle.Event) string {
	return x.p.T("explain.cut_off", "node", e.Node, "attempt", e.Attempt, "failure", e.Failure, "error", e.Error)
}

func (x *explainer) failed(e *chronicle.Event) string {
	return x.p.T("explain.attempt_failed", "node", e.Node, "attempt", e.Attempt, "failure", e.Failure, "error", trim(e.Error))
}

func (x *explainer) unfinished(e *chronicle.Event) string {
	return x.p.T("explain.could_not_finish", "node", e.Node, "attempt", e.Attempt, "error", trim(e.Error))
}

func (x *explainer) history() []string {
	p := x.p
	var lines []string
	asks := map[int64]bool{}
	for i := range x.events {
		e := &x.events[i]
		if !x.wanted(e.Node) && e.Kind != chronicle.Resumed && e.Kind != chronicle.HaltRequested {
			continue
		}
		var s string
		switch e.Kind {
		case chronicle.StepStarted:
			if e.Choice == nil {
				continue
			}
			s = x.choice(e)
		case chronicle.NoClient:
			s = x.noClient(e)
		case chronicle.StepFinished:
			switch {
			case e.Failure == chronicle.FailLimit || e.Failure == chronicle.FailOffline:
				s = x.cutOff(e)
			case e.Failure != "":
				s = x.failed(e)
				if e.Outcome != "" {
					s += p.T("explain.its_outcome", "outcome", e.Outcome)
				}
			case e.Error != "":
				s = x.unfinished(e)
			case e.Outcome == "":
				continue // the step asked a person; the asked event says so
			default:
				if cs := x.checks(e.Node, e.Attempt); len(cs) > 0 {
					s = p.T("explain.finished_checks", "node", e.Node, "attempt", e.Attempt, "outcome", e.Outcome, "took", dur(e.Millis),
						"checks", strings.Join(cs, ", "))
				} else {
					s = p.T("explain.finished", "node", e.Node, "attempt", e.Attempt, "outcome", e.Outcome, "took", dur(e.Millis))
				}
			}
			if t := x.transitionAfter(i, e.Node); t != nil && e.Outcome != "" {
				s += p.T("explain.went_to", "to", t.To)
			}
		case chronicle.Asked:
			asks[e.Seq] = true
			switch {
			case e.Reason == chronicle.ReasonHuman && len(e.Questions) > 0:
				s = p.T("explain.asked_choose_answer", "node", e.Node, "outcomes", x.either(e.Outcomes), "questions", questions(e.Questions))
			case e.Reason == chronicle.ReasonHuman:
				s = p.T("explain.asked_choose", "node", e.Node, "outcomes", x.either(e.Outcomes))
			default:
				s = p.T("explain.asked", "node", e.Node, "questions", questions(e.Questions))
			}
		case chronicle.Answered:
			if !asks[e.Ask] {
				continue // only the first answer to a question counts
			}
			delete(asks, e.Ask)
			key := "explain.answered"
			a := x.answers(e)
			if a != "" {
				key += "_answers"
			}
			if e.Outcome != "" {
				key += "_chose"
			}
			s = p.T(key, "who", x.who(e), "node", e.Node, "via", x.via(e), "answers", a, "outcome", e.Outcome)
		case chronicle.StepInterrupted:
			s = p.T("explain.interrupted", "node", e.Node, "attempt", e.Attempt)
		case chronicle.Stopped:
			s = p.T("explain.stopped", "node", e.Node, "error", trim(e.Error))
		case chronicle.HaltRequested:
			s = p.T("explain.halt_requested")
		case chronicle.Halted:
			s = p.T("explain.halted", "node", e.Node)
		case chronicle.Resumed:
			s = p.T("explain.resumed")
			if e.Child != 0 {
				s = p.T("explain.retried_child", "who", x.who(e), "via", x.via(e), "child", e.Child, "node", e.To)
			}
		case chronicle.ItemKept:
			s = p.T("explain.item_kept", "node", e.Node, "item", e.Item, "child", e.Child)
		case chronicle.ChildStarted:
			if e.Outcome == chronicle.Retried {
				s = p.T("explain.child_retried", "node", e.Node, "child", e.Child, "at", e.To)
			} else if e.Item != "" {
				s = p.T("explain.child_started_item", "node", e.Node, "item", e.Item, "child", e.Child)
			} else {
				s = p.T("explain.child_started", "node", e.Node, "child", e.Child)
			}
		case chronicle.ItemFinished:
			s = p.T("explain.item_finished", "node", e.Node, "item", e.Item, "child", e.Child, "outcome", e.Outcome)
		case chronicle.ItemNotRun:
			s = p.T("explain.item_not_run"+"_"+e.Outcome, "node", e.Node, "item", e.Item, "needs", e.Needs)
		case chronicle.LockWaiting:
			s = p.T("explain.lock_waited", "node", e.Node, "lock", e.Lock, "holder", e.Holder)
		case chronicle.Retried, chronicle.Rewound:
			s = p.T("explain."+e.Kind, "who", x.who(e), "via", x.via(e), "node", e.Node)
		default:
			continue
		}
		lines = append(lines, x.at(e.At)+"  "+s)
	}
	if len(lines) == 0 {
		if x.node != "" {
			return []string{p.T("explain.nothing_ran_at", "node", x.node)}
		}
		return []string{p.T("explain.nothing_ran")}
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
	p := x.p
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
		case chronicle.Retried, chronicle.Rewound:
			if !x.wanted(e.Node) {
				continue
			}
			if count[e.Node] == 0 {
				order = append(order, e.Node)
			}
			count[e.Node]++
			backs = append(backs, back{e: e, into: e.Node})
		}
	}
	if len(backs) == 0 {
		if x.node != "" {
			return []string{p.T("explain.never_back_to", "node", x.node)}
		}
		return []string{p.T("explain.never_back")}
	}
	var lines []string
	for _, to := range order {
		lines = append(lines, p.T("explain.back_count", "node", to, "times", x.times(count[to])))
	}
	for _, b := range backs {
		args := []any{"node", b.e.Node, "outcome", b.e.Outcome, "to", b.into, "why", b.why}
		if b.e.Kind == chronicle.Retried || b.e.Kind == chronicle.Rewound {
			lines = append(lines, x.at(b.e.At)+"  "+p.T("explain."+b.e.Kind, "who", x.who(b.e), "via", x.via(b.e), "node", b.e.Node))
		} else if b.why != "" {
			lines = append(lines, x.at(b.e.At)+"  "+p.T("explain.back_why", args...))
		} else {
			lines = append(lines, x.at(b.e.At)+"  "+p.T("explain.back", args...))
		}
	}
	return lines
}

func (x *explainer) times(n int) string {
	switch n {
	case 1:
		return x.p.T("explain.once")
	case 2:
		return x.p.T("explain.twice")
	}
	return x.p.T("explain.times", "n", n)
}

// why gives what the chronicle records about the step that sent the flow
// back: the checks that did not pass, the answers of a person, or the
// output of an agent step.
func (x *explainer) why(i int, t *chronicle.Event) string {
	p := x.p
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
			parts = append(parts, p.T("explain.required_check", "check", e.Check, "outcome", e.Outcome))
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
				parts = append(parts, p.T("explain.why.no", "who", x.who(e), "ids", strings.Join(no, ", ")))
			case e.Outcome != "":
				parts = append(parts, p.T("explain.why.chose", "who", x.who(e), "outcome", e.Outcome))
			}
			break
		}
		if e.Kind == chronicle.StepStarted && e.Node == t.Node {
			break
		}
	}
	if out := clip(fin.Output); out != "" {
		parts = append(parts, p.T("explain.why.output", "output", out))
	}
	return strings.Join(parts, "; ")
}

func (x *explainer) failures() []string {
	p := x.p
	var lines []string
	for i := range x.events {
		e := &x.events[i]
		if !x.wanted(e.Node) {
			continue
		}
		var s string
		switch {
		case e.Kind == chronicle.StepFinished && (e.Failure == chronicle.FailLimit || e.Failure == chronicle.FailOffline):
			s = x.cutOff(e)
		case e.Kind == chronicle.StepFinished && e.Failure != "":
			s = x.failed(e)
		case e.Kind == chronicle.StepFinished && e.Error != "":
			s = x.unfinished(e)
		case e.Kind == chronicle.StepFinished && (e.Outcome == "fail" || e.Outcome == "timeout" || e.Outcome == "blocked"):
			exit := ""
			if e.Exit != nil {
				exit = p.T("explain.exit_code", "code", *e.Exit)
			}
			var req []string
			for _, c := range x.events {
				if c.Kind == chronicle.CheckFinished && c.Node == e.Node && c.Attempt == e.Attempt && !c.Soft && c.Outcome != "ok" && c.Error == "" {
					req = append(req, p.T("explain.required_check", "check", c.Check, "outcome", c.Outcome))
				}
			}
			checks := ""
			if len(req) > 0 {
				checks = ": " + strings.Join(req, ", ")
			}
			s = p.T("explain.finished_failing", "node", e.Node, "attempt", e.Attempt, "outcome", e.Outcome, "exit", exit, "checks", checks)
		case e.Kind == chronicle.CheckFinished && e.Error != "":
			s = p.T("explain.check_error", "check", e.Check, "node", e.Node, "attempt", e.Attempt, "error", trim(e.Error))
		case e.Kind == chronicle.Stopped:
			s = p.T("explain.stopped", "node", e.Node, "error", trim(e.Error))
		default:
			continue
		}
		lines = append(lines, x.at(e.At)+"  "+s)
	}
	if len(lines) == 0 {
		if x.node != "" {
			return []string{p.T("explain.nothing_failed_at", "node", x.node)}
		}
		return []string{p.T("explain.nothing_failed")}
	}
	return lines
}

func (x *explainer) warnings() []string {
	var lines []string
	for _, e := range x.events {
		if e.Kind == chronicle.CheckFinished && e.Soft && e.Outcome != "ok" && e.Error == "" && x.wanted(e.Node) {
			lines = append(lines, x.at(e.At)+"  "+x.p.T("explain.soft_warned", "check", e.Check, "node", e.Node, "outcome", e.Outcome, "attempt", e.Attempt))
		}
	}
	if len(lines) == 0 {
		return []string{x.p.T("explain.no_warnings")}
	}
	return lines
}
