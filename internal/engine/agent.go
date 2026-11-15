package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/ask"
	"hetman/internal/chronicle"
	"hetman/internal/language"
	"hetman/internal/orders"
	"hetman/internal/prompt"
)

// PromptFile is the rendered prompt of one attempt of an agent step,
// relative to the campaign folder, with forward slashes.
func PromptFile(node string, attempt int) string {
	return "prompts/" + execBase(node, "", attempt) + ".txt"
}

// StderrFile holds what an adapter wrote to its standard error during one
// attempt, relative to the campaign folder, with forward slashes.
func StderrFile(node string, attempt int) string {
	return "logs/" + execBase(node, "", attempt) + ".stderr.log"
}

// Clock tells the time and waits. The engine uses it for waits that can
// last long, such as for a subscription limit to reset, so that tests can
// replace it.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (e *Engine) clock() Clock {
	if e.Clock != nil {
		return e.Clock
	}
	return realClock{}
}

// waitPoll is how often a waiting campaign looks at the chronicle, so that a
// halt requested meanwhile takes effect.
var waitPoll = time.Second

// wait waits until the given time, or less when a halt may have been
// requested meanwhile; the caller looks at the chronicle again afterwards.
func (e *Engine) wait(ctx context.Context, node string, until time.Time) error {
	if !e.waitSaid.Equal(until) {
		e.waitSaid = until
		e.say("engine.limit_wait", "node", node, "until", until.Local().Format("2006-01-02 15:04:05"))
	}
	d := until.Sub(e.clock().Now())
	if d > waitPoll {
		d = waitPoll
	}
	select {
	case <-ctx.Done():
		return ErrInterrupted
	case <-e.clock().After(d):
		return nil
	}
}

// describe returns the description of an executor's adapter, asked once
// per drive.
func (e *Engine) describe(ctx context.Context, x *orders.Executor) (adapter.Description, error) {
	if d, ok := e.described[x.Name]; ok {
		return d, nil
	}
	argv := append([]string{e.Orders.Program(x.Run[0])}, x.Run[1:]...)
	env, err := e.env("", "", 0, "")
	if err != nil {
		return adapter.Description{}, err
	}
	d, err := describe(ctx, argv, e.Orders.Dir, env)
	if err != nil {
		return d, err
	}
	if e.described == nil {
		e.described = map[string]adapter.Description{}
	}
	e.described[x.Name] = d
	return d, nil
}

// visitStart returns the index of the first event of the current visit of
// a node: the event after the transition, retry or rewind that made it the
// position, or 0.
func visitStart(events []chronicle.Event, node string) int {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == chronicle.Transition && events[i].To == node || sentBack(events[i]) && events[i].Node == node {
			return i + 1
		}
	}
	return 0
}

// sentBack reports whether an event is a person sending the campaign back.
func sentBack(e chronicle.Event) bool {
	return e.Kind == chronicle.Retried || e.Kind == chronicle.Rewound
}

// carryOver is what an attempt takes from the attempts before it in the
// same visit.
type carryOver struct {
	session  string // conversation to continue
	feedback string // why the previous output was rejected
	why      string // the failure that rejected it: invalid output or denied tool calls
	rejected int    // the attempt whose output was rejected

	client string // the client (clientKey) of the latest attempt of the visit

	// Dialogue steps.
	rounds  int              // rounds of questions asked in this visit
	asked   *chronicle.Event // the latest round of questions
	answer  *chronicle.Event // the person's answer to it
	current string           // the latest session id of the visit
}

// carry looks at the earlier attempts of this visit. The conversation of
// the previous attempt is continued when that attempt was cut off (the
// engine stopped, no connectivity, a subscription limit) and its adapter
// can resume. Feedback is given when the last attempt that counted
// returned output that did not match the schema.
func carry(visit []chronicle.Event, node string, canResume bool) carryOver {
	var c carryOver
	last, cutOff := 0, false
	sessions := map[int]string{}
	for i := range visit {
		ev := visit[i]
		if ev.Node != node {
			continue
		}
		switch ev.Kind {
		case chronicle.Asked:
			c.rounds++
			c.asked, c.answer = &visit[i], nil
			c.feedback, c.rejected = "", 0
		case chronicle.Answered:
			if c.asked != nil && c.answer == nil && ev.Ask == c.asked.Seq {
				c.answer = &visit[i]
			}
		case chronicle.StepStarted:
			// An attempt with no finish after it was cut off by the engine
			// stopping, whether or not its step_interrupted is recorded yet.
			last, cutOff = ev.Attempt, true
			c.client = clientKey(ev.Executor, ev.Model, ev.Effort)
			if ev.Session != "" {
				sessions[ev.Attempt] = ev.Session
				c.current = ev.Session
			}
		case chronicle.AgentSession:
			sessions[ev.Attempt] = ev.Session
			c.current = ev.Session
		case chronicle.StepFinished:
			cutOff = ev.Failure == chronicle.FailLimit || ev.Failure == chronicle.FailOffline
			if chronicle.Counts(ev.Failure) {
				c.feedback, c.rejected, c.why = "", 0, ""
				if ev.Failure == chronicle.FailInvalidOutput || ev.Failure == chronicle.FailDenied {
					c.feedback, c.rejected, c.why = ev.Error, ev.Attempt, ev.Failure
				}
			}
		}
	}
	if cutOff && canResume {
		c.session = sessions[last]
	}
	return c
}

