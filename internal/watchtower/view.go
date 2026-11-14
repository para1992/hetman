package watchtower

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/orders"
)

// Campaign states shown on the page.
const (
	StateRunning     = "running"
	StateHalted      = "halted"
	StateDone        = "done"
	StateFailed      = "failed"
	StateError       = "error"
	StateInterrupted = "interrupted"
)

// Step, check and tile statuses shown on the page.
const (
	StatusPassed      = "passed"
	StatusRunning     = "running"
	StatusWaiting     = "waiting"
	StatusFailed      = "failed"
	StatusWarning     = "warning" // a soft check failed
	StatusNotRun      = "notrun"
	StatusInterrupted = "interrupted"
)

// Summary is one row of the campaign list.
type Summary struct {
	ID            int64      `json:"id"`
	Flow          string     `json:"flow"`
	State         string     `json:"state"`
	Node          string     `json:"node"` // current position
	HaltRequested bool       `json:"haltRequested"`
	Error         string     `json:"error,omitempty"`
	Started       time.Time  `json:"started"`
	Ended         *time.Time `json:"ended,omitempty"` // last event once the campaign is not running
	Marks         []Mark     `json:"marks"`
}

// Mark is the status of one node in the list's mini pipeline.
type Mark struct {
	Node     string `json:"node"`
	Status   string `json:"status"`
	Attempts int    `json:"attempts"`
	Current  bool   `json:"current"`
	Side     bool   `json:"side"` // off the main line
}

// Campaign is everything the campaign view shows.
type Campaign struct {
	Summary
	Orders        string               `json:"orders"`
	OrdersProblem string               `json:"ordersProblem,omitempty"`
	End           string               `json:"end,omitempty"` // done or failed once ended
	Columns       []Column             `json:"columns"`
	Nodes         map[string][]Attempt `json:"nodes"` // attempt history per node
	LastSeq       int64                `json:"lastSeq"`
}

// Column is one node of the pipeline. Main-line columns carry the nodes off
// the main line that are drawn under them in Side.
type Column struct {
	Node     string   `json:"node"`
	Type     string   `json:"type"`
	Known    bool     `json:"known"` // the node exists in the current orders
	Status   string   `json:"status"`
	Attempts int      `json:"attempts"`
	Current  bool     `json:"current"`
	Tiles    []Tile   `json:"tiles"`
	Exits    []Exit   `json:"exits"`
	Side     []Column `json:"side,omitempty"`
}

// Tile is one check of a gate, or the single command of a step.
type Tile struct {
	Check    string `json:"check,omitempty"`
	Soft     bool   `json:"soft,omitempty"`
	Status   string `json:"status"`
	Outcome  string `json:"outcome,omitempty"`
	Attempts int    `json:"attempts"`
}

// Exit is one transition out of a node.
type Exit struct {
	Outcome string `json:"outcome"`
	To      string `json:"to"`
	Taken   int    `json:"taken"` // how many times the campaign took it
	Last    bool   `json:"last"`  // the most recent transition out of the node
	Main    bool   `json:"main"`  // it is the link to the next main-line column
}

// Attempt is one execution of a node.
type Attempt struct {
	N          int        `json:"n"`
	Status     string     `json:"status"`
	Outcome    string     `json:"outcome,omitempty"`
	Error      string     `json:"error,omitempty"`
	Started    time.Time  `json:"started"`
	Finished   *time.Time `json:"finished,omitempty"`
	Ms         int64      `json:"ms"`
	Exit       *int       `json:"exit,omitempty"`
	Log        string     `json:"log,omitempty"`
	Result     string     `json:"result,omitempty"` // present only if the file exists
	Artifacts  []string   `json:"artifacts,omitempty"`
	Transition string     `json:"transition,omitempty"`
	Cleanup    []Cleanup  `json:"cleanup,omitempty"`
	Checks     []CheckRun `json:"checks,omitempty"`
}

