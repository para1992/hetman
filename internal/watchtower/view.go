package watchtower

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"hetman/internal/ask"
	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/explain"
	"hetman/internal/language"
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
	StateWaiting     = "waiting" // waits for a subscription limit to reset, then continues on its own
	StateLimit       = "limit"   // stopped by a subscription limit with no time to retry; waits for a person
	StateOffline     = "offline" // stopped by lost connectivity; waits for a person
	StateAsking      = "asking"  // waits for a person to answer questions
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
	StatusAsking      = "asking" // waits for a person to answer
)

// Summary is one row of the campaign list.
type Summary struct {
	ID            int64      `json:"id"`
	Flow          string     `json:"flow"`
	State         string     `json:"state"`
	Node          string     `json:"node"` // current position
	HaltRequested bool       `json:"haltRequested"`
	Error         string     `json:"error,omitempty"`
	Wait          string     `json:"wait,omitempty"`      // limit or offline while the step at Node waits
	WaitUntil     *time.Time `json:"waitUntil,omitempty"` // when a limit wait ends on its own
	Driven        bool       `json:"driven"`              // a process drives the campaign now
	Started       time.Time  `json:"started"`
	Ended         *time.Time `json:"ended,omitempty"` // last event once the campaign is not running
	Marks         []Mark     `json:"marks"`
	LockWait      string     `json:"lockWait,omitempty"`   // the lock the step at Node waits for
	LockHolder    string     `json:"lockHolder,omitempty"` // who holds it
	Parent        int64      `json:"parent,omitempty"`     // a child campaign: the campaign whose step runs it
	Top           int64      `json:"top,omitempty"`        // a child campaign: the campaign a person started
	Path          string     `json:"path,omitempty"`       // a child campaign: where it runs, such as "items/b"
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
	Ask           *Ask                 `json:"ask,omitempty"`      // the question open now
	Words         []explain.Section    `json:"words,omitempty"`    // the campaign in plain words
	Language      *Language            `json:"language,omitempty"` // the language for the person recorded at the start
	Retry         string               `json:"retry,omitempty"`    // the node a retry runs again; empty when nothing failed
	Rewind        []string             `json:"rewind,omitempty"`   // the nodes a rewind may send the campaign back to
	Returns       []Return             `json:"returns,omitempty"`  // every retry and rewind, oldest first
	NoClient      string               `json:"noClient,omitempty"` // in words: no client of the role of the step at Node can run, and what is known of each

	returned map[string]bool // nodes sent back to that have not run since
}

// Return is a person sending the campaign back to a node.
type Return struct {
	Kind string    `json:"kind"` // retried or rewound
	Node string    `json:"node"`
	At   time.Time `json:"at"`
	By   string    `json:"by,omitempty"`
	Via  string    `json:"via,omitempty"`
}

// Language is the language for the person a campaign started with.
type Language struct {
	Code    string `json:"code"`
	English string `json:"english,omitempty"` // empty for a code this hetman does not know
	Native  string `json:"native,omitempty"`
}

// Ask is one round of questions to a person and, once given, the answers.
type Ask struct {
	Seq        int64          `json:"seq"`
	Node       string         `json:"node"`
	Reason     string         `json:"reason"`
	Round      int            `json:"round,omitempty"`
	At         time.Time      `json:"at"`
	Questions  []ask.Question `json:"questions"`
	Show       []string       `json:"show,omitempty"`
	Outcomes   []Outcome      `json:"outcomes,omitempty"`
	Answers    []ask.Answer   `json:"answers,omitempty"`
	Outcome    string         `json:"outcome,omitempty"` // the outcome chosen at a human node
	By         string         `json:"by,omitempty"`
	Via        string         `json:"via,omitempty"`
	AnsweredAt *time.Time     `json:"answeredAt,omitempty"`
	Campaign   int64          `json:"campaign,omitempty"` // the child campaign that asks, when it is not this one
	Path       string         `json:"path,omitempty"`     // where that child runs`
	Command    string         `json:"command,omitempty"`  // while open: the command that answers it
	Example    string         `json:"example,omitempty"`  // while open: the same with every answer given as a flag
}