// keptSession is the conversation a node with keep_session continues on a
// later visit: the latest session id its previous visit reported, with the
// executor and model of the attempt that reported it. start is the first
// event of the current visit; on a first visit it is empty.
type keptSession struct {
	session, executor, model string
}

func keptOf(events []chronicle.Event, start int, node string) keptSession {
	var k keptSession
	if start == 0 {
		return k
	}
	var executor, model string
	for _, ev := range events[visitStart(events[:start-1], node) : start-1] {
		if ev.Node != node {
			continue
		}
		switch ev.Kind {
		case chronicle.StepStarted:
			executor, model = ev.Executor, ev.Model
			if ev.Session != "" {
				k = keptSession{ev.Session, executor, model}
			}
		case chronicle.AgentSession:
			k = keptSession{ev.Session, executor, model}
		}
	}
	return k
}

// attempted reports whether a node has started an attempt in these events.
func attempted(events []chronicle.Event, node string) bool {
	for _, ev := range events {
		if ev.Kind == chronicle.StepStarted && ev.Node == node {
			return true
		}
	}
	return false
}

// agentStep runs one attempt of an agent node.
func (e *Engine) agentStep(ctx context.Context, node *orders.Node, attempt int, st chronicle.State, events []chronicle.Event) error {
	run, unlock, err := e.runner(ctx, node, events)
	defer unlock()
	if err != nil {
		return err
	}
	if run == nil {
		return nil // no client of the role can run; the campaign waits
	}
	x := run.x
	desc, err := e.describe(ctx, x)
	if err != nil {
		if ctx.Err() != nil {
			return ErrInterrupted
		}
		return e.stop(node.ID, fmt.Sprintf("executor %s: %v", x.Name, err))
	}
	if node.Dialogue > 0 && !desc.Has(adapter.FeatureResume) {
		return e.stop(node.ID, fmt.Sprintf("%s holds a dialogue, but the adapter of executor %s (%s) does not declare the %q feature, "+
			"so it cannot continue the conversation with a person's answers; use an adapter that can, or remove dialogue", node.ID, x.Name, desc.Name, adapter.FeatureResume))
	}
	if node.KeepSession && !desc.Has(adapter.FeatureResume) {
		return e.stop(node.ID, fmt.Sprintf("%s keeps its session across visits, but the adapter of executor %s (%s) does not declare the %q feature, "+
			"so it cannot continue the conversation of an earlier visit; use an adapter that can, or remove %s", node.ID, x.Name, desc.Name, adapter.FeatureResume, orders.KeepSessionKey))
	}
	if !desc.Has(adapter.FeatureProtect) && !e.unfenced[x.Name] {
		e.say("engine.unprotected", "executor", x.Name, "adapter", desc.Name)
		if e.unfenced == nil {
			e.unfenced = map[string]bool{}
		}
		e.unfenced[x.Name] = true
	}
	start := visitStart(events, node.ID)
	visit := events[start:]
	c := carry(visit, node.ID, desc.Has(adapter.FeatureResume))
	// A conversation belongs to one client: on another client the step
	// starts afresh, with the full prompt and what the person said so far.
	moved := c.client != "" && c.client != clientKey(x.Name, run.model, run.effort)
	if moved {
		c.session = ""
	}
	// A node that keeps its session continues, in the first attempt of a
	// later visit, the conversation of its previous visit when that ran on
	// the same executor and model. An attempt resumed after being cut off
	// continues it as well; once such an attempt fails, the next one starts
	// afresh.
	kept := false // this attempt continues the conversation of an earlier visit
	if node.KeepSession {
		k := keptOf(events, start, node.ID)
		if !attempted(visit, node.ID) && k.session != "" && k.executor == x.Name && k.model == run.model {
			c.session = k.session
		}
		kept = c.session != "" && c.session == k.session
	}
	left := node.Dialogue - c.rounds // rounds of questions the step may still ask
	lang, langSet, err := e.language()
	if err != nil {
		return e.stop(node.ID, fmt.Sprintf("%s: %v", node.ID, err))
	}
	var text string
	if c.answer != nil && !moved {
		// The person answered the step's questions: the same conversation
		// continues with the answers as the next message.
		c.session = c.current
		text = dialogueMessage(c.asked, c.answer, left)
	} else {
		text, err = e.render(node, events, lang, langSet)
		if err != nil {
			return e.stop(node.ID, fmt.Sprintf("%s: prompt %s: %v", node.ID, filepath.Base(node.Prompt), err))
		}
	}
	if langSet {
		text += languageInstruction(lang)
	}
	if moved && c.rounds > 0 {
		text += dialogueHistory(visit, node.ID)
	}
	if (c.answer == nil || moved) && left > 0 {
		text += askInstructions(left)
	}
	if c.answer == nil {
		text += unableInstruction()
	}
	schemaJSON := node.SchemaJSON
	if left > 0 {
		if schemaJSON, err = ask.DialogueSchema(node.SchemaJSON); err != nil {
			return e.stop(node.ID, fmt.Sprintf("%s: schema: %v", node.ID, err))
		}
	}
	if schemaJSON, err = unableSchema(schemaJSON); err != nil {
		return e.stop(node.ID, fmt.Sprintf("%s: schema: %v", node.ID, err))
	}
	workdir, err := e.workdir(node)
	if err != nil {
		return e.stop(node.ID, err.Error())
	}
	protect, writable := e.fence()
	if c.feedback != "" && c.why == chronicle.FailDenied {
		text += fmt.Sprintf("\n\n---\nYour previous answer (attempt %d) was not accepted because the client denied tool calls you made:\n- %s\nThe tools entries this step allows: %s. A denied call does not mean its tool is unavailable; use a call that matches an entry. If the step cannot be done with them, answer with the outcome %q and say what you need.\n",
			c.rejected, strings.Join(deniedOf(c.feedback), "\n- "), strings.Join(node.Tools, ", "), orders.Unable)
	} else if c.feedback != "" {
		text += fmt.Sprintf("\n\n---\nYour previous answer (attempt %d) was rejected because it does not match the required output schema:\n- %s\nAnswer again with a single JSON object that matches the schema.\n",
			c.rejected, strings.ReplaceAll(c.feedback, "; ", "\n- "))
	}
	dir := e.dir()
	for _, sub := range []string{"work", "logs", "results", "prompts"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	env, err := e.env(node.ID, "", attempt, "")
	if err != nil {
		return err
	}
	promptRel := PromptFile(node.ID, attempt)
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(promptRel)), []byte(text), 0o644); err != nil {
		return err
	}
	if err := e.record(chronicle.Event{Kind: chronicle.StepStarted, Node: node.ID, Attempt: attempt, Details: chronicle.Details{
		Executor: x.Name, Model: run.model, Effort: run.effort, Prompt: promptRel, Session: c.session, Choice: run.choice}}); err != nil {
		return err
	}
	unlock()
	if kept {
		e.say("engine.agent_kept_session", "node", node.ID, "attempt", attempt, "executor", x.Name, "session", c.session)
	} else if c.session != "" {
		e.say("engine.agent_started_session", "node", node.ID, "attempt", attempt, "executor", x.Name, "session", c.session)
	} else {
		e.say("engine.agent_started", "node", node.ID, "attempt", attempt, "executor", x.Name)
	}
	began := time.Now()
	fin := chronicle.Event{Kind: chronicle.StepFinished, Node: node.ID, Attempt: attempt,
		Details: chronicle.Details{Log: LogFile(node.ID, "", attempt), Stderr: StderrFile(node.ID, attempt), Executor: x.Name, Role: node.Role}}
	logf, err := os.Create(filepath.Join(dir, filepath.FromSlash(fin.Log)))
	if err != nil {
		return err
	}
	defer logf.Close()
	errf, err := os.Create(filepath.Join(dir, filepath.FromSlash(fin.Stderr)))
	if err != nil {
		return err
	}
	defer errf.Close()

	var session *string
	if c.session != "" {
		session = &c.session
	}
	step := adapter.Step{
		RunID: strconv.FormatInt(e.Campaign, 10), NodeID: node.ID, Attempt: attempt, Prompt: text,
		Model: run.model, Effort: run.effort, Tools: node.Tools, Workdir: workdir,
		Schema: schemaJSON, Session: session, Protect: protect, Writable: writable,
	}
	reported := "" // the session id this attempt reported
	recordErr := error(nil)
	// launch runs one adapter process of the attempt: for the attempt's own
	// start message, or for a repair round of it.
	launch := func(st adapter.Step, timeout time.Duration) adapterRun {
		return runAdapter(ctx, adapterProc{
			argv:    append([]string{e.Orders.Program(x.Run[0])}, x.Run[1:]...),
			dir:     e.Orders.Dir,
			env:     env,
			start:   adapter.EncodeStart(st),
			log:     logf,
			stderr:  errf,
			timeout: timeout,
			idle:    node.IdleTimeout,
			started: func(pid int) {
				ident, _ := processIdent(pid)
				if err := e.record(chronicle.Event{Kind: chronicle.ProcessStarted, Node: node.ID, Attempt: attempt,
					Details: chronicle.Details{Pid: pid, Ident: ident}}); err != nil {
					e.say("engine.cannot_record_process", "pid", pid, "node", node.ID, "error", err)
				}
			},
			session: func(id string) {
				reported = id
				if err := e.record(chronicle.Event{Kind: chronicle.AgentSession, Node: node.ID, Attempt: attempt,
					Details: chronicle.Details{Session: id}}); err != nil && recordErr == nil {
					recordErr = err
				}
				e.say("engine.session", "node", node.ID, "session", id)
			},
		})
	}
	if kept {
		fmt.Fprintf(logf, "hetman: continuing session %s of the previous visit of %s (%s)\n", c.session, node.ID, orders.KeepSessionKey)
	}
	r := launch(step, node.Timeout)
	if recordErr != nil {
		return recordErr
	}
	if r.interrupted {
		return ErrInterrupted
	}
	if r.startErr != nil {
		fin.Error = fmt.Sprintf("cannot start %s: %v", x.Run[0], r.startErr)
		fin.Millis = time.Since(began).Milliseconds()
		if err := e.record(fin); err != nil {
			return err
		}
		return e.stop(node.ID, fmt.Sprintf("%s attempt %d: %s", node.ID, attempt, fin.Error))
	}
	exit := r.exit
	fin.Exit = &exit
	questions := e.judge(node, attempt, r, &fin, left > 0)
	// An answer that does not match the schema goes back to the same
	// conversation, which is asked to correct only the answer, a few times
	// at most, before the attempt counts as failed. The adapter continues
	// the conversation by its id, so the client's process need not live.
	for round := 1; round <= MaxRepairs && fin.Failure == chronicle.FailInvalidOutput && desc.Has(adapter.FeatureRepair); round++ {
		sess := reported
		if sess == "" {
			sess = c.session
		}
		if sess == "" {
			break
		}
		budget := node.Timeout
		if budget > 0 {
			if budget -= time.Since(began); budget < minRepairTime {
				break
			}
		}
		repairText := repairMessage(fin.Error)
		repairRel := RepairPromptFile(node.ID, attempt, round)
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(repairRel)), []byte(repairText), 0o644); err != nil {
			return err
		}
		if err := e.record(chronicle.Event{Kind: chronicle.OutputRepair, Node: node.ID, Attempt: attempt, Details: chronicle.Details{
			Round: round, Error: fin.Error, Session: sess, Prompt: repairRel}}); err != nil {
			return err
		}
		fmt.Fprintf(logf, "hetman: answer rejected (%s): %s\n", fin.Failure, fin.Error)
		if r.terminal != nil && len(r.terminal.Output) > 0 {
			fmt.Fprintf(logf, "hetman: rejected answer: %s\n", r.terminal.Output)
		}
		fmt.Fprintf(logf, "hetman: asking session %s to correct only the answer (repair %d of %d)\n", sess, round, MaxRepairs)
		e.say("engine.repair", "node", node.ID, "attempt", attempt, "round", round, "max", MaxRepairs, "error", fin.Error)
		st := step
		st.Prompt, st.Session, st.Repair = repairText, &sess, round
		rr := launch(st, budget)
		if recordErr != nil {
			return recordErr
		}
		if rr.interrupted {
			return ErrInterrupted
		}
		// Tool calls denied in the turn that gave the rejected answer stay
		// denied calls of the attempt, and count for fail_on_denied.
		if rr.terminal != nil {
			rr.terminal.Denied = mergeDenied(fin.Denied, rr.terminal.Denied)
		}
		next := chronicle.Event{Kind: chronicle.StepFinished, Node: node.ID, Attempt: attempt,
			Details: chronicle.Details{Log: fin.Log, Stderr: fin.Stderr, Executor: x.Name, Role: node.Role}}
		var q []ask.Question
		why := ""
		if rr.startErr != nil {
			why = fmt.Sprintf("cannot start %s: %v", x.Run[0], rr.startErr)
		} else {
			exit := rr.exit
			next.Exit = &exit
			q = e.judge(node, attempt, rr, &next, left > 0)
			if !answered(rr) {
				why = fmt.Sprintf("%s: %s", next.Failure, next.Error)
			}
		}
		if why != "" {
			// The repair gave no answer: the attempt fails as it would have
			// without it.
			if rr.terminal != nil {
				fin.Denied = rr.terminal.Denied
			}
			fmt.Fprintf(logf, "hetman: the repair gave no answer (%s)\n", why)
			e.say("engine.repair_no_answer", "node", node.ID, "attempt", attempt, "round", round, "error", why)
			break
		}
		r, fin, questions = rr, next, q
	}
	if questions != nil {
		if reported == "" {
			reported = c.session
		}
		if reported == "" {
			questions, fin.Output = nil, nil
			fin.Failure, fin.Error = chronicle.FailProtocol, "the step asked questions but its adapter reported no session, so the conversation cannot be continued with the answers"
		}
	}
	if kept && fin.Failure == chronicle.FailPermanent {
		// The kept conversation may be lost or expired: the attempt counts
		// as failed, and the next one starts afresh.
		fin.Outcome = ""
	}
	if chronicle.Counts(fin.Failure) && fin.Failure != chronicle.FailUnable && fin.Outcome == "" && st.Failures+1 >= node.MaxAttempts {
		fin.Outcome = orders.Fail
	}
	if fin.Failure != "" {
		fmt.Fprintf(logf, "hetman: attempt %d failed (%s): %s\n", attempt, fin.Failure, fin.Error)
	}
	fin.Millis = time.Since(began).Milliseconds()
	if err := e.record(fin); err != nil {
		return err
	}
	took := time.Duration(fin.Millis) * time.Millisecond
	switch {
	case questions != nil:
		e.say("engine.dialogue", "node", node.ID, "n", len(questions), "round", c.rounds+1, "rounds", node.Dialogue, "took", took)
		return e.record(chronicle.Event{Kind: chronicle.Asked, Node: node.ID, Attempt: attempt, Details: chronicle.Details{
			Reason: chronicle.ReasonDialogue, Questions: questions, Round: c.rounds + 1, Session: reported}})
	case fin.Failure == "":
		e.say("engine.finished", "node", node.ID, "outcome", fin.Outcome, "took", took)
	case fin.Failure == chronicle.FailUnable:
		return e.stop(node.ID, fmt.Sprintf("%s attempt %d cannot proceed: %s%s", node.ID, attempt, fin.Error, allowedAfterAll(node, fin.Denied)))
	case fin.Failure == chronicle.FailPermanent && !kept:
		e.say("engine.failed_permanent", "node", node.ID, "attempt", attempt, "error", fin.Error)
	case fin.Failure == chronicle.FailLimit && run.choice != nil:
		role := e.Orders.Roles[node.Role]
		e.say("engine.client_limit", "node", node.ID, "attempt", attempt, "client", role.Clients[run.choice.Client-1].Label(run.choice.Client-1),
			"role", role.Name, "error", fin.Error)
	case fin.Failure == chronicle.FailLimit && fin.RetryAt != nil:
		e.say("engine.limit_hit", "node", node.ID, "attempt", attempt, "error", fin.Error)
	case fin.Failure == chronicle.FailLimit:
		e.say("engine.limit_hit_no_retry", "node", node.ID, "attempt", attempt, "error", fin.Error)
	case fin.Failure == chronicle.FailOffline:
		e.say("engine.offline", "node", node.ID, "attempt", attempt, "error", fin.Error)
	case fin.Outcome == orders.Fail:
		e.say("engine.failed_all", "node", node.ID, "attempt", attempt, "failure", fin.Failure, "error", fin.Error, "n", node.MaxAttempts)
	default:
		e.say("engine.failed_left", "node", node.ID, "attempt", attempt, "failure", fin.Failure, "error", fin.Error,
			"n", node.MaxAttempts-st.Failures-1, "max", node.MaxAttempts)
	}
	return nil
}

