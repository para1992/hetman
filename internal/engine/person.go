package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// answerPoll is how often a campaign that waits for a person looks at the
// chronicle for the answers, or for a halt.
var answerPoll = time.Second

// AnswerCommand is the command that answers the questions of a campaign.
func AnswerCommand(o *orders.Orders, id int64) string {
	return fmt.Sprintf("hetman answer -f %s -c %d", ShellQuote(o.Path), id)
}

// ResumeCommand is the command that resumes a campaign.
func ResumeCommand(o *orders.Orders, id int64) string {
	return fmt.Sprintf("hetman resume -f %s -c %d", ShellQuote(o.Path), id)
}

// ShellQuote quotes an argument for a POSIX shell when it needs quoting.
func ShellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t'\"\\$`&|;<>()*?[]#~!{}") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// awaitAnswer waits a moment for a person to answer; the caller looks at the
// chronicle again afterwards.
func (e *Engine) awaitAnswer(ctx context.Context, asked *chronicle.Event) error {
	if e.saidAsk != asked.Seq {
		e.saidAsk = asked.Seq
		what := fmt.Sprintf("%d question(s)", len(asked.Questions))
		if asked.Reason == chronicle.ReasonHuman {
			what = "a decision"
			if len(asked.Questions) > 0 {
				what += fmt.Sprintf(" and %d question(s)", len(asked.Questions))
			}
		}
		e.say("%s waits for a person (%s); answer with: %s", asked.Node, what, AnswerCommand(e.Orders, e.Campaign))
	}
	select {
	case <-ctx.Done():
		return ErrInterrupted
	case <-e.clock().After(answerPoll):
		return nil
	}
}

// humanStep starts a visit of a human node: the node asks its questions and
// the outcomes to choose from, then the campaign waits.
func (e *Engine) humanStep(node *orders.Node, attempt int) error {
	if err := e.record(chronicle.Event{Kind: chronicle.StepStarted, Node: node.ID, Attempt: attempt}); err != nil {
		return err
	}
	return e.record(chronicle.Event{Kind: chronicle.Asked, Node: node.ID, Attempt: attempt, Details: chronicle.Details{
		Reason: chronicle.ReasonHuman, Questions: node.Ask, Show: node.Show, Outcomes: node.Decisions}})
}

// finishAsked ends the step of a gate or a human node once a person has
// answered: a gate passes when every answer is yes; a human node takes the
// outcome the person chose.
func (e *Engine) finishAsked(events []chronicle.Event, st chronicle.State) error {
	asked, answer := st.Ask, st.Answer
	outcome := answer.Outcome
	if asked.Reason == chronicle.ReasonGate {
		outcome = orders.Blocked
		if ask.AllYes(asked.Questions, answer.Answers) {
			outcome = orders.Pass
		}
	}
	fin := chronicle.Event{Kind: chronicle.StepFinished, Node: asked.Node, Attempt: asked.Attempt, Outcome: outcome}
	for _, ev := range events {
		if ev.Kind == chronicle.StepStarted && ev.Node == asked.Node && ev.Attempt == asked.Attempt {
			fin.Millis = time.Since(ev.At).Milliseconds()
		}
	}
	if err := e.writeAnswers(asked, answer); err != nil {
		e.say("cannot write the answers of %s to the work folder: %v", asked.Node, err)
	}
	by := ""
	if answer.By != "" {
		by = " by " + answer.By
	}
	e.say("%s answered%s: %s", asked.Node, by, outcome)
	return e.record(fin)
}

// AnswersFile is where the latest answers of a node are written for later
// steps, relative to the campaign's work folder (HETMAN_RUN_DIR).
func AnswersFile(node string) string { return "answers/" + node + ".json" }

