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
	if node.Dialogue > 0 && !desc.Has(adapter.FeatureResume) {
		return e.stop(node.ID, fmt.Sprintf("%s holds a dialogue, but the adapter of executor %s (%s) does not declare the %q feature, "+
			"so it cannot continue the conversation with a person's answers; use an adapter that can, or remove dialogue", node.ID, x.Name, desc.Name, adapter.FeatureResume))
	}
	c := carry(events[visitStart(events, node.ID):], node.ID, desc.Has(adapter.FeatureResume))
	left := node.Dialogue - c.rounds // rounds of questions the step may still ask
	lang, langSet, err := e.language()
	if err != nil {
		return e.stop(node.ID, fmt.Sprintf("%s: %v", node.ID, err))
	}
	var text string
	if c.answer != nil {
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
	if c.answer == nil && left > 0 {
		text += askInstructions(left)
	}
	schemaJSON := node.SchemaJSON
	if left > 0 {
		if schemaJSON, err = ask.DialogueSchema(node.SchemaJSON); err != nil {
			return e.stop(node.ID, fmt.Sprintf("%s: schema: %v", node.ID, err))
		}
	}
	if c.feedback != "" {
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
		Schema: schemaJSON, Session: session,
	})
	reported := "" // the session id this attempt reported
	recordErr := error(nil)
	r := runAdapter(ctx, adapterProc{
		argv:    append([]string{e.Orders.Program(x.Run[0])}, x.Run[1:]...),
		dir:     e.Orders.Dir,
		env:     env,
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
			reported = id
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
	questions := e.judge(node, attempt, r, &fin, left > 0)
	if questions != nil {
		if reported == "" {
			reported = c.session
		}
		if reported == "" {
			questions, fin.Output = nil, nil
			fin.Failure, fin.Error = chronicle.FailProtocol, "the step asked questions but its adapter reported no session, so the conversation cannot be continued with the answers"
		}
	}
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
	case questions != nil:
		e.say("%s asks the person %d question(s), round %d of at most %d (%s)", node.ID, len(questions), c.rounds+1, node.Dialogue, took)
		return e.record(chronicle.Event{Kind: chronicle.Asked, Node: node.ID, Attempt: attempt, Details: chronicle.Details{
			Reason: chronicle.ReasonDialogue, Questions: questions, Round: c.rounds + 1, Session: reported}})
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
	if obj, ok := v.(map[string]any); ok && asking && obj[orders.OutcomeField] == ask.Outcome {
		raw, _ := json.Marshal(obj[ask.Field])
		qs, err := ask.Parse(raw)
		if err == nil && len(obj) != 2 {
			err = fmt.Errorf("an answer with outcome %q holds only %q", ask.Outcome, ask.Field)
		}
		if err != nil {
			fin.Failure, fin.Error = chronicle.FailInvalidOutput, err.Error()
			return nil
		}
		var compact bytes.Buffer
		json.Compact(&compact, m.Output)
		fin.Output = compact.Bytes()
		return qs
	}
	if problems := node.Schema.Validate(v); len(problems) > 0 {
		fin.Failure, fin.Error = chronicle.FailInvalidOutput, strings.Join(problems, "; ")
		return nil
	}
	var compact bytes.Buffer
	json.Compact(&compact, m.Output)
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
