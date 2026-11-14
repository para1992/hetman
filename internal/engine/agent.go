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
	"hetman/internal/chronicle"
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
		e.say("%s waits for the subscription limit to reset; runs again at %s", node, until.Local().Format("2006-01-02 15:04:05"))
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
	d, err := describe(ctx, argv, e.Orders.Dir, e.env("", "", 0, ""))
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
// a node: the event after the transition that made it the position, or 0.
func visitStart(events []chronicle.Event, node string) int {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == chronicle.Transition && events[i].To == node {
			return i + 1
		}
	}
	return 0
}

// carryOver is what an attempt takes from the attempts before it in the
// same visit.
type carryOver struct {
	session  string // conversation to continue
	feedback string // why the previous output was rejected
	rejected int    // the attempt whose output was rejected
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
	for _, ev := range visit {
		if ev.Node != node {
			continue
		}
		switch ev.Kind {
		case chronicle.StepStarted:
			// An attempt with no finish after it was cut off by the engine
			// stopping, whether or not its step_interrupted is recorded yet.
			last, cutOff = ev.Attempt, true
			if ev.Session != "" {
				sessions[ev.Attempt] = ev.Session
			}
		case chronicle.AgentSession:
			sessions[ev.Attempt] = ev.Session
		case chronicle.StepFinished:
			cutOff = ev.Failure == chronicle.FailLimit || ev.Failure == chronicle.FailOffline
			if chronicle.Counts(ev.Failure) {
				c.feedback, c.rejected = "", 0
				if ev.Failure == chronicle.FailInvalidOutput {
					c.feedback, c.rejected = ev.Error, ev.Attempt
				}
			}
		}
	}
	if cutOff && canResume {
		c.session = sessions[last]
	}
	return c
}

// agentStep runs one attempt of an agent node.
func (e *Engine) agentStep(ctx context.Context, node *orders.Node, attempt int, st chronicle.State, events []chronicle.Event) error {
	x := e.Orders.Executors[node.Executor]
	desc, err := e.describe(ctx, x)
	if err != nil {
		if ctx.Err() != nil {
			return ErrInterrupted
		}
		return e.stop(node.ID, fmt.Sprintf("executor %s: %v", x.Name, err))
	}
	c := carry(events[visitStart(events, node.ID):], node.ID, desc.Has(adapter.FeatureResume))
	text, err := e.render(node, events)
	if err != nil {
		return e.stop(node.ID, fmt.Sprintf("%s: prompt %s: %v", node.ID, filepath.Base(node.Prompt), err))
	}
	if c.feedback != "" {
		text += fmt.Sprintf("\n\n---\nYour previous answer (attempt %d) was rejected because it does not match the required output schema:\n- %s\nAnswer again with a single JSON object that matches the schema.\n",
			c.rejected, strings.ReplaceAll(c.feedback, "; ", "\n- "))
	}
	dir := CampaignDir(e.Orders, e.Campaign)
	for _, sub := range []string{"work", "logs", "results", "prompts"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	promptRel := PromptFile(node.ID, attempt)
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(promptRel)), []byte(text), 0o644); err != nil {
		return err
	}
	if err := e.record(chronicle.Event{Kind: chronicle.StepStarted, Node: node.ID, Attempt: attempt, Details: chronicle.Details{
		Executor: x.Name, Model: node.Model, Effort: node.Effort, Prompt: promptRel, Session: c.session}}); err != nil {
		return err
	}
	if c.session != "" {
		e.say("%s attempt %d started (executor %s, continuing session %s)", node.ID, attempt, x.Name, c.session)
	} else {
		e.say("%s attempt %d started (executor %s)", node.ID, attempt, x.Name)
	}
	began := time.Now()
	fin := chronicle.Event{Kind: chronicle.StepFinished, Node: node.ID, Attempt: attempt,
		Details: chronicle.Details{Log: LogFile(node.ID, "", attempt), Stderr: StderrFile(node.ID, attempt)}}
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
	start := adapter.EncodeStart(adapter.Step{
		RunID: strconv.FormatInt(e.Campaign, 10), NodeID: node.ID, Attempt: attempt, Prompt: text,
		Model: node.Model, Effort: node.Effort, Tools: node.Tools, Workdir: e.Orders.Dir,
		Schema: node.SchemaJSON, Session: session,
	})
	recordErr := error(nil)
	r := runAdapter(ctx, adapterProc{
		argv:    append([]string{e.Orders.Program(x.Run[0])}, x.Run[1:]...),
		dir:     e.Orders.Dir,
		env:     e.env(node.ID, "", attempt, ""),
		start:   start,
		log:     logf,
		stderr:  errf,
		timeout: node.Timeout,
		idle:    node.IdleTimeout,
		started: func(pid int) {
			ident, _ := processIdent(pid)
			if err := e.record(chronicle.Event{Kind: chronicle.ProcessStarted, Node: node.ID, Attempt: attempt,
				Details: chronicle.Details{Pid: pid, Ident: ident}}); err != nil {
				e.say("cannot record process %d of %s: %v", pid, node.ID, err)
			}
		},
		session: func(id string) {
			if err := e.record(chronicle.Event{Kind: chronicle.AgentSession, Node: node.ID, Attempt: attempt,
				Details: chronicle.Details{Session: id}}); err != nil && recordErr == nil {
				recordErr = err
			}
			e.say("  %s session %s", node.ID, id)
		},
	})
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
	e.judge(node, attempt, r, &fin)
	if chronicle.Counts(fin.Failure) && fin.Outcome == "" && st.Failures+1 >= node.MaxAttempts {
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
	case fin.Failure == "":
		e.say("%s finished: %s (%s)", node.ID, fin.Outcome, took)
	case fin.Failure == chronicle.FailPermanent:
		e.say("%s attempt %d failed (permanent): %s; no retry", node.ID, attempt, fin.Error)
	case fin.Failure == chronicle.FailLimit && fin.RetryAt != nil:
		e.say("%s attempt %d hit the subscription limit (%s); it does not count", node.ID, attempt, fin.Error)
	case fin.Failure == chronicle.FailLimit:
		e.say("%s attempt %d hit the subscription limit (%s) with no time to retry; waiting for a person", node.ID, attempt, fin.Error)
	case fin.Failure == chronicle.FailOffline:
		e.say("%s attempt %d lost: no connectivity (%s); waiting for a person", node.ID, attempt, fin.Error)
	case fin.Outcome == orders.Fail:
		e.say("%s attempt %d failed (%s): %s; all %d attempts used", node.ID, attempt, fin.Failure, fin.Error, node.MaxAttempts)
	default:
		e.say("%s attempt %d failed (%s): %s; %d of %d attempts left", node.ID, attempt, fin.Failure, fin.Error,
			node.MaxAttempts-st.Failures-1, node.MaxAttempts)
	}
	return nil
}

