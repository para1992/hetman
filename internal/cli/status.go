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
	"hetman/internal/explain"
	"hetman/internal/i18n"
	"hetman/internal/language"
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
	denied                  []string // tool calls the client denied
	executor, model, effort string
	prompt, log, stderr     string
	sessionIn, sessionOut   string
	running, interrupted    bool
	outcome, failure, err   string
	output                  string
	dropped                 string // a cannot-proceed reason given next to another outcome
	retryAt                 *time.Time
	asked                   int               // questions this attempt asked the person
	choice                  *chronicle.Choice // how the client of a step run through a role was chosen
}

// resultWidth is the longest text the RESULT column of status shows.
const resultWidth = 60

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
	// A child campaign is shown with the orders of its segment.
	o, err := engine.OrdersOf(c.o, c.chron, k.ID)
	if err != nil {
		return c.fail(err)
	}
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
			v.attempt, v.running, v.outcome, v.err, v.artifacts, v.millis = e.Attempt, true, "", "", nil, 0
			v.checks = map[string]chronicle.Event{}
			if e.Executor != "" {
				v.agent = append(v.agent, &agentAttempt{n: e.Attempt, executor: e.Executor, model: e.Model, effort: e.Effort,
					prompt: e.Prompt, sessionIn: e.Session, running: true, choice: e.Choice})
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
				a.denied, a.dropped = e.Denied, e.Dropped
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

	w, p := c.stdout, c.p
	fmt.Fprintln(w, p.T("status.head", "id", k.ID, "flow", k.Flow, "started", k.Started.Local().Format("2006-01-02 15:04:05")))
	if len(events) > 0 && events[0].Language != "" {
		code := events[0].Language
		if l, ok := language.Lookup(code); ok {
			code += " " + l.Label()
		}
		fmt.Fprintln(w, p.T("status.language", "language", code))
	}
	if len(events) > 0 {
		if t := engine.TaskOf(events[0].Input); t != nil {
			fmt.Fprintln(w, p.T("status.task", "id", t.ID, "kind", t.Kind))
		}
	}
	if k.Parent != 0 {
		top, _ := engine.Top(c.chron, k.ID)
		where := events[0].ParentNode
		if events[0].Item != "" {
			where = p.T("status.part_of_item", "node", where, "item", events[0].Item)
		}
		fmt.Fprintln(w, p.T("status.part_of", "top", top, "where", where, "parent", k.Parent))
	}
	fmt.Fprintln(w, p.T("status.state", "state", c.describe(k.ID, st)))
	if st.End == "" {
		ws, _ := engine.Waiters(c.chron, events)
		for _, x := range ws {
			fmt.Fprintf(w, "  %s\n", waiterLine(p, x))
		}
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, p.T("status.header"))
	order := append([]string{}, o.Order...)
	for n := range views {
		if o.Nodes[n] == nil {
			order = append(order, n)
		}
	}
	var reasons []string // long reasons, printed under the table
	for _, n := range order {
		typ := "?"
		var node *orders.Node
		if node = o.Nodes[n]; node != nil {
			typ = node.Type
		}
		v := views[n]
		marker := " "
		if n == st.Node && st.End == "" {
			marker = ">"
		}
		if v == nil {
			status, outcome := p.T("status.st.pending"), ""
			if n == st.Node && st.End == "" && st.LockWait != "" {
				status, outcome = p.T("status.st.waiting_lock"), p.T("status.lock_held", "lock", st.LockWait, "holder", st.LockHolder)
			}
			fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t\t\t\n", marker, n, typ, status, outcome)
			continue
		}
		status, outcome, dur := "finished", v.outcome, ""
		switch {
		case n == st.Node && st.End == "" && st.Asking():
			status, outcome = "asking", p.T("status.waits_for_person")
		case v.running && st.Running && st.Node == n:
			status, outcome = "running", ""
		case v.running:
			status = "interrupted"
		case v.err != "":
			status, outcome = "error", v.err
		case n == st.Node && st.End == "" && st.Waiting != "":
			status, outcome = "waiting", st.Waiting
		case n == st.Node && st.End == "" && st.LockWait != "" && !st.Running:
			status, outcome = "waiting_lock", p.T("status.lock_held", "lock", st.LockWait, "holder", st.LockHolder)
		case v.outcome == "" && len(v.agent) > 0 && v.agent[len(v.agent)-1].failure != "":
			status, outcome = "attempt_failed", v.agent[len(v.agent)-1].failure
		}
		if v.millis > 0 || status == "finished" {
			dur = (time.Duration(v.millis) * time.Millisecond).String()
		}
		attempts := fmt.Sprint(v.attempts)
		if v.interrupted > 0 {
			attempts += p.T("status.interrupted_count", "n", v.interrupted)
		}
		// A long reason would stretch the table; it goes under it.
		if len([]rune(outcome)) > resultWidth {
			reasons = append(reasons, p.T("status.reason", "node", n, "reason", outcome))
			outcome = p.T("status.reason_below")
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\t%s\t\n", marker, n, typ, p.T("status.st."+status), outcome, attempts, dur)
		if node != nil {
			for _, name := range node.Checks {
				fmt.Fprintf(tw, "%s\t%s\t\n", p.T("status.check_row", "check", name), checkLine(p, v.checks, name, o.Checks[name]))
			}
			if node.Composite() {
				c.printChildren(tw, events, n, "    ", 0)
			}
		}
	}
	tw.Flush()
	for _, r := range reasons {
		fmt.Fprintln(w, r)
	}
	for _, n := range order {
		if v := views[n]; v != nil && len(v.artifacts) > 0 {
			fmt.Fprintln(w, p.T("status.artifacts", "node", n, "files", strings.Join(v.artifacts, ", ")))
		}
	}
	for _, n := range order {
		if v := views[n]; v != nil && len(v.agent) > 0 {
			printAgent(p, w, n, v.agent, st.Running && st.Node == n && alive)
		}
	}
	if st.End == "" && st.Waiting != "" {
		// The step waits because no client of its role can run.
		for i := len(events) - 1; i >= 0; i-- {
			e := &events[i]
			if e.Node == st.Node && (e.Kind == chronicle.StepStarted || e.Kind == chronicle.StepFinished) {
				break
			}
			if e.Kind == chronicle.NoClient && e.Node == st.Node && e.Choice != nil {
				fmt.Fprintln(w, p.T("status.no_client", "node", e.Node, "role", e.Choice.Role, "usage", explain.UsageWords(p, e.Choice, time.Local)))
				break
			}
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
				printAnswered(p, w, q, e)
				delete(asks, e.Ask) // a later answer to the same question did not count
			}
		}
	}
	top, _ := engine.Top(c.chron, k.ID)
	if asked, _, _ := engine.Pending(c.chron, k.ID); asked != nil && st.End == "" {
		ao, _ := engine.OrdersOf(c.o, c.chron, asked.Campaign)
		printAsked(p, w, c.o, ao, top, asked, true)
	}

	var warnings []string
	for _, e := range events {
		if e.Kind == chronicle.CheckFinished && e.Soft && e.Outcome != orders.OK && e.Error == "" {
			warnings = append(warnings, p.T("status.soft_warning", "check", e.Check, "node", e.Node, "attempt", e.Attempt, "outcome", e.Outcome))
		}
	}
	for _, s := range warnings {
		fmt.Fprintln(w, p.T("status.warning", "warning", s))
	}
	if listEvents {
		fmt.Fprintln(w, "chronicle:")
		for _, e := range events {
			fmt.Fprintf(w, "  %4d %s %s\n", e.Seq, e.At.Local().Format("15:04:05.000"), eventLine(e))
		}
	}
	return exitDone
}

