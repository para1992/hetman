package engine

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configure(*exec.Cmd) {}

// procTree is a started process with a job object of its own that every
// process it starts inherits, so that the whole tree can be killed even
// after the process itself has exited. Without the job object (it could
// not be made) only a running tree can be killed, found by parent ids.
type procTree struct {
	p     *os.Process
	mu    sync.Mutex
	job   windows.Handle
	ended bool
}

// track puts a started process into a new job object. A process it started
// in the moment before is not in the job.
func track(p *os.Process) *procTree {
	t := &procTree{p: p}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return t
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return t
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return t
	}
	t.job = job
	return t
}

// kill kills the running process and all of its descendants.
func (t *procTree) kill() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ended {
		return
	}
	if t.job != 0 && windows.TerminateJobObject(t.job, 1) == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(t.p.Pid))
	if err := kill.Run(); err != nil {
		t.p.Kill()
	}
}

// end kills what is left of the tree once the process has exited and
// been waited for, and releases the job object.
func (t *procTree) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ended = true
	if t.job != 0 {
		windows.TerminateJobObject(t.job, 1)
		windows.CloseHandle(t.job)
		t.job = 0
	}
}

var contain struct {
	once sync.Once
	job  windows.Handle
	err  error
}

// ContainChildren puts the engine process into a job object that kills
// every process in it when its last handle closes. Processes started later
// inherit the job, so when the engine dies, however it dies, the system
// kills every step process tree with it. The handle is never closed.
func ContainChildren() error {
	contain.once.Do(func() {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			contain.err = err
			return
		}
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(job)
			contain.err = err
			return
		}
		if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
			windows.CloseHandle(job)
			contain.err = err
			return
		}
		contain.job = job
	})
	return contain.err
}

const stillActive = 259

// processIdent returns the creation time of a running process and whether
// it is running.
func processIdent(pid int) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code != stillActive {
		return "", false
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", false
	}
	return strconv.FormatInt(created.Nanoseconds(), 10), true
}

// cleanupStale kills the process tree of an earlier step process if that
// process still runs with the recorded creation time. Normally the job
// object has already killed it together with the engine.
func cleanupStale(pid int, ident string) string {
	if ident == "" {
		return cleanupUnverified
	}
	cur, alive := processIdent(pid)
	switch {
	case !alive:
		return cleanupGone
	case cur != ident:
		return cleanupReused
	}
	exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	waitGone(func() bool { _, alive := processIdent(pid); return alive })
	return cleanupKilled
}
