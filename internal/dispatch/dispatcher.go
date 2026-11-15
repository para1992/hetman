package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/engine"
	"hetman/internal/i18n"
	"hetman/internal/orders"
)

// ErrBusy is returned when another dispatcher already runs for the folder.
var ErrBusy = newError(nil, "cli.dispatch.busy")

// LockFile is the file a running dispatcher holds locked, in .hetman.
const LockFile = "dispatcher.lock"

// Dispatcher starts a campaign for each queued task and drives it, up to the
// limits of the routing file. Only one dispatcher runs per folder.
//
// It keeps no state of its own: on every look it reads the queue and the
// chronicle again. A campaign it drives is locked like one driven by
// hetman run. A campaign that waits for a person is let go, so that it
// takes no place while it waits; once answered, it is driven again in turn.
// A campaign that nobody drives and that can go on without a person, such
// as one cut off when a dispatcher was killed, is driven again, before any
// new task is started. Halted, stopped and waiting campaigns are left to a
// person.
type Dispatcher struct {
	Routes *Routes
	Queue  *Queue
	Chron  *chronicle.Chronicle
	Limit  int           // campaigns running at once; zero for the routing file's limit
	Once   bool          // return once nothing is queued or running, instead of waiting for new tasks
	Poll   time.Duration // how often the queue is looked at; zero for a second
	Out    io.Writer     // progress lines; nil for none
	Text   *i18n.Printer // the language of the progress lines; nil for English
	// Language returns the language for the person of a campaign started
	// now; nil for the language key of the orders.
	Language func(*orders.Orders) (string, error)

	outMu    sync.Mutex
	mu       sync.Mutex
	inflight map[int64]string // campaigns driven here, to their task kind
	said     map[string]bool  // problems already reported
	retry    map[int64]time.Time
	drives   sync.WaitGroup
	wake     chan struct{}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (d *Dispatcher) out() io.Writer {
	if d.Out == nil {
		return io.Discard
	}
	return lockedWriter{&d.outMu, d.Out}
}

func (d *Dispatcher) say(key string, args ...any) {
	fmt.Fprintln(d.out(), d.Text.T(key, args...))
}

// sayOnce reports a problem the first time it is seen.
func (d *Dispatcher) sayOnce(key string, args ...any) {
	msg := d.Text.T(key, args...)
	d.mu.Lock()
	seen := d.said[msg]
	d.said[msg] = true
	d.mu.Unlock()
	if !seen {
		fmt.Fprintln(d.out(), msg)
	}
}

func (d *Dispatcher) limit() int {
	if d.Limit > 0 {
		return d.Limit
	}
	return d.Routes.Limit
}

// Run dispatches until ctx is cancelled or, with Once, until nothing is
// queued, ready or running. Campaigns still driven when ctx is cancelled
// are cut off as with an interrupted hetman run and continued by the next
// dispatcher.
func (d *Dispatcher) Run(ctx context.Context) error {
	release, err := engine.LockPath(filepath.Join(d.Routes.Dir, ".hetman", LockFile))
	if errors.Is(err, engine.ErrLocked) {
		return ErrBusy
	}
	if err != nil {
		return err
	}
	defer release()
	d.inflight, d.said, d.retry = map[int64]string{}, map[string]bool{}, map[int64]time.Time{}
	d.wake = make(chan struct{}, 1)
	poll := d.Poll
	if poll <= 0 {
		poll = time.Second
	}
	defer d.drives.Wait()
	for ctx.Err() == nil {
		pending, err := d.look(ctx)
		if err != nil {
			return err
		}
		d.mu.Lock()
		running := len(d.inflight)
		d.mu.Unlock()
		if d.Once && !pending && running == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-d.wake:
		case <-time.After(poll):
		}
	}
	return nil
}

// candidate is a task that may take a place: a queued one to start, or a
// started one whose campaign is ready to go on.
type candidate struct {
	task     Task
	priority int
	orders   string // the campaign's orders file when it goes on
}

