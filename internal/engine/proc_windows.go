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

// killTree kills the process and all of its descendants with the system
// taskkill tool, falling back to killing the process alone.
func killTree(p *os.Process) {
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid))
	if err := kill.Run(); err != nil {
		p.Kill()
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