// CheckRun is one execution of a gate check within an attempt.
type CheckRun struct {
	Check     string     `json:"check"`
	Soft      bool       `json:"soft"`
	Status    string     `json:"status"`
	Outcome   string     `json:"outcome,omitempty"`
	Error     string     `json:"error,omitempty"`
	Started   *time.Time `json:"started,omitempty"`
	Finished  *time.Time `json:"finished,omitempty"`
	Ms        int64      `json:"ms"`
	Exit      *int       `json:"exit,omitempty"`
	Log       string     `json:"log,omitempty"`
	Result    string     `json:"result,omitempty"`
	Artifacts []string   `json:"artifacts,omitempty"`
}

// Cleanup is what was done with a process left by an interrupted attempt.
type Cleanup struct {
	At      time.Time `json:"at"`
	Check   string    `json:"check,omitempty"`
	Outcome string    `json:"outcome"`
	Pid     int       `json:"pid,omitempty"`
	Ident   string    `json:"ident,omitempty"`
}

// Event is a chronicle event as the page receives it.
type Event struct {
	Seq       int64     `json:"seq"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	Node      string    `json:"node,omitempty"`
	Check     string    `json:"check,omitempty"`
	Attempt   int       `json:"attempt,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	To        string    `json:"to,omitempty"`
	Error     string    `json:"error,omitempty"`
	Exit      *int      `json:"exit,omitempty"`
	Ms        int64     `json:"ms,omitempty"`
	Log       string    `json:"log,omitempty"`
	Artifacts []string  `json:"artifacts,omitempty"`
	Soft      bool      `json:"soft,omitempty"`
	Pid       int       `json:"pid,omitempty"`
	Ident     string    `json:"ident,omitempty"`
}

func eventsJSON(events []chronicle.Event) []Event {
	out := make([]Event, len(events))
	for i, e := range events {
		out[i] = Event{Seq: e.Seq, At: e.At, Kind: e.Kind, Node: e.Node, Check: e.Check, Attempt: e.Attempt,
			Outcome: e.Outcome, To: e.To, Error: e.Error, Exit: e.Exit, Ms: e.Millis, Log: e.Log,
			Artifacts: e.Artifacts, Soft: e.Soft, Pid: e.Pid, Ident: e.Ident}
	}
	return out
}

