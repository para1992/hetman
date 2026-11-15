//go:build !windows

package engine

import (
	"os"
	"os/exec"
	"sync"
	"syscall"

	"hetman/internal/proctree"
)

// configure puts the step in its own process group so that the whole tree
// can be killed with one signal. The group id equals the step's process id.
func configure(cmd *exec.Cmd) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	setParentDeathSignal(attr)
	cmd.SysProcAttr = attr
}

// procTree is a started process with the process group it leads.
type procTree struct {
	p     *os.Process
	mu    sync.Mutex
	ended bool
}

func track(p *os.Process) *procTree { return &procTree{p: p} }

// kill kills the running process, its process group and, where they can
// be found, its descendants that left the group.
func (t *procTree) kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return
	}
	desc := proctree.Descendants(t.p.Pid)
	if err := syscall.Kill(-t.p.Pid, syscall.SIGKILL); err != nil {
		t.p.Kill()
	}
	for _, pid := range desc {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// end kills what is left of the process group once its leader has exited
// and been waited for. Call it right after the wait: while the group has
// members its id cannot be reused, and when it has none the signal finds
// nothing.
func (t *procTree) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ended = true
	syscall.Kill(-t.p.Pid, syscall.SIGKILL)
}

// ContainChildren is a no-op on Unix: step trees are process groups, found
// again after a crash through the chronicle (see cleanupStale).
func ContainChildren() error { return nil }

// cleanupStale kills what is left of the process group of an earlier step
// process that was started with the given pid and start time.
//
// The system does not reuse a process id while a process group with that id
// still exists. So if the leader is gone and the group still has members,
// they are the step's own descendants. If a process with that id is alive
// but started at another time, the id was reused and the old group is gone.
func cleanupStale(pid int, ident string) string {
	if ident == "" {
		return cleanupUnverified
	}
	if cur, alive := processIdent(pid); alive && cur != ident {
		return cleanupReused
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		return cleanupGone
	}
	waitGone(func() bool { return syscall.Kill(-pid, 0) == nil })
	return cleanupKilled
}
