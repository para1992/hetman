package engine

import (
	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// A limit on a transition counts the transitions recorded in the chronicle
// of the campaign, so the count survives a restart. A retry keeps it: it
// runs a failed step again and undoes no transition. A rewind to a node
// undoes every transition taken since the campaign first reached that node
// in the history the rewind keeps, so those no longer count. A transition
// taken instead of a limited one counts for the outcome it was taken on.

// taken counts the transitions of node on outcome that still count.
func taken(events []chronicle.Event, node, outcome string) int {
	type mark struct {
		arrival string // the node reached, for an arrival
		counts  bool   // a transition of node on outcome
	}
	var history []mark
	for _, ev := range events {
		switch ev.Kind {
		case chronicle.CampaignStarted:
			history = append(history, mark{arrival: ev.Node})
		case chronicle.Transition:
			history = append(history, mark{counts: ev.Node == node && ev.Outcome == outcome})
			if !orders.IsEnd(ev.To) {
				history = append(history, mark{arrival: ev.To})
			}
		case chronicle.Rewound:
			for i, m := range history {
				if m.arrival == ev.Node {
					history = history[:i+1]
					break
				}
			}
		}
	}
	n := 0
	for _, m := range history {
		if m.counts {
			n++
		}
	}
	return n
}

// limited returns the outcome whose transition the flow takes when node
// produced outcome: outcome itself, or, when its transition has been taken
// as often as its limit allows, the outcome the limit names instead, and
// so on along the limits of that one.
func limited(node *orders.Node, outcome string, events []chronicle.Event) (string, []limitHit) {
	var hits []limitHit
	seen := map[string]bool{}
	for {
		lim, ok := node.Limits[outcome]
		if !ok || seen[outcome] {
			return outcome, hits
		}
		seen[outcome] = true
		if taken(events, node.ID, outcome) < lim.Max {
			return outcome, hits
		}
		hits = append(hits, limitHit{outcome: outcome, max: lim.Max, then: lim.Then})
		outcome = lim.Then
	}
}

// limitHit is one limit that turned the flow to another outcome.
type limitHit struct {
	outcome, then string
	max           int
}