// build derives the campaign view from its events. o may be nil when the
// orders cannot be read; driven tells whether a process holds the lock.
func build(k chronicle.Campaign, events []chronicle.Event, o *orders.Orders, campaignDir string, driven func() bool) *Campaign {
	st := chronicle.Replay(events)
	c := &Campaign{Summary: Summary{ID: k.ID, Flow: k.Flow, Node: st.Node, HaltRequested: st.HaltRequested && !st.Halted,
		Error: st.Error, Started: k.Started}, Orders: k.Orders, End: st.End, Nodes: map[string][]Attempt{}}
	switch {
	case st.End == orders.Done:
		c.State = StateDone
	case st.End == orders.Failed:
		c.State = StateFailed
	case st.Error != "":
		c.State = StateError
	case st.Halted:
		c.State = StateHalted
	case driven():
		c.State = StateRunning
	default:
		c.State = StateInterrupted
	}
	if n := len(events); n > 0 {
		c.LastSeq = events[n-1].Seq
		if c.State != StateRunning {
			at := events[n-1].At
			c.Ended = &at
		}
	}
	live := c.State == StateRunning

	// Attempt history per node, in chronicle order.
	soft := map[string]bool{}
	if o != nil {
		for name, ch := range o.Checks {
			soft[name] = ch.Soft
		}
	}
	var seen []string
	cur := map[string]*Attempt{}
	transitions := map[string][]chronicle.Event{}
	for _, e := range events {
		switch e.Kind {
		case chronicle.StepStarted:
			if _, ok := c.Nodes[e.Node]; !ok {
				seen = append(seen, e.Node)
			}
			c.Nodes[e.Node] = append(c.Nodes[e.Node], Attempt{N: e.Attempt, Status: StatusRunning, Started: e.At,
				Log: logIfScript(o, e.Node, e.Attempt)})
			list := c.Nodes[e.Node]
			cur[e.Node] = &list[len(list)-1]
		case chronicle.StepInterrupted:
			if a := cur[e.Node]; a != nil && a.N == e.Attempt && a.Status == StatusRunning {
				a.Status = StatusInterrupted
				at := e.At
				a.Finished = &at
			}
		case chronicle.ProcessCleanup:
			if a := find(c.Nodes[e.Node], e.Attempt); a != nil {
				a.Cleanup = append(a.Cleanup, Cleanup{At: e.At, Check: e.Check, Outcome: e.Outcome, Pid: e.Pid, Ident: e.Ident})
			}
		case chronicle.CheckFinished:
			if a := find(c.Nodes[e.Node], e.Attempt); a != nil {
				fin := e.At
				began := e.At.Add(-time.Duration(e.Millis) * time.Millisecond)
				r := CheckRun{Check: e.Check, Soft: e.Soft, Outcome: e.Outcome, Error: e.Error, Started: &began,
					Finished: &fin, Ms: e.Millis, Exit: e.Exit, Log: e.Log, Artifacts: e.Artifacts,
					Result: existing(campaignDir, engine.ResultFile(e.Node, e.Check, e.Attempt))}
				switch {
				case e.Error != "":
					r.Status = StatusFailed
				case e.Outcome == orders.OK:
					r.Status = StatusPassed
				case e.Soft:
					r.Status = StatusWarning
				default:
					r.Status = StatusFailed
				}
				a.Checks = append(a.Checks, r)
			}
		case chronicle.StepFinished:
			if a := find(c.Nodes[e.Node], e.Attempt); a != nil {
				fin := e.At
				a.Finished, a.Outcome, a.Error, a.Ms, a.Exit, a.Artifacts = &fin, e.Outcome, e.Error, e.Millis, e.Exit, e.Artifacts
				if e.Log != "" {
					a.Log = e.Log
				}
				a.Status = finishedStatus(e.Outcome, e.Error)
				if a.Status == StatusPassed {
					for _, ch := range a.Checks {
						if ch.Status == StatusWarning {
							a.Status = StatusWarning
						}
					}
				}
				if len(a.Checks) == 0 {
					a.Result = existing(campaignDir, engine.ResultFile(e.Node, "", e.Attempt))
				}
			}
		case chronicle.Transition:
			transitions[e.Node] = append(transitions[e.Node], e)
			if list := c.Nodes[e.Node]; len(list) > 0 {
				list[len(list)-1].Transition = e.To
			}
		}
	}
	// An attempt still marked running is only running while a process
	// drives the campaign; otherwise its driver died during the step.
	for _, list := range c.Nodes {
		for i := range list {
			if list[i].Status == StatusRunning && (!live || i < len(list)-1) {
				list[i].Status = StatusInterrupted
			}
		}
	}
	// Checks of a gate that were not reached in an attempt.
	for node, list := range c.Nodes {
		def := nodeDef(o, node)
		if def == nil || len(def.Checks) == 0 {
			continue
		}
		for i := range list {
			list[i].Checks = fillChecks(list[i].Checks, def.Checks, soft, node, list[i].N, list[i].Started, list[i].Status)
		}
	}

	c.Columns = layout(o, st, transitions, seen)
	for i := range c.Columns {
		fill(&c.Columns[i], c, st, soft)
		for j := range c.Columns[i].Side {
			fill(&c.Columns[i].Side[j], c, st, soft)
		}
	}
	for _, col := range c.Columns {
		c.Marks = append(c.Marks, Mark{Node: col.Node, Status: col.Status, Attempts: col.Attempts, Current: col.Current})
		for _, s := range col.Side {
			if s.Attempts > 0 || s.Current {
				c.Marks = append(c.Marks, Mark{Node: s.Node, Status: s.Status, Attempts: s.Attempts, Current: s.Current, Side: true})
			}
		}
	}
	return c
}

func find(list []Attempt, n int) *Attempt {
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].N == n {
			return &list[i]
		}
	}
	return nil
}

func finishedStatus(outcome, errMsg string) string {
	switch {
	case errMsg != "":
		return StatusFailed
	case outcome == orders.Fail || outcome == orders.Timeout || outcome == orders.Blocked:
		return StatusFailed
	default:
		return StatusPassed
	}
}