// judge turns how the adapter ended into the outcome or failure of the
// attempt, and stores the output.
func (e *Engine) judge(node *orders.Node, attempt int, r adapterRun, fin *chronicle.Event) {
	switch {
	case r.timedOut:
		fin.Failure, fin.Error = chronicle.FailTimeout, fmt.Sprintf("killed after timeout %s", node.Timeout)
		return
	case r.idleOut:
		fin.Failure, fin.Error = chronicle.FailIdle, fmt.Sprintf("no message from the adapter for %s; killed", node.IdleTimeout)
		return
	case r.protocol != "":
		fin.Failure, fin.Error = chronicle.FailProtocol, r.protocol
		return
	case r.lateExit:
		fin.Failure, fin.Error = chronicle.FailCrash, fmt.Sprintf("the adapter did not exit within %s after its %s; killed", exitGrace, r.terminal.Type)
		return
	case r.terminal == nil:
		fin.Failure, fin.Error = chronicle.FailCrash, fmt.Sprintf("the adapter exited with code %d without a result or an error", r.exit)
		return
	case r.exit != 0:
		fin.Failure, fin.Error = chronicle.FailCrash, fmt.Sprintf("the adapter exited with code %d after its %s", r.exit, r.terminal.Type)
		return
	}
	m := r.terminal
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
		return
	}
	// The output is kept in the result file of the attempt whether or not
	// it is valid, so that a person can see what was rejected.
	var pretty bytes.Buffer
	if json.Indent(&pretty, m.Output, "", "  ") == nil {
		pretty.WriteByte('\n')
		os.WriteFile(filepath.Join(CampaignDir(e.Orders, e.Campaign), filepath.FromSlash(ResultFile(node.ID, "", attempt))), pretty.Bytes(), 0o644)
	}
	var v any
	if err := json.Unmarshal(m.Output, &v); err != nil {
		fin.Failure, fin.Error = chronicle.FailInvalidOutput, "output is not valid JSON: "+err.Error()
		return
	}
	if problems := node.Schema.Validate(v); len(problems) > 0 {
		fin.Failure, fin.Error = chronicle.FailInvalidOutput, strings.Join(problems, "; ")
		return
	}
	var compact bytes.Buffer
	json.Compact(&compact, m.Output)
	fin.Output = compact.Bytes()
	fin.Outcome = v.(map[string]any)[orders.OutcomeField].(string)
}

// render fills in the prompt template of a node.
func (e *Engine) render(node *orders.Node, events []chronicle.Event) (string, error) {
	return node.Template.Render(func(ph prompt.Placeholder) (string, error) {
		ref, err := e.Orders.Ref(ph.Name)
		if err != nil {
			return "", err
		}
		switch ref.Kind {
		case orders.RefInput:
			var input any = map[string]any{}
			if len(events) > 0 && len(events[0].Input) > 0 {
				if err := json.Unmarshal(events[0].Input, &input); err != nil {
					return "", fmt.Errorf("run input: %v", err)
				}
			}
			v, ok := field(input, ref.Path)
			if !ok {
				return "", fmt.Errorf("{{%s}}: the run input has no field %q", ph.Name, strings.Join(ref.Path, "."))
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
		default:
			return rework(events, node.ID), nil
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
	for _, a := range fin.Artifacts {
		if path.Clean(filepath.ToSlash(a)) != path.Clean(ref.File) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(CampaignDir(e.Orders, e.Campaign), "work", filepath.FromSlash(ref.File)))
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
func rework(events []chronicle.Event, node string) string {
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
	fmt.Fprintf(&b, "This step runs again: step %q finished with outcome %q and sent the flow back here.\n", t.Node, t.Outcome)
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
			failed = append(failed, s+")")
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "Checks that did not pass: %s\n", strings.Join(failed, ", "))
	}
	if len(fin.Artifacts) > 0 {
		fmt.Fprintf(&b, "Artifacts of %q in the work folder: %s\n", t.Node, strings.Join(fin.Artifacts, ", "))
	}
	return b.String()
}
