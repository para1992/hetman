package engine

import (
	"os"
	"strconv"
	"strings"
)

// processIdent returns the start time of a process (field 22 of
// /proc/<pid>/stat, in clock ticks since boot) and whether it exists.
func processIdent(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	// The command name in parentheses may contain spaces; fields after it
	// start with the state (field 3).
	s := string(data)
	fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(fields) < 20 {
		return "", false
	}
	return fields[19], true
}
