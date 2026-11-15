package engine

import (
	"errors"

	"hetman/internal/chronicle"
	"hetman/internal/i18n"
	"hetman/internal/orders"
)

// Retry and rewind send a campaign back to a node. Each is one chronicle
// event (retried, rewound); nothing recorded before it changes, and Replay
// makes the node the position again with a fresh attempt budget. The files
// earlier attempts left in the campaign's folder stay where they are: logs,
// results and prompts carry the attempt number, which keeps counting, so
// nothing is written over; the work folder is left as the last step left
// it, and the steps that run again find it that way.

// Reasons a retry or a rewind is refused.
var (
	ErrDriven        = errors.New("campaign is being driven")
	ErrNothingFailed = errors.New("nothing failed to retry")
	ErrNotVisited    = errors.New("node not visited")
	ErrNotInOrders   = errors.New("node not in the orders")
)

// Refusal is a refused retry or rewind. It wraps one of the reasons above
// and says itself in the language of whoever reads it.
type Refusal struct {
	Err error
	Msg i18n.Message
}

func (r *Refusal) Error() string                   { return r.Msg.String() }
func (r *Refusal) Unwrap() error                   { return r.Err }
func (r *Refusal) Localize(p *i18n.Printer) string { return r.Msg.In(p) }

func refuse(err error, key string, args ...any) error {
	return &Refusal{Err: err, Msg: i18n.M(key, args...)}
}

// RetryNode is the node a retry runs again: the step whose outcome ended the
// campaign in failed, the node where it stopped in the error state, or the
// node whose last attempt failed and whose transition is not recorded yet.
// It is empty when nothing failed.
func RetryNode(events []chronicle.Event) string {
	st := chronicle.Replay(events)
	switch {
	case st.End == orders.Failed:
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Kind == chronicle.Transition {
				return events[i].Node
			}
		}
	case st.End != "":
	case st.Error != "":
		return st.Node
	case st.HasOutcome && st.Outcome == orders.Fail:
		return st.Node
	}
	return ""
}

// Visited lists the nodes a campaign has been at, in the order it first
// reached them: those a rewind may send it back to.
func Visited(events []chronicle.Event) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !orders.IsEnd(n) && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, e := range events {
		switch e.Kind {
		case chronicle.CampaignStarted, chronicle.StepStarted, chronicle.Retried, chronicle.Rewound:
			add(e.Node)
		case chronicle.Transition:
			add(e.To)
		}
	}
	return out
}

// Retry runs the failed step of a campaign again with a fresh attempt
// budget (see RetryNode). It is refused while another process drives the
// campaign. On success the caller holds the campaign lock and must drive the
// campaign, then call release.
func Retry(o *orders.Orders, ch *chronicle.Chronicle, id int64, by, via string) (release func(), node string, err error) {
	return sendBack(o, ch, id, chronicle.Retried, by, via, func(events []chronicle.Event) (string, error) {
		n := RetryNode(events)
		if n == "" {
			return "", refuse(ErrNothingFailed, "engine.refuse.nothing_failed", "id", id)
		}
		return n, nil
	})
}

// Rewind sends a campaign back to a node it has visited; everything after
// that node runs again. Like Retry it is refused while another process
// drives the campaign, and the caller drives it afterwards.
func Rewind(o *orders.Orders, ch *chronicle.Chronicle, id int64, node, by, via string) (release func(), err error) {
	release, _, err = sendBack(o, ch, id, chronicle.Rewound, by, via, func(events []chronicle.Event) (string, error) {
		for _, n := range Visited(events) {
			if n == node {
				return n, nil
			}
		}
		return "", refuse(ErrNotVisited, "engine.refuse.not_visited", "id", id, "node", node)
	})
	return release, err
}

func sendBack(o *orders.Orders, ch *chronicle.Chronicle, id int64, kind, by, via string,
	pick func([]chronicle.Event) (string, error)) (func(), string, error) {
	if _, err := ch.Campaign(id); err != nil {
		return nil, "", err
	}
	release, err := Lock(o, id)
	if errors.Is(err, ErrLocked) {
		return nil, "", refuse(ErrDriven, "engine.refuse.driven", "id", id)
	}
	if err != nil {
		return nil, "", err
	}
	fail := func(err error) (func(), string, error) {
		release()
		return nil, "", err
	}
	events, err := ch.Events(id)
	if err != nil {
		return fail(err)
	}
	node, err := pick(events)
	if err != nil {
		return fail(err)
	}
	if o.Nodes[node] == nil {
		return fail(refuse(ErrNotInOrders, "engine.refuse.not_in_orders", "node", node, "path", o.Path))
	}
	// A driver that died during a step may have left its process running;
	// it is dealt with as on resume before the campaign moves.
	if st := chronicle.Replay(events); st.Running {
		e := &Engine{Orders: o, Chron: ch, Campaign: id}
		if err := e.cleanup(events, st.Node, st.Attempt); err != nil {
			return fail(err)
		}
		if err := e.record(chronicle.Event{Kind: chronicle.StepInterrupted, Node: st.Node, Attempt: st.Attempt}); err != nil {
			return fail(err)
		}
	}
	if _, err := ch.Append(chronicle.Event{Campaign: id, Kind: kind, Node: node, Details: chronicle.Details{By: by, Via: via}}); err != nil {
		return fail(err)
	}
	return release, node, nil
}
