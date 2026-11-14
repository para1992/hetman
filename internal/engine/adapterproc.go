package engine

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"hetman/internal/adapter"
)

// Grace periods of an adapter process; variables so that tests can shorten
// them.
var (
	// cancelGrace is how long an adapter may take to exit after a cancel
	// message before its process tree is killed.
	cancelGrace = 3 * time.Second
	// exitGrace is how long an adapter may take to exit after its result
	// or error message.
	exitGrace = 5 * time.Second
	// drainGrace is how long standard output is still read after the
	// adapter exited, in case a process it started holds it open.
	drainGrace = time.Second
	// describeTimeout limits an adapter's --describe.
	describeTimeout = 30 * time.Second
)

// maxLine is the longest message line accepted from an adapter.
const maxLine = 64 << 20

// adapterProc is one adapter process to run for a step.
type adapterProc struct {
	argv    []string
	dir     string
	env     []string
	start   []byte   // the start message line
	log     *os.File // receives the text of log messages
	stderr  *os.File // receives the adapter's standard error
	timeout time.Duration
	idle    time.Duration
	started func(pid int)
	session func(id string) // called for each session message
}

// adapterRun is how an adapter process ended.
type adapterRun struct {
	startErr    error
	exit        int
	terminal    *adapter.Message // the result or error message, if one came
	timedOut    bool
	idleOut     bool
	interrupted bool
	protocol    string // the first protocol violation
	lateExit    bool   // killed because it did not exit after its terminal message
}

func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

func timerC(t *time.Timer) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

// runAdapter runs an adapter process under the protocol: it writes the
// start message, reads messages until the result or error and the exit of
// the process, and enforces the timeout and the idle timeout. Cancelling
// ctx sends a cancel message and kills the process tree if the adapter does
// not exit within cancelGrace.
func runAdapter(ctx context.Context, p adapterProc) adapterRun {
	var r adapterRun
	inR, inW, err := os.Pipe()
	if err != nil {
		r.startErr = err
		return r
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		r.startErr = err
		return r
	}
	cmd := exec.Command(p.argv[0], p.argv[1:]...)
	cmd.Dir, cmd.Env = p.dir, p.env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, p.stderr
	configure(cmd)
	err = cmd.Start()
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		r.startErr = err
		return r
	}
	p.started(cmd.Process.Pid)
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()

	// Writes to the adapter go through one goroutine, so that a large
	// prompt never blocks the engine on an adapter that does not read.
	writes := make(chan []byte, 2)
	go func() {
		for b := range writes {
			inW.Write(b)
		}
		inW.Close()
	}()
	defer close(writes)
	writes <- p.start

	done := make(chan struct{})
	defer close(done)
	lines := make(chan []byte, 16)
	go func() {
		defer outR.Close()
		defer close(lines)
		br := bufio.NewReaderSize(outR, 64<<10)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				select {
				case lines <- line:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	var overall, idle, grace, drain *time.Timer
	if p.timeout > 0 {
		overall = time.NewTimer(p.timeout)
	}
	if p.idle > 0 {
		idle = time.NewTimer(p.idle)
	}
	defer func() { stopTimer(overall); stopTimer(idle); stopTimer(grace); stopTimer(drain) }()

	kill := func() {
		killTree(cmd.Process)
		<-exited
	}
	exitedC := (<-chan struct{})(exited)
	hasExited := false
	for {
		if hasExited && lines == nil {
			break
		}
		select {
		case line, ok := <-lines:
			if !ok {
				lines = nil
				continue
			}
			line = bytes.TrimRight(line, "\r\n")
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			if r.terminal != nil {
				fmt.Fprintf(p.log, "hetman: ignored a message after the %s: %s\n", r.terminal.Type, clip(line))
				continue
			}
			if len(line) > maxLine {
				r.protocol = fmt.Sprintf("message longer than %d bytes", maxLine)
				kill()
				return r
			}
			m, err := adapter.ParseMessage(line)
			if err != nil {
				r.protocol = fmt.Sprintf("%v: %s", err, clip(line))
				kill()
				return r
			}
			if idle != nil {
				idle.Reset(p.idle)
			}
			switch m.Type {
			case adapter.TypeLog:
				text := m.Text
				if len(text) == 0 || text[len(text)-1] != '\n' {
					text += "\n"
				}
				io.WriteString(p.log, text)
			case adapter.TypeSession:
				p.session(m.ID)
			default:
				r.terminal = &m
				stopTimer(idle)
				idle = nil
				grace = time.NewTimer(exitGrace)
			}
		case <-exitedC:
			exitedC, hasExited = nil, true
			r.exit = cmd.ProcessState.ExitCode()
			drain = time.NewTimer(drainGrace)
		case <-timerC(drain):
			lines = nil
		case <-timerC(grace):
			grace = nil
			if !hasExited {
				r.lateExit = true
				kill()
				r.exit = cmd.ProcessState.ExitCode()
				return r
			}
		case <-timerC(overall):
			r.timedOut = true
			kill()
			return r
		case <-timerC(idle):
			r.idleOut = true
			kill()
			return r
		case <-ctx.Done():
			r.interrupted = true
			writes <- adapter.EncodeCancel()
			wait := time.NewTimer(cancelGrace)
			select {
			case <-exited:
			case <-wait.C:
				kill()
			}
			wait.Stop()
			return r
		}
	}
	return r
}

// clip shortens a line for a message.
func clip(line []byte) string {
	const max = 200
	if len(line) > max {
		return string(line[:max]) + "..."
	}
	return string(line)
}

// describe runs an adapter with --describe.
func describe(ctx context.Context, argv []string, dir string, env []string) (adapter.Description, error) {
	cmd := exec.Command(argv[0], append(append([]string{}, argv[1:]...), adapter.DescribeFlag)...)
	cmd.Dir, cmd.Env = dir, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second // a process it left behind must not hold the output open
	configure(cmd)
	if err := cmd.Start(); err != nil {
		return adapter.Description{}, fmt.Errorf("cannot start %s: %v", argv[0], err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t := time.NewTimer(describeTimeout)
	defer t.Stop()
	select {
	case err := <-exited:
		if err != nil {
			return adapter.Description{}, fmt.Errorf("%s %s failed: %v %s", argv[0], adapter.DescribeFlag, err, bytes.TrimSpace(stderr.Bytes()))
		}
	case <-t.C:
		killTree(cmd.Process)
		<-exited
		return adapter.Description{}, fmt.Errorf("%s %s did not finish within %s", argv[0], adapter.DescribeFlag, describeTimeout)
	case <-ctx.Done():
		killTree(cmd.Process)
		<-exited
		return adapter.Description{}, errors.New("interrupted")
	}
	return adapter.ParseDescription(stdout.Bytes())
}
