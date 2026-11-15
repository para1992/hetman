package adapter

import (
	"os"
	"path/filepath"
	"sort"
)

// Fence turns a step's protect and writable lists into paths to deny, each
// together with everything inside it, for a client whose deny rules always
// win over its allow rules: a protected path that holds a writable folder
// is not denied as a whole; instead each entry in it is, except the one on
// the way to the writable folder. Entries are those that exist now, so a
// file created later next to that way is not covered.
func Fence(protect, writable []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	var walk func(p string, depth int)
	walk = func(p string, depth int) {
		var inside []string
		for _, w := range writable {
			r, ok := Rel(p, w)
			if ok && r == "." {
				return
			}
			if ok {
				inside = append(inside, w)
			}
		}
		if len(inside) == 0 {
			add(p)
			return
		}
		entries, err := os.ReadDir(p)
		if err != nil || depth > 64 {
			return
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			walk(filepath.Join(p, name), depth+1)
		}
	}
	for _, p := range protect {
		walk(filepath.Clean(p), 0)
	}
	return out
}
