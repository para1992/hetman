package dispatch

import (
	"encoding/json"
	"strings"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/i18n"
	"hetman/internal/orders"
)

// Task states as shown: the recorded ones while a task has no campaign,
// then the state of its campaign.
const (
	StateQueued    = "queued"
	StateCancelled = "cancelled"
	StateRunning   = "running" // being started, being driven, or ready for the dispatcher to continue
	StateWaiting   = "waiting" // waits for a person: to answer, or to resume after a limit or lost connectivity
	StateDone      = "done"
	StateFailed    = "failed"
	StateHalted    = "halted"
	StateError     = "error" // stopped in the error state; a person fixes the cause and resumes
)

// View is a task as the commands and the page show it.
type View struct {
	ID       int64           `json:"id"`
	Kind     string          `json:"kind"`
	Key      string          `json:"key,omitempty"`
	State    string          `json:"state"`
	Campaign int64           `json:"campaign,omitempty"`
	Flow     string          `json:"flow,omitempty"`
	Orders   string          `json:"orders,omitempty"` // orders file of the campaign
	Node     string          `json:"node,omitempty"`   // where the campaign is
	Waits    string          `json:"waits,omitempty"`  // what a waiting, halted or stopped task waits for
	Command  string          `json:"command,omitempty"`
	Driven   bool            `json:"driven"` // a process drives its campaign now
	Added    time.Time       `json:"added"`
	Started  *time.Time      `json:"started,omitempty"`
	Ended    *time.Time      `json:"ended,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`

	ready bool // the campaign can go on without a person and nobody drives it
	busy  bool // the campaign runs now, in this process or another
}

// look derives the view of a campaign from its events. driven reports
// whether a process holds the campaign's lock.
func look(p *i18n.Printer, v *View, k chronicle.Campaign, events []chronicle.Event, driven bool) {
	st := chronicle.Replay(events)
	v.Flow, v.Orders, v.Node, v.Driven = k.Flow, k.Orders, st.Node, driven
	o := &orders.Orders{Path: k.Orders}
	switch {
	case st.End == orders.Done:
		v.State = StateDone
	case st.End == orders.Failed:
		v.State = StateFailed
	case st.Error != "":
		v.State, v.Waits, v.Command = StateError, p.T("cli.task.waits.error", "error", st.Error), engine.ResumeCommand(o, k.ID)
	case st.Halted || (st.HaltRequested && !driven):
		v.State, v.Waits, v.Command = StateHalted, p.T("cli.task.waits.halted"), engine.ResumeCommand(o, k.ID)
	case st.Asking():
		v.State, v.Command = StateWaiting, engine.AnswerCommand(o, k.ID)
		v.Waits = asks(p, st.Ask)
	case st.Waiting != "" && st.WaitUntil.IsZero():
		v.State, v.Command = StateWaiting, engine.ResumeCommand(o, k.ID)
		v.Waits = p.T("cli.task.waits.limit", "node", st.Node)
		if st.Waiting == chronicle.WaitOffline {
			v.Waits = p.T("cli.task.waits.offline", "node", st.Node)
		}
	default:
		v.State = StateRunning
		v.busy = driven
		v.ready = !driven
		if !st.WaitUntil.IsZero() && st.Waiting != "" {
			v.Waits = p.T("cli.task.waits.limit_reset", "at", st.WaitUntil.Local().Format(time.DateTime))
		}
	}
	if st.End != "" && len(events) > 0 {
		at := events[len(events)-1].At
		v.Ended = &at
	}
}

// asks says what an open question waits for.
func asks(p *i18n.Printer, ask *chronicle.Event) string {
	switch ask.Reason {
	case chronicle.ReasonHuman:
		return p.T("cli.task.waits.human", "node", ask.Node, "outcomes", strings.Join(ask.Outcomes, ", "))
	case chronicle.ReasonDialogue:
		return p.T("cli.task.waits.dialogue", "node", ask.Node, "n", len(ask.Questions))
	default:
		return p.T("cli.task.waits.gate", "node", ask.Node, "n", len(ask.Questions))
	}
}

// Views returns the views of tasks. dir is the folder of the routing file,
// which holds the chronicle; chron may be nil when it does not exist yet.
// What a task waits for is said in the language of p.
func Views(tasks []Task, chron *chronicle.Chronicle, dir string, p *i18n.Printer) ([]View, error) {
	out := make([]View, 0, len(tasks))
	for _, t := range tasks {
		v, err := viewOf(p, t, chron, dir)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func viewOf(p *i18n.Printer, t Task, chron *chronicle.Chronicle, dir string) (View, error) {
	v := View{ID: t.ID, Kind: t.Kind, Key: t.Key, Campaign: t.Campaign, Added: t.Added}
	if !t.Started.IsZero() {
		s := t.Started
		v.Started = &s
	}
	switch t.State {
	case Queued:
		v.State = StateQueued
		return v, nil
	case Cancelled:
		v.State = StateCancelled
		return v, nil
	case Starting:
		v.State, v.busy = StateRunning, true
		return v, nil
	}
	if chron == nil || t.Campaign == 0 {
		v.State = StateRunning
		return v, nil
	}
	k, err := chron.Campaign(t.Campaign)
	if err != nil {
		return v, err
	}
	switch t.State {
	case Done, Failed:
		// An ended campaign never changes, so its outcome was kept.
		v.State, v.Flow, v.Orders = t.State, k.Flow, k.Orders
		if !t.Ended.IsZero() {
			e := t.Ended
			v.Ended = &e
		}
		return v, nil
	}
	events, err := chron.Events(t.Campaign)
	if err != nil {
		return v, err
	}
	driven, _ := engine.Driven(&orders.Orders{Dir: dir}, t.Campaign)
	look(p, &v, k, events, driven)
	return v, nil
}
