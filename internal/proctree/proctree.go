// Package proctree finds and kills the processes descended from a process,
// including those that left its process group or session.
//
// Only Linux offers what this needs: a subreaper, to which orphaned
// descendants are re-parented instead of to the init process, and /proc,
// from which the parent of every process can be read. Elsewhere the
// functions do nothing, and a process tree is only as contained as its
// process group (or, on Windows, its job object) keeps it.
package proctree

import "time"

// KillDescendants kills every live descendant of pid and waits a moment for
// them to go, repeating while new ones appear. Called with the current
// process after making it a subreaper, it kills whatever the processes it
// started left behind, wherever they moved to.
func KillDescendants(pid int) {
	for round := 0; round < 50; round++ {
		pids := Descendants(pid)
		if len(pids) == 0 {
			return
		}
		for _, p := range pids {
			kill(p)
		}
		time.Sleep(20 * time.Millisecond)
		for _, p := range pids {
			reap(p)
		}
	}
}