// look reads the queue and the chronicle once and starts what the limits
// allow. pending reports whether something is queued or ready that could
// start once a place is free.
func (d *Dispatcher) look(ctx context.Context) (pending bool, err error) {
	tasks, err := d.Queue.Open()
	if err != nil {
		return false, err
	}
	running := 0
	perRoute := map[string]int{}
	var ready, queued []candidate
	for _, t := range tasks {
		d.mu.Lock()
		_, ours := d.inflight[t.Campaign]
		retryAt := d.retry[t.Campaign]
		d.mu.Unlock()
		if t.State == Started && ours {
			running++
			perRoute[t.Kind]++
			continue
		}
		if t.State == Starting {
			if t, err = d.recover(t); err != nil {
				return false, err
			}
		}
		prio := 0
		if r := d.Routes.Routes[t.Kind]; r != nil {
			prio = r.Priority
		}
		switch t.State {
		case Queued:
			if err := d.Routes.Known(t.Kind); err != nil {
				d.sayOnce("cli.dispatch.waits", "id", t.ID, "error", d.Text.Error(err))
				continue
			}
			queued = append(queued, candidate{task: t, priority: prio})
		case Started:
			v, err := viewOf(d.Text, t, d.Chron, d.Routes.Dir)
			if err != nil {
				return false, err
			}
			switch {
			case v.State == StateDone || v.State == StateFailed:
				at := time.Now()
				if v.Ended != nil {
					at = *v.Ended
				}
				if err := d.Queue.ended(t.ID, v.State, at); err != nil {
					return false, err
				}
			case v.busy:
				running++
				perRoute[t.Kind]++
			case v.ready && time.Now().After(retryAt):
				ready = append(ready, candidate{task: t, priority: prio, orders: v.Orders})
			}
		}
	}
	order := func(list []candidate) {
		sort.SliceStable(list, func(i, j int) bool {
			a, b := list[i], list[j]
			if a.priority != b.priority {
				return a.priority > b.priority
			}
			if !a.task.Added.Equal(b.task.Added) {
				return a.task.Added.Before(b.task.Added)
			}
			return a.task.ID < b.task.ID
		})
	}
	order(ready)
	order(queued)
	pending = len(ready)+len(queued) > 0
	for _, c := range append(ready, queued...) {
		if ctx.Err() != nil || running >= d.limit() {
			break
		}
		if r := d.Routes.Routes[c.task.Kind]; r != nil && r.Limit > 0 && perRoute[c.task.Kind] >= r.Limit {
			continue
		}
		var started bool
		if c.task.State == Queued {
			started, err = d.start(ctx, c.task)
		} else {
			started, err = d.resume(ctx, c.task, c.orders)
		}
		if err != nil {
			return pending, err
		}
		if started {
			running++
			perRoute[c.task.Kind]++
		}
	}
	return pending, nil
}

// recover settles a task left starting by a dispatcher that died while it
// created the campaign: the campaign is linked if it was created, else the
// task is queued again.
func (d *Dispatcher) recover(t Task) (Task, error) {
	list, err := d.Chron.Campaigns(1 << 20)
	if err != nil {
		return t, err
	}
	since := t.Started.Add(-time.Minute)
	for _, k := range list {
		if k.Started.Before(since) {
			break
		}
		events, err := d.Chron.Events(k.ID)
		if err != nil {
			return t, err
		}
		if len(events) == 0 {
			continue
		}
		if ref := engine.TaskOf(events[0].Input); ref != nil && ref.ID == t.ID {
			if err := d.Queue.link(t.ID, k.ID); err != nil {
				return t, err
			}
			d.say("cli.dispatch.recovered", "id", t.ID, "campaign", k.ID)
			t.State, t.Campaign = Started, k.ID
			return t, nil
		}
	}
	if _, err := d.Queue.db.Exec(`UPDATE tasks SET state = ?, started_at = '', changed = `+nextChange+` WHERE id = ? AND state = ?`, Queued, t.ID, Starting); err != nil {
		return t, err
	}
	t.State, t.Started = Queued, time.Time{}
	return t, nil
}

// loadOrders loads an orders file the way hetman run does before it starts
// a step; a problem is reported once and the task waits.
func (d *Dispatcher) loadOrders(path string, t Task) *orders.Orders {
	o, err := orders.Load(path)
	if err != nil {
		d.sayOnce("cli.dispatch.rejected", "id", t.ID, "path", path, "error", err)
		return nil
	}
	if errs, _ := o.CheckPrograms(); len(errs) > 0 {
		d.sayOnce("cli.dispatch.rejected", "id", t.ID, "path", path, "error", errs)
		return nil
	}
	return o
}