// judge turns how the adapter ended into the outcome or failure of the
// attempt, and stores the output. While the step may still ask, an output
// with the outcome "questions" is a round of questions, which judge returns.
func (e *Engine) judge(node *orders.Node, attempt int, r adapterRun, fin *chronicle.Event, asking bool) []ask.Question {
	switch {
	case r.timedOut:
		fin.Failure, fin.Error = chronicle.FailTimeout, fmt.Sprintf("killed after timeout %s", node.Timeout)
		return nil
	case r.idleOut:
		fin.Failure, fin.Error = chronicle.FailIdle, fmt.Sprintf("no message from the adapter for %s; killed", node.IdleTimeout)
		return nil
	case r.protocol != "":
		fin.Failure, fin.Error = chronicle.FailProtocol, r.protocol
		return nil
	case r.lateExit:
		fin.Failure, fin.Error = chronicle.FailCrash, fmt.Sprintf("the adapter did not exit within %s after its %s; killed", exitGrace, r.terminal.Type)
		return nil
	case r.terminal == nil:
		fin.Failure, fin.Error = chronicle.FailCrash, fmt.Sprintf("the adapter exited with code %d without a result or an error", r.exit)
		return nil
	case r.exit != 0:
		fin.Failure, fin.Error = chronicle.FailCrash, fmt.Sprintf("the adapter exited with code %d after its %s", r.exit, r.terminal.Type)
		return nil
	}
	m := r.terminal
	fin.Denied = m.Denied
	if m.Type == adapter.TypeError {
		fin.Error = m.Message
		if fin.Error == "" {
			fin.Error = "no message"
		}
		switch m.Kind {
		case adapter.ErrPermanent:
			fin.Failure, fin.Outcome = chronicle.FailPermanent, orders.Fail
		case adapter.ErrLimit:
			fin.Failure = chronicle.FailLimit
			if t := m.RetryTime(); !t.IsZero() {
				t = t.UTC()
				fin.RetryAt = &t
			}
		case adapter.ErrOffline:
			fin.Failure = chronicle.FailOffline
		default:
			fin.Failure = chronicle.FailTransient
		}
		return nil
	}
	// The output is kept in the result file of the attempt whether or not
	// it is valid, so that a person can see what was rejected.
	var pretty bytes.Buffer
	if json.Indent(&pretty, m.Output, "", "  ") == nil {
		pretty.WriteByte('\n')
		os.WriteFile(filepath.Join(e.dir(), filepath.FromSlash(ResultFile(node.ID, "", attempt))), pretty.Bytes(), 0o644)
	}
	var v any
	if err := json.Unmarshal(m.Output, &v); err != nil {
		fin.Failure, fin.Error = chronicle.FailInvalidOutput, "output is not valid JSON: "+err.Error()
		return nil
	}
	if deniedFails(node, m.Denied) {
		if reason, ok, _ := unableReason(v); !ok || reason == "" {
			fin.Failure, fin.Error = chronicle.FailDenied, deniedError(m.Denied)
			return nil
		}
	}
	if reason, ok, err := unableReason(v); ok {
		if err != nil {
			fin.Failure, fin.Error = chronicle.FailInvalidOutput, err.Error()
			return nil
		}
		if _, follow := node.On[orders.Unable]; follow {
			var compact bytes.Buffer
			json.Compact(&compact, m.Output)
			fin.Output, fin.Outcome = compact.Bytes(), orders.Unable
		} else {
			fin.Failure, fin.Error = chronicle.FailUnable, reason
		}
		return nil
	}
	// The engine invites a reason field for an answer that cannot proceed;
	// next to any other outcome it is no part of the step's schema, so it
	// is left out before validation and of the stored output. A reason
	// that says something is kept on the event for a person to read; the
	// answer still counts as the outcome it names.
	raw := m.Output
	if obj, ok := v.(map[string]any); ok {
		if r, has := obj[orders.UnableField]; has {
			delete(obj, orders.UnableField)
			fin.Dropped = droppedReason(r)
			raw, _ = json.Marshal(obj)
		}
	}
	if obj, ok := v.(map[string]any); ok && asking && obj[orders.OutcomeField] == ask.Outcome {
		field, _ := json.Marshal(obj[ask.Field])
		qs, err := ask.Parse(field)
		if err == nil && len(obj) != 2 {
			err = fmt.Errorf("an answer with outcome %q holds only %q", ask.Outcome, ask.Field)
		}
		if err != nil {
			fin.Failure, fin.Error = chronicle.FailInvalidOutput, err.Error()
			return nil
		}
		var compact bytes.Buffer
		json.Compact(&compact, raw)
		fin.Output = compact.Bytes()
		return qs
	}
	if problems := node.Schema.Validate(v); len(problems) > 0 {
		fin.Failure, fin.Error = chronicle.FailInvalidOutput, strings.Join(problems, "; ")
		return nil
	}
	var compact bytes.Buffer
	json.Compact(&compact, raw)
	fin.Output = compact.Bytes()
	fin.Outcome = v.(map[string]any)[orders.OutcomeField].(string)
	return nil
}

