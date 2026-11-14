package engine

import "syscall"

// setParentDeathSignal makes the kernel kill the step's direct process when
// the engine dies, so a killed engine does not leave the step running.
func setParentDeathSignal(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGKILL
}
