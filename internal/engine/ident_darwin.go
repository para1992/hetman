package engine

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// processIdent returns the start time of a process and whether it exists.
func processIdent(pid int) (string, bool) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(k.Proc.P_pid) != pid {
		return "", false
	}
	t := k.Proc.P_starttime
	return strconv.FormatInt(int64(t.Sec), 10) + "." + strconv.FormatInt(int64(t.Usec), 10), true
}
