package edit

import (
	"fmt"
	"strings"
)

// diffContext is how many unchanged lines a hunk shows around a change.
const diffContext = 2

// unified renders the difference between two texts as a unified diff of
// lines; it is empty when they are equal. existed tells whether the old
// file exists, for the header.
func unified(name string, a, b []byte, existed bool) string {
	if existed && string(a) == string(b) {
		return ""
	}
	x, y := splitLines(a), splitLines(b)
	ops := diffLines(x, y)
	var out strings.Builder
	from := "--- " + name
	if !existed {
		from = "--- /dev/null"
	}
	fmt.Fprintf(&out, "%s\n+++ %s\n", from, name)
	// Group the changes into hunks with context.
	i := 0
	for i < len(ops) {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := i - diffContext
		if start < 0 {
			start = 0
		}
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			// A run of unchanged lines long enough ends the hunk.
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				end += diffContext
				if end > run {
					end = run
				}
				break
			}
			end = run
		}
		aStart, bStart, aLen, bLen := 0, 0, 0, 0
		for k := 0; k < start; k++ {
			if ops[k].kind != '+' {
				aStart++
			}
			if ops[k].kind != '-' {
				bStart++
			}
		}
		for k := start; k < end; k++ {
			if ops[k].kind != '+' {
				aLen++
			}
			if ops[k].kind != '-' {
				bLen++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(aStart, aLen), hunkRange(bStart, bLen))
		for k := start; k < end; k++ {
			fmt.Fprintf(&out, "%c%s\n", ops[k].kind, ops[k].line)
		}
		i = end
	}
	return out.String()
}

func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if n == 1 {
		return fmt.Sprint(start + 1)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}

func splitLines(s []byte) []string {
	if len(s) == 0 {
		return nil
	}
	text := strings.TrimSuffix(string(s), "\n")
	return strings.Split(text, "\n")
}

type diffOp struct {
	kind byte // ' ', '-' or '+'
	line string
}

// diffLines finds a shortest edit between two lists of lines by the
// longest common subsequence. Orders files are small enough for that.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffOp{' ', a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffOp{'-', a[i]})
			i++
		default:
			out = append(out, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffOp{'+', b[j]})
	}
	return out
}
