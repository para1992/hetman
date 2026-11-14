//go:build !windows && !linux

package engine

import "syscall"

// setParentDeathSignal has no equivalent here: a step outlives an engine
// that is killed without a chance to clean up.
func setParentDeathSignal(*syscall.SysProcAttr) {}
