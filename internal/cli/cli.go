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
	"strconv"
	"syscall"

	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/i18n"
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

// usage is the help of the command line.
func usage(p *i18n.Printer) string { return p.T("cli.usage") }

// Main runs the command line and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	// Until the orders are read, only the person's own choice is known.
	p := personPrinter(nil)
	if len(args) == 0 {
		fmt.Fprint(stderr, usage(p))
		return exitUsage
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "scribe":
		return scribeCmd(args, stdout, stderr)
	case "version":
		return version(p, args, stdout, stderr)
	case "task":
		return taskCmd(args, stdout, stderr)
	case "dispatch":
		return dispatchCmd(args, stdout, stderr)
	}
	fs := flag.NewFlagSet("hetman "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", orders.DefaultFile, p.T("flag.f"))
	var campaign *int64
	var events *bool
	var addr, input *string
	var answers *answerFlags
	switch cmd {
	case "check":
	case "run":
		input = fs.String("input", "", p.T("flag.input"))
	case "serve":
		addr = fs.String("addr", DefaultAddr, p.T("flag.addr"))
	case "status", "halt", "resume", "answer":
		campaign = fs.Int64("c", 0, p.T("flag.c"))
		if cmd == "status" {
			events = fs.Bool("events", false, p.T("flag.events"))
		}
		if cmd == "answer" {
			answers = addAnswerFlags(p, fs)
		}
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage(p))
		return exitDone
	default:
		fmt.Fprintf(stderr, "%s\n\n%s", p.T("cli.unknown_command", "command", strconv.Quote(cmd)), usage(p))
		return exitUsage
	}
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, p.T("cli.unexpected_argument", "command", cmd, "arg", strconv.Quote(fs.Arg(0))))
		return exitUsage
	}
	o, err := orders.Load(*file)
	if err != nil {
		var ps orders.Problems
		if errors.As(err, &ps) {
			fmt.Fprintln(stderr, ps.Error())
			fmt.Fprintln(stderr, p.T("cli.orders_rejected", "n", len(ps)))
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
			fmt.Fprintln(stderr, p.T("cli.orders_rejected", "n", len(errs)))
			return exitUsage
		}
	}
	p = personPrinter(o)
	var lang string
	if cmd == "check" || cmd == "run" {
		if lang, err = languageFor(o); err != nil {
			fmt.Fprintf(stderr, "hetman %s: %v\n", cmd, err)
			return exitUsage
		}
	}
	if cmd == "check" {
		fmt.Fprintln(stdout, p.T("cli.check_ok", "path", o.Path, "flow", o.Flow, "nodes", len(o.Nodes), "checks", len(o.Checks), "language", languageNote(p, lang)))
		return exitDone
	}
	var runInput json.RawMessage
	if input != nil && *input != "" {
		if runInput, err = readInput(p, *input); err != nil {
			fmt.Fprintln(stderr, p.T("cli.bad_input", "file", *input, "error", err))
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
	c := &command{o: o, chron: chron, stdout: stdout, stderr: stderr, p: p}
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
func writeScribe(p *i18n.Printer, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hetman scribe skill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", DefaultScribeDir, p.T("flag.dir"))
	force := fs.Bool("force", false, p.T("flag.skill_force"))
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, p.T("cli.unexpected_argument", "command", "scribe skill", "arg", strconv.Quote(fs.Arg(0))))
		return exitUsage
	}
	path, changed, err := scribe.Write(filepath.FromSlash(*dir), *force)
	switch {
	case errors.Is(err, scribe.ErrExists):
		fmt.Fprintln(stderr, p.T("cli.skill_exists", "path", path))
		return exitFailed
	case err != nil:
		fmt.Fprintln(stderr, "hetman scribe skill:", err)
		return exitFailed
	case changed:
		fmt.Fprintln(stdout, p.T("cli.skill_wrote", "path", path))
	default:
		fmt.Fprintln(stdout, p.T("cli.skill_up_to_date", "path", path))
	}
	return exitDone
}