// Outcome is one outcome a person may choose at a human node.
type Outcome struct {
	Name string `json:"name"`
	To   string `json:"to,omitempty"`
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
	// Agent nodes: as in the current orders.
	Role        string `json:"role,omitempty"`
	Executor    string `json:"executor,omitempty"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	MaxAttempts int    `json:"maxAttempts,omitempty"`
	Used        int    `json:"used,omitempty"`  // the current node: failed attempts that count against this visit's budget
	Again       bool   `json:"again,omitempty"` // ran after the node of the latest retry or rewind and before it: runs again when reached
}

// Tile is one check of a gate, the single command of a step, or one child
// campaign of a segment or for-each step.
type Tile struct {
	Check    string            `json:"check,omitempty"`
	Soft     bool              `json:"soft,omitempty"`
	Status   string            `json:"status"`
	Outcome  string            `json:"outcome,omitempty"`
	Attempts int               `json:"attempts"`
	Item     string            `json:"item,omitempty"`    // for-each: the item id
	Child    int64             `json:"child,omitempty"`   // the child campaign; 0 for an item not started
	Node     string            `json:"node,omitempty"`    // where the child is
	Note     string            `json:"note,omitempty"`    // what the child waits for, or why an item does not run: a message key of the page
	Args     map[string]string `json:"args,omitempty"`    // the parameters of Note
	Attempt  int               `json:"attempt,omitempty"` // the attempt of the node the child belongs to
	Earlier  bool              `json:"earlier,omitempty"` // the child belongs to an earlier attempt and is never driven again
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
	// Agent steps.
	Executor  string          `json:"executor,omitempty"`
	Model     string          `json:"model,omitempty"`
	Effort    string          `json:"effort,omitempty"`
	Prompt    string          `json:"prompt,omitempty"`    // rendered prompt file
	Stderr    string          `json:"stderr,omitempty"`    // adapter diagnostics file
	SessionIn string          `json:"sessionIn,omitempty"` // conversation continued by this attempt
	Session   string          `json:"session,omitempty"`   // conversation reported by the adapter
	Output    json.RawMessage `json:"output,omitempty"`    // validated output
	Failure   string          `json:"failure,omitempty"`   // why the attempt failed
	RetryAt   *time.Time      `json:"retryAt,omitempty"`   // limit: when it is tried again
	Ask       *Ask            `json:"ask,omitempty"`       // the questions this attempt put to a person
	// A step run through a role: how its client was chosen, and the same
	// in words of the reader's language.
	Choice   *chronicle.Choice `json:"choice,omitempty"`
	Client   string            `json:"client,omitempty"`   // the client, its role and why it was chosen
	Switched string            `json:"switched,omitempty"` // the client whose limit made the step change client
	Compared string            `json:"compared,omitempty"` // the quota mode: the shares compared
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

	Input    json.RawMessage `json:"input,omitempty"`
	Language string          `json:"language,omitempty"`
	Executor string          `json:"executor,omitempty"`
	Model    string          `json:"model,omitempty"`
	Effort   string          `json:"effort,omitempty"`
	Prompt   string          `json:"prompt,omitempty"`
	Session  string          `json:"session,omitempty"`
	Output   json.RawMessage `json:"output,omitempty"`
	Failure  string          `json:"failure,omitempty"`
	RetryAt  *time.Time      `json:"retryAt,omitempty"`

	Role      string            `json:"role,omitempty"`
	Choice    *chronicle.Choice `json:"choice,omitempty"`
	WaitUntil *time.Time        `json:"waitUntil,omitempty"`

	Reason    string         `json:"reason,omitempty"`
	Questions []ask.Question `json:"questions,omitempty"`
	Show      []string       `json:"show,omitempty"`
	Outcomes  []string       `json:"outcomes,omitempty"`
	Round     int            `json:"round,omitempty"`
	Answers   []ask.Answer   `json:"answers,omitempty"`
	Ask       int64          `json:"ask,omitempty"`
	By        string         `json:"by,omitempty"`
	Via       string         `json:"via,omitempty"`
}

func eventsJSON(events []chronicle.Event) []Event {
	out := make([]Event, len(events))
	for i, e := range events {
		out[i] = Event{Seq: e.Seq, At: e.At, Kind: e.Kind, Node: e.Node, Check: e.Check, Attempt: e.Attempt,
			Outcome: e.Outcome, To: e.To, Error: e.Error, Exit: e.Exit, Ms: e.Millis, Log: e.Log,
			Artifacts: e.Artifacts, Soft: e.Soft, Pid: e.Pid, Ident: e.Ident,
			Input: e.Input, Language: e.Language, Executor: e.Executor, Model: e.Model, Effort: e.Effort, Prompt: e.Prompt,
			Session: e.Session, Output: e.Output, Failure: e.Failure, RetryAt: e.RetryAt,
			Role: e.Role, Choice: e.Choice, WaitUntil: e.WaitUntil,
			Reason: e.Reason, Questions: e.Questions, Show: e.Show, Outcomes: e.Outcomes, Round: e.Round,
			Answers: e.Answers, Ask: e.Ask, By: e.By, Via: e.Via}
	}
	return out
}

// build derives the campaign view from its events. o may be nil when the
// orders cannot be read; driven tells whether a process holds the lock.
func build(k chronicle.Campaign, events []chronicle.Event, o *orders.Orders, campaignDir string, driven func() bool) *Campaign {
	st := chronicle.Replay(events)
	c := &Campaign{Summary: Summary{ID: k.ID, Flow: k.Flow, Node: st.Node, HaltRequested: st.HaltRequested && !st.Halted,
		Error: st.Error, Started: k.Started}, Orders: k.Orders, End: st.End, Nodes: map[string][]Attempt{},
		returned: map[string]bool{}}
	// A child campaign is retried and rewound through the campaign a person
	// started, so only that one offers either.
	if o != nil && k.Parent == 0 {
		if n := engine.RetryNode(events); n != "" && o.Nodes[n] != nil {
			c.Retry = n
		}
		for _, n := range engine.Visited(events) {
			if o.Nodes[n] != nil {
				c.Rewind = append(c.Rewind, n)
			}
		}
	}
	if len(events) > 0 && events[0].Language != "" {
		l, _ := language.Lookup(events[0].Language)
		c.Language = &Language{Code: events[0].Language, English: l.English, Native: l.Native}
	}
	if st.End == "" && st.LockWait != "" && !st.Running {
		c.LockWait, c.LockHolder = st.LockWait, st.LockHolder
	}
	if st.End == "" && st.Waiting != "" {
		c.Wait = st.Waiting
		if !st.WaitUntil.IsZero() {
			until := st.WaitUntil
			c.WaitUntil = &until
		}
	}
	switch {
	case st.End == orders.Done:
		c.State = StateDone
	case st.End == orders.Failed:
		c.State = StateFailed
	case st.Error != "":
		c.State = StateError
	case st.Halted:
		c.State = StateHalted
	case st.Asking():
		c.State = StateAsking
	case st.Waiting == chronicle.WaitOffline:
		c.State = StateOffline
	case st.Waiting == chronicle.WaitLimit && st.WaitUntil.IsZero():
		c.State = StateLimit
	case driven():
		c.State = StateRunning
		if st.Waiting == chronicle.WaitLimit {
			c.State = StateWaiting
		}
	default:
		c.State = StateInterrupted
	}
	if st.End == "" {
		c.Driven = driven()
	}
	if n := len(events); n > 0 {
		c.LastSeq = events[n-1].Seq
		if c.State != StateRunning && !(c.State == StateAsking && c.Driven) {
			at := events[n-1].At
			c.Ended = &at
		}
	}
	live := c.State == StateRunning || c.State == StateWaiting

	// Attempt history per node, in chronicle order.
	soft := map[string]bool{}
	if o != nil {
		for name, ch := range o.Checks {
			soft[name] = ch.Soft
		}
	}
	var seen []string
	cur := map[string]*Attempt{}
	asks := map[int64]*Ask{}
	transitions := map[string][]chronicle.Event{}
	lastStart := map[string]int{}
	// The latest retry or rewind, and the last start of its node before it:
	// the nodes that started in between run again when the flow reaches them.
	lastReturn, returnFrom, returnTo := -1, -1, ""
	for i, e := range events {
		switch e.Kind {
		case chronicle.Retried, chronicle.Rewound:
			c.Returns = append(c.Returns, Return{Kind: e.Kind, Node: e.Node, At: e.At, By: e.By, Via: e.Via})
			c.returned[e.Node] = true
			lastReturn, returnTo = i, e.Node
			returnFrom = -1
			if at, ok := lastStart[e.Node]; ok {
				returnFrom = at
			}
		case chronicle.StepStarted:
			lastStart[e.Node] = i
			c.returned[e.Node] = false
			if _, ok := c.Nodes[e.Node]; !ok {
				seen = append(seen, e.Node)
			}
			c.Nodes[e.Node] = append(c.Nodes[e.Node], Attempt{N: e.Attempt, Status: StatusRunning, Started: e.At,
				Log: logIfScript(o, e.Node, e.Attempt), Executor: e.Executor, Model: e.Model, Effort: e.Effort,
				Prompt: e.Prompt, SessionIn: e.Session, Choice: e.Choice})
			if e.Executor != "" {
				list := c.Nodes[e.Node]
				list[len(list)-1].Log = engine.LogFile(e.Node, "", e.Attempt)
				list[len(list)-1].Stderr = engine.StderrFile(e.Node, e.Attempt)
			}
			list := c.Nodes[e.Node]
			cur[e.Node] = &list[len(list)-1]
		case chronicle.StepInterrupted:
			if a := cur[e.Node]; a != nil && a.N == e.Attempt && a.Status == StatusRunning {
				a.Status = StatusInterrupted
				at := e.At
				a.Finished = &at
			}
		case chronicle.AgentSession:
			if a := find(c.Nodes[e.Node], e.Attempt); a != nil {
				a.Session = e.Session
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
				a.Output, a.Failure, a.RetryAt = e.Output, e.Failure, e.RetryAt
				if e.Stderr != "" {
					a.Stderr = e.Stderr
				}
				if e.Failure == chronicle.FailLimit || e.Failure == chronicle.FailOffline {
					// Cut off; the step runs again and the attempt does not count.
					a.Status = StatusInterrupted
				}
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
		case chronicle.Asked:
			q := &Ask{Seq: e.Seq, Node: e.Node, Reason: e.Reason, Round: e.Round, At: e.At, Questions: e.Questions, Show: e.Show}
			for _, out := range e.Outcomes {
				x := Outcome{Name: out}
				if def := nodeDef(o, e.Node); def != nil {
					x.To = def.On[out]
				}
				q.Outcomes = append(q.Outcomes, x)
			}
			asks[e.Seq] = q
			if a := find(c.Nodes[e.Node], e.Attempt); a != nil {
				a.Ask = q
			}
		case chronicle.Answered:
			if q := asks[e.Ask]; q != nil && q.AnsweredAt == nil {
				at := e.At
				q.Answers, q.Outcome, q.By, q.Via, q.AnsweredAt = e.Answers, e.Outcome, e.By, e.Via, &at
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
			a := &list[i]
			switch {
			case a.Ask != nil && a.Ask.AnsweredAt == nil && st.Ask != nil && st.Ask.Seq == a.Ask.Seq:
				a.Status = StatusAsking
			case a.Ask != nil && a.Finished == nil && a.Ask.AnsweredAt != nil:
				// Answered; the step finishes when a process drives the campaign.
				a.Status = StatusWaiting
			case a.Status == StatusRunning && (!live || i < len(list)-1):
				a.Status = StatusInterrupted
			}
		}
	}
	if st.Asking() && st.End == "" {
		c.Ask = asks[st.Ask.Seq]
		if c.Ask != nil && o != nil {
			c.Ask.Command = engine.AnswerCommand(o, k.ID)
			c.Ask.Example = answerExample(c.Ask)
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
	mark := func(col *Column) {
		fill(col, c, st, soft)
		if at, ran := lastStart[col.Node]; ran && at > returnFrom && at < lastReturn && col.Node != returnTo && !col.Current {
			col.Again = true
		}
		if col.Current {
			col.Used = st.Failures
		}
	}
	for i := range c.Columns {
		mark(&c.Columns[i])
		for j := range c.Columns[i].Side {
			mark(&c.Columns[i].Side[j])
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
	if def := nodeDef(o, node); def != nil && (len(def.Checks) > 0 || len(def.Ask) > 0 || def.Type == orders.Human) {
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
	case col.Current && st.Asking():
		col.Status = StatusAsking
	case col.Current && (c.Wait != "" || c.LockWait != ""):
		col.Status = StatusWaiting
	case len(list) > 0:
		col.Status = list[len(list)-1].Status
		if col.Current && col.Status != StatusRunning && !st.HasOutcome && c.State != StateError &&
			(list[len(list)-1].Transition != "" || c.returned[col.Node]) {
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
	if def.Type == orders.Agent {
		col.Role, col.Executor, col.Model, col.Effort, col.MaxAttempts = def.Role, def.Executor, def.Model, def.Effort, def.MaxAttempts
	}
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

// answerExample is the answer command with an answer given for every
// question as a flag, for people who script it.
func answerExample(q *Ask) string {
	parts := []string{q.Command, "-ask", strconv.FormatInt(q.Seq, 10)}
	for _, x := range q.Questions {
		v := "TEXT"
		switch x.Kind {
		case ask.YesNo:
			v = ask.Yes
		case ask.Choice:
			v = x.Options[0]
		}
		parts = append(parts, "-a", engine.ShellQuote(x.ID+"="+v))
	}
	if len(q.Outcomes) > 1 {
		parts = append(parts, "-outcome", q.Outcomes[0].Name)
	}
	return strings.Join(parts, " ")
}
