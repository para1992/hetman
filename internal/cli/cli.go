// Package cli implements the hetman command line.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	"hetman/internal/language"
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
	exitWaiting     = 5 // an agent step hit a subscription limit or lost connectivity
	exitInterrupted = 130
)

const usage = `usage: hetman <command> [flags]

commands:
  check    check the orders without running anything
  run      start a new campaign and run it
  status   show where a campaign is
  halt     halt a campaign after its current step
  resume   continue a halted, stopped or interrupted campaign
  answer   answer the questions a campaign asks a person
  serve    show every campaign live on a web page (Watchtower)
  task     queue internal tasks, list, show and cancel them, check the routing
           file (hetman task help lists it all)
  dispatch run a campaign of the routed flow for each queued task
           (hetman dispatch -h lists its flags)
  scribe   set the flow up: a wizard, operations on the orders file, a
           sentence through a model, campaigns explained in plain words
           (hetman scribe help lists it all)
  version  print the version of hetman

flags (check, run, status, halt, resume, serve):
  -f FILE  orders file (default orders.yaml)
flags (run):
  -input FILE  JSON file with the run input, an object prompt templates can refer to
flags (status, halt, resume, answer):
  -c ID    campaign id (default: the latest campaign)
flags (answer; without -a, -comment and -outcome it asks in the terminal):
  -a ID=ANSWER       answer one question (yes or no, an option, or text); repeat for each
  -comment ID=TEXT   comment on one answer; repeat as needed
  -outcome NAME      the outcome chosen at a human node
  -ask N             answer only if question #N is still the open one
  -by NAME           who answers (default: the user name of this computer)
flags (status):
  -events  also list every chronicle event
flags (serve):
  -addr ADDRESS  where to listen (default 127.0.0.1:8740, reachable only from this computer)
flags (scribe skill):
  -dir DIR  folder for the skill file (default .agents/skills/hetman-scribe)
  -force    replace a different skill file that is already there

environment (check, run):
  HETMAN_LANGUAGE=CODE  the language for the person, overriding the language key of the orders
                        for campaigns started now; a campaign keeps the language it started with

exit codes of run and resume: 0 done, 1 failed, 3 halted, 4 error, 5 waiting for a person, 130 interrupted
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
		return scribeCmd(args, stdout, stderr)
	case "version":
		return version(args, stdout, stderr)
	case "task":
		return taskCmd(args, stdout, stderr)
	case "dispatch":
		return dispatchCmd(args, stdout, stderr)
	}
	fs := flag.NewFlagSet("hetman "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, "orders file")
	var campaign *int64
	var events *bool
	var addr, input *string
	var answers *answerFlags
	switch cmd {
	case "check":
	case "run":
		input = fs.String("input", "", "JSON file with the run input")
	case "serve":
		addr = fs.String("addr", DefaultAddr, "address to listen on")
	case "status", "halt", "resume", "answer":
		campaign = fs.Int64("c", 0, "campaign id (default: latest)")
		if cmd == "status" {
			events = fs.Bool("events", false, "list every chronicle event")
		}
		if cmd == "answer" {
			answers = addAnswerFlags(fs)
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
	var lang string
	if cmd == "check" || cmd == "run" {
		if lang, err = languageFor(o); err != nil {
			fmt.Fprintf(stderr, "hetman %s: %v\n", cmd, err)
			return exitUsage
		}
	}
	if cmd == "check" {
		fmt.Fprintf(stdout, "%s: ok (flow %s, %d nodes, %d checks%s)\n", o.Path, o.Flow, len(o.Nodes), len(o.Checks), languageNote(lang))
		return exitDone
	}
	var runInput json.RawMessage
	if input != nil && *input != "" {
		if runInput, err = readInput(*input); err != nil {
			fmt.Fprintf(stderr, "hetman run: -input %s: %v\n", *input, err)
			return exitUsage
		}
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
		return c.run(runInput, lang)
	case "status":
		return c.status(*campaign, *events)
	case "halt":
		return c.halt(*campaign)
	case "serve":
		return c.serve(*addr)
	case "answer":
		return c.answer(*campaign, answers)
	default:
		return c.resume(*campaign)
	}
}

// writeScribe writes the setup assistant skill into a folder.
func writeScribe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hetman scribe skill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", DefaultScribeDir, "folder for the skill file")
	force := fs.Bool("force", false, "replace a different skill file")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "hetman scribe skill: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	path, changed, err := scribe.Write(filepath.FromSlash(*dir), *force)
	switch {
	case errors.Is(err, scribe.ErrExists):
		fmt.Fprintf(stderr, "hetman scribe skill: %s: a different file already exists; use -force to replace it\n", path)
		return exitFailed
	case err != nil:
		fmt.Fprintln(stderr, "hetman scribe skill:", err)
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
		c.ordersOf(k)
		return k, err
	}
	k, err := c.chron.Campaign(id)
	if err == nil {
		c.ordersOf(k)
	}
	return k, err
}

// ordersOf makes the command work with the orders file a campaign was
// started from, when it is another flow in the same folder than the one
// given with -f, so that it is driven and shown by its own nodes.
func (c *command) ordersOf(k chronicle.Campaign) {
	if k.Orders == "" || k.Orders == c.o.Path || filepath.Dir(k.Orders) != c.o.Dir {
		return
	}
	o, err := orders.Load(k.Orders)
	if err != nil {
		fmt.Fprintf(c.stderr, "hetman: campaign %d was started from %s, which does not load (%v); using %s\n", k.ID, k.Orders, err, c.o.Path)
		return
	}
	c.o = o
}

func (c *command) state(id int64) (chronicle.State, error) {
	events, err := c.chron.Events(id)
	if err != nil {
		return chronicle.State{}, err
	}
	return chronicle.Replay(events), nil
}

// readInput reads a run input file: one JSON object.
func readInput(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("must hold one JSON object: %v", err)
	}
	if obj == nil {
		return nil, errors.New("must hold one JSON object, not null")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

// languageEnv names the variable a person sets to override the language
// for the person without editing the orders.
const languageEnv = "HETMAN_LANGUAGE"

// languageFor is the code of the language a campaign started now uses: the
// one in the environment variable when it is set, else the one in the
// orders, else none.
func languageFor(o *orders.Orders) (string, error) {
	code := os.Getenv(languageEnv)
	if code == "" {
		return o.Language, nil
	}
	if _, ok := language.Lookup(code); !ok {
		return "", fmt.Errorf("%s: %s", languageEnv, language.Unknown(code))
	}
	return code, nil
}

// languageNote names a campaign's language for the person after other
// facts; it is empty when there is none.
func languageNote(code string) string {
	l, ok := language.Lookup(code)
	if !ok {
		return ""
	}
	return fmt.Sprintf(", language %s %s", l.Code, l.Label())
}

func (c *command) run(input json.RawMessage, lang string) int {
	id, err := c.chron.NewCampaign(c.o.Flow, c.o.Path, c.o.Start, input, lang)
	if err != nil {
		return c.fail(err)
	}
	release, err := engine.Lock(c.o, id)
	if err != nil {
		return c.fail(err)
	}
	defer release()
	fmt.Fprintf(c.stdout, "campaign %d started (flow %s%s)\n", id, c.o.Flow, languageNote(lang))
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
	e.WaitHooks()
	switch {
	case errors.Is(err, engine.ErrInterrupted):
		if asked, _, _ := engine.Pending(c.chron, id); asked != nil {
			fmt.Fprintf(c.stderr, "campaign %d still waits for a person at %s; answer with %s, then continue with %s\n",
				id, asked.Node, engine.AnswerCommand(c.o, id), engine.ResumeCommand(c.o, id))
			return exitInterrupted
		}
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
	case engine.Waiting:
		fmt.Fprintf(c.stdout, "campaign %d waits for a person; when the cause is gone, run hetman resume -c %d\n", id, id)
		return exitWaiting
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
