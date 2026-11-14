// Package cli implements the hetman command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/orders"
)

// Exit codes.
const (
	exitDone        = 0
	exitFailed      = 1 // campaign reached the failed end, or a command failed
	exitUsage       = 2 // bad arguments or rejected orders
	exitHalted      = 3
	exitError       = 4 // campaign stopped in the error state
	exitInterrupted = 130
)

const usage = `usage: hetman <command> [flags]

commands:
  check    check the orders without running anything
  run      start a new campaign and run it
  status   show where a campaign is
  halt     halt a campaign after its current step
  resume   continue a halted, stopped or interrupted campaign

flags (all commands):
  -f FILE  orders file (default orders.yaml)
flags (status, halt, resume):
  -c ID    campaign id (default: the latest campaign)
flags (status):
  -events  also list every chronicle event

exit codes of run and resume: 0 done, 1 failed, 3 halted, 4 error, 130 interrupted
`

// Main runs the command line and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("hetman "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, "orders file")
	var campaign *int64
	var events *bool
	switch cmd {
	case "check", "run":
	case "status", "halt", "resume":
		campaign = fs.Int64("c", 0, "campaign id (default: latest)")
		if cmd == "status" {
			events = fs.Bool("events", false, "list every chronicle event")
		}
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitDone
	default:
		fmt.Fprintf(stderr, "hetman: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "hetman %s: unexpected argument %q\n", cmd, fs.Arg(0))
		return exitUsage
	}
	o, err := orders.Load(*file)
	if err != nil {
		var ps orders.Problems
		if errors.As(err, &ps) {
			fmt.Fprintln(stderr, ps.Error())
			fmt.Fprintf(stderr, "orders rejected: %d problem(s)\n", len(ps))
		} else {
			fmt.Fprintln(stderr, "hetman:", err)
		}
		return exitUsage
	}
	if cmd == "check" {
		fmt.Fprintf(stdout, "%s: ok (flow %s, %d nodes, %d checks)\n", o.Path, o.Flow, len(o.Nodes), len(o.Checks))
		return exitDone
	}
	if err := os.MkdirAll(engine.StateDir(o), 0o755); err != nil {
		fmt.Fprintln(stderr, "hetman:", err)
		return exitFailed
	}
	chron, err := chronicle.Open(engine.ChroniclePath(o))
	if err != nil {
		fmt.Fprintln(stderr, "hetman:", err)
		return exitFailed
	}
	defer chron.Close()
	c := &command{o: o, chron: chron, stdout: stdout, stderr: stderr}
	switch cmd {
	case "run":
		return c.run()
	case "status":
		return c.status(*campaign, *events)
	case "halt":
		return c.halt(*campaign)
	default:
		return c.resume(*campaign)
	}
}

type command struct {
	o      *orders.Orders
	chron  *chronicle.Chronicle
	stdout io.Writer
	stderr io.Writer
}

func (c *command) fail(err error) int {
	fmt.Fprintln(c.stderr, "hetman:", err)
	return exitFailed
}

func (c *command) campaign(id int64) (chronicle.Campaign, error) {
	if id == 0 {
		k, err := c.chron.Latest()
		if errors.Is(err, chronicle.ErrNoCampaign) {
			return k, errors.New("no campaigns yet; start one with hetman run")
		}
		return k, err
	}
	return c.chron.Campaign(id)
}

func (c *command) state(id int64) (chronicle.State, error) {
	events, err := c.chron.Events(id)
	if err != nil {
		return chronicle.State{}, err
	}
	return chronicle.Replay(events), nil
}

func (c *command) run() int {
	id, err := c.chron.NewCampaign(c.o.Flow, c.o.Path, c.o.Start)
	if err != nil {
		return c.fail(err)
	}
	release, err := engine.Lock(c.o, id)
	if err != nil {
		return c.fail(err)
	}
	defer release()
	fmt.Fprintf(c.stdout, "campaign %d started (flow %s)\n", id, c.o.Flow)
	return c.drive(id)
}

func (c *command) drive(id int64) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := engine.ContainChildren(); err != nil {
		fmt.Fprintf(c.stderr, "hetman: step processes are not tied to this process (%v); they are cleaned up on resume instead\n", err)
	}
	e := &engine.Engine{Orders: c.o, Chron: c.chron, Campaign: id, Out: c.stdout}
	res, err := e.Drive(ctx)
	switch {
	case errors.Is(err, engine.ErrInterrupted):
		fmt.Fprintf(c.stderr, "campaign %d interrupted; the current step runs again on hetman resume -c %d\n", id, id)
		return exitInterrupted
	case err != nil:
		return c.fail(err)
	}
	switch res {
	case engine.Done:
		return exitDone
	case engine.Failed:
		return exitFailed
	case engine.Halted:
		fmt.Fprintf(c.stdout, "campaign %d halted; continue with hetman resume -c %d\n", id, id)
		return exitHalted
	default:
		fmt.Fprintf(c.stdout, "campaign %d stopped in the error state; fix the cause and run hetman resume -c %d\n", id, id)
		return exitError
	}
}

func (c *command) halt(id int64) int {
	k, err := c.campaign(id)
	if err != nil {
		return c.fail(err)
	}
	st, err := c.state(k.ID)
	if err != nil {
		return c.fail(err)
	}
	switch {
	case st.End != "":
		return c.fail(fmt.Errorf("campaign %d has already ended (%s)", k.ID, st.End))
	case st.HaltRequested:
		fmt.Fprintf(c.stdout, "campaign %d is already halted\n", k.ID)
		return exitDone
	}
	if _, err := c.chron.Append(chronicle.Event{Campaign: k.ID, Kind: chronicle.HaltRequested, Node: st.Node}); err != nil {
		return c.fail(err)
	}
	release, err := engine.Lock(c.o, k.ID)
	if errors.Is(err, engine.ErrLocked) {
		fmt.Fprintf(c.stdout, "campaign %d will halt after its current step\n", k.ID)
		return exitDone
	}
	if err != nil {
		return c.fail(err)
	}
	defer release()
	// No process is driving the campaign, so the halt takes effect now.
	if _, err := c.chron.Append(chronicle.Event{Campaign: k.ID, Kind: chronicle.Halted, Node: st.Node}); err != nil {
		return c.fail(err)
	}
	fmt.Fprintf(c.stdout, "campaign %d halted at %s\n", k.ID, st.Node)
	return exitDone
}

func (c *command) resume(id int64) int {
	k, err := c.campaign(id)
	if err != nil {
		return c.fail(err)
	}
	st, err := c.state(k.ID)
	if err != nil {
		return c.fail(err)
	}
	if st.End != "" {
		return c.fail(fmt.Errorf("campaign %d has already ended (%s)", k.ID, st.End))
	}
	if _, err := c.chron.Append(chronicle.Event{Campaign: k.ID, Kind: chronicle.Resumed, Node: st.Node}); err != nil {
		return c.fail(err)
	}
	release, err := engine.Lock(c.o, k.ID)
	if errors.Is(err, engine.ErrLocked) {
		fmt.Fprintf(c.stdout, "campaign %d is being run by another process; it continues there\n", k.ID)
		return exitDone
	}
	if err != nil {
		return c.fail(err)
	}
	defer release()
	fmt.Fprintf(c.stdout, "campaign %d resumed at %s\n", k.ID, st.Node)
	return c.drive(k.ID)
}
