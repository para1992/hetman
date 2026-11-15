package explain

import (
	"strconv"
	"strings"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/i18n"
	"hetman/internal/orders"
)

// ClientName names a client of a choice for a person: its position in the
// role, the executor and the model.
func ClientName(c *chronicle.Choice, n int) string {
	for _, u := range c.Usage {
		if u.Client == n {
			return orders.ClientLabel(n, u.Executor, u.Model)
		}
	}
	return strconv.Itoa(n)
}

// Why says why a client was chosen, in the language of p.
func Why(p *i18n.Printer, c *chronicle.Choice) string {
	return p.T("explain.chose." + c.Reason)
}

// UsageWords says what was known of each client of a choice: the share of
// each window and where it comes from, and which clients could not run and
// until when. Times are shown in loc.
func UsageWords(p *i18n.Printer, c *chronicle.Choice, loc *time.Location) string {
	var parts []string
	for _, u := range c.Usage {
		var ws []string
		for _, w := range u.Windows {
			if w.Used == nil {
				ws = append(ws, p.T("explain.usage.window_unknown", "window", w.Name))
				continue
			}
			ws = append(ws, p.T("explain.usage.window", "window", w.Name, "used", Percent(*w.Used), "source", p.T("explain.source."+w.Source)))
		}
		switch {
		case u.Exhausted && u.Until != nil:
			ws = append(ws, p.T("explain.usage.exhausted_until", "until", u.Until.In(loc).Format("2006-01-02 15:04:05")))
		case u.Exhausted:
			ws = append(ws, p.T("explain.usage.exhausted"))
		case len(ws) == 0:
			ws = append(ws, p.T("explain.usage.can_run"))
		}
		parts = append(parts, p.T("explain.usage.client", "client", ClientName(c, u.Client), "windows", strings.Join(ws, ", ")))
	}
	return strings.Join(parts, "; ")
}

// Percent writes a share as a whole percentage.
func Percent(share float64) string {
	return strconv.FormatFloat(share*100, 'f', 0, 64) + "%"
}

// choice says how the client of an attempt was chosen.
func (x *explainer) choice(e *chronicle.Event) string {
	c := e.Choice
	s := x.p.T("explain.client", "node", e.Node, "attempt", e.Attempt, "client", ClientName(c, c.Client), "role", c.Role, "mode", c.Mode, "why", Why(x.p, c))
	if c.From > 0 {
		s += x.p.T("explain.client_from", "from", ClientName(c, c.From))
	}
	if c.Reason == chronicle.ChoseQuota {
		s += x.p.T("explain.client_compared", "usage", UsageWords(x.p, c, x.loc))
	}
	return s
}

// noClient says that no client of a role could run a step.
func (x *explainer) noClient(e *chronicle.Event) string {
	role, usage := "", ""
	if e.Choice != nil {
		role, usage = e.Choice.Role, UsageWords(x.p, e.Choice, x.loc)
	}
	if e.WaitUntil != nil {
		return x.p.T("explain.no_client_until", "node", e.Node, "role", role, "usage", usage, "until", x.at(*e.WaitUntil))
	}
	return x.p.T("explain.no_client", "node", e.Node, "role", role, "usage", usage)
}
