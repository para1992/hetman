package engine

import (
	"encoding/json"
	"fmt"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// Top returns the campaign a person started that a campaign belongs to: the
// campaign itself, or the parent of its parent and so on.
func Top(ch *chronicle.Chronicle, id int64) (int64, error) {
	for depth := 0; depth <= orders.MaxDepth+1; depth++ {
		k, err := ch.Campaign(id)
		if err != nil {
			return 0, err
		}
		if k.Parent == 0 {
			return id, nil
		}
		id = k.Parent
	}
	return 0, fmt.Errorf("campaign %d: its parents nest deeper than %d levels", id, orders.MaxDepth)
}

// OrdersOf returns the orders a campaign runs, given the orders of the
// campaign a person started: those orders for that campaign, the segment of
// the node that started it for a child.
func OrdersOf(o *orders.Orders, ch *chronicle.Chronicle, id int64) (*orders.Orders, error) {
	k, err := ch.Campaign(id)
	if err != nil {
		return nil, err
	}
	if k.Parent == 0 || o == nil {
		return o, nil
	}
	po, err := OrdersOf(o, ch, k.Parent)
	if err != nil {
		return nil, err
	}
	events, err := ch.Events(id)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("campaign %d has no events", id)
	}
	n := po.Nodes[events[0].ParentNode]
	if n == nil || n.Segment == nil {
		return nil, fmt.Errorf("campaign %d was started by node %q of campaign %d, which no longer runs a segment", id, events[0].ParentNode, k.Parent)
	}
	return n.Segment, nil
}

// childAsks lists the questions the running children of a campaign wait on,
// depth first in the order the children started.
func childAsks(ch *chronicle.Chronicle, st chronicle.State, depth int) ([]*chronicle.Event, error) {
	if depth > orders.MaxDepth {
		return nil, nil
	}
	var out []*chronicle.Event
	for _, id := range st.Children {
		events, err := ch.Events(id)
		if err != nil {
			return nil, err
		}
		cst := chronicle.Replay(events)
		if cst.End != "" {
			continue
		}
		if cst.Asking() {
			out = append(out, cst.Ask)
			continue
		}
		more, err := childAsks(ch, cst, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}
	return out, nil
}

// Child is one child of the latest attempt of a segment or for-each node,
// or an item of a for-each that has no child campaign.
type Child struct {
	Item    string            // for-each: the item id
	Needs   []string          // for-each: the items it needs
	ID      int64             // the child campaign; 0 while the item has not started
	Outcome string            // done or failed once the child reached its end
	NotRun  string            // why the item will not run
	Events  []chronicle.Event // the child's events
	State   chronicle.State   // the child's state
}

// ChildrenOf lists the children of the latest attempt of a segment or
// for-each node in a campaign's events: for a for-each, every item of its
// list in list order.
func ChildrenOf(ch *chronicle.Chronicle, events []chronicle.Event, node string) ([]Child, error) {
	attempt := 0
	var start *chronicle.Event
	for i := range events {
		if ev := &events[i]; ev.Kind == chronicle.StepStarted && ev.Node == node {
			attempt, start = ev.Attempt, ev
		}
	}
	if start == nil {
		return nil, nil
	}
	var out []Child
	at := map[string]int{}
	if len(start.Items) > 0 {
		var list []Item
		json.Unmarshal(start.Items, &list)
		for _, it := range list {
			at[it.ID] = len(out)
			out = append(out, Child{Item: it.ID, Needs: it.Needs})
		}
	}
	get := func(item string) *Child {
		i, ok := at[item]
		if !ok {
			at[item] = len(out)
			i = len(out)
			out = append(out, Child{Item: item})
		}
		return &out[i]
	}
	for _, ev := range events {
		if ev.Seq < start.Seq || ev.Node != node || ev.Attempt != attempt {
			continue
		}
		switch ev.Kind {
		case chronicle.ChildStarted:
			get(ev.Item).ID = ev.Child
		case chronicle.ItemFinished:
			get(ev.Item).Outcome = ev.Outcome
		case chronicle.ItemNotRun:
			get(ev.Item).NotRun = ev.Error
		}
	}
	for i := range out {
		c := &out[i]
		if c.ID == 0 {
			continue
		}
		evs, err := ch.Events(c.ID)
		if err != nil {
			return nil, err
		}
		c.Events, c.State = evs, chronicle.Replay(evs)
		if c.Outcome == "" {
			c.Outcome = c.State.End
		}
	}
	return out, nil
}

// Waiter is a child campaign, at any depth, that holds its campaign up: it
// waits for a person to answer, waits for a person after a subscription limit
// or lost connectivity, stopped in the error state, or waits for a lock.
type Waiter struct {
	Campaign int64
	Path     string // where it runs, such as "items/b" or "review > items/b"
	State    chronicle.State
}

// Waiters lists the running children of a campaign that hold it up, depth
// first in the order they started.
func Waiters(ch *chronicle.Chronicle, events []chronicle.Event) ([]Waiter, error) {
	return waiters(ch, events, "", 0)
}

func waiters(ch *chronicle.Chronicle, events []chronicle.Event, prefix string, depth int) ([]Waiter, error) {
	st := chronicle.Replay(events)
	if !st.Running || depth > orders.MaxDepth {
		return nil, nil
	}
	label := map[int64]string{}
	for _, ev := range events {
		if ev.Kind == chronicle.ChildStarted && ev.Node == st.Node && ev.Attempt == st.Attempt {
			label[ev.Child] = ev.Node
			if ev.Item != "" {
				label[ev.Child] += "/" + ev.Item
			}
		}
	}
	var out []Waiter
	for _, id := range st.Children {
		evs, err := ch.Events(id)
		if err != nil {
			return nil, err
		}
		cst := chronicle.Replay(evs)
		path := prefix + label[id]
		switch {
		case cst.End != "":
			continue
		case cst.Asking() || cst.Error != "" || cst.LockWait != "" || (cst.Waiting != "" && cst.WaitUntil.IsZero()):
			out = append(out, Waiter{Campaign: id, Path: path, State: cst})
			continue
		}
		more, err := waiters(ch, evs, path+" > ", depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}
	return out, nil
}