func version(p *i18n.Printer, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, p.T("cli.unexpected_argument", "command", "version", "arg", strconv.Quote(args[0])))
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
	p      *i18n.Printer // the language of the person reading
}

func (c *command) fail(err error) int {
	fmt.Fprintln(c.stderr, "hetman:", err)
	return exitFailed
}

func (c *command) campaign(id int64) (chronicle.Campaign, error) {
	if id == 0 {
		k, err := c.chron.Latest()
		if errors.Is(err, chronicle.ErrNoCampaign) {
			return k, errors.New(c.p.T("cli.no_campaigns"))
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
		fmt.Fprintln(c.stderr, c.p.T("cli.orders_of_failed", "id", k.ID, "orders", k.Orders, "error", err, "used", c.o.Path))
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
func readInput(p *i18n.Printer, path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, errors.New(p.T("cli.input_not_object", "error", err))
	}
	if obj == nil {
		return nil, errors.New(p.T("cli.input_null"))
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

// personPrinter speaks the person's language: the one in the environment
// variable, else the one in the orders, else English. An unknown code in
// the variable is reported by check and run; the interface falls back.
func personPrinter(o *orders.Orders) *i18n.Printer {
	var fromOrders string
	if o != nil {
		fromOrders = o.Language
	}
	return i18n.For(i18n.Resolve(os.Getenv(languageEnv), fromOrders))
}

// languageNote names a campaign's language for the person after other
// facts; it is empty when there is none.
func languageNote(p *i18n.Printer, code string) string {
	l, ok := language.Lookup(code)
	if !ok {
		return ""
	}
	return p.T("cli.language_note", "code", l.Code, "label", l.Label())
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
	fmt.Fprintln(c.stdout, c.p.T("cli.started", "id", id, "flow", c.o.Flow, "language", languageNote(c.p, lang)))
	return c.drive(id)
}

func (c *command) drive(id int64) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := engine.ContainChildren(); err != nil {
		fmt.Fprintln(c.stderr, c.p.T("cli.not_contained", "error", err))
	}
	e := &engine.Engine{Orders: c.o, Chron: c.chron, Campaign: id, Out: c.stdout, Text: c.p}
	res, err := e.Drive(ctx)
	e.WaitHooks()
	switch {
	case errors.Is(err, engine.ErrInterrupted):
		if asked, _, _ := engine.Pending(c.chron, id); asked != nil {
			fmt.Fprintln(c.stderr, c.p.T("cli.interrupted_asking", "id", id, "node", asked.Node,
				"answer", engine.AnswerCommand(c.o, id), "resume", engine.ResumeCommand(c.o, id)))
			return exitInterrupted
		}
		fmt.Fprintln(c.stderr, c.p.T("cli.interrupted", "id", id))
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
		fmt.Fprintln(c.stdout, c.p.T("cli.halted", "id", id))
		return exitHalted
	case engine.Waiting:
		fmt.Fprintln(c.stdout, c.p.T("cli.waiting", "id", id))
		return exitWaiting
	default:
		fmt.Fprintln(c.stdout, c.p.T("cli.error_state", "id", id))
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
		fmt.Fprintln(c.stdout, c.p.T("cli.halt_already", "id", k.ID))
	case engine.HaltPending:
		fmt.Fprintln(c.stdout, c.p.T("cli.halt_pending", "id", k.ID))
	default:
		fmt.Fprintln(c.stdout, c.p.T("cli.halt_done", "id", k.ID, "node", st.Node))
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
		fmt.Fprintln(c.stdout, c.p.T("cli.resume_elsewhere", "id", k.ID))
		return exitDone
	}
	defer release()
	fmt.Fprintln(c.stdout, c.p.T("cli.resumed", "id", k.ID, "node", st.Node))
	return c.drive(k.ID)
}