func (e *Engine) writeAnswers(asked, answer *chronicle.Event) error {
	type item struct {
		ID       string `json:"id"`
		Question string `json:"question"`
		Kind     string `json:"kind"`
		Value    string `json:"value"`
		Comment  string `json:"comment,omitempty"`
	}
	doc := struct {
		Node    string    `json:"node"`
		Outcome string    `json:"outcome,omitempty"`
		By      string    `json:"by,omitempty"`
		At      time.Time `json:"at"`
		Answers []item    `json:"answers"`
	}{Node: asked.Node, Outcome: answer.Outcome, By: answer.By, At: answer.At, Answers: []item{}}
	byID := map[string]ask.Answer{}
	for _, a := range answer.Answers {
		byID[a.ID] = a
	}
	for _, q := range asked.Questions {
		doc.Answers = append(doc.Answers, item{q.ID, q.Question, q.Kind, byID[q.ID].Value, byID[q.ID].Comment})
	}
	data, _ := json.MarshalIndent(doc, "", "  ")
	path := filepath.Join(CampaignDir(e.Orders, e.Campaign), "work", filepath.FromSlash(AnswersFile(asked.Node)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// answersOf finds the latest answered questions of a node: the asked event
// and the answer that counted for it, or nils.
func answersOf(events []chronicle.Event, node string) (asked, answer *chronicle.Event) {
	byseq := map[int64]*chronicle.Event{}
	done := map[int64]bool{}
	for i := range events {
		ev := &events[i]
		switch {
		case ev.Kind == chronicle.Asked && ev.Node == node:
			byseq[ev.Seq] = ev
		case ev.Kind == chronicle.Answered && ev.Node == node && byseq[ev.Ask] != nil && !done[ev.Ask]:
			done[ev.Ask] = true
			asked, answer = byseq[ev.Ask], ev
		}
	}
	return asked, answer
}

// answersText renders the answers of an asked event for a prompt.
func answersText(asked, answer *chronicle.Event) string {
	s := ask.Write(asked.Questions, answer.Answers)
	if asked.Reason == chronicle.ReasonHuman && answer.Outcome != "" {
		s += "Decision: " + answer.Outcome + "\n"
	}
	return s
}

// Errors of Answer.
var (
	ErrNothingAsked = errors.New("nothing is asked")
	ErrAnswered     = errors.New("already answered")
	ErrOtherAsk     = errors.New("another question is open now")
)

// Reply is what a person answers to the open question of a campaign.
type Reply struct {
	Ask int64 // the seq of the asked event being answered; 0 for whatever is open
	ask.Given
	By  string // who answers, when known
	Via string // where the answer comes from
}

// Where an answer comes from, in Details.Via.
const (
	ViaCommand = "command"
	ViaPage    = "page"
)

// Pending returns the asked event a campaign waits on, or nil.
func Pending(ch *chronicle.Chronicle, id int64) (*chronicle.Event, chronicle.State, error) {
	st, err := state(ch, id)
	if err != nil || !st.Asking() {
		return nil, st, err
	}
	return st.Ask, st, nil
}

// Answer checks a reply against the open question of a campaign and records
// it. A reply that does not fit changes nothing. Whatever drives the
// campaign continues with it; with no driver, the campaign continues on
// resume.
func Answer(ch *chronicle.Chronicle, id int64, r Reply) (chronicle.Event, error) {
	st, err := state(ch, id)
	if err != nil {
		return chronicle.Event{}, err
	}
	switch {
	case st.End != "":
		return chronicle.Event{}, fmt.Errorf("campaign %d: %w (%s)", id, ErrEnded, st.End)
	case st.Ask != nil && st.Answer != nil:
		return chronicle.Event{}, fmt.Errorf("campaign %d: the questions of %s are %w", id, st.Ask.Node, ErrAnswered)
	case st.Ask == nil:
		return chronicle.Event{}, fmt.Errorf("campaign %d: %w now", id, ErrNothingAsked)
	case r.Ask != 0 && r.Ask != st.Ask.Seq:
		return chronicle.Event{}, fmt.Errorf("campaign %d: %w (%s asks #%d, not #%d)", id, ErrOtherAsk, st.Ask.Node, st.Ask.Seq, r.Ask)
	}
	answers, outcome, err := ask.Check(st.Ask.Questions, st.Ask.Outcomes, r.Given)
	if err != nil {
		return chronicle.Event{}, err
	}
	ev, err := ch.Append(chronicle.Event{Campaign: id, Kind: chronicle.Answered, Node: st.Ask.Node, Attempt: st.Ask.Attempt,
		Outcome: outcome, Details: chronicle.Details{Reason: st.Ask.Reason, Answers: answers, Ask: st.Ask.Seq, By: r.By, Via: r.Via}})
	if err != nil {
		return ev, err
	}
	after, err := state(ch, id)
	if err != nil {
		return ev, err
	}
	if after.Answer == nil || after.Answer.Seq != ev.Seq {
		return ev, fmt.Errorf("campaign %d: the questions of %s were %w by someone else first", id, st.Ask.Node, ErrAnswered)
	}
	return ev, nil
}

// Notice is what the notification hook reads on its standard input.
type Notice struct {
	Campaign  int64          `json:"campaign"`
	Flow      string         `json:"flow"`
	Orders    string         `json:"orders"`
	Node      string         `json:"node"`
	Reason    string         `json:"reason"`
	Message   string         `json:"message"`
	Event     int64          `json:"event"`
	At        time.Time      `json:"at"`
	Questions []ask.Question `json:"questions,omitempty"`
	Outcomes  []string       `json:"outcomes,omitempty"`
	Show      []string       `json:"show,omitempty"`
	Command   string         `json:"command"`
}

// notice builds the notice for an event that makes the campaign wait for a
// person, or reports false for any other event.
func (e *Engine) notice(ev chronicle.Event) (Notice, bool) {
	n := Notice{Campaign: e.Campaign, Flow: e.Orders.Flow, Orders: e.Orders.Path, Node: ev.Node, Event: ev.Seq, At: ev.At,
		Command: ResumeCommand(e.Orders, e.Campaign)}
	switch {
	case ev.Kind == chronicle.Asked:
		n.Reason, n.Questions, n.Outcomes, n.Command = ev.Reason, ev.Questions, ev.Outcomes, AnswerCommand(e.Orders, e.Campaign)
		for _, s := range ev.Show {
			n.Show = append(n.Show, filepath.Join(e.Orders.Dir, filepath.FromSlash(s)))
		}
		switch ev.Reason {
		case chronicle.ReasonHuman:
			n.Message = fmt.Sprintf("%s waits for a decision: %s", ev.Node, strings.Join(ev.Outcomes, ", "))
		case chronicle.ReasonDialogue:
			n.Message = fmt.Sprintf("agent step %s asks %d question(s) (round %d)", ev.Node, len(ev.Questions), ev.Round)
		default:
			n.Message = fmt.Sprintf("gate %s asks %d question(s)", ev.Node, len(ev.Questions))
		}
	case ev.Kind == chronicle.StepFinished && ev.Failure == chronicle.FailOffline:
		n.Reason, n.Message = chronicle.ReasonOffline, fmt.Sprintf("%s lost connectivity (%s); resume runs it again", ev.Node, ev.Error)
	case ev.Kind == chronicle.StepFinished && ev.Failure == chronicle.FailLimit && ev.RetryAt == nil:
		n.Reason, n.Message = chronicle.ReasonLimit, fmt.Sprintf("%s hit the subscription limit with no time to retry (%s); resume runs it again", ev.Node, ev.Error)
	case ev.Kind == chronicle.Stopped:
		n.Reason, n.Message = chronicle.ReasonError, ev.Error
	default:
		return n, false
	}
	return n, true
}

// NotifyLog is the log of the notification hook run for an event, relative
// to the campaign folder.
func NotifyLog(seq int64) string { return "logs/notify." + strconv.FormatInt(seq, 10) + ".log" }

// notify runs the notification hook for an event in the background. The
// campaign never waits for it; it is killed after its timeout, and how it
// ended is recorded as a notified event.
func (e *Engine) notify(ev chronicle.Event) {
	h := e.Orders.Notify
	if h == nil {
		return
	}
	n, ok := e.notice(ev)
	if !ok {
		return
	}
	payload, _ := json.Marshal(n)
	payload = append(payload, '\n')
	e.hooks.Add(1)
	go func() {
		defer e.hooks.Done()
		res := chronicle.Event{Campaign: e.Campaign, Kind: chronicle.Notified, Node: ev.Node,
			Details: chronicle.Details{Reason: n.Reason, Ask: ev.Seq, Log: NotifyLog(ev.Seq)}}
		began := time.Now()
		res.Outcome, res.Exit, res.Error = e.runHook(h, ev, n.Reason, payload, res.Log)
		res.Millis = time.Since(began).Milliseconds()
		if res.Outcome != orders.OK {
			e.say("notification hook for %s: %s %s (log %s)", ev.Node, res.Outcome, res.Error, res.Log)
		}
		if _, err := e.Chron.Append(res); err != nil {
			e.say("cannot record the notification hook: %v", err)
		}
	}()
}

func (e *Engine) runHook(h *orders.Notify, ev chronicle.Event, reason string, payload []byte, logRel string) (outcome string, exit *int, msg string) {
	dir := CampaignDir(e.Orders, e.Campaign)
	for _, sub := range []string{"logs", "work"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return "error", nil, err.Error()
		}
	}
	log, err := os.Create(filepath.Join(dir, filepath.FromSlash(logRel)))
	if err != nil {
		return "error", nil, err.Error()
	}
	defer log.Close()
	cmd := exec.Command(e.Orders.Program(h.Run[0]), h.Run[1:]...)
	cmd.Dir = e.Orders.Dir
	env, err := e.env(ev.Node, "", ev.Attempt, "")
	if err != nil {
		fmt.Fprintln(log, "hetman:", err)
		return "error", nil, err.Error()
	}
	cmd.Env = append(env, "HETMAN_REASON="+reason)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout, cmd.Stderr = log, log
	configure(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(log, "hetman: cannot start the notification hook:", err)
		return "error", nil, err.Error()
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t := time.NewTimer(h.Timeout)
	defer t.Stop()
	select {
	case <-exited:
	case <-t.C:
		killTree(cmd.Process)
		<-exited
		fmt.Fprintf(log, "\nhetman: killed after timeout %s\n", h.Timeout)
		return orders.Timeout, nil, "killed after " + h.Timeout.String()
	}
	code := cmd.ProcessState.ExitCode()
	if code != 0 {
		return orders.Fail, &code, fmt.Sprintf("exit code %d", code)
	}
	return orders.OK, &code, ""
}

// WaitHooks waits for notification hooks still running; each ends by its
// timeout at the latest. Call it before the process exits, so that a hook
// started just before is not cut off.
func (e *Engine) WaitHooks() { e.hooks.Wait() }
