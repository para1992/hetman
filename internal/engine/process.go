package engine

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// Results of cleaning up after an interrupted step, recorded as the outcome
// of a process_cleanup event.
const (
	cleanupGone       = "gone"       // nothing of the old process tree was alive
	cleanupKilled     = "killed"     // the old process tree was alive and is now killed
	cleanupReused     = "reused"     // the id now belongs to an unrelated process; left alone
	cleanupUnverified = "unverified" // the start time could not be read; left alone
	cleanupUnrecorded = "unrecorded" // the attempt's process was never recorded; nothing to check
)

// waitGone waits up to a few seconds for alive to report false. Death after
// SIGKILL or TerminateProcess is asynchronous and has no event to wait on.
func waitGone(alive func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for alive() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

type procResult struct {
	exit        int
	startErr    error
	timedOut    bool
	interrupted bool
}

// runProcess starts argv directly (no shell) and waits for it to exit. Output
// goes to log. Exceeding timeout or cancelling ctx kills the whole process
// tree. started is called with the process id once the process runs.
// Waiting is event driven: the process exit, the timer and ctx are selected
// on together.
func runProcess(ctx context.Context, argv []string, dir string, env []string, log *os.File, timeout time.Duration, started func(pid int)) procResult {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = log
	cmd.Stderr = log
	configure(cmd)
	if err := cmd.Start(); err != nil {
		return procResult{startErr: err}
	}
	started(cmd.Process.Pid)
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
