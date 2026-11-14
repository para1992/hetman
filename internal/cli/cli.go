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
	"path/filepath"
	"syscall"

	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/orders"
	"hetman/internal/scribe"
)

// Version is the version of this build, set by the release script with
// -ldflags "-X hetman/internal/cli.Version=...".
var Version = "dev"

// DefaultScribeDir is where scribe writes the setup assistant skill unless
// told otherwise, relative to the current folder.
const DefaultScribeDir = ".agents/skills/hetman-scribe"

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
  serve    show every campaign live on a web page (Watchtower)
  scribe   write the setup assistant skill for a coding agent
  version  print the version of hetman

flags (check, run, status, halt, resume, serve):
  -f FILE  orders file (default orders.yaml)
flags (status, halt, resume):
  -c ID    campaign id (default: the latest campaign)
flags (status):
  -events  also list every chronicle event
flags (serve):
  -addr ADDRESS  where to listen (default 127.0.0.1:8740, reachable only from this computer)
flags (scribe):
  -dir DIR  folder for the skill file (default .agents/skills/hetman-scribe)
  -force    replace a different skill file that is already there

exit codes of run and resume: 0 done, 1 failed, 3 halted, 4 error, 130 interrupted
`

// Main runs the command line and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "scribe":
		return writeScribe(args, stdout, stderr)
	case "version":
		return version(args, stdout, stderr)
	}
	fs := flag.NewFlagSet("hetman "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, "orders file")
	var campaign *int64
	var events *bool
	var addr *string
	switch cmd {
	case "check", "run":
	case "serve":
		addr = fs.String("addr", DefaultAddr, "address to listen on")
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
	// The commands that start steps look for their programs first, so that
	// a missing script is reported before any step with side effects runs.
	// Status, halt and serve work on a campaign even when a program is gone.
	if cmd == "check" || cmd == "run" || cmd == "resume" {
		errs, warnings := o.CheckPrograms()
		if len(warnings) > 0 {
			fmt.Fprintln(stderr, warnings.Error())
		}
		if len(errs) > 0 {
			fmt.Fprintln(stderr, errs.Error())
			fmt.Fprintf(stderr, "orders rejected: %d problem(s)\n", len(errs))
			return exitUsage
		}
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
	case "serve":
		return c.serve(*addr)
	default:
		return c.resume(*campaign)
	}
}

// writeScribe writes the setup assistant skill into a folder.
func writeScribe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hetman scribe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", DefaultScribeDir, "folder for the skill file")
	force := fs.Bool("force", false, "replace a different skill file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "hetman scribe: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	path, changed, err := scribe.Write(filepath.FromSlash(*dir), *force)
	switch {
	case errors.Is(err, scribe.ErrExists):
		fmt.Fprintf(stderr, "hetman scribe: %s: a different file already exists; use -force to replace it\n", path)
		return exitFailed
	case err != nil:
		fmt.Fprintln(stderr, "hetman scribe:", err)
		return exitFailed
	case changed:
		fmt.Fprintf(stdout, "wrote %s\n", path)
	default:
		fmt.Fprintf(stdout, "%s is up to date\n", path)
	}
	return exitDone
}

func version(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "hetman version: unexpected argument %q\n", args[0])
		return exitUsage
	}
	fmt.Fprintf(stdout, "hetman %s\n", Version)
	return exitDone
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
	result, st, err := engine.Halt(c.o, c.chron, k.ID)
	if err != nil {
		return c.fail(err)
	}
	switch result {
	case engine.HaltAlready:
		fmt.Fprintf(c.stdout, "campaign %d is already halted\n", k.ID)
	case engine.HaltPending:
		fmt.Fprintf(c.stdout, "campaign %d will halt after its current step\n", k.ID)
	default:
		fmt.Fprintf(c.stdout, "campaign %d halted at %s\n", k.ID, st.Node)
	}
	return exitDone
}

func (c *command) resume(id int64) int {
	k, err := c.campaign(id)
	if err != nil {
		return c.fail(err)
	}
	release, st, err := engine.Resume(c.o, c.chron, k.ID)
	if err != nil {
		return c.fail(err)
	}
	if release == nil {
		fmt.Fprintf(c.stdout, "campaign %d is being run by another process; it continues there\n", k.ID)
		return exitDone
	}
	defer release()
	fmt.Fprintf(c.stdout, "campaign %d resumed at %s\n", k.ID, st.Node)
	return c.drive(k.ID)
}
