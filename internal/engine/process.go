package engine

import (
	"context"
	"os"
	"os/exec"
	"time"
)

type procResult struct {
	exit        int
	startErr    error
	timedOut    bool
	interrupted bool
}

// runProcess starts argv directly (no shell) and waits for it to exit. Output
// goes to log. Exceeding timeout or cancelling ctx kills the whole process
// tree. Waiting is event driven: the process exit, the timer and ctx are
// selected on together.
func runProcess(ctx context.Context, argv []string, dir string, env []string, log *os.File, timeout time.Duration) procResult {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = log
	cmd.Stderr = log
	configure(cmd)
	if err := cmd.Start(); err != nil {
		return procResult{startErr: err}
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	var expired <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expired = t.C
	}
	var r procResult
	select {
	case <-exited:
		r.exit = cmd.ProcessState.ExitCode()
		return r
	case <-expired:
		r.timedOut = true
	case <-ctx.Done():
		r.interrupted = true
	}
	killTree(cmd.Process)
	<-exited
	r.exit = cmd.ProcessState.ExitCode()
	return r
}
