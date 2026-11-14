package main

import (
	"os"
	"os/exec"
	"strconv"
	"time"
)

// stop kills the client and every process it started with the system
// taskkill tool, falling back to killing the client alone.
func stop(p *os.Process, exited <-chan struct{}) {
	if exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run() != nil {
		p.Kill()
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		p.Kill()
		<-exited
	}
}
