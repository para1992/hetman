package engine

import (
	"errors"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// Retry and rewind with child campaigns follow these rules:
//
//   - Both are actions on the campaign a person started; asked of a child
//     they are refused and name that campaign.
//   - Neither runs while the campaign or any of its unfinished children is
//     being driven: a child holds its locks only while it is driven, so no
//     lock can be held either.
//   - Sending a campaign back to a segment or for-each node starts a new
//     attempt of it with new children. The children of earlier attempts and
//     their history stay as they are and are never driven again.
//   - A retry of a for-each node keeps the items that reached done in the
//     attempt before (item_kept) and runs the others as new children.
//   - A retry of a segment node whose child failed or stopped in the error
//     state retries that child at its own failed node, under the new attempt.
//   - A campaign stopped in the error state because a child did is retried
//     inside the child: the deepest child that stopped by itself is retried
//     at its node, and the campaign continues its running step.

// ErrChildBusy is the reason a retry or rewind is refused while a child
// campaign is being driven.
var ErrChildBusy = errors.New("a child campaign is being driven")

// refuseChild refuses a retry or rewind asked of a child campaign.
func refuseChild(ch *chronicle.Chronicle, id int64) error {
	k, err := ch.Campaign(id)
	if err != nil || k.Parent == 0 {
		return err
	}
	top, err := Top(ch, id)
	if err != nil {
		return err
	}
	return refuse(ErrChild, "engine.refuse.child", "id", id, "top", top)
}

// busyChild returns an unfinished child campaign, at any depth, that a
// process drives now, or 0.
func busyChild(o *orders.Orders, ch *chronicle.Chronicle, events []chronicle.Event, depth int) (int64, error) {
	if depth > orders.MaxDepth {
		return 0, nil
	}
	for _, id := range chronicle.Replay(events).Children {
		if driven, err := Driven(o, id); err != nil || driven {
			return id, err
		}
		evs, err := ch.Events(id)
		if err != nil {
			return 0, err
		}
		if b, err := busyChild(o, ch, evs, depth+1); b != 0 || err != nil {
			return b, err
		}
	}
	return 0, nil
}

// cleanupChildren checks the processes of the steps running in the
// unfinished children of a campaign before it is sent back, as resume does
// for a running step, and records those steps as interrupted.
func cleanupChildren(top *Engine, events []chronicle.Event, depth int) error {
	if depth > orders.MaxDepth {
		return nil
	}
	for _, id := range chronicle.Replay(events).Children {
		evs, err := top.Chron.Events(id)
		if err != nil {
			return err
		}
		cst := chronicle.Replay(evs)
		if cst.End != "" {
			continue
		}
		if len(cst.Children) > 0 {
			if err := cleanupChildren(top, evs, depth+1); err != nil {
				return err
			}
		}
		if !cst.Running || len(cst.Children) > 0 {
			continue
		}
		ce := &Engine{Chron: top.Chron, Campaign: id, parent: top}
		if err := ce.cleanup(evs, cst.Node, cst.Attempt); err != nil {
			return err
		}
		if err := ce.record(chronicle.Event{Kind: chronicle.StepInterrupted, Node: cst.Node, Attempt: cst.Attempt}); err != nil {
			return err
		}
	}
	return nil
}

// erroredChild finds the child, at any depth, whose own error stopped a
// campaign in the error state: a running child in the error state whose
// running step has no child in the error state. It returns the child and
// its events, or 0.
func erroredChild(ch *chronicle.Chronicle, events []chronicle.Event, depth int) (int64, []chronicle.Event, error) {
	if depth > orders.MaxDepth {
		return 0, nil, nil
	}
	for _, id := range chronicle.Replay(events).Children {
		evs, err := ch.Events(id)
		if err != nil {
			return 0, nil, err
		}
		cst := chronicle.Replay(evs)
		if cst.End != "" || cst.Error == "" {
			continue
		}
		if deeper, devs, err := erroredChild(ch, evs, depth+1); deeper != 0 || err != nil {
			return deeper, devs, err
		}
		return id, evs, nil
	}
	return 0, nil, nil
}

// retryChild retries a campaign stopped in the error state because a child
// did: the child is retried at its node, and the campaign itself only
// resumes, keeping its running step and children. It reports false when no
// child caused the error.
func retryChild(o *orders.Orders, ch *chronicle.Chronicle, id int64, events []chronicle.Event, by, via string) (bool, string, error) {
	st := chronicle.Replay(events)
	if st.End != "" || st.Error == "" || !st.Running {
		return false, "", nil
	}
	child, cevents, err := erroredChild(ch, events, 0)
	if err != nil || child == 0 {
		return false, "", err
	}
	node := RetryNode(cevents)
	co, err := OrdersOf(o, ch, child)
	if err != nil {
		return true, "", err
	}
	if node == "" || co.Nodes[node] == nil {
		return true, "", refuse(ErrNotInOrders, "engine.refuse.not_in_orders", "node", node, "path", co.Path)
	}
	if _, err := ch.Append(chronicle.Event{Campaign: child, Kind: chronicle.Retried, Node: node, Details: chronicle.Details{By: by, Via: via}}); err != nil {
		return true, "", err
	}
	_, err = ch.Append(chronicle.Event{Campaign: id, Kind: chronicle.Resumed, Node: st.Node,
		Details: chronicle.Details{Child: child, To: node, By: by, Via: via}})
	return true, node, err
}

// retriedVisit reports whether the current visit of a node began with a
// retry of it, and returns that event.
func retriedVisit(events []chronicle.Event, node string) *chronicle.Event {
	i := visitStart(events, node) - 1
	if i >= 0 && events[i].Kind == chronicle.Retried && events[i].Node == node {
		return &events[i]
	}
	return nil
}

// previousAttempt is the step_started event of the attempt of a node before
// the given one, or nil.
func previousAttempt(events []chronicle.Event, node string, attempt int) *chronicle.Event {
	var best *chronicle.Event
	for i := range events {
		if ev := &events[i]; ev.Kind == chronicle.StepStarted && ev.Node == node && ev.Attempt < attempt {
			best = ev
		}
	}
	return best
}

// keptItems lists the items that reached done in an attempt of a for-each
// node, by item id with their child campaign.
func keptItems(events []chronicle.Event, node string, start *chronicle.Event) map[string]int64 {
	out := map[string]int64{}
	for _, ev := range events {
		if ev.Seq <= start.Seq || ev.Node != node || ev.Attempt != start.Attempt {
			continue
		}
		if (ev.Kind == chronicle.ItemFinished && ev.Outcome == orders.Done) || ev.Kind == chronicle.ItemKept {
			out[ev.Item] = ev.Child
		}
	}
	return out
}
