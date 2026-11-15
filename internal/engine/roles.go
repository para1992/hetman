package engine

import (
	"context"
	"sort"
	"sync"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// runner is who runs an attempt of an agent step: an executor with a model
// and an effort, and, for a step run through a role, how it was chosen.
type runner struct {
	x      *orders.Executor
	model  string
	effort string
	choice *chronicle.Choice // nil for a node that names its executor
}

// clientKey identifies a client: a conversation belongs to one.
func clientKey(executor, model, effort string) string {
	return executor + "\x00" + model + "\x00" + effort
}

// measureFor is how long a usage measured by an adapter is used before the
// adapter is asked again.
const measureFor = time.Minute

// measured is the usage an adapter reported, and when.
type measured struct {
	at    time.Time
	usage *adapter.Usage // nil when the adapter could not tell
}

// roleState is shared by the campaigns of a tree: choices of a role are
// made one at a time, so that round robin turns are not taken twice by
// children that run at the same time.
type roleState struct {
	mu       sync.Mutex
	measured map[string]measured // by executor
}

// roles returns the role state of the tree the campaign belongs to.
func (e *Engine) roles() *roleState {
	t := e.top()
	t.rolesOnce.Do(func() { t.roleState = &roleState{measured: map[string]measured{}} })
	return t.roleState
}

// runner chooses who runs the next attempt of an agent node. For a node
// that names an executor that is the executor. For a node run through a
// role, it is a client of the role; when none can run, a no_client event
// is recorded and the runner is nil. The returned function must be called
// once the attempt's step_started is recorded: until then other campaigns
// of the tree wait to choose for a role.
func (e *Engine) runner(ctx context.Context, node *orders.Node, events []chronicle.Event) (*runner, func(), error) {
	if node.Role == "" {
		return &runner{x: e.Orders.Executors[node.Executor], model: node.Model, effort: node.Effort}, func() {}, nil
	}
	rs := e.roles()
	rs.mu.Lock()
	var once sync.Once
	unlock := func() { once.Do(rs.mu.Unlock) }
	role := e.Orders.Roles[node.Role]
	choice, err := e.choose(ctx, node, role, events)
	if err != nil {
		unlock()
		return nil, unlock, err
	}
	if choice.Client == 0 {
		defer unlock()
		ev := chronicle.Event{Kind: chronicle.NoClient, Node: node.ID, Details: chronicle.Details{Choice: choice}}
		for _, u := range choice.Usage {
			if u.Until != nil && (ev.WaitUntil == nil || u.Until.Before(*ev.WaitUntil)) {
				t := *u.Until
				ev.WaitUntil = &t
			}
		}
		if ev.WaitUntil != nil {
			e.say("engine.no_client_until", "node", node.ID, "role", role.Name, "until", ev.WaitUntil.Local().Format("2006-01-02 15:04:05"))
		} else {
			e.say("engine.no_client", "node", node.ID, "role", role.Name)
		}
		return nil, unlock, e.record(ev)
	}
	c := role.Clients[choice.Client-1]
	if choice.From > 0 {
		e.say("engine.client_switched", "node", node.ID, "role", role.Name, "client", role.Clients[choice.From-1].Label(choice.From-1),
			"to", c.Label(choice.Client-1), "reason", e.Text.T("engine.chose."+choice.Reason))
	} else {
		e.say("engine.client_chosen", "node", node.ID, "role", role.Name, "client", c.Label(choice.Client-1), "reason", e.Text.T("engine.chose."+choice.Reason))
	}
	return &runner{x: e.Orders.Executors[c.Executor], model: c.Model, effort: c.Effort, choice: choice}, unlock, nil
}

// choose picks the client of a role for the next attempt of a node.
//
// A client cannot run when its last attempt, in any campaign of the
// folder, hit a limit that resets later; when an attempt in this visit of
// the step hit a limit with no reset time (until a person resumes); and,
// in the quota mode, when its share of a window is used up, until that
// window resets. Among the clients that can run, the client that ran the
// step's cut-off attempt or asked its questions continues; otherwise the
// mode decides: fixed takes the first in the list, round_robin the next
// after the client chosen last for the role in the campaign and its
// children, quota the one with the smallest share (see byShare).
func (e *Engine) choose(ctx context.Context, node *orders.Node, role *orders.Role, events []chronicle.Event) (*chronicle.Choice, error) {
	now := e.clock().Now()
	seen, err := attemptsSeen(e.Chron, now)
	if err != nil {
		return nil, err
	}
	visit := events[visitStart(events, node.ID):]
	choice := &chronicle.Choice{Role: role.Name, Mode: role.Select}
	quota := role.Select == orders.SelectQuota
	for i, c := range role.Clients {
		var m *measured
		if quota {
			m = e.measure(ctx, c.Executor, now)
		}
		choice.Usage = append(choice.Usage, clientUsage(i, c, seen[c.Executor], m, now, quota))
	}
	stuck := stuckInVisit(visit)
	for i, c := range role.Clients {
		if u := &choice.Usage[i]; !u.Exhausted && stuck[c.Executor] {
			u.Exhausted, u.Until, u.Why = true, nil, chronicle.SourceLimit
		}
	}
	can := func(i int) bool { return !choice.Usage[i].Exhausted }

	// The client of the attempt that was cut off continues, and one whose
	// limit made the step stop is named as the client switched from.
	last, cut, limited := lastAttempt(visit, node.ID)
	if last != nil {
		for i, c := range role.Clients {
			if clientKey(c.Executor, c.Model, c.Effort) != clientKey(last.Executor, last.Model, last.Effort) {
				continue
			}
			if cut && can(i) {
				choice.Client, choice.Reason = i+1, chronicle.ChoseResume
				return choice, nil
			}
			if limited {
				choice.From = i + 1
			}
			break
		}
	}
	order := make([]int, 0, len(role.Clients))
	switch role.Select {
	case orders.SelectRoundRobin:
		choice.Reason = chronicle.ChoseTurn
		next := 0
		if prev, err := e.lastChoice(role.Name); err != nil {
			return nil, err
		} else if prev != nil {
			next = prev.Client % len(role.Clients)
		}
		for k := range role.Clients {
			order = append(order, (next+k)%len(role.Clients))
		}
	case orders.SelectQuota:
		choice.Reason = chronicle.ChoseQuota
		for i := range role.Clients {
			order = append(order, i)
		}
		sort.SliceStable(order, func(a, b int) bool { return byShare(choice.Usage[order[a]], choice.Usage[order[b]]) })
	default:
		choice.Reason = chronicle.ChoseFixed
		for i := range role.Clients {
			order = append(order, i)
		}
	}
	for _, i := range order {
		if can(i) {
			choice.Client = i + 1
			return choice, nil
		}
	}
	choice.Reason = chronicle.ChoseNone
	return choice, nil
}

// lastAttempt is the step_started of the latest attempt of the node in its
// visit. cut tells whether that attempt was cut off (the engine stopped, a
// limit, lost connectivity) or asked the person questions, so that its
// conversation would continue; limited whether it ended at a limit.
func lastAttempt(visit []chronicle.Event, node string) (last *chronicle.Event, cut, limited bool) {
	for i := range visit {
		ev := &visit[i]
		if ev.Node != node {
			continue
		}
		switch ev.Kind {
		case chronicle.StepStarted:
			last, cut, limited = ev, true, false
		case chronicle.StepFinished:
			cut = ev.Failure == chronicle.FailLimit || ev.Failure == chronicle.FailOffline
			limited = ev.Failure == chronicle.FailLimit
		case chronicle.Asked:
			cut = true
		}
	}
	return last, cut, limited
}

// stuckInVisit lists the executors whose last attempt in this visit hit a
// limit with no reset time, since the campaign was last resumed: they are
// not tried again until a person resumes.
func stuckInVisit(visit []chronicle.Event) map[string]bool {
	out := map[string]bool{}
	for _, ev := range visit {
		switch {
		case ev.Kind == chronicle.Resumed:
			out = map[string]bool{}
		case ev.Kind == chronicle.StepFinished && ev.Executor != "":
			out[ev.Executor] = ev.Failure == chronicle.FailLimit && ev.RetryAt == nil
		}
	}
	return out
}

// lastChoice is the latest choice for a role in the campaign tree this
// campaign belongs to, or nil.
func (e *Engine) lastChoice(role string) (*chronicle.Choice, error) {
	tree, err := e.Chron.Tree(e.top().Campaign)
	if err != nil {
		return nil, err
	}
	starts, err := e.Chron.OfKinds(chronicle.StepStarted)
	if err != nil {
		return nil, err
	}
	for i := len(starts) - 1; i >= 0; i-- {
		if c := starts[i].Choice; c != nil && c.Role == role && tree[starts[i].Campaign] {
			return c, nil
		}
	}
	return nil, nil
}

// measure returns the usage the adapter of an executor reports, asking it
// again when what it said last is older than measureFor; nil when the
// adapter does not declare the usage feature or could not tell.
func (e *Engine) measure(ctx context.Context, executor string, now time.Time) *measured {
	rs := e.roles()
	if m, ok := rs.measured[executor]; ok && now.Sub(m.at) < measureFor && !now.Before(m.at) {
		return &m
	}
	x := e.Orders.Executors[executor]
	desc, err := e.describe(ctx, x)
	if err != nil || !desc.Has(adapter.FeatureUsage) {
		return nil
	}
	m := measured{at: now}
	argv := append([]string{e.Orders.Program(x.Run[0])}, x.Run[1:]...)
	env, err := e.env("", "", 0, "")
	if err == nil {
		var out []byte
		out, err = query(ctx, argv, append([]string{adapter.UsageFlag}, adapter.UsageWindows...), e.Orders.Dir, env)
		if err == nil {
			var u adapter.Usage
			if u, err = adapter.ParseUsage(out, adapter.UsageWindows); err == nil {
				m.usage = &u
			}
		}
	}
	if err != nil {
		e.say("engine.usage_failed", "executor", executor, "error", err)
	}
	rs.measured[executor] = m
	return &m
}

// seenAttempt is a finished attempt of an agent step, as the engine saw it.
type seenAttempt struct {
	at      time.Time // when it finished
	millis  int64
	failure string
	retryAt *time.Time
}

// attemptsSeen reads, per executor, the finished attempts of agent steps of
// every campaign of the folder in the longest window, oldest first.
func attemptsSeen(ch *chronicle.Chronicle, now time.Time) (map[string][]seenAttempt, error) {
	list, err := ch.OfKinds(chronicle.StepFinished)
	if err != nil {
		return nil, err
	}
	since := now.Add(-orders.WindowLength(orders.WindowWeekly))
	out := map[string][]seenAttempt{}
	for _, ev := range list {
		if ev.Executor == "" || ev.At.Before(since) {
			continue
		}
		out[ev.Executor] = append(out[ev.Executor], seenAttempt{at: ev.At, millis: ev.Millis, failure: ev.Failure, retryAt: ev.RetryAt})
	}
	for _, l := range out {
		sort.SliceStable(l, func(i, j int) bool { return l[i].at.Before(l[j].at) })
	}
	return out, nil
}

// clientUsage puts together what is known of one client: the windows (in
// the quota mode) and whether it can run. A window's share is measured when
// the adapter reported it, else estimated from the attempts seen when the
// role states an allowance for it, else unknown. When the client's last
// attempt hit a limit that resets later, the window the reset falls in is
// used up until then, whatever the other sources say.
func clientUsage(i int, c *orders.Client, seen []seenAttempt, m *measured, now time.Time, quota bool) chronicle.ClientUsage {
	u := chronicle.ClientUsage{Client: i + 1, Executor: c.Executor, Model: c.Model}
	var limitUntil *time.Time
	limitWindow := orders.WindowFiveHour
	if n := len(seen); n > 0 {
		if l := seen[n-1]; l.failure == chronicle.FailLimit && l.retryAt != nil && l.retryAt.After(now) {
			t := *l.retryAt
			limitUntil = &t
			if t.Sub(l.at) > orders.WindowLength(orders.WindowFiveHour) {
				limitWindow = orders.WindowWeekly
			}
		}
	}
	if quota {
		for _, name := range orders.Windows {
			u.Windows = append(u.Windows, window(name, c, seen, m, now))
		}
	}
	if limitUntil != nil {
		u.Exhausted, u.Until, u.Why = true, limitUntil, chronicle.SourceLimit
		for k := range u.Windows {
			if w := &u.Windows[k]; w.Name == limitWindow {
				one := 1.0
				w.Source, w.Used, w.ResetsAt, w.At, w.Allowance = chronicle.SourceLimit, &one, limitUntil, nil, ""
			}
		}
		return u
	}
	for _, w := range u.Windows {
		if w.Used == nil || *w.Used < 1 || w.ResetsAt != nil && !w.ResetsAt.After(now) {
			continue // room left, or a report older than the reset it names
		}
		// Used up: the client runs again once every used-up window has
		// reset; with one whose reset is not known, nobody can say when.
		switch {
		case !u.Exhausted:
			u.Exhausted, u.Until, u.Why = true, w.ResetsAt, w.Source
		case u.Until != nil && (w.ResetsAt == nil || w.ResetsAt.After(*u.Until)):
			u.Until = w.ResetsAt
		}
	}
	return u
}

// window works out the usage of one window of a client.
func window(name string, c *orders.Client, seen []seenAttempt, m *measured, now time.Time) chronicle.WindowUsage {
	w := chronicle.WindowUsage{Name: name, Source: chronicle.SourceUnknown}
	length := orders.WindowLength(name)
	var in []seenAttempt
	for _, a := range seen {
		if a.at.After(now.Add(-length)) && a.failure != chronicle.FailLimit && a.failure != chronicle.FailOffline {
			in = append(in, a)
			w.Attempts++
			w.Millis += a.millis
		}
	}
	if m != nil && m.usage != nil {
		for _, mw := range m.usage.Windows {
			if mw.Name != name {
				continue
			}
			used, at := mw.Used, m.at
			w.Source, w.Used, w.At = chronicle.SourceMeasured, &used, &at
			if t := mw.ResetTime(); !t.IsZero() {
				t = t.UTC()
				w.ResetsAt = &t
			}
			return w
		}
	}
	a, ok := c.Allowance[name]
	if !ok {
		return w
	}
	w.Source, w.Allowance = chronicle.SourceEstimated, a.Text
	share := func(list []seenAttempt) float64 {
		if a.Work > 0 {
			var ms int64
			for _, x := range list {
				ms += x.millis
			}
			return float64(ms) / float64(a.Work.Milliseconds())
		}
		return float64(len(list)) / a.Attempts
	}
	used := share(in)
	w.Used = &used
	if used >= 1 {
		// The window rolls: it is no longer used up once enough of the
		// oldest attempts have left it.
		for k := range in {
			if share(in[k+1:]) < 1 {
				t := in[k].at.Add(length).UTC()
				w.ResetsAt = &t
				break
			}
		}
	}
	return w
}

// byShare orders two clients for the quota mode. Each client's known
// shares (measured, estimated or from a limit; a window with no known share
// is left out) give two numbers: worst, the larger share, and other, the
// smaller (both the same when one window is known). The client with the
// smaller worst share comes first; on a tie the smaller other share; on a
// tie again the client listed first (the sort is stable). A client with no
// known share in either window comes after every client with one.
func byShare(a, b chronicle.ClientUsage) bool {
	aw, ao, aok := shares(a)
	bw, bo, bok := shares(b)
	switch {
	case aok != bok:
		return aok
	case !aok:
		return false
	case aw != bw:
		return aw < bw
	}
	return ao < bo
}

// shares returns the larger and the smaller known share of a client.
func shares(u chronicle.ClientUsage) (worst, other float64, ok bool) {
	for _, w := range u.Windows {
		if w.Used == nil {
			continue
		}
		v := *w.Used
		if !ok {
			worst, other, ok = v, v, true
			continue
		}
		worst, other = max(worst, v), min(other, v)
	}
	return worst, other, ok
}
