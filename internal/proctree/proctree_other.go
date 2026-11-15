//go:build !linux

package proctree

// Subreaper has no equivalent here.
func Subreaper() error { return nil }

// Descendants cannot be found here; it returns none.
func Descendants(int) []int { return nil }

func kill(int) {}

func reap(int) {}
