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
	"text/tabwriter"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/dispatch"
	"hetman/internal/engine"
	"hetman/internal/orders"
)

const taskUsage = `usage: hetman task <command> [flags]

commands:
  add     queue a task: hetman task add -kind KIND [-key KEY] [-input FILE] [-force]
  list    list the tasks with their states
  show    show one task and its campaign: hetman task show ID
  cancel  cancel a queued task: hetman task cancel ID
  check   check the routing file and every orders file it routes to

flags:
  -r FILE      routing file (default routes.yaml)
  -kind KIND   the kind of the task; the routing file names the flow that handles it
  -key KEY     de-duplication key: nothing is added while a task with this key exists
               that was not cancelled, whatever its state
  -force       add the task even when its key is taken
  -input FILE  JSON file with the task input, one object; - reads standard input;
               without it the input is {}
  -state S     list only tasks in this state
`

// Stdin of task add -input -.
var taskStdin io.Reader = os.Stdin

// taskCmd runs hetman task.
func taskCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, taskUsage)
		return exitUsage
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("hetman task "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("r", dispatch.DefaultRoutesFile, "routing file")
	var kind, key, input, state *string
	var force *bool
	switch sub {
	case "add":
		kind = fs.String("kind", "", "kind of the task")
		key = fs.String("key", "", "de-duplication key")
		input = fs.String("input", "", "JSON file with the task input; - for standard input")
		force = fs.Bool("force", false, "add the task even when its key is taken")
	case "list":
		state = fs.String("state", "", "list only tasks in this state")
	case "show", "cancel", "check":
	case "help", "-h", "--help":
		fmt.Fprint(stdout, taskUsage)
		return exitDone
	default:
		fmt.Fprintf(stderr, "hetman task: unknown command %q\n\n%s", sub, taskUsage)
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
			fmt.Fprintf(stderr, "hetman task %s: give one task id, as in hetman task %s 3\n", sub, sub)
			return exitUsage
		}
		n, err := strconv.ParseInt(pos[0], 10, 64)
		if err != nil || n <= 0 {
			fmt.Fprintf(stderr, "hetman task %s: %q is not a task id\n", sub, pos[0])
			return exitUsage
		}
		id = n
	case len(pos) > 0:
		fmt.Fprintf(stderr, "hetman task %s: unexpected argument %q\n", sub, pos[0])
		return exitUsage
	}
	routes, code := loadRoutes(*file, stderr)
	if routes == nil {
		return code
	}
	if sub == "check" {
		fmt.Fprintf(stdout, "%s: ok (%d route(s), up to %d campaign(s) at once)\n", routes.Path, len(routes.Kinds), routes.Limit)
		for _, k := range routes.Kinds {
			r := routes.Routes[k]
			fmt.Fprintf(stdout, "  %s -> %s%s\n", k, filepath.Base(r.Orders), routeNote(r))
		}
		return exitDone
	}
	t, err := openTasks(routes)
	if err != nil {
		fmt.Fprintln(stderr, "hetman:", err)
		return exitFailed
	}
	defer t.close()
	t.stdout, t.stderr = stdout, stderr
	switch sub {
	case "add":
		return t.add(*kind, *key, *input, *force)
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

func routeNote(r *dispatch.Route) string {
	s := ""
	if r.Priority != 0 {
		s += fmt.Sprintf(", priority %d", r.Priority)
	}
	if r.Limit > 0 {
		s += fmt.Sprintf(", at most %d at once", r.Limit)
	}
	if s != "" {
		s = " (" + s[2:] + ")"
	}
	return s
}

// loadRoutes loads and checks a routing file, printing its problems.
func loadRoutes(path string, stderr io.Writer) (*dispatch.Routes, int) {
	routes, warnings, err := dispatch.LoadRoutes(path)
	if len(warnings) > 0 {
		fmt.Fprintln(stderr, warnings.Error())
	}
	if err != nil {
		var ps orders.Problems
		if errors.As(err, &ps) {
			fmt.Fprintln(stderr, ps.Error())
			fmt.Fprintf(stderr, "routing file rejected: %d problem(s)\n", len(ps))
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
	fmt.Fprintln(t.stderr, "hetman:", err)
	return exitFailed
}

func (t *tasks) add(kind, key, inputFile string, force bool) int {
	if kind == "" {
		fmt.Fprintln(t.stderr, "hetman task add: -kind is required")
		return exitUsage
	}
	if err := t.routes.Known(kind); err != nil {
		fmt.Fprintln(t.stderr, "hetman task add:", err)
		fmt.Fprintln(t.stderr, "nothing was added")
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
		fmt.Fprintf(t.stderr, "hetman task add: -input %s: %v\n", inputFile, err)
		return exitUsage
	}
	input, err := dispatch.CheckInput(data)
	if err != nil {
		fmt.Fprintln(t.stderr, "hetman task add:", err)
		return exitUsage
	}
	task, added, err := t.queue.Add(kind, input, key, force)
	if err != nil {
		return t.fail(err)
	}
	if !added {
		v, err := dispatch.Views([]dispatch.Task{task}, t.chron, t.routes.Dir)
		if err != nil {
			return t.fail(err)
		}
		fmt.Fprintf(t.stdout, "task %d already has the key %q and is %s; nothing added (-force adds it again)\n", task.ID, key, v[0].State)
		return exitDone
	}
	fmt.Fprintf(t.stdout, "task %d queued (kind %s)\n", task.ID, kind)
	return exitDone
}

// taskListLimit is how many tasks list shows, newest first.
const taskListLimit = 1000

func (t *tasks) list(state string) int {
	list, err := t.queue.List(taskListLimit)
	if err != nil {
		return t.fail(err)
	}
	views, err := dispatch.Views(list, t.chron, t.routes.Dir)
	if err != nil {
		return t.fail(err)
	}
	tw := tabwriter.NewWriter(t.stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tKIND\tSTATE\tCAMPAIGN\tAGE\tKEY\tWAITS FOR")
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
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", v.ID, v.Kind, v.State, campaign, age(v.Added), v.Key, v.Waits)
	}
	tw.Flush()
	if shown == 0 {
		fmt.Fprintln(t.stdout, "no tasks")
	}
	return exitDone
}

// age is how long ago a task was added, roughly.
func age(at time.Time) string {
	d := time.Since(at)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (t *tasks) show(id int64) int {
	task, err := t.queue.Get(id)
	if err != nil {
		return t.fail(err)
	}
	views, err := dispatch.Views([]dispatch.Task{task}, t.chron, t.routes.Dir)
	if err != nil {
		return t.fail(err)
	}
	v := views[0]
	w := t.stdout
	fmt.Fprintf(w, "task %d  kind %s", v.ID, v.Kind)
	if v.Key != "" {
		fmt.Fprintf(w, "  key %s", v.Key)
	}
	fmt.Fprintf(w, "\nstate: %s\n", v.State)
	if v.Waits != "" {
		fmt.Fprintf(w, "waits for %s\n", v.Waits)
	}
	fmt.Fprintf(w, "added %s\n", v.Added.Local().Format(time.DateTime))
	if v.Started != nil {
		fmt.Fprintf(w, "started %s\n", v.Started.Local().Format(time.DateTime))
	}
	if v.Ended != nil {
		fmt.Fprintf(w, "ended %s\n", v.Ended.Local().Format(time.DateTime))
	}
	if v.Campaign != 0 {
		fmt.Fprintf(w, "campaign %d (flow %s, %s)\n", v.Campaign, v.Flow, v.Orders)
		fmt.Fprintf(w, "  status: hetman status -f %s -c %d\n", v.Orders, v.Campaign)
		if v.Command != "" {
			fmt.Fprintf(w, "  next: %s\n", v.Command)
		}
	}
	input, err := task.RunInput()
	if err != nil {
		input = task.Input
	}
	var b bytes.Buffer
	if json.Indent(&b, input, "", "  ") == nil {
		fmt.Fprintf(w, "input:\n%s\n", b.String())
	}
	return exitDone
}

func (t *tasks) cancel(id int64) int {
	task, err := t.queue.Cancel(id)
	if err != nil {
		return t.fail(err)
	}
	fmt.Fprintf(t.stdout, "task %d cancelled (kind %s)\n", task.ID, task.Kind)
	return exitDone
}

const dispatchUsage = `usage: hetman dispatch [-r FILE] [-once] [-limit N]

Starts a campaign of the routed flow for each queued task, by priority and
then age, and keeps going. Campaigns that nobody drives and that can go on
without a person, such as those cut off when a dispatcher stopped, are
continued first.

flags:
  -r FILE   routing file (default routes.yaml)
  -once     stop once nothing is queued or running, instead of waiting for new tasks
  -limit N  campaigns running at once (default: the limit of the routing file, else 1)
  -poll D   how often the queue is looked at for new tasks (default 1s)
`

// dispatchCmd runs hetman dispatch.
func dispatchCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hetman dispatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, dispatchUsage) }
	file := fs.String("r", dispatch.DefaultRoutesFile, "routing file")
	once := fs.Bool("once", false, "stop once nothing is queued or running")
	limit := fs.Int("limit", 0, "campaigns running at once")
	poll := fs.Duration("poll", 0, "how often the queue is looked at")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "hetman dispatch: unexpected argument %q\n", fs.Arg(0))
		return exitUsage
	}
	if *limit < 0 {
		fmt.Fprintln(stderr, "hetman dispatch: -limit must be at least 1")
		return exitUsage
	}
	routes, code := loadRoutes(*file, stderr)
	if routes == nil {
		return code
	}
	t, err := openTasks(routes)
	if err != nil {
		fmt.Fprintln(stderr, "hetman:", err)
		return exitFailed
	}
	defer t.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := engine.ContainChildren(); err != nil {
		fmt.Fprintf(stderr, "hetman: step processes are not tied to this process (%v); they are cleaned up when a campaign continues instead\n", err)
	}
	d := &dispatch.Dispatcher{Routes: routes, Queue: t.queue, Chron: t.chron, Limit: *limit, Once: *once, Poll: *poll,
		Out: stdout, Language: languageFor}
	n := *limit
	if n == 0 {
		n = routes.Limit
	}
	mode := "Ctrl+C to stop"
	if *once {
		mode = "until nothing is queued or running"
	}
	fmt.Fprintf(stdout, "dispatching tasks of %s: %d route(s), up to %d campaign(s) at once (%s)\n", routes.Path, len(routes.Kinds), n, mode)
	if err := d.Run(ctx); err != nil {
		fmt.Fprintln(stderr, "hetman dispatch:", err)
		return exitFailed
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stdout, "dispatcher stopped")
		return exitInterrupted
	}
	if waiting := t.waiting(); waiting > 0 {
		fmt.Fprintf(stdout, "nothing left to start; %d task(s) wait for a person (hetman task list)\n", waiting)
	} else {
		fmt.Fprintln(stdout, "nothing left to start")
	}
	return exitDone
}

// waiting counts the tasks that wait for a person, are halted or stopped.
func (t *tasks) waiting() int {
	list, err := t.queue.Open()
	if err != nil {
		return 0
	}
	views, err := dispatch.Views(list, t.chron, t.routes.Dir)
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