// printChildren lists the children of the latest attempt of a segment or
// for-each node under its row, and the children of those that run a segment
// themselves, indented further.
func (c *command) printChildren(tw io.Writer, events []chronicle.Event, node, indent string, depth int) {
	p := c.p
	list, err := engine.ChildrenOf(c.chron, events, node)
	if err != nil || depth > orders.MaxDepth {
		return
	}
	// The children of earlier attempts are listed under the item they
	// tried, so that a failed earlier attempt does not read as the item's
	// state.
	earlier, _ := engine.EarlierChildren(c.chron, events, node)
	rest := engine.AttachEarlier(list, earlier)
	for _, ch := range list {
		name := p.T("status.child.segment")
		if ch.Item != "" {
			name = p.T("status.child.item", "item", ch.Item)
		}
		switch {
		case ch.NotRun != "":
			fmt.Fprintf(tw, "%s%s\t\t%s\t%s\t\t\t\n", indent, name, p.T("status.st.not_run"), notRunWhy(p, ch))
			printBefore(p, tw, ch, indent)
			continue
		case ch.ID == 0:
			// An item waits for the items it needs that are not done yet;
			// with all of them done it waits for a free place under parallel.
			open := engine.OpenNeeds(list, ch)
			needs := p.T("status.child.slot")
			if len(open) > 0 {
				needs = p.T("status.child.needs", "items", strings.Join(open, ", "))
			}
			fmt.Fprintf(tw, "%s%s\t\t%s\t%s\t\t\t\n", indent, name, p.T("status.st.pending"), needs)
			printBefore(p, tw, ch, indent)
			continue
		}
		status, detail := childStatus(p, ch.State, c.childDriven(ch.ID))
		switch {
		case ch.KeptFrom > 0:
			status, detail = p.T("status.st.finished"), p.T("status.child.kept", "outcome", ch.Outcome, "n", ch.KeptFrom)
		case ch.Queued:
			// The step runs the child again when its turn comes; the
			// error it stopped with before is its earlier try.
			status, detail = p.T("status.st.waiting"), p.T("status.child.queued", "node", ch.State.Node)
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t\t\t\n", indent, name, p.T("status.child.campaign", "id", ch.ID), status, detail)
		if ch.Queued {
			fmt.Fprintf(tw, "%s  %s\t\t\t%s\t\t\t\n", indent, p.T("status.child.before_resume"), p.T("status.child.stopped_before", "error", ch.State.Error))
		}
		printBefore(p, tw, ch, indent)
		if ch.State.End == "" && ch.State.Running && !ch.Queued {
			c.printChildren(tw, ch.Events, ch.State.Node, indent+"  ", depth+1)
		}
	}
	// An earlier child whose item the latest list does not hold any more.
	for _, ch := range rest {
		name := p.T("status.child.segment")
		if ch.Item != "" {
			name = p.T("status.child.item", "item", ch.Item)
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t\t\t\n", indent, name, p.T("status.child.campaign", "id", ch.ID),
			p.T("status.child.earlier", "n", ch.Attempt), earlierState(p, ch.State))
	}
}

// printBefore lists the earlier attempts of a child under its row,
// indented and marked as earlier, newest first.
func printBefore(p *i18n.Printer, tw io.Writer, ch engine.Child, indent string) {
	for _, b := range ch.Before {
		fmt.Fprintf(tw, "%s  %s\t%s\t\t%s\t\t\t\n", indent, p.T("status.child.earlier_attempt", "n", b.Attempt),
			p.T("status.child.campaign", "id", b.ID), earlierState(p, b.State))
	}
}

// earlierState is how a child of an earlier attempt ended: its end, or the
// node it stopped at.
func earlierState(p *i18n.Printer, st chronicle.State) string {
	if st.End != "" {
		return st.End
	}
	return p.T("status.d.at", "node", st.Node)
}

// notRunWhy says why a for-each item did not run, in the reader's language.
func notRunWhy(p *i18n.Printer, ch engine.Child) string {
	if ch.NotRunNeeds == "" {
		return ch.NotRun
	}
	return p.T("status.child.why"+"_"+ch.NotRunKind, "item", ch.NotRunNeeds)
}

func (c *command) childDriven(id int64) bool {
	alive, _ := engine.Driven(c.o, id)
	return alive
}

// childStatus is the STATUS and OUTCOME cells of a child campaign's row.
func childStatus(p *i18n.Printer, st chronicle.State, alive bool) (string, string) {
	switch {
	case st.End != "":
		return p.T("status.st.finished"), st.End
	case st.Error != "":
		return p.T("status.st.error"), st.Error
	case st.Asking():
		return p.T("status.st.asking"), p.T("status.child.asking", "node", st.Node)
	case st.Halted:
		return p.T("status.st.halted"), p.T("status.child.halted", "node", st.Node)
	case st.LockWait != "" && !st.Running:
		return p.T("status.st.waiting_lock"), p.T("status.child.lock", "lock", st.LockWait, "node", st.Node, "holder", st.LockHolder)
	case st.Waiting != "":
		return p.T("status.st.waiting"), p.T("status.child.waiting", "wait", st.Waiting, "node", st.Node)
	case st.Running && alive:
		return p.T("status.st.running"), p.T("status.child.running", "node", st.Node, "attempt", st.Attempt)
	case st.Running:
		return p.T("status.st.interrupted"), st.Node
	}
	return p.T("status.d.at", "node", st.Node), ""
}

// waiterLine says what holds a campaign up in one of its children.
func waiterLine(p *i18n.Printer, x engine.Waiter) string {
	st := x.State
	at := p.T("status.waiter.at", "path", x.Path, "id", x.Campaign)
	switch {
	case st.Asking():
		return p.T("status.waiter.asking", "at", at, "node", st.Node)
	case st.Error != "":
		return p.T("status.waiter.error", "at", at, "error", st.Error)
	case st.LockWait != "":
		return p.T("status.waiter.lock", "at", at, "node", st.Node, "lock", st.LockWait, "holder", st.LockHolder)
	case st.Waiting == chronicle.WaitOffline:
		return p.T("status.waiter.offline", "at", at, "node", st.Node)
	}
	return p.T("status.waiter.limit", "at", at, "node", st.Node)
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
func printAgent(p *i18n.Printer, w io.Writer, node string, attempts []*agentAttempt, running bool) {
	fmt.Fprintln(w, p.T("status.agent", "node", node))
	for i, a := range attempts {
		var facts []string
		if a.executor != "" {
			facts = append(facts, p.T("status.fact.executor", "executor", a.executor))
		}
		if a.model != "" {
			facts = append(facts, p.T("status.fact.model", "model", a.model))
		}
		if a.effort != "" {
			facts = append(facts, p.T("status.fact.effort", "effort", a.effort))
		}
		var status string
		switch {
		case a.running && running && i == len(attempts)-1:
			status = p.T("status.st.running")
		case a.running || a.interrupted:
			status = p.T("status.st.interrupted")
		case a.failure == chronicle.FailLimit && a.choice != nil:
			status = p.T("status.agent.client_limit", "error", a.err)
		case a.failure == chronicle.FailLimit && a.retryAt != nil:
			status = p.T("status.agent.limit_retry", "at", a.retryAt.Local().Format("2006-01-02 15:04:05"), "error", a.err)
		case a.failure == chronicle.FailLimit || a.failure == chronicle.FailOffline:
			status = p.T("status.agent.waits", "failure", a.failure, "error", a.err)
		case a.failure != "" && a.outcome != "":
			status = p.T("status.agent.failed_outcome", "failure", a.failure, "error", a.err, "outcome", a.outcome)
		case a.failure != "":
			status = p.T("status.agent.failed", "failure", a.failure, "error", a.err)
		case a.asked > 0:
			status = p.T("status.agent.asked", "n", a.asked)
		default:
			status = p.T("status.agent.outcome", "outcome", a.outcome)
		}
		fmt.Fprintln(w, p.T("status.agent.attempt", "n", a.n, "status", status, "facts", strings.Join(facts, ", ")))
		if c := a.choice; c != nil {
			fmt.Fprintln(w, p.T("status.agent.client", "client", explain.ClientName(c, c.Client), "role", c.Role, "mode", c.Mode, "why", explain.Why(p, c)))
			if c.From > 0 {
				fmt.Fprintln(w, p.T("status.agent.switched", "from", explain.ClientName(c, c.From)))
			}
			if c.Reason == chronicle.ChoseQuota {
				fmt.Fprintln(w, p.T("status.agent.compared", "usage", explain.UsageWords(p, c, time.Local)))
			}
		}
		for _, d := range a.denied {
			fmt.Fprintln(w, p.T("status.agent.denied", "call", d))
		}
		if a.dropped != "" {
			fmt.Fprintln(w, p.T("status.agent.dropped", "field", orders.UnableField, "outcome", a.outcome, "reason", a.dropped))
		}
		if a.prompt != "" {
			fmt.Fprintln(w, p.T("status.agent.prompt", "file", a.prompt))
		}
		if a.sessionIn != "" {
			fmt.Fprintln(w, p.T("status.agent.continued", "session", a.sessionIn))
		}
		if a.sessionOut != "" {
			fmt.Fprintln(w, p.T("status.agent.session", "session", a.sessionOut))
		}
		if a.output != "" {
			out := a.output
			if len(out) > 300 {
				out = out[:300] + "..."
			}
			fmt.Fprintln(w, p.T("status.agent.output", "output", out))
		}
	}
}

// checkLine renders the TYPE to TIME cells of a check row.
func checkLine(p *i18n.Printer, results map[string]chronicle.Event, name string, def *orders.Check) string {
	kind := p.T("status.kind.required")
	if def != nil && def.Soft {
		kind = p.T("status.kind.soft")
	}
	e, ok := results[name]
	dur := (time.Duration(e.Millis) * time.Millisecond).String()
	finished := p.T("status.st.finished")
	switch {
	case !ok:
		return kind + "\t" + p.T("status.st.not_run") + "\t\t\t"
	case e.Error != "":
		return kind + "\t" + p.T("status.st.error") + "\t" + e.Error + "\t\t" + dur
	case e.Outcome == orders.OK:
		return kind + "\t" + finished + "\tok\t\t" + dur
	case e.Soft:
		return kind + "\t" + finished + "\t" + p.T("status.check_warning", "outcome", e.Outcome) + "\t\t" + dur
	default:
		return kind + "\t" + finished + "\t" + p.T("status.check_blocked", "outcome", e.Outcome) + "\t\t" + dur
	}
}

// describe summarises the campaign state. Whether a process is driving the
// campaign is learned by trying its lock.
func (c *command) describe(id int64, st chronicle.State) string {
	p := c.p
	switch {
	case st.End != "":
		return st.End
	case st.Error != "":
		if events, err := c.chron.Events(id); err == nil && chronicle.StopHint(events) == chronicle.HintRetry {
			return p.T("status.d.error_retry", "error", st.Error)
		}
		return p.T("status.d.error", "error", st.Error)
	}
	alive, _ := engine.Driven(c.o, id)
	if st.LockWait != "" && !st.Running && !st.Halted {
		if alive {
			return p.T("status.d.lock", "node", st.Node, "lock", st.LockWait, "holder", st.LockHolder)
		}
		return p.T("status.d.lock_undriven", "node", st.Node, "lock", st.LockWait)
	}
	if st.Asking() {
		if alive {
			return p.T("status.d.asking_driven", "node", st.Node)
		}
		return p.T("status.d.asking", "node", st.Node)
	}
	if st.Ask != nil && !alive {
		return p.T("status.d.answered", "node", st.Node)
	}
	where := p.T("status.d.at", "node", st.Node)
	if st.Running {
		where = p.T("status.d.running", "node", st.Node, "attempt", st.Attempt)
	}
	switch {
	case st.Halted:
		return p.T("status.d.halted", "node", st.Node)
	case st.Waiting == chronicle.WaitLimit && !st.WaitUntil.IsZero():
		until := st.WaitUntil.Local().Format("2006-01-02 15:04:05")
		if alive {
			return p.T("status.d.limit_wait", "node", st.Node, "until", until)
		}
		return p.T("status.d.limit_wait_undriven", "node", st.Node, "until", until)
	case st.Waiting == chronicle.WaitLimit:
		return p.T("status.d.limit", "node", st.Node)
	case st.Waiting == chronicle.WaitOffline:
		return p.T("status.d.offline", "node", st.Node)
	case st.HaltRequested && alive:
		return p.T("status.d.halt_requested", "where", where)
	case alive:
		return where
	case st.Running:
		return p.T("status.d.interrupted", "where", where)
	default:
		return p.T("status.d.undriven", "where", where)
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
	if e.Role != "" {
		parts = append(parts, "role="+e.Role)
	}
	if c := e.Choice; c != nil {
		parts = append(parts, fmt.Sprintf("role=%s select=%s client=%d reason=%s", c.Role, c.Mode, c.Client, c.Reason))
		if c.From > 0 {
			parts = append(parts, fmt.Sprintf("from=%d", c.From))
		}
		u, _ := json.Marshal(c.Usage)
		parts = append(parts, "usage="+string(u))
	}
	if e.WaitUntil != nil {
		parts = append(parts, "wait_until="+e.WaitUntil.Local().Format(time.RFC3339))
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
	if e.Dropped != "" {
		parts = append(parts, fmt.Sprintf("dropped=%q", e.Dropped))
	}
	if e.RetryAt != nil {
		parts = append(parts, "retry_at="+e.RetryAt.Local().Format(time.RFC3339))
	}
	if len(e.Input) > 0 {
		parts = append(parts, "input="+string(e.Input))
	}
	if e.Language != "" {
		parts = append(parts, "language="+e.Language)
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
