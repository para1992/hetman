package watchtower

import (
	"strings"

	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/orders"
)

// children adds what child campaigns tell to a campaign view: a segment or
// for-each column gets one tile per child with the child's own state; a
// child campaign learns where it runs; a campaign whose child waits for a
// person shows that wait as its own. o is the orders of the campaign a
// person started.
func (s *Server) children(c *Campaign, o *orders.Orders, k chronicle.Campaign, events []chronicle.Event) {
	if k.Parent != 0 && len(events) > 0 {
		c.Parent = k.Parent
		c.Top, _ = engine.Top(s.Chron, k.ID)
		c.Path = events[0].ParentNode
		if events[0].Item != "" {
			c.Path += "/" + events[0].Item
		}
	}
	for i := range c.Columns {
		s.childTiles(&c.Columns[i], o, events)
		for j := range c.Columns[i].Side {
			s.childTiles(&c.Columns[i].Side[j], o, events)
		}
	}
	if c.State != StateRunning && c.State != StateInterrupted {
		return
	}
	ws, err := engine.Waiters(s.Chron, events)
	if err != nil {
		return
	}
	for _, w := range ws {
		st := w.State
		switch {
		case st.Asking() && c.Ask == nil:
			c.State = StateAsking
			c.Ask = childAsk(st.Ask, o, s.Chron, c, w)
		case st.Waiting == chronicle.WaitOffline && c.State == StateInterrupted:
			c.State, c.Wait = StateOffline, st.Waiting
		case st.Waiting == chronicle.WaitLimit && c.State == StateInterrupted:
			c.State, c.Wait = StateLimit, st.Waiting
		}
	}
}

// childAsk is the open question of a child campaign as its parent shows it.
func childAsk(ev *chronicle.Event, o *orders.Orders, ch *chronicle.Chronicle, c *Campaign, w engine.Waiter) *Ask {
	q := &Ask{Seq: ev.Seq, Node: ev.Node, Reason: ev.Reason, Round: ev.Round, At: ev.At, Questions: ev.Questions, Show: ev.Show,
		Campaign: w.Campaign, Path: w.Path}
	co, _ := engine.OrdersOf(o, ch, w.Campaign)
	for _, out := range ev.Outcomes {
		x := Outcome{Name: out}
		if def := nodeDef(co, ev.Node); def != nil {
			x.To = def.On[out]
		}
		q.Outcomes = append(q.Outcomes, x)
	}
	if o != nil {
		top := c.ID
		if c.Top != 0 {
			top = c.Top
		}
		q.Command = engine.AnswerCommand(o, top)
		q.Example = answerExample(q)
	}
	return q
}

// childTiles makes the tiles of a segment or for-each column: one per child
// of its latest attempt, and one per for-each item not started.
func (s *Server) childTiles(col *Column, o *orders.Orders, events []chronicle.Event) {
	if col.Type != orders.Segment && col.Type != orders.ForEach {
		return
	}
	list, err := engine.ChildrenOf(s.Chron, events, col.Node)
	if err != nil || len(list) == 0 {
		return
	}
	var tiles []Tile
	for _, ch := range list {
		t := Tile{Item: ch.Item, Child: ch.ID, Outcome: ch.Outcome, Status: StatusNotRun}
		switch {
		case ch.NotRunNeeds != "":
			t.Note, t.Args = "page.tile.child.why"+"_"+ch.NotRunKind, map[string]string{"item": ch.NotRunNeeds}
		case ch.ID == 0:
			if len(ch.Needs) > 0 {
				t.Note, t.Args = "page.tile.child.needs", map[string]string{"items": strings.Join(ch.Needs, ", ")}
			}
		default:
			driven, _ := engine.Driven(o, ch.ID)
			t.Status, t.Node, t.Note, t.Args = childTileStatus(ch.State, driven)
			t.Attempts = ch.State.Attempts[ch.State.Node]
		}
		tiles = append(tiles, t)
	}
	col.Tiles = tiles
}

// childTileStatus is the status of a child's tile, where the child is, and
// what it waits for as a message key of the page with its parameters.
func childTileStatus(st chronicle.State, driven bool) (status, node, note string, args map[string]string) {
	node = st.Node
	switch {
	case st.End == orders.Done:
		return StatusPassed, "", "", nil
	case st.End == orders.Failed:
		return StatusFailed, "", "", nil
	case st.Error != "":
		return StatusFailed, node, "page.tile.child.error", map[string]string{"error": st.Error}
	case st.Asking():
		return StatusAsking, node, "page.tile.needs_answer", nil
	case st.Halted:
		return StatusWaiting, node, "page.tile.child.halted", nil
	case st.LockWait != "" && !st.Running:
		return StatusWaiting, node, "page.tile.lock", map[string]string{"lock": st.LockWait, "holder": st.LockHolder}
	case st.Waiting == chronicle.WaitOffline:
		return StatusWaiting, node, "page.tile.offline", nil
	case st.Waiting == chronicle.WaitLimit:
		return StatusWaiting, node, "page.tile.limit", nil
	case st.Running && driven:
		return StatusRunning, node, "", nil
	case st.Running:
		return StatusInterrupted, node, "", nil
	}
	return StatusWaiting, node, "", nil
}