// render fills in the prompt template of a node. lang is the campaign's
// language for the person; when none is set, the language placeholders name
// the default.
func (e *Engine) render(node *orders.Node, events []chronicle.Event, lang language.Language, langSet bool) (string, error) {
	if !langSet {
		lang, _ = language.Lookup(language.Default)
	}
	return node.Template.Render(func(ph prompt.Placeholder) (string, error) {
		ref, err := e.Orders.Ref(ph.Name)
		if err != nil {
			return "", err
		}
		switch ref.Kind {
		case orders.RefInput:
			input, err := e.runInput(events)
			if err != nil {
				return "", err
			}
			v, ok := field(input, ref.Path)
			if !ok {
				return "", e.missingInput(ph.Name, ref.Path)
			}
			return show(v), nil
		case orders.RefOutput:
			out := lastOutput(events, ref.Node)
			if out == nil {
				return "", nil
			}
			var v any
			json.Unmarshal(out, &v)
			v, _ = field(v, ref.Path)
			return show(v), nil
		case orders.RefArtifact:
			return e.artifact(events, ref)
		case orders.RefAnswers:
			asked, answer := answersOf(events, ref.Node)
			if answer == nil {
				return "", nil
			}
			if len(ref.Path) == 0 {
				return answersText(asked, answer), nil
			}
			for _, a := range answer.Answers {
				if a.ID == ref.Path[0] {
					if len(ref.Path) == 2 {
						return a.Comment, nil
					}
					return a.Value, nil
				}
			}
			return "", nil
		case orders.RefLanguage:
			switch {
			case len(ref.Path) == 0:
				return lang.English, nil
			case ref.Path[0] == orders.LanguageNative:
				return lang.Native, nil
			}
			return lang.Code, nil
		case orders.RefWork:
			return WorkDir(e.top().Orders, e.Campaign), nil
		default:
			return rework(e.Orders, e.dir(), events, node.ID), nil
		}
	})
}