// start creates the campaign of a queued task and drives it.
func (d *Dispatcher) start(ctx context.Context, t Task) (bool, error) {
	o := d.loadOrders(d.Routes.Routes[t.Kind].Orders, t)
	if o == nil {
		return false, nil
	}
	lang := o.Language
	if d.Language != nil {
		var err error
		if lang, err = d.Language(o); err != nil {
			d.sayOnce("cli.dispatch.waits", "id", t.ID, "error", d.Text.Error(err))
			return false, nil
		}
	}
	input, err := t.RunInput()
	if err != nil {
		return false, fmt.Errorf("task %d: input: %v", t.ID, err)
	}
	ok, err := d.Queue.claim(t.ID)
	if err != nil || !ok {
		return false, err // cancelled meanwhile when not ok
	}
	id, err := d.Chron.NewCampaign(o.Flow, o.Path, o.Start, input, lang)
	if err != nil {
		return false, err
	}
	if err := d.Queue.link(t.ID, id); err != nil {
		return false, err
	}
	release, err := engine.Lock(o, id)
	if err != nil {
		return false, err
	}
	d.say("cli.dispatch.started", "id", t.ID, "kind", t.Kind, "campaign", id, "flow", o.Flow)
	t.State, t.Campaign = Started, id
	d.drive(ctx, o, t, release)
	return true, nil
}

// resume drives again the campaign of a started task that nobody drives.
// A campaign someone else took meanwhile is left to them.
func (d *Dispatcher) resume(ctx context.Context, t Task, ordersPath string) (bool, error) {
	o := d.loadOrders(ordersPath, t)
	if o == nil {
		return false, nil
	}
	release, err := engine.Lock(o, t.Campaign)
	if errors.Is(err, engine.ErrLocked) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Looked at again under the lock: a person may have halted it or it may
	// have ended in the moment before.
	events, err := d.Chron.Events(t.Campaign)
	if err != nil {
		release()
		return false, err
	}
	k, err := d.Chron.Campaign(t.Campaign)
	if err != nil {
		release()
		return false, err
	}
	var v View
	look(d.Text, &v, k, events, false)
	if !v.ready {
		release()
		return false, nil
	}
	d.say("cli.dispatch.continues", "id", t.ID, "kind", t.Kind, "campaign", t.Campaign, "node", v.Node)
	d.drive(ctx, o, t, release)
	return true, nil
}

// drive runs a campaign whose lock is held in the background, then lets the
// lock go.
func (d *Dispatcher) drive(ctx context.Context, o *orders.Orders, t Task, release func()) {
	id := t.Campaign
	d.mu.Lock()
	d.inflight[id] = t.Kind
	d.mu.Unlock()
	d.drives.Add(1)
	go func() {
		defer d.drives.Done()
		e := &engine.Engine{Orders: o, Chron: d.Chron, Campaign: id, Out: d.out(), Text: d.Text, StopAtAsk: true}
		res, err := e.Drive(ctx)
		e.WaitHooks()
		release()
		d.mu.Lock()
		delete(d.inflight, id)
		if err != nil && !errors.Is(err, engine.ErrInterrupted) {
			d.retry[id] = time.Now().Add(time.Minute)
		}
		d.mu.Unlock()
		d.report(o, t, res, err)
		if err == nil && (res == engine.Done || res == engine.Failed) {
			if err := d.Queue.ended(t.ID, res.String(), time.Now()); err != nil {
				d.say("cli.dispatch.task_error", "id", t.ID, "error", err)
			}
		}
		select {
		case d.wake <- struct{}{}:
		default:
		}
	}()
}

func (d *Dispatcher) report(o *orders.Orders, t Task, res engine.Result, err error) {
	id := t.Campaign
	switch {
	case errors.Is(err, engine.ErrInterrupted):
		d.say("cli.dispatch.interrupted", "id", t.ID, "campaign", id)
	case err != nil:
		d.say("cli.dispatch.drive_error", "id", t.ID, "campaign", id, "error", d.Text.Error(err))
	case res == engine.Asking:
		asked, _, _ := engine.Pending(d.Chron, id)
		what := d.Text.T("cli.dispatch.a_person")
		if asked != nil {
			what = asks(d.Text, asked)
		}
		d.say("cli.dispatch.asking", "id", t.ID, "campaign", id, "what", what, "command", engine.AnswerCommand(o, id))
	case res == engine.Halted, res == engine.Errored, res == engine.Waiting:
		d.say("cli.dispatch.stopped_"+res.String(), "id", t.ID, "campaign", id, "command", engine.ResumeCommand(o, id))
	default:
		d.say("cli.dispatch.ended_"+res.String(), "id", t.ID, "campaign", id)
	}
}
