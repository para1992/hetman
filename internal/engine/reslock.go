package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
)

// LockDir holds the files of the resource locks steps name with locks. It
// is shared by every campaign and every hetman process that uses the same
// state folder.
func LockDir(o *orders.Orders) string { return filepath.Join(StateDir(o), "locks") }

// lockPoll is how often a step waiting for a lock tries it again.
var lockPoll = 50 * time.Millisecond

// errHaltedWaiting is returned when a halt came while a step waited for a
// lock; the step has not started.
var errHaltedWaiting = errors.New("halted while waiting for a lock")

// takeLocks takes every lock the step of a node needs, one by one in name
// order, waiting while another step holds one. A lock is an exclusive lock
// on a file that the system drops when the holder closes it or its process
// ends, however it ends, so a killed engine leaves no lock behind. Taking
// locks in one order means no two waiting steps can each hold what the
// other waits for. The returned release gives them all back.
func (e *Engine) takeLocks(ctx context.Context, node *orders.Node, attempt int) (func(), error) {
	names := e.Orders.LocksOf(node)
	if len(names) == 0 {
		return func() {}, nil
	}
	dir := LockDir(e.top().Orders)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var held []func()
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			held[i]()
		}
		e.held = nil
	}
	for _, name := range names {
		path := filepath.Join(dir, name+".lock")
		said := ""
		lastLook := time.Now()
		for {
			free, err := lockFile(path)
			if err == nil {
				os.WriteFile(filepath.Join(dir, name+".holder"), []byte(e.holder(node.ID, attempt)), 0o644)
				held = append(held, free)
				break
			}
			if !errors.Is(err, ErrLocked) {
				release()
				return nil, fmt.Errorf("lock %s: %w", name, err)
			}
			holder := readHolder(dir, name)
			if said == "" || said != holder {
				said = holder
				if err := e.record(chronicle.Event{Kind: chronicle.LockWaiting, Node: node.ID, Attempt: attempt,
					Details: chronicle.Details{Lock: name, Holder: holder}}); err != nil {
					release()
					return nil, err
				}
				e.say("engine.lock_wait", "node", node.ID, "lock", name, "holder", holder)
			}
			if time.Since(lastLook) >= time.Second {
				lastLook = time.Now()
				if e.halting() {
					release()
					return nil, errHaltedWaiting
				}
			}
			select {
			case <-ctx.Done():
				release()
				return nil, ErrInterrupted
			case <-time.After(lockPoll):
			}
		}
	}
	e.held = names
	return release, nil
}

// holder describes the step that takes a lock, for whoever waits for it.
func (e *Engine) holder(node string, attempt int) string {
	where := fmt.Sprintf("campaign %d", e.Campaign)
	if p := e.path(); p != "" {
		where += " (" + p + ")"
	}
	if top := e.top(); top != e {
		where += fmt.Sprintf(" of campaign %d", top.Campaign)
	}
	return fmt.Sprintf("%s at %s, attempt %d, process %d", where, node, attempt, os.Getpid())
}

// readHolder reads who took a lock last. It is only asked while the lock is
// taken, so the holder it names is the one holding it, except for the moment
// between a new holder taking the lock and writing its name.
//
// The holder writes its name just after taking the lock, so an empty or
// missing file is looked at again for a moment before giving up.
func readHolder(dir, name string) string {
	for i := 0; i < 20; i++ {
		data, err := os.ReadFile(filepath.Join(dir, name+".holder"))
		if s := strings.TrimSpace(string(data)); err == nil && s != "" {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	return "another step"
}
