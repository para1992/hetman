package engine

import (
	"fmt"
	"os"
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

// stamp sums up the files of orders as their sizes and times of change.
func stamp(o *orders.Orders) string {
	var b strings.Builder
	for _, f := range o.Files() {
		if fi, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%s\x00%d\x00%d\n", f, fi.Size(), fi.ModTime().UnixNano())
		} else {
			fmt.Fprintf(&b, "%s\x00-\n", f)
		}
	}
	return b.String()
}

// refresh reads the orders again when their files changed.
func (e *Engine) refresh() {
	top := e.top()
	s := stamp(top.Orders)
	if e.stamp == "" {
		e.stamp = s
		return
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
