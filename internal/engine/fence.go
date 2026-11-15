package engine

import (
	"fmt"

	"hetman/internal/adapter"
	"os"
	"path/filepath"

	"hetman/internal/orders"
)

// WorkDir is the work folder of a campaign (HETMAN_RUN_DIR): where steps
// hand files to each other. top is the orders of the campaign a person
// started; every campaign of a tree lives in its state folder.
func WorkDir(top *orders.Orders, id int64) string {
	return filepath.Join(CampaignDir(top, id), "work")
}

// workdir is the folder an agent step works in. It must exist and must not
// lie in the state folder of the campaign tree, outside the work folder.
func (e *Engine) workdir(node *orders.Node) (string, error) {
	dir := node.Workdir
	if dir == "" {
		return "", fmt.Errorf("%s names no workdir", node.ID)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("%s: workdir %s: %v", node.ID, dir, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s: workdir %s is not a folder", node.ID, dir)
	}
	// Compared with symbolic links resolved too, so a link into the state
	// folder does not get past the rule.
	state, work := StateDir(e.top().Orders), WorkDir(e.top().Orders, e.Campaign)
	_, inState := adapter.Rel(state, dir)
	_, inWork := adapter.Rel(work, dir)
	if inState && !inWork {
		return "", fmt.Errorf("%s: workdir %s lies in %s, which holds hetman's own files", node.ID, dir, state)
	}
	return dir, nil
}

// fence is what an agent step's file tools may not write, and the folder
// they must still be able to write: the state folder of the campaign tree
// (the chronicle and every campaign folder) and the files of the orders
// are protected; the campaign's work folder stays writable, since steps
// hand files to each other there. Scripts and gates are trusted code and
// are not fenced.
func (e *Engine) fence() (protect, writable []string) {
	top := e.top().Orders
	work := WorkDir(top, e.Campaign)
	protect = append(protect, StateDir(top))
	for _, f := range top.Files() {
		if f != work && !orders.Within(work, f) && !orders.Within(StateDir(top), f) {
			protect = append(protect, f)
		}
	}
	// A segment's orders may lie outside the folder of the top orders; its
	// files are in Files already, as are those of every nested segment.
	return protect, []string{work}
}