func nodeDef(o *orders.Orders, node string) *orders.Node {
	if o == nil {
		return nil
	}
	return o.Nodes[node]
}

// logIfScript is the log of a step that runs a single command, known before
// the step finishes so that it can be followed live.
func logIfScript(o *orders.Orders, node string, attempt int) string {
	if def := nodeDef(o, node); def != nil && len(def.Checks) > 0 {
		return ""
	}
	return engine.LogFile(node, "", attempt)
}

func existing(campaignDir, rel string) string {
	if _, err := os.Stat(filepath.Join(campaignDir, filepath.FromSlash(rel))); err == nil {
		return rel
	}
	return ""
}

// fillChecks puts the checks of a gate attempt in the order of the gate and
// adds those without a result: the next one is running while the attempt
// runs, the rest wait; after an attempt that stopped early they did not run.
func fillChecks(done []CheckRun, names []string, soft map[string]bool, node string, attempt int, began time.Time, attemptStatus string) []CheckRun {
	byName := map[string]CheckRun{}
	for _, r := range done {
		byName[r.Check] = r
	}
	out := make([]CheckRun, 0, len(names))
	next := true
	for _, name := range names {
		if r, ok := byName[name]; ok {
			out = append(out, r)
			if r.Finished != nil {
				began = *r.Finished
			}
			continue
		}
		r := CheckRun{Check: name, Soft: soft[name], Status: StatusNotRun}
		switch attemptStatus {
		case StatusRunning:
			r.Status = StatusWaiting
			if next {
				r.Status, r.Log = StatusRunning, engine.LogFile(node, name, attempt)
				at := began
				r.Started = &at
			}
		case StatusInterrupted:
			if next {
				r.Status, r.Log = StatusInterrupted, engine.LogFile(node, name, attempt)
			}
		}
		next = false
		out = append(out, r)
	}
	return out
}

// fill sets the status and tiles of a column from the attempt history.
func fill(col *Column, c *Campaign, st chronicle.State, soft map[string]bool) {
	list := c.Nodes[col.Node]
	col.Attempts = len(list)
	col.Current = col.Node == st.Node && st.End == ""
	switch {
	case len(list) > 0:
		col.Status = list[len(list)-1].Status
		if col.Current && col.Status != StatusRunning && !st.HasOutcome && c.State != StateError &&
			list[len(list)-1].Transition != "" {
			// The node is the position again after a return for rework.
			col.Status = StatusWaiting
		}
	case col.Current:
		col.Status = StatusWaiting
	default:
		col.Status = StatusNotRun
	}
	if len(col.Tiles) == 0 {
		col.Tiles = []Tile{{Status: col.Status, Attempts: col.Attempts}}
		if len(list) > 0 {
			col.Tiles[0].Outcome = list[len(list)-1].Outcome
		}
		return
	}
	var last []CheckRun
	if len(list) > 0 && col.Status != StatusWaiting {
		last = list[len(list)-1].Checks
	}
	for i := range col.Tiles {
		t := &col.Tiles[i]
		t.Soft = soft[t.Check]
		t.Status = col.Status
		for _, r := range last {
			if r.Check == t.Check {
				t.Status, t.Outcome = r.Status, r.Outcome
			}
		}
		for _, a := range list {
			for _, r := range a.Checks {
				if r.Check == t.Check && r.Status != StatusNotRun && r.Status != StatusWaiting {
					t.Attempts++
				}
			}
		}
	}
}

