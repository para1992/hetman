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
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/dispatch"
	"hetman/internal/engine"
	"hetman/internal/i18n"
	"hetman/internal/orders"
	"hetman/internal/runinput"
)

// Stdin of task add -input -.
var taskStdin io.Reader = os.Stdin

// taskCmd runs hetman task. Its texts are in the person's language: the
// one in HETMAN_LANGUAGE, else English, since a routing file names several
// orders files.
func taskCmd(args []string, stdout, stderr io.Writer) int {
	p := personPrinter(nil)
	if len(args) == 0 {
		fmt.Fprint(stderr, p.T("cli.task.usage"))
		return exitUsage
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("hetman task "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("r", dispatch.DefaultRoutesFile, p.T("flag.task_r"))
	var kind, key, input, state *string
	var force, saveOnly *bool
	switch sub {
	case "add", "new":
		kind = fs.String("kind", "", p.T("flag.task_kind"))
		key = fs.String("key", "", p.T("flag.task_key"))
		force = fs.Bool("force", false, p.T("flag.task_force"))
		if sub == "add" {
			input = fs.String("input", "", p.T("flag.task_input"))
		} else {
			saveOnly = fs.Bool("save-only", false, p.T("flag.form_save_only"))
		}
	case "list":
		state = fs.String("state", "", p.T("flag.task_state"))
	case "show", "cancel", "check":
	case "help", "-h", "--help":
		fmt.Fprint(stdout, p.T("cli.task.usage"))
		return exitDone
	default:
		fmt.Fprintf(stderr, "%s\n\n%s", p.T("cli.task.unknown_command", "command", strconv.Quote(sub)), p.T("cli.task.usage"))
		return exitUsage
	}
	pos, ok := parseMixed(fs, args)
	if !ok {
		return exitUsage
	}
	var id int64
	switch {
	case sub == "show" || sub == "cancel":
		if len(pos) != 1 {
			fmt.Fprintln(stderr, p.T("cli.task.need_id", "command", sub))
			return exitUsage
		}
		n, err := strconv.ParseInt(pos[0], 10, 64)
		if err != nil || n <= 0 {
			fmt.Fprintln(stderr, p.T("cli.task.bad_id", "command", sub, "arg", strconv.Quote(pos[0])))
			return exitUsage
		}
		id = n
	case len(pos) > 0:
		fmt.Fprintln(stderr, p.T("cli.task.unexpected_argument", "command", sub, "arg", strconv.Quote(pos[0])))
		return exitUsage
	}
	routes, code := loadRoutes(p, *file, stderr)
	if routes == nil {
		return code
	}
	if sub == "check" {
		fmt.Fprintln(stdout, p.T("cli.task.check_ok", "path", routes.Path, "n", len(routes.Kinds), "limit", routes.Limit))
		for _, k := range routes.Kinds {
			r := routes.Routes[k]
			fmt.Fprintln(stdout, p.T("cli.task.route", "kind", k, "file", filepath.Base(r.Orders), "note", routeNote(p, r)))
		}
		return exitDone
	}
	t, err := openTasks(routes)
	if err != nil {
		fmt.Fprintln(stderr, "hetman:", err)
		return exitFailed
	}
	defer t.close()
	t.stdout, t.stderr, t.p = stdout, stderr, p
	switch sub {
	case "add":
		return t.add(*kind, *key, *input, *force)
	case "new":
		return t.newTask(*kind, *key, *force, *saveOnly)
	case "list":
		return t.list(*state)
	case "show":
		return t.show(id)
	default:
		return t.cancel(id)
	}
}

// parseMixed parses flags that may come before and after positional
// arguments, and returns the positional ones.
func parseMixed(fs *flag.FlagSet, args []string) ([]string, bool) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, false
		}
		if fs.NArg() == 0 {
			return pos, true
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func routeNote(p *i18n.Printer, r *dispatch.Route) string {
	var parts []string
	if r.Priority != 0 {
		parts = append(parts, p.T("cli.task.route_priority", "priority", r.Priority))
	}
	if r.Limit > 0 {
		parts = append(parts, p.T("cli.task.route_limit", "limit", r.Limit))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// loadRoutes loads and checks a routing file, printing its problems. The
// problems themselves stay English, as those of the orders check do.
func loadRoutes(p *i18n.Printer, path string, stderr io.Writer) (*dispatch.Routes, int) {
	routes, warnings, err := dispatch.LoadRoutes(path)
	if len(warnings) > 0 {
		fmt.Fprintln(stderr, warnings.Error())
	}
	if err != nil {
		var ps orders.Problems
		if errors.As(err, &ps) {
			fmt.Fprintln(stderr, ps.Error())
			fmt.Fprintln(stderr, p.T("cli.task.routes_rejected", "n", len(ps)))
		} else {
			fmt.Fprintln(stderr, "hetman:", err)
		}
		return nil, exitUsage
	}
	return routes, exitDone
}

type tasks struct {
	routes *dispatch.Routes
	queue  *dispatch.Queue
	chron  *chronicle.Chronicle
	stdout io.Writer
	stderr io.Writer
	p      *i18n.Printer
}

func openTasks(routes *dispatch.Routes) (*tasks, error) {
	if err := os.MkdirAll(filepath.Join(routes.Dir, ".hetman"), 0o755); err != nil {
		return nil, err
	}
	q, err := dispatch.OpenQueue(dispatch.QueuePath(routes.Dir))
	if err != nil {
		return nil, err
	}
	ch, err := chronicle.Open(filepath.Join(routes.Dir, ".hetman", "chronicle.db"))
	if err != nil {
		q.Close()
		return nil, err
	}
	return &tasks{routes: routes, queue: q, chron: ch}, nil
}

func (t *tasks) close() {
	t.queue.Close()
	t.chron.Close()
}

func (t *tasks) fail(err error) int {
	fmt.Fprintln(t.stderr, "hetman:", t.p.Error(err))
	return exitFailed
}

// state is the shown name of a task state.
func (t *tasks) state(s string) string { return t.p.T("cli.task.state." + s) }

func (t *tasks) add(kind, key, inputFile string, force bool) int {
	if kind == "" {
		fmt.Fprintln(t.stderr, t.p.T("cli.task.kind_required"))
		return exitUsage
	}
	if err := t.routes.Known(kind); err != nil {
		fmt.Fprintln(t.stderr, "hetman task add:", t.p.Error(err))
		fmt.Fprintln(t.stderr, t.p.T("cli.task.nothing_added"))
		return exitUsage
	}
	data := []byte("{}")
	var err error
	switch inputFile {
	case "":
	case "-":
		data, err = io.ReadAll(taskStdin)
	default:
		data, err = os.ReadFile(inputFile)
	}
	if err != nil {
		fmt.Fprintln(t.stderr, t.p.T("cli.task.bad_input", "file", inputFile, "error", err))
		return exitUsage
	}
	return t.queueInput(kind, key, data, force)
}

// newTask asks for the input of a task in the terminal, through the form
// made from the input section of the orders the kind is routed to, then
// queues it as add does.
func (t *tasks) newTask(kind, key string, force, saveOnly bool) int {
	if kind == "" {
		fmt.Fprintln(t.stderr, t.p.T("cli.task.kind_required"))
		return exitUsage
	}
	if err := t.routes.Known(kind); err != nil {
		fmt.Fprintln(t.stderr, "hetman task new:", t.p.Error(err))
		fmt.Fprintln(t.stderr, t.p.T("cli.task.nothing_added"))
		return exitUsage
	}
	o, err := orders.Load(t.routes.Routes[kind].Orders)
	if err != nil {
		return t.fail(err)
	}
	data, _, code := askInput(t.p, o, filepath.Join(t.routes.Dir, ".hetman"), kind, "task new", t.stdout, t.stderr)
	if data == nil {
		return code
	}
	if saveOnly {
		return exitDone
	}
	return t.queueInput(kind, key, data, force)
}

// queueInput checks a task's input, against the input section of the
// routed orders when they have one, and queues the task.
func (t *tasks) queueInput(kind, key string, data []byte, force bool) int {
	input, err := dispatch.CheckInput(data)
	if err != nil {
		fmt.Fprintln(t.stderr, "hetman task add:", t.p.Error(err))
		return exitUsage
	}
	o, err := orders.Load(t.routes.Routes[kind].Orders)
	if err != nil {
		return t.fail(err)
	}
	if o.Input != nil {
		if input, err = runinput.Check(o.Input, input); err != nil {
			fmt.Fprintln(t.stderr, t.p.T("cli.form.input_refused", "command", "task add", "error", t.p.Error(err)))
			fmt.Fprintln(t.stderr, t.p.T("cli.task.nothing_added"))
			return exitUsage
		}
	}
	task, added, err := t.queue.Add(kind, input, key, force)
	if err != nil {
		return t.fail(err)
	}
	if !added {
		v, err := dispatch.Views([]dispatch.Task{task}, t.chron, t.routes.Dir, t.p)
		if err != nil {
			return t.fail(err)
		}
		fmt.Fprintln(t.stdout, t.p.T("cli.task.duplicate", "id", task.ID, "key", strconv.Quote(key), "state", t.state(v[0].State)))
		return exitDone
	}
	fmt.Fprintln(t.stdout, t.p.T("cli.task.queued", "id", task.ID, "kind", kind))
	return exitDone
}

// taskListLimit is how many tasks list shows, newest first.
const taskListLimit = 1000

// list prints the tasks. The -state filter takes the English state
// identifiers in every language; the STATE column shows them in the
// person's language, as status shows the state of its nodes.
func (t *tasks) list(state string) int {
	list, err := t.queue.List(taskListLimit)
	if err != nil {
		return t.fail(err)
	}
	views, err := dispatch.Views(list, t.chron, t.routes.Dir, t.p)
	if err != nil {
		return t.fail(err)
	}
	tw := tabwriter.NewWriter(t.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, t.p.T("cli.task.list_header"))
	shown := 0
	for _, v := range views {
		if state != "" && v.State != state {
			continue
		}
		shown++
		campaign := ""
		if v.Campaign != 0 {
			campaign = strconv.FormatInt(v.Campaign, 10)
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", v.ID, v.Kind, t.state(v.State), campaign, age(v.Added), v.Key, v.Waits)
	}
	tw.Flush()
	if shown == 0 {
		fmt.Fprintln(t.stdout, t.p.T("cli.task.no_tasks"))
	}
	return exitDone
}

// age is how long ago a task was added, roughly, in the units of a Go
// duration.
func age(at time.Time) string {
	d := time.Since(at)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}

func (t *tasks) show(id int64) int {
	task, err := t.queue.Get(id)
	if err != nil {
		return t.fail(err)
	}
	views, err := dispatch.Views([]dispatch.Task{task}, t.chron, t.routes.Dir, t.p)
	if err != nil {
		return t.fail(err)
	}
	v := views[0]
	w, p := t.stdout, t.p
	head := p.T("cli.task.show.head", "id", v.ID, "kind", v.Kind)
	if v.Key != "" {
		head += p.T("cli.task.show.key", "key", v.Key)
	}
	fmt.Fprintln(w, head)
	fmt.Fprintln(w, p.T("cli.task.show.state", "state", t.state(v.State)))
	if v.Waits != "" {
		fmt.Fprintln(w, p.T("cli.task.show.waits", "waits", v.Waits))
	}
	fmt.Fprintln(w, p.T("cli.task.show.added", "at", v.Added.Local().Format(time.DateTime)))
	if v.Started != nil {
		fmt.Fprintln(w, p.T("cli.task.show.started", "at", v.Started.Local().Format(time.DateTime)))
	}
	if v.Ended != nil {
		fmt.Fprintln(w, p.T("cli.task.show.ended", "at", v.Ended.Local().Format(time.DateTime)))
	}
	if v.Campaign != 0 {
		fmt.Fprintln(w, p.T("cli.task.show.campaign", "id", v.Campaign, "flow", v.Flow, "orders", v.Orders))
		fmt.Fprintln(w, p.T("cli.task.show.status", "command", "hetman status -f "+v.Orders+" -c "+strconv.FormatInt(v.Campaign, 10)))
		if v.Command != "" {
			fmt.Fprintln(w, p.T("cli.task.show.next", "command", v.Command))
		}
	}
	input, err := task.RunInput()
	if err != nil {
		input = task.Input
	}
	var b bytes.Buffer
	if json.Indent(&b, input, "", "  ") == nil {
		fmt.Fprintln(w, p.T("cli.task.show.input"))
		fmt.Fprintln(w, b.String())
	}
	return exitDone
}

func (t *tasks) cancel(id int64) int {
	task, err := t.queue.Cancel(id)
	if err != nil {
		return t.fail(err)
	}
	fmt.Fprintln(t.stdout, t.p.T("cli.task.cancelled", "id", task.ID, "kind", task.Kind))
	return exitDone
}

// dispatchCmd runs hetman dispatch.
func dispatchCmd(args []string, stdout, stderr io.Writer) int {
	p := personPrinter(nil)
	fs := flag.NewFlagSet("hetman dispatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, p.T("cli.dispatch.usage")) }
	file := fs.String("r", dispatch.DefaultRoutesFile, p.T("flag.task_r"))
	once := fs.Bool("once", false, p.T("flag.task_once"))
	limit := fs.Int("limit", 0, p.T("flag.task_limit"))
	poll := fs.Duration("poll", 0, p.T("flag.task_poll"))
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, p.T("cli.dispatch.unexpected_argument", "arg", strconv.Quote(fs.Arg(0))))
		return exitUsage
	}
	if *limit < 0 {
		fmt.Fprintln(stderr, p.T("cli.dispatch.bad_limit"))
		return exitUsage
	}
	routes, code := loadRoutes(p, *file, stderr)
	if routes == nil {
		return code
	}
	t, err := openTasks(routes)
	if err != nil {
		fmt.Fprintln(stderr, "hetman:", err)
		return exitFailed
	}
	defer t.close()
	t.p = p
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := engine.ContainChildren(); err != nil {
		fmt.Fprintln(stderr, p.T("cli.dispatch.not_contained", "error", err))
	}
	d := &dispatch.Dispatcher{Routes: routes, Queue: t.queue, Chron: t.chron, Limit: *limit, Once: *once, Poll: *poll,
		Out: stdout, Text: p, Language: languageFor}
	n := *limit
	if n == 0 {
		n = routes.Limit
	}
	mode := p.T("cli.dispatch.mode_stay")
	if *once {
		mode = p.T("cli.dispatch.mode_once")
	}
	fmt.Fprintln(stdout, p.T("cli.dispatch.start", "path", routes.Path, "n", len(routes.Kinds), "limit", n, "mode", mode))
	if err := d.Run(ctx); err != nil {
		fmt.Fprintln(stderr, "hetman dispatch:", p.Error(err))
		return exitFailed
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stdout, p.T("cli.dispatch.stopped"))
		return exitInterrupted
	}
	if waiting := t.waiting(); waiting > 0 {
		fmt.Fprintln(stdout, p.T("cli.dispatch.left_waiting", "n", waiting))
	} else {
		fmt.Fprintln(stdout, p.T("cli.dispatch.left"))
	}
	return exitDone
}

// waiting counts the tasks that wait for a person, are halted or stopped.
func (t *tasks) waiting() int {
	list, err := t.queue.Open()
	if err != nil {
		return 0
	}
	views, err := dispatch.Views(list, t.chron, t.routes.Dir, t.p)
	if err != nil {
		return 0
	}
	n := 0
	for _, v := range views {
		if v.State == dispatch.StateWaiting || v.State == dispatch.StateHalted || v.State == dispatch.StateError {
			n++
		}
	}
	return n
}