// field walks into a JSON value.
func field(v any, path []string) (any, bool) {
	for _, f := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[f]; !ok {
			return nil, false
		}
	}
	return v, true
}

// show renders a JSON value for a prompt: a string as it is, nothing for
// null or a missing value, anything else as indented JSON.
func show(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}

// lastOutput is the validated output of the latest successful attempt of
// an agent node, or nil.
func lastOutput(events []chronicle.Event, node string) json.RawMessage {
	for i := len(events) - 1; i >= 0; i-- {
		if ev := events[i]; ev.Kind == chronicle.StepFinished && ev.Node == node && len(ev.Output) > 0 {
			return ev.Output
		}
	}
	return nil
}

// lastFinished is the latest step_finished event of a node with an outcome.
func lastFinished(events []chronicle.Event, node string) *chronicle.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if ev := &events[i]; ev.Kind == chronicle.StepFinished && ev.Node == node && ev.Outcome != "" {
			return ev
		}
	}
	return nil
}

// artifact reads a file that the latest finished execution of a node listed
// as an artifact. A file not listed gives nothing.
func (e *Engine) artifact(events []chronicle.Event, ref orders.Ref) (string, error) {
	fin := lastFinished(events, ref.Node)
	if fin == nil {
		return "", nil
	}
	for _, a := range artifactsOf(events, fin) {
		if path.Clean(filepath.ToSlash(a)) != path.Clean(ref.File) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(e.dir(), "work", filepath.FromSlash(ref.File)))
		if err != nil {
			return "", fmt.Errorf("artifact %s of %s: %v", ref.File, ref.Node, err)
		}
		return string(data), nil
	}
	return "", nil
}

