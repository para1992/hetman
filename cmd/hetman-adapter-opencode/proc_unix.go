//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// stop asks the client and the processes it started to end, as Ctrl-C
// would, and kills the client if it has not exited a second later.
//
// The client stays in the adapter's process group, so that when the engine
// kills the adapter's group it kills the client's tree too. When the
// adapter leads its group, as it does under the engine, the terminate
// signal goes to the whole group, with the adapter itself ignoring it, so
// the tools the client started stop as well. Otherwise (the adapter was
// started from a shell or a test) only the client is signalled.
func stop(p *os.Process, exited <-chan struct{}) {
	if syscall.Getpgrp() == os.Getpid() {
		signal.Ignore(syscall.SIGTERM)
		syscall.Kill(0, syscall.SIGTERM)
	} else {
		p.Signal(syscall.SIGTERM)
	}
	select {
	case <-exited:
		return
	case <-time.After(time.Second):
	}
	p.Kill()
	<-exited
}
