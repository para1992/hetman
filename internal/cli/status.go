package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/orders"
)

type nodeView struct {
	attempts    int
	interrupted int
	attempt     int // latest attempt
	running     bool
	outcome     string
	err         string
	millis      int64
	artifacts   []string
	checks      map[string]chronicle.Event // of the latest attempt
	agent       []*agentAttempt            // every attempt of an agent node
}

// agentAttempt is what status shows of one attempt of an agent node.
type agentAttempt struct {
	n                       int
	executor, model, effort string
	prompt, log, stderr     string
	sessionIn, sessionOut   string
	running, interrupted    bool
	outcome, failure, err   string
	output                  string
	retryAt                 *time.Time
	asked                   int // questions this attempt asked the person
}

func (c *command) status(id int64, listEvents bool) int {
	k, err := c.campaign(id)
	if err != nil {
		return c.fail(err)
	}
	events, err := c.chron.Events(k.ID)
	if err != nil {
		return c.fail(err)
	}
	st := chronicle.Replay(events)
	alive, _ := engine.Driven(c.o, k.ID)
	views := map[string]*nodeView{}
	view := func(n string) *nodeView {
		if views[n] == nil {
			views[n] = &nodeView{}
		}
		return views[n]
	}
	for _, e := range events {
		switch e.Kind {
		case chronicle.StepStarted:
			v := view(e.Node)
			v.attempts++
			v.attempt, v.running, v.outcome, v.err, v.artifacts = e.Attempt, true, "", "", nil
			v.checks = map[string]chronicle.Event{}
			if e.Executor != "" {
				v.agent = append(v.agent, &agentAttempt{n: e.Attempt, executor: e.Executor, model: e.Model, effort: e.Effort,
					prompt: e.Prompt, sessionIn: e.Session, running: true})
			}
		case chronicle.StepInterrupted:
			v := view(e.Node)
			v.running = false
			v.interrupted++
			if a := v.lastAgent(e.Attempt); a != nil {
				a.running, a.interrupted = false, true
			}
		case chronicle.AgentSession:
			if a := view(e.Node).lastAgent(e.Attempt); a != nil {
				a.sessionOut = e.Session
			}
		case chronicle.StepFinished:
			v := view(e.Node)
			v.running, v.outcome, v.err, v.millis, v.artifacts = false, e.Outcome, e.Error, e.Millis, e.Artifacts
			if e.Failure != "" {
				// A failed agent attempt is not an error of the campaign; its
				// reason is listed with the attempts.
				v.err = ""
			}
			if a := v.lastAgent(e.Attempt); a != nil {
				a.running, a.outcome, a.failure, a.err, a.output, a.retryAt = false, e.Outcome, e.Failure, e.Error, string(e.Output), e.RetryAt
				a.log, a.stderr = e.Log, e.Stderr
			}
		case chronicle.CheckFinished:
			view(e.Node).checks[e.Check] = e
		case chronicle.Asked:
			if a := view(e.Node).lastAgent(e.Attempt); a != nil {
				a.asked = len(e.Questions)
			}
		}
	}

	w := c.stdout
	fmt.Fprintf(w, "campaign %d  flow %s  started %s\n", k.ID, k.Flow, k.Started.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "state: %s\n", c.describe(k.ID, st))

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  NODE\tTYPE\tSTATUS\tOUTCOME\tATTEMPTS\tTIME\t")
	order := append([]string{}, c.o.Order...)
	for n := range views {
		if c.o.Nodes[n] == nil {
			order = append(order, n)
		}
	}
	for _, n := range order {
		typ := "?"
		var node *orders.Node
		if node = c.o.Nodes[n]; node != nil {
			typ = node.Type
		}
		v := views[n]
		marker := " "
		if n == st.Node && st.End == "" {
			marker = ">"
		}
		if v == nil {
			fmt.Fprintf(tw, "%s %s\t%s\tpending\t\t\t\t\n", marker, n, typ)
			continue
		}
		status, outcome, dur := "finished", v.outcome, ""
		switch {
		case n == st.Node && st.End == "" && st.Asking():
			status, outcome = "asking", "waits for a person"
		case v.running && st.Running && st.Node == n:
			status, outcome = "running", ""
		case v.running:
			status = "interrupted"
		case v.err != "":
			status, outcome = "error", v.err
		case n == st.Node && st.End == "" && st.Waiting != "":
			status, outcome = "waiting", st.Waiting
		case v.outcome == "" && len(v.agent) > 0 && v.agent[len(v.agent)-1].failure != "":
			status, outcome = "attempt failed", v.agent[len(v.agent)-1].failure
		}
		if v.millis > 0 || status == "finished" {
			dur = (time.Duration(v.millis) * time.Millisecond).String()
		}
		attempts := fmt.Sprint(v.attempts)
		if v.interrupted > 0 {
			attempts += fmt.Sprintf(" (%d interrupted)", v.interrupted)
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\t%s\t\n", marker, n, typ, status, outcome, attempts, dur)
		if node != nil {
			for _, name := range node.Checks {
				fmt.Fprintf(tw, "    check %s\t%s\t\n", name, checkLine(v.checks, name, c.o.Checks[name]))
			}
		}
	}
	tw.Flush()
	for _, n := range order {
		if v := views[n]; v != nil && len(v.artifacts) > 0 {
			fmt.Fprintf(w, "artifacts of %s: %s\n", n, strings.Join(v.artifacts, ", "))
		}
	}
	for _, n := range order {
		if v := views[n]; v != nil && len(v.agent) > 0 {
			printAgent(w, n, v.agent, st.Running && st.Node == n && alive)
		}
	}

	// Every answer given, then the question open now.
	asks := map[int64]*chronicle.Event{}
	for i := range events {
		e := &events[i]
		switch e.Kind {
		case chronicle.Asked:
			asks[e.Seq] = e
		case chronicle.Answered:
			if q := asks[e.Ask]; q != nil {
				printAnswered(w, q, e)
				delete(asks, e.Ask) // a later answer to the same question did not count
			}
		}
	}
	if st.Asking() && st.End == "" {
		printAsked(w, c.o, k.ID, st.Ask, true)
	}

	var warnings []string
	for _, e := range events {
		if e.Kind == chronicle.CheckFinished && e.Soft && e.Outcome != orders.OK && e.Error == "" {
			warnings = append(warnings, fmt.Sprintf("soft check %s of %s (attempt %d): %s", e.Check, e.Node, e.Attempt, e.Outcome))
		}
	}
	for _, s := range warnings {
		fmt.Fprintln(w, "warning:", s)
	}
	if listEvents {
		fmt.Fprintln(w, "chronicle:")
		for _, e := range events {
			fmt.Fprintf(w, "  %4d %s %s\n", e.Seq, e.At.Local().Format("15:04:05.000"), eventLine(e))
		}
	}
	return exitDone
}

func (v *nodeView) lastAgent(attempt int) *agentAttempt {
	for i := len(v.agent) - 1; i >= 0; i-- {
		if v.agent[i].n == attempt {
			return v.agent[i]
		}
	}
	return nil
}

// printAgent lists the attempts of an agent node.
func printAgent(w io.Writer, node string, attempts []*agentAttempt, running bool) {
	fmt.Fprintf(w, "agent %s:\n", node)
	for i, a := range attempts {
		var facts []string
		if a.executor != "" {
			facts = append(facts, "executor "+a.executor)
		}
		if a.model != "" {
			facts = append(facts, "model "+a.model)
		}
		if a.effort != "" {
			facts = append(facts, "effort "+a.effort)
		}
		var status string
		switch {
		case a.running && running && i == len(attempts)-1:
			status = "running"
		case a.running || a.interrupted:
			status = "interrupted"
		case a.failure == chronicle.FailLimit && a.retryAt != nil:
			status = fmt.Sprintf("subscription limit, retry at %s (does not count): %s", a.retryAt.Local().Format("2006-01-02 15:04:05"), a.err)
		case a.failure == chronicle.FailLimit || a.failure == chronicle.FailOffline:
			status = fmt.Sprintf("%s, waits for a person (does not count): %s", a.failure, a.err)
		case a.failure != "" && a.outcome != "":
			status = fmt.Sprintf("failed (%s): %s -> outcome %s", a.failure, a.err, a.outcome)
		case a.failure != "":
			status = fmt.Sprintf("failed (%s): %s", a.failure, a.err)
		case a.asked > 0:
			status = fmt.Sprintf("asked the person %d question(s)", a.asked)
		default:
			status = "outcome " + a.outcome
		}
		fmt.Fprintf(w, "  attempt %d  %s  [%s]\n", a.n, status, strings.Join(facts, ", "))
		if a.prompt != "" {
			fmt.Fprintf(w, "    prompt   %s\n", a.prompt)
		}
		if a.sessionIn != "" {
			fmt.Fprintf(w, "    continued session %s\n", a.sessionIn)
		}
		if a.sessionOut != "" {
			fmt.Fprintf(w, "    session  %s\n", a.sessionOut)
		}
		if a.output != "" {
			out := a.output
			if len(out) > 300 {
				out = out[:300] + "..."
			}
			fmt.Fprintf(w, "    output   %s\n", out)
		}
	}
}

// checkLine renders the TYPE to TIME cells of a check row.
func checkLine(results map[string]chronicle.Event, name string, def *orders.Check) string {
	kind := "required"
	if def != nil && def.Soft {
		kind = "soft"
	}
	e, ok := results[name]
	dur := (time.Duration(e.Millis) * time.Millisecond).String()
	switch {
	case !ok:
		return kind + "\tnot run\t\t\t"
	case e.Error != "":
		return kind + "\terror\t" + e.Error + "\t\t" + dur
	case e.Outcome == orders.OK:
		return kind + "\tfinished\tok\t\t" + dur
	case e.Soft:
		return kind + "\tfinished\t" + e.Outcome + " (warning)\t\t" + dur
	default:
		return kind + "\tfinished\t" + e.Outcome + " (blocked the gate)\t\t" + dur
	}
}

// describe summarises the campaign state. Whether a process is driving the
// campaign is learned by trying its lock.
func (c *command) describe(id int64, st chronicle.State) string {
	switch {
	case st.End != "":
		return st.End
	case st.Error != "":
		return "error: " + st.Error + " (fix the cause, then hetman resume)"
	}
	alive, _ := engine.Driven(c.o, id)
	where := "at " + st.Node
	if st.Asking() {
		hint := "the running hetman process continues once answered"
		if !alive {
			hint = "no hetman process drives it: after answering, hetman resume continues it"
		}
		return fmt.Sprintf("waiting for a person at %s: hetman answer (%s)", st.Node, hint)
	}
	if st.Ask != nil && !alive {
		return fmt.Sprintf("answered at %s, but no hetman process drives it (hetman resume to continue)", st.Node)
	}
	if st.Running {
		where = fmt.Sprintf("running %s (attempt %d)", st.Node, st.Attempt)
	}
	switch {
	case st.Halted:
		return fmt.Sprintf("halted before %s (hetman resume to continue)", st.Node)
	case st.Waiting == chronicle.WaitLimit && !st.WaitUntil.IsZero():
		until := st.WaitUntil.Local().Format("2006-01-02 15:04:05")
		if alive {
			return fmt.Sprintf("waiting at %s for the subscription limit to reset; runs again at %s", st.Node, until)
		}
		return fmt.Sprintf("waiting at %s for the subscription limit until %s, but no hetman process is running it (hetman resume to continue; it waits until then)", st.Node, until)
	case st.Waiting == chronicle.WaitLimit:
		return fmt.Sprintf("stopped at %s: the subscription limit was reached and no time to retry was given (hetman resume runs the step again)", st.Node)
	case st.Waiting == chronicle.WaitOffline:
		return fmt.Sprintf("stopped at %s: connectivity was lost (hetman resume runs the step again)", st.Node)
	case st.HaltRequested && alive:
		return where + ", halt requested: halts after the current step"
	case alive:
		return where
	case st.Running:
		return where + ", but no hetman process is running it: interrupted (hetman resume to continue)"
	default:
		return where + ", no hetman process is running it (hetman resume to continue)"
	}
}

func eventLine(e chronicle.Event) string {
	parts := []string{e.Kind}
	if e.Node != "" {
		parts = append(parts, e.Node)
	}
	if e.Check != "" {
		parts = append(parts, "check="+e.Check)
	}
	if e.Attempt > 0 {
		parts = append(parts, fmt.Sprintf("attempt=%d", e.Attempt))
	}
	if e.Outcome != "" {
		parts = append(parts, "outcome="+e.Outcome)
	}
	if e.To != "" {
		parts = append(parts, "to="+e.To)
	}
	if e.Soft {
		parts = append(parts, "soft")
	}
	if e.Pid != 0 {
		parts = append(parts, fmt.Sprintf("pid=%d", e.Pid))
	}
	if e.Ident != "" {
		parts = append(parts, "started="+e.Ident)
	}
	if e.Exit != nil {
		parts = append(parts, fmt.Sprintf("exit=%d", *e.Exit))
	}
	if e.Executor != "" {
		parts = append(parts, "executor="+e.Executor)
	}
	if e.Model != "" {
		parts = append(parts, "model="+e.Model)
	}
	if e.Effort != "" {
		parts = append(parts, "effort="+e.Effort)
	}
	if e.Prompt != "" {
		parts = append(parts, "prompt="+e.Prompt)
	}
	if e.Session != "" {
		parts = append(parts, "session="+e.Session)
	}
	if e.Failure != "" {
		parts = append(parts, "failure="+e.Failure)
	}
	if e.RetryAt != nil {
		parts = append(parts, "retry_at="+e.RetryAt.Local().Format(time.RFC3339))
	}
	if len(e.Input) > 0 {
		parts = append(parts, "input="+string(e.Input))
	}
	if len(e.Output) > 0 {
		parts = append(parts, "output="+string(e.Output))
	}
	if e.Reason != "" {
		parts = append(parts, "reason="+e.Reason)
	}
	if e.Round > 0 {
		parts = append(parts, fmt.Sprintf("round=%d", e.Round))
	}
	if len(e.Questions) > 0 {
		q, _ := json.Marshal(e.Questions)
		parts = append(parts, "questions="+string(q))
	}
	if len(e.Show) > 0 {
		parts = append(parts, "show="+strings.Join(e.Show, ","))
	}
	if len(e.Outcomes) > 0 {
		parts = append(parts, "outcomes="+strings.Join(e.Outcomes, ","))
	}
	if e.Ask != 0 {
		parts = append(parts, fmt.Sprintf("ask=%d", e.Ask))
	}
	if len(e.Answers) > 0 {
		a, _ := json.Marshal(e.Answers)
		parts = append(parts, "answers="+string(a))
	}
	if e.By != "" {
		parts = append(parts, "by="+e.By)
	}
	if e.Via != "" {
		parts = append(parts, "via="+e.Via)
	}
	if e.Log != "" && e.Kind == chronicle.Notified {
		parts = append(parts, "log="+e.Log)
	}
	if e.Error != "" {
		parts = append(parts, fmt.Sprintf("error=%q", e.Error))
	}
	return strings.Join(parts, " ")
}
