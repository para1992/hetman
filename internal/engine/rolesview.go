package engine

import (
	"sort"
	"time"

	"hetman/internal/adapter"
	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// RoleView is a role as the roles view shows it: its clients with what is
// known of their usage now.
type RoleView struct {
	Name    string       `json:"name"`
	Flow    string       `json:"flow"` // the flow whose orders declare it: the served one or a segment
	Select  string       `json:"select"`
	Clients []ClientView `json:"clients"`
}

// ClientView is one client of a role. Its windows are measured when the
// last choice that asked its adapter recorded a measurement (with the time
// it was taken), else estimated from the attempts seen against the
// allowance, else unknown; a limit that resets later overrides them.
type ClientView struct {
	chronicle.ClientUsage
	Effort    string            `json:"effort,omitempty"`
	Allowance map[string]string `json:"allowance,omitempty"` // per window, as written
}

// RolesView lists the roles of orders and of the segments they run, with
// the usage of their clients at now, from the chronicle shared by every
// campaign of the folder. It asks no adapter anything.
func RolesView(o *orders.Orders, ch *chronicle.Chronicle, now time.Time) ([]RoleView, error) {
	seen, err := attemptsSeen(ch, now)
	if err != nil {
		return nil, err
	}
	m, err := lastMeasured(ch)
	if err != nil {
		return nil, err
	}
	var out []RoleView
	done := map[string]bool{}
	var walk func(o *orders.Orders, depth int)
	walk = func(o *orders.Orders, depth int) {
		if o == nil || done[o.Path] || depth > orders.MaxDepth {
			return
		}
		done[o.Path] = true
		for _, name := range sortedRoles(o) {
			r := o.Roles[name]
			v := RoleView{Name: name, Flow: o.Flow, Select: r.Select}
			for i, c := range r.Clients {
				cv := ClientView{ClientUsage: clientUsage(i, c, seen[c.Executor], m[c.Executor], now, true), Effort: c.Effort}
				for w, a := range c.Allowance {
					if cv.Allowance == nil {
						cv.Allowance = map[string]string{}
					}
					cv.Allowance[w] = a.Text
				}
				v.Clients = append(v.Clients, cv)
			}
			out = append(out, v)
		}
		for _, id := range o.Order {
			walk(o.Nodes[id].Segment, depth+1)
		}
	}
	walk(o, 0)
	return out, nil
}

func sortedRoles(o *orders.Orders) []string {
	var names []string
	for name := range o.Roles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// lastMeasured is, per executor, the latest usage its adapter reported, as
// recorded with the choices of every campaign of the folder.
func lastMeasured(ch *chronicle.Chronicle) (map[string]*measured, error) {
	list, err := ch.OfKinds(chronicle.StepStarted, chronicle.NoClient)
	if err != nil {
		return nil, err
	}
	out := map[string]*measured{}
	for _, ev := range list {
		if ev.Choice == nil {
			continue
		}
		for _, u := range ev.Choice.Usage {
			var m *measured
			for _, w := range u.Windows {
				if w.Source != chronicle.SourceMeasured || w.Used == nil || w.At == nil {
					continue
				}
				if m == nil {
					m = &measured{at: *w.At, usage: &adapter.Usage{}}
				}
				uw := adapter.UsageWindow{Name: w.Name, Used: *w.Used}
				if w.ResetsAt != nil {
					uw.ResetsAt = w.ResetsAt.Format(time.RFC3339Nano)
				}
				m.usage.Windows = append(m.usage.Windows, uw)
			}
			if m != nil {
				out[u.Executor] = m
			}
		}
	}
	return out, nil
}
