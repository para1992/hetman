//go:build !windows

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

// configure puts the step in its own process group so that the whole tree
// can be killed with one signal.
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
