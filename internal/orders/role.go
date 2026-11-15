package orders

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Selection modes of a role: how the client of a step is chosen.
const (
	SelectFixed      = "fixed"       // always the first client that can run
	SelectRoundRobin = "round_robin" // the clients take turns, per role across a campaign and its children
	SelectQuota      = "quota"       // the client that has used the smallest share of its allowance
)

// SelectModes lists the selection modes.
var SelectModes = []string{SelectFixed, SelectRoundRobin, SelectQuota}

// Usage windows of a client's allowance.
const (
	WindowFiveHour = "five_hour" // the last five hours
	WindowWeekly   = "weekly"    // the last seven days
	WindowMonthly  = "monthly"   // only in an allowance, converted to a weekly one
)

// Windows lists the usage windows the engine compares, shortest first.
var Windows = []string{WindowFiveHour, WindowWeekly}

// WindowLength is how far back a usage window reaches.
func WindowLength(name string) time.Duration {
	if name == WindowFiveHour {
		return 5 * time.Hour
	}
	return 7 * 24 * time.Hour
}

// MonthToWeek converts an allowance given per month into one per week: a
// year has twelve months and fifty-two weeks.
const MonthToWeek = 12.0 / 52.0

// Role is a pool of clients a step may run on.
type Role struct {
	Name    string
	Select  string
	Clients []*Client
}

// Client is one entry of a role: an executor with its own model and effort.
type Client struct {
	Executor string
	Model    string
	Effort   string
	// Allowance per window (five_hour, weekly), used to estimate the share
	// of a client whose adapter cannot report it. A monthly allowance is
	// stored here converted to a weekly one.
	Allowance map[string]Allowance
}

// Allowance is how much a client may do in one window: a number of
// attempts, or the time attempts may take.
type Allowance struct {
	Attempts float64
	Work     time.Duration
	Monthly  bool   // given per month and converted
	Text     string // as written, with "/monthly" when given per month
}

// Label names a client for a person: its position from 1, then the
// executor and the model, such as "2 (second, model-two)".
func (c *Client) Label(i int) string {
	return ClientLabel(i+1, c.Executor, c.Model)
}

// ClientLabel names the client at position n (from 1) of a role.
func ClientLabel(n int, executor, model string) string {
	if model != "" {
		return fmt.Sprintf("%d (%s, %s)", n, executor, model)
	}
	return fmt.Sprintf("%d (%s)", n, executor)
}

// roles reads the top-level roles.
func (p *parser) roles(n *yaml.Node, o *Orders) {
	entries, ok := p.mapping(n, "roles")
	if !ok {
		return
	}
	for _, e := range entries {
		name, ok := p.name(e.keyN, "role name")
		if !ok {
			continue
		}
		what := fmt.Sprintf("role %q", name)
		fields, ok := p.mapping(e.value, what, "select", "clients")
		if !ok {
			continue
		}
		r := &Role{Name: name}
		for _, f := range fields {
			switch f.key {
			case "select":
				if s, ok := p.scalar(f.value, what+": select"); ok {
					if !contains(SelectModes, s) {
						p.errorf(f.value, "%s: unknown select %q (known: %s)", what, s, strings.Join(SelectModes, ", "))
					} else {
						r.Select = s
					}
				}
			case "clients":
				r.Clients = p.clients(f.value, what, o)
			}
		}
		if !hasKey(e.value, "select") {
			p.errorf(e.keyN, "%s: missing key \"select\" (%s; a role has no default)", what, strings.Join(SelectModes, ", "))
		}
		if !hasKey(e.value, "clients") {
			p.errorf(e.keyN, "%s: missing key \"clients\" (the list of clients the role may run on)", what)
		}
		o.Roles[name] = r
	}
}

