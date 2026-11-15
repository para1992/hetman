package adapter

import (
	"path/filepath"
)

// A folder may be reached through a symbolic link, as the system's
// temporary folder is on macOS (/var is /private/var). A client usually
// reports and checks paths with links resolved, while the engine passes
// them as the orders spell them, so paths are compared in both spellings.

// Real returns path with its symbolic links resolved. For a path that does
// not exist yet, its deepest existing parent is resolved and the rest is
// appended. A path that cannot be resolved is returned cleaned.
func Real(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for p := path; ; {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			if rest == "" {
				return r
			}
			return filepath.Join(r, rest)
		}
		up := filepath.Dir(p)
		if up == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = up
	}
}

// Spellings returns path and, when it differs, path with its links
// resolved.
func Spellings(path string) []string {
	path = filepath.Clean(path)
	if r := Real(path); r != path {
		return []string{path, r}
	}
	return []string{path}
}

// Rel returns path relative to dir when it lies in dir or is dir, with
// either of them spelled as given or with links resolved.
func Rel(dir, path string) (string, bool) {
	for _, d := range Spellings(dir) {
		for _, p := range Spellings(path) {
			if r, err := filepath.Rel(d, p); err == nil && (r == "." || filepath.IsLocal(r)) {
				return r, true
			}
		}
	}
	return "", false
}