// rework explains why the flow came back to a node it has run before: the
// step that sent it back, that step's outcome, and its output, failed
// checks or artifacts. It is empty on the first visit.
func rework(o *orders.Orders, campaignDir string, events []chronicle.Event, node string) string {
	at := visitStart(events, node) - 1
	if at < 0 {
		return ""
	}
	seen := false
	for _, ev := range events[:at] {
		if ev.Kind == chronicle.StepStarted && ev.Node == node {
			seen = true
			break
		}
	}
	if !seen {
		return ""
	}
	t := events[at]
	var b strings.Builder
	if sentBack(t) {
		what := "asked for this step to be retried"
		if t.Kind == chronicle.Rewound {
			what = "sent the campaign back to this step; every step after it runs again"
		}
		fmt.Fprintf(&b, "This step runs again: a person %s.\n", what)
		return b.String()
	}
	at, via := sender(o, events, at)
	t = events[at]
	if len(via) > 0 {
		fmt.Fprintf(&b, "This step runs again: step %q finished with outcome %q and sent the flow back here through %s.\n", t.Node, t.Outcome, quoteList(via))
	} else {
		fmt.Fprintf(&b, "This step runs again: step %q finished with outcome %q and sent the flow back here.\n", t.Node, t.Outcome)
	}
	fin := lastFinished(events[:at], t.Node)
	if fin == nil {
		return b.String()
	}
	if len(fin.Output) > 0 {
		var v any
		json.Unmarshal(fin.Output, &v)
		fmt.Fprintf(&b, "Output of %q:\n%s\n", t.Node, show(v))
	}
	var failed []string
	for _, ev := range events[:at] {
		if ev.Kind == chronicle.CheckFinished && ev.Node == t.Node && ev.Attempt == fin.Attempt && ev.Outcome != orders.OK {
			s := ev.Check + " (" + ev.Outcome
			if ev.Soft {
				s += ", soft"
			}
			if ev.Log != "" {
				s += ", log " + filepath.Join(campaignDir, filepath.FromSlash(ev.Log))
			}
			if len(ev.Artifacts) > 0 {
				s += ", artifacts in the work folder: " + strings.Join(ev.Artifacts, " ")
			}
			failed = append(failed, s+")")
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "Checks that did not pass: %s\n", strings.Join(failed, ", "))
	}
	if arts := artifactsOf(events[:at], fin); len(arts) > 0 {
		fmt.Fprintf(&b, "Artifacts of %q in the work folder: %s\n", t.Node, strings.Join(arts, ", "))
	}
	if asked, answer := answersOf(events[:at], t.Node); answer != nil && asked.Attempt == fin.Attempt {
		who := "A person"
		if answer.By != "" {
			who = answer.By
		}
		fmt.Fprintf(&b, "%s answered the questions of %q:\n%s", who, t.Node, answersText(asked, answer))
	}
	return b.String()
}

