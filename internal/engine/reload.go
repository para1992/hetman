package engine

import (
	"fmt"
	"strings"

	"hetman/internal/orders"
)

// Before every move a campaign looks at the files of the orders it was
// started from: the orders file, its prompt templates, schemas and
// programs, and every segment's. When any of them changed since it last
// read them, it reads the orders again, so an edit takes effect at the
// next step that starts, in the campaign a person started and in its
// running children alike. A child takes its segment from the orders of
// the campaign a person started, read afresh, through the nodes that led
// to it. Orders that do not pass the check now are not used; the campaign
// goes on with the version it read before and says so.
//
// What a campaign compares with is how the files looked when its orders
// were read (orders.Orders.Stamp), so orders read long before the campaign
// moves are read again when a file changed in between.

// refresh reads the orders again when their files changed.
func (e *Engine) refresh() {
	top := e.top()
	s := top.Orders.StampNow()
	if e.stamp == "" {
		// The orders may have been read well before this campaign moves,
		// as by the page server, which keeps them between requests; they
		// are compared with their files as they were read, not as they are
		// now, so that an edit made in between is not missed.
		if e.stamp = top.Orders.Stamp(); e.stamp == "" {
			e.stamp = s
			return
		}
	}
	if s == e.stamp {
		return
	}
	e.stamp = s
	path := top.Orders.Path
	o, err := orders.Load(path)
	if err != nil {
		e.say("engine.orders_kept", "path", path, "error", firstLine(err.Error()))
		return
	}
	if e.parent == nil {
		e.Orders = o
		e.described = nil
		e.say("engine.orders_reread", "path", path)
		return
	}
	// The way from the campaign a person started down to this one.
	var way []string
	for x := e; x.parent != nil; x = x.parent {
		way = append([]string{x.node}, way...)
	}
	cur := o
	for _, id := range way {
		n := cur.Nodes[id]
		if n == nil || n.Segment == nil {
			e.say("engine.orders_kept", "path", path, "error", fmt.Sprintf("node %q no longer runs a segment", id))
			return
		}
		cur = n.Segment
	}
	e.Orders = cur
	e.described = nil
	e.say("engine.orders_reread", "path", cur.Path)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
