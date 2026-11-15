package adapter

import (
	"fmt"
	"strings"
)

// A client's message for a denied call says only that the call was denied,
// and a model may take it for the whole tool being unavailable. Adapters
// therefore add to the prompt which entries of each tool the step allows,
// in the form the model should use.

// ScopeEntry is one tools entry as an adapter reads it: a tool, and the
// pattern it is limited to, empty for the whole tool.
type ScopeEntry struct {
	Tool, Pattern string
}

// ScopeNote is the paragraph an adapter adds to the prompt about the
// step's tools; shell names the client's shell tool, whose patterns match
// whole command lines. It is empty without entries.
func ScopeNote(entries []ScopeEntry, shell string) string {
	if len(entries) == 0 {
		return ""
	}
	var tools []string
	patterns := map[string][]string{}
	whole := map[string]bool{}
	for _, e := range entries {
		if _, seen := patterns[e.Tool]; !seen && !whole[e.Tool] {
			tools = append(tools, e.Tool)
			patterns[e.Tool] = nil
		}
		if e.Pattern == "" {
			whole[e.Tool] = true
		} else {
			patterns[e.Tool] = append(patterns[e.Tool], "`"+e.Pattern+"`")
		}
	}
	var b strings.Builder
	b.WriteString("\n\n---\nThe tools of this step, and how far each reaches; any other call is denied without asking anybody:\n")
	limited := false
	for _, t := range tools {
		switch {
		case whole[t]:
			fmt.Fprintf(&b, "- %s: allowed\n", t)
		case strings.EqualFold(t, shell):
			limited = true
			fmt.Fprintf(&b, "- %s: only a command line that matches one of %s\n", t, strings.Join(patterns[t], ", "))
		default:
			fmt.Fprintf(&b, "- %s: only for %s\n", t, strings.Join(patterns[t], ", "))
		}
	}
	b.WriteString("A denied call does not mean its tool is unavailable: a call that matches an entry above is still allowed.")
	if limited {
		fmt.Fprintf(&b, " Run each command as an entry allows it, on its own: commands joined with &&, ; or | form one command line, which must match an entry as a whole.")
	}
	b.WriteString("\n")
	return b.String()
}