// outcomeOrder lists the outcomes of a node: the success outcomes first,
// then the rest by name, so that the layout is the same on every run.
func outcomeOrder(on map[string]string) []string {
	var rest []string
	var out []string
	for _, first := range []string{orders.OK, orders.Pass} {
		if _, ok := on[first]; ok {
			out = append(out, first)
		}
	}
	for k := range on {
		if k != orders.OK && k != orders.Pass {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// layout places the nodes. The main line starts at the start node; from each
// node it follows the transition the campaign took last, unless that leads
// back to a node already on the line, in which case (and for nodes not run
// yet) it follows the first outcome in outcomeOrder whose target is not on
// the line yet. It stops at a transition to an end. Every other node is drawn
// under the main-line column it is first reached from, searching breadth
// first from the main line in order; nodes not reachable at all, and nodes
// known only from the chronicle, go under the last column.
func layout(o *orders.Orders, st chronicle.State, transitions map[string][]chronicle.Event, seen []string) []Column {
	if o == nil {
		// Without orders only the nodes that ran can be shown, in the order
		// they first ran.
		var cols []Column
		for _, n := range seen {
			cols = append(cols, column(nil, n, transitions, ""))
		}
		return cols
	}
	onLine := map[string]bool{}
	var line []string
	for n := o.Start; n != "" && !onLine[n] && o.Nodes[n] != nil; {
		onLine[n] = true
		line = append(line, n)
		def := o.Nodes[n]
		next := ""
		if ts := transitions[n]; len(ts) > 0 {
			to := ts[len(ts)-1].To
			if orders.IsEnd(to) {
				break
			}
			if !onLine[to] && o.Nodes[to] != nil {
				next = to
			}
		}
		if next == "" {
			for _, out := range outcomeOrder(def.On) {
				to := def.On[out]
				if orders.IsEnd(to) {
					break
				}
				if !onLine[to] {
					next = to
					break
				}
			}
		}
		n = next
	}
	cols := make([]Column, len(line))
	for i, n := range line {
		nextNode := ""
		if i+1 < len(line) {
			nextNode = line[i+1]
		}
		cols[i] = column(o, n, transitions, nextNode)
	}
	if len(cols) == 0 {
		return nil
	}
	// The link from the last column to the end of the flow: the transition
	// to an end the campaign took, else the one to done.
	end := &cols[len(cols)-1]
	pick := -1
	for i, x := range end.Exits {
		if orders.IsEnd(x.To) && (x.Last || (pick < 0 && x.To == orders.Done)) {
			pick = i
		}
	}
	if pick >= 0 {
		end.Exits[pick].Main = true
	}
	placed := map[string]bool{}
	for _, n := range line {
		placed[n] = true
	}
	type item struct {
		node   string
		anchor int
	}
	queue := make([]item, len(line))
	for i, n := range line {
		queue[i] = item{n, i}
	}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		def := o.Nodes[it.node]
		if def == nil {
			continue
		}
		for _, out := range outcomeOrder(def.On) {
			to := def.On[out]
			if orders.IsEnd(to) || placed[to] || o.Nodes[to] == nil {
				continue
			}
			placed[to] = true
			cols[it.anchor].Side = append(cols[it.anchor].Side, column(o, to, transitions, ""))
			queue = append(queue, item{to, it.anchor})
		}
	}
	last := &cols[len(cols)-1]
	for _, n := range o.Order {
		if !placed[n] {
			placed[n] = true
			last.Side = append(last.Side, column(o, n, transitions, ""))
		}
	}
	for _, n := range seen {
		if !placed[n] {
			placed[n] = true
			last.Side = append(last.Side, column(nil, n, transitions, ""))
		}
	}
	return cols
}

func column(o *orders.Orders, node string, transitions map[string][]chronicle.Event, next string) Column {
	col := Column{Node: node}
	def := nodeDef(o, node)
	if def == nil {
		return col
	}
	col.Known, col.Type = true, def.Type
	for _, name := range def.Checks {
		col.Tiles = append(col.Tiles, Tile{Check: name})
	}
	ts := transitions[node]
	for _, out := range outcomeOrder(def.On) {
		x := Exit{Outcome: out, To: def.On[out], Main: next != "" && def.On[out] == next}
		for i, t := range ts {
			if t.Outcome == out {
				x.Taken++
				x.Last = i == len(ts)-1
			}
		}
		col.Exits = append(col.Exits, x)
	}
	// Only one exit is the main link even when several outcomes lead to the
	// next column: the one taken last, else the first.
	mains := 0
	for _, x := range col.Exits {
		if x.Main {
			mains++
		}
	}
	if mains > 1 {
		keep := -1
		for i, x := range col.Exits {
			if x.Main && (keep < 0 || x.Last) {
				keep = i
			}
		}
		for i := range col.Exits {
			col.Exits[i].Main = i == keep
		}
	}
	return col
}
