//go:build !windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

// configure puts the step in its own process group so that the whole tree
// can be killed with one signal. The group id equals the step's process id.
func configure(cmd *exec.Cmd) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	setParentDeathSignal(attr)
	cmd.SysProcAttr = attr
}

// killTree kills the step's process group.
func killTree(p *os.Process) {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		p.Kill()
	}
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
