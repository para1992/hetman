package proctree

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Subreaper makes the current process the one its orphaned descendants are
// re-parented to, so that a descendant whose parent exits, or that starts
// a session of its own, stays a descendant that KillDescendants finds.
func Subreaper() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}

// Descendants returns the live (not zombie) descendants of pid, read from
// /proc, parents before children.
func Descendants(pid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	live := map[int]bool{}
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid, state, ok := stat(p)
		if !ok {
			continue
		}
		children[ppid] = append(children[ppid], p)
		live[p] = state != "Z" && state != "X"
	}
	var out []int
	queue := []int{pid}
	seen := map[int]bool{pid: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if seen[c] {
				continue
			}
			seen[c] = true
			queue = append(queue, c)
			if live[c] {
				out = append(out, c)
			}
		}
	}
	return out
}

// stat reads the parent and the state of a process from /proc/<pid>/stat.
func stat(pid int) (ppid int, state string, ok bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, "", false
	}
	// The command name in parentheses may contain spaces; fields after it
	// start with the state (field 3) and the parent (field 4).
	s := string(data)
	fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(fields) < 2 {
		return 0, "", false
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, "", false
	}
	return ppid, fields[0], true
}

func kill(pid int) { syscall.Kill(pid, syscall.SIGKILL) }

// reap collects pid if it is a child of the current process that has
// exited, as a descendant re-parented to a subreaper becomes.
func reap(pid int) {
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
}