// clients reads the client list of a role.
func (p *parser) clients(n *yaml.Node, what string, o *Orders) []*Client {
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		p.errorf(n, "%s: clients must be a non-empty list, such as [{executor: first, model: m1}]", what)
		return nil
	}
	var out []*Client
	seen := map[string]int{}
	for i, item := range n.Content {
		item = resolve(item)
		cw := fmt.Sprintf("%s: client %d", what, i+1)
		fields, ok := p.mapping(item, cw, "executor", "model", "effort", "allowance")
		if !ok {
			continue
		}
		c := &Client{}
		for _, f := range fields {
			switch f.key {
			case "executor":
				if name, ok := p.name(f.value, cw+": executor"); ok {
					if o.Executors[name] == nil {
						p.errorf(f.value, "%s: executor %q is not declared under the top-level executors", cw, name)
					}
					c.Executor = name
				}
			case "model":
				c.Model, _ = p.scalar(f.value, cw+": model")
			case "effort":
				c.Effort, _ = p.scalar(f.value, cw+": effort")
			case "allowance":
				c.Allowance = p.allowance(f.value, cw)
			}
		}
		if !hasKey(item, "executor") {
			p.errorf(item, "%s: missing key \"executor\"", cw)
		}
		key := c.Executor + "\x00" + c.Model + "\x00" + c.Effort
		if j, dup := seen[key]; dup && c.Executor != "" {
			p.errorf(item, "%s repeats client %d (same executor, model and effort)", cw, j)
		}
		seen[key] = i + 1
		out = append(out, c)
	}
	return out
}

// allowance reads what a client may do per window.
func (p *parser) allowance(n *yaml.Node, what string) map[string]Allowance {
	what += ": allowance"
	fields, ok := p.mapping(n, what, WindowFiveHour, WindowWeekly, WindowMonthly)
	if !ok {
		return nil
	}
	if len(fields) == 0 {
		p.errorf(n, "%s must name at least one window: %s, %s or %s", what, WindowFiveHour, WindowWeekly, WindowMonthly)
		return nil
	}
	if hasKey(n, WindowWeekly) && hasKey(n, WindowMonthly) {
		p.errorf(n, "%s: give either %s or %s; a monthly allowance is converted to a weekly one", what, WindowWeekly, WindowMonthly)
		return nil
	}
	out := map[string]Allowance{}
	for _, f := range fields {
		v := f.value
		var a Allowance
		switch {
		case v.Kind == yaml.ScalarNode && v.Tag == "!!int":
			k, err := strconv.Atoi(v.Value)
			if err != nil || k < 1 {
				p.errorf(v, "%s: %s must be a positive number of attempts or a duration such as 3h, got %q", what, f.key, v.Value)
				continue
			}
			a.Attempts = float64(k)
		case v.Kind == yaml.ScalarNode && v.Tag == "!!str":
			d, err := time.ParseDuration(v.Value)
			if err != nil || d <= 0 {
				p.errorf(v, "%s: %s must be a positive number of attempts or a duration such as 3h, got %q", what, f.key, v.Value)
				continue
			}
			a.Work = d
		default:
			p.errorf(v, "%s: %s must be a positive number of attempts or a duration such as 3h, got %q", what, f.key, v.Value)
			continue
		}
		a.Text = v.Value
		window := f.key
		if window == WindowMonthly {
			a.Text += "/" + WindowMonthly
			window, a.Monthly = WindowWeekly, true
			a.Attempts *= MonthToWeek
			a.Work = time.Duration(float64(a.Work) * MonthToWeek)
		}
		out[window] = a
	}
	return out
}

// roleKey reads the role an agent node names.
func (p *parser) roleKey(node *Node, f entry, o *Orders) {
	what := fmt.Sprintf("node %q", node.ID)
	name, ok := p.name(f.value, what+": role")
	if !ok {
		return
	}
	if o.Roles[name] == nil {
		p.errorf(f.value, "%s: role %q is not declared under the top-level roles", what, name)
		return
	}
	node.Role = name
}