// languageInstruction tells an agent step which language to write the text
// for a person in. It is added to every attempt of a campaign that has a
// language for the person, and to no attempt of one that has none.
func languageInstruction(l language.Language) string {
	return fmt.Sprintf(`

---
Write all text meant for a person in %s: questions for the person, summaries, plans and any other document a person is meant to read. Code, code comments, identifiers, commit messages, file names and anything written into the repository stay in English unless this step's prompt says otherwise.
`, l.Label())
}

// askInstructions tells a dialogue step how it may ask the person.
func askInstructions(left int) string {
	return fmt.Sprintf(`

---
If you cannot finish this step well without answers from a person, you may ask instead of answering. Reply with a JSON object whose "outcome" is %[1]q and whose %[2]q lists what to ask, and nothing else, for example:
{"outcome": %[1]q, %[2]q: [{"id": "scope", "question": "Should the export be included?", "kind": "yesno"}]}
Each question has an "id" (letters, digits, '_', '-' and '.'), the "question" and a "kind": "yesno", "choice" (with "options", a list of at least two answers) or "text". The answers come back as the next message in this conversation. You may ask %[3]d more time(s); ask everything you need at once.
`, ask.Outcome, ask.Field, left)
}

// dialogueHistory repeats every round of questions of this visit with the
// person's answers, for a step that continues on another client and so
// without the conversation in which they were asked.
func dialogueHistory(visit []chronicle.Event, node string) string {
	var b strings.Builder
	b.WriteString("\n\n---\nEarlier in this step you asked a person questions, and they answered. The conversation in which you asked is not available, so here is everything that was asked and answered; take it into account and do not ask it again:\n")
	var asked *chronicle.Event
	for i := range visit {
		ev := &visit[i]
		switch {
		case ev.Node != node:
		case ev.Kind == chronicle.Asked:
			asked = ev
		case ev.Kind == chronicle.Answered && asked != nil && ev.Ask == asked.Seq:
			fmt.Fprintf(&b, "Round %d:\n%s\n", asked.Round, ask.Write(asked.Questions, ev.Answers))
			asked = nil
		}
	}
	return b.String()
}

// dialogueMessage is the next message of a dialogue step: the person's
// answers to its questions.
func dialogueMessage(asked, answer *chronicle.Event, left int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The person answered your questions (round %d):\n%s\n", asked.Round, ask.Write(asked.Questions, answer.Answers))
	if left > 0 {
		fmt.Fprintf(&b, "If you still need to know something, you may ask again in the same way %d more time(s). ", left)
	} else {
		b.WriteString("You may not ask any more questions. ")
	}
	b.WriteString("Otherwise continue the step and reply with a single JSON object that matches the required output schema.\n")
	return b.String()
}
