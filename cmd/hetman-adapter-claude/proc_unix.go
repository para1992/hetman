//go:build !windows

package main

import (
	"os"
	"syscall"
	"time"
)

// stop asks the client to end, as Ctrl-C would, and kills it if it has not
// exited a second later. The client stays in the adapter's process group, so
// when the engine kills the adapter's tree it kills the client too.
func stop(p *os.Process, exited <-chan struct{}) {
	p.Signal(syscall.SIGTERM)
	select {
	case <-exited:
		return
	case <-time.After(time.Second):
	}
	p.Kill()
	<-exited
}
