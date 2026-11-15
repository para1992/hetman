package engine

import (
	"strconv"
	"strings"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// sender finds the step that really sent the flow back, starting from the
// transition at index at. A script node only passes the flow on when its
// execution in that visit listed no artifacts: it reads the situation and
// picks a way, such as counting the rounds of a loop, but hands nothing to
// the step it leads to. sender looks through such nodes to the transition
// that led to them, as long as the flow reached them by a transition, and
// returns the index of the transition it stops at and the nodes it looked
// through, in the order the flow passed them. Gates, agent, human, segment
// and for-each nodes are never looked through.
func sender(o *orders.Orders, events []chronicle.Event, at int) (int, []string) {
	var via []string
	for len(via) < len(events) {
		t := events[at]
		n := o.Nodes[t.Node]
		if t.Kind != chronicle.Transition || n == nil || n.Type != orders.Script {
			break
		}
		fin := lastFinished(events[:at], t.Node)
		if fin == nil || len(fin.Artifacts) > 0 {
			break
		}
		prev := visitStart(events[:at], t.Node) - 1
		if prev < 0 || events[prev].Kind != chronicle.Transition {
			break
		}
		via = append([]string{t.Node}, via...)
		at = prev
	}
	return at, via
}

// artifactsOf lists the artifacts of an execution: those its result file
// listed, and for a gate those the result files of its checks listed in
// that attempt, in the order the checks ran, each name once. All of them are
// files in the one work folder of the campaign, so a name two checks list
// is one file, as the later check left it.
func artifactsOf(events []chronicle.Event, fin *chronicle.Event) []string {
	out := append([]string{}, fin.Artifacts...)
	seen := map[string]bool{}
	for _, a := range out {
		seen[a] = true
	}
	for _, ev := range events {
		if ev.Kind == chronicle.CheckFinished && ev.Node == fin.Node && ev.Attempt == fin.Attempt {
			for _, a := range ev.Artifacts {
				if !seen[a] {
					seen[a] = true
					out = append(out, a)
				}
			}
		}
	}
	return out
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ", ")
}
