package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/orders"
)

type nodeView struct {
	attempts    int
	interrupted int
	attempt     int // latest attempt
	running     bool
	outcome     string
	err         string
	millis      int64
	artifacts   []string
	checks      map[string]chronicle.Event // of the latest attempt
}

func (c *command) status(id int64, listEvents bool) int {
	k, err := c.campaign(id)
	if err != nil {
		return c.fail(err)
	}
	events, err := c.chron.Events(k.ID)
	if err != nil {
		return c.fail(err)
	}
	st := chronicle.Replay(events)
	views := map[string]*nodeView{}
	view := func(n string) *nodeView {
		if views[n] == nil {
			views[n] = &nodeView{}
		}
		return views[n]
	}
	for _, e := range events {
		switch e.Kind {
		case chronicle.StepStarted:
			v := view(e.Node)
			v.attempts++
			v.attempt, v.running, v.outcome, v.err, v.artifacts = e.Attempt, true, "", "", nil
			v.checks = map[string]chronicle.Event{}
		case chronicle.StepInterrupted:
			v := view(e.Node)
			v.running = false
			v.interrupted++
		case chronicle.StepFinished:
			v := view(e.Node)
			v.running, v.outcome, v.err, v.millis, v.artifacts = false, e.Outcome, e.Error, e.Millis, e.Artifacts
		case chronicle.CheckFinished:
			view(e.Node).checks[e.Check] = e
		}
	}

	w := c.stdout
	fmt.Fprintf(w, "campaign %d  flow %s  started %s\n", k.ID, k.Flow, k.Started.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "state: %s\n", c.describe(k.ID, st))

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  NODE\tTYPE\tSTATUS\tOUTCOME\tATTEMPTS\tTIME\t")
	order := append([]string{}, c.o.Order...)
	for n := range views {
		if c.o.Nodes[n] == nil {
			order = append(order, n)
		}
	}
	for _, n := range order {
		typ := "?"
		var node *orders.Node
		if node = c.o.Nodes[n]; node != nil {
			typ = node.Type
		}
		v := views[n]
		marker := " "
		if n == st.Node && st.End == "" {
			marker = ">"
		}
		if v == nil {
			fmt.Fprintf(tw, "%s %s\t%s\tpending\t\t\t\t\n", marker, n, typ)
			continue
		}
		status, outcome, dur := "finished", v.outcome, ""
		switch {
		case v.running && st.Running && st.Node == n:
			status, outcome = "running", ""
		case v.running:
			status = "interrupted"
		case v.err != "":
			status, outcome = "error", v.err
		}
		if v.millis > 0 || status == "finished" {
			dur = (time.Duration(v.millis) * time.Millisecond).String()
		}
		attempts := fmt.Sprint(v.attempts)
		if v.interrupted > 0 {
			attempts += fmt.Sprintf(" (%d interrupted)", v.interrupted)
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\t%s\t\n", marker, n, typ, status, outcome, attempts, dur)
		if node != nil {
			for _, name := range node.Checks {
				fmt.Fprintf(tw, "    check %s\t%s\t\n", name, checkLine(v.checks, name, c.o.Checks[name]))
			}
		}
	}
	tw.Flush()
	for _, n := range order {
		if v := views[n]; v != nil && len(v.artifacts) > 0 {
			fmt.Fprintf(w, "artifacts of %s: %s\n", n, strings.Join(v.artifacts, ", "))
		}
	}

	var warnings []string
	for _, e := range events {
		if e.Kind == chronicle.CheckFinished && e.Soft && e.Outcome != orders.OK && e.Error == "" {
			warnings = append(warnings, fmt.Sprintf("soft check %s of %s (attempt %d): %s", e.Check, e.Node, e.Attempt, e.Outcome))
		}
	}
	for _, s := range warnings {
		fmt.Fprintln(w, "warning:", s)
	}
	if listEvents {
		fmt.Fprintln(w, "chronicle:")
		for _, e := range events {
			fmt.Fprintf(w, "  %4d %s %s\n", e.Seq, e.At.Local().Format("15:04:05.000"), eventLine(e))
		}
	}
	return exitDone
}

// checkLine renders the TYPE to TIME cells of a check row.
func checkLine(results map[string]chronicle.Event, name string, def *orders.Check) string {
	kind := "required"
	if def != nil && def.Soft {
		kind = "soft"
	}
	e, ok := results[name]
	dur := (time.Duration(e.Millis) * time.Millisecond).String()
	switch {
	case !ok:
		return kind + "\tnot run\t\t\t"
	case e.Error != "":
		return kind + "\terror\t" + e.Error + "\t\t" + dur
	case e.Outcome == orders.OK:
		return kind + "\tfinished\tok\t\t" + dur
	case e.Soft:
		return kind + "\tfinished\t" + e.Outcome + " (warning)\t\t" + dur
	default:
		return kind + "\tfinished\t" + e.Outcome + " (blocked the gate)\t\t" + dur
	}
}

// describe summarises the campaign state. Whether a process is driving the
// campaign is learned by trying its lock.
func (c *command) describe(id int64, st chronicle.State) string {
	switch {
	case st.End != "":
		return st.End
	case st.Error != "":
		return "error: " + st.Error + " (fix the cause, then hetman resume)"
	}
	alive := false
	if release, err := engine.Lock(c.o, id); err == nil {
		release()
	} else if errors.Is(err, engine.ErrLocked) {
		alive = true
	}
	where := "at " + st.Node
	if st.Running {
		where = fmt.Sprintf("running %s (attempt %d)", st.Node, st.Attempt)
	}
	switch {
	case st.Halted:
		return fmt.Sprintf("halted before %s (hetman resume to continue)", st.Node)
	case st.HaltRequested && alive:
		return where + ", halt requested: halts after the current step"
	case alive:
		return where
	case st.Running:
		return where + ", but no hetman process is running it: interrupted (hetman resume to continue)"
	default:
		return where + ", no hetman process is running it (hetman resume to continue)"
	}
}

func eventLine(e chronicle.Event) string {
	parts := []string{e.Kind}
	if e.Node != "" {
		parts = append(parts, e.Node)
	}
	if e.Check != "" {
		parts = append(parts, "check="+e.Check)
	}
	if e.Attempt > 0 {
		parts = append(parts, fmt.Sprintf("attempt=%d", e.Attempt))
	}
	if e.Outcome != "" {
		parts = append(parts, "outcome="+e.Outcome)
	}
	if e.To != "" {
		parts = append(parts, "to="+e.To)
	}
	if e.Soft {
		parts = append(parts, "soft")
	}
	if e.Pid != 0 {
		parts = append(parts, fmt.Sprintf("pid=%d", e.Pid))
	}
	if e.Ident != "" {
		parts = append(parts, "started="+e.Ident)
	}
	if e.Exit != nil {
		parts = append(parts, fmt.Sprintf("exit=%d", *e.Exit))
	}
	if e.Error != "" {
		parts = append(parts, fmt.Sprintf("error=%q", e.Error))
	}
	return strings.Join(parts, " ")
}
