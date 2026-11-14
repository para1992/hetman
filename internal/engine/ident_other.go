//go:build !windows && !linux && !darwin

package engine

// processIdent cannot read a start time here, so stale processes are never
// killed on this system.
func processIdent(int) (string, bool) { return "", false }
