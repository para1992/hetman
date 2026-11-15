package engine

import (
	"path/filepath"
	"strings"

	"hetman/internal/adapter"

	"hetman/internal/orders"
)

// ShowPath resolves an entry of the show list of an asked event to the file
// it names: a path relative to the orders folder of the campaign that asks
// (no), or, after orders.WorkPrefix, relative to that campaign's work
// folder, which lies in the state folder of the orders a person started
// (top). ok is false for an entry that would leave its folder.
func ShowPath(top, no *orders.Orders, campaign int64, entry string) (path string, ok bool) {
	if no == nil {
		no = top
	}
	base, rel := no.Dir, entry
	if rest, found := strings.CutPrefix(entry, orders.WorkPrefix); found {
		base, rel = WorkDir(top, campaign), rest
	}
	if rel == "" || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", false
	}
	path = filepath.Join(base, filepath.FromSlash(rel))
	// A symbolic link inside the folder must not lead out of it: the file
	// is compared with the folder after resolving links on both sides.
	if r, err := filepath.Rel(adapter.Real(base), adapter.Real(path)); err != nil || !filepath.IsLocal(r) {
		return "", false
	}
	return path, true
}
