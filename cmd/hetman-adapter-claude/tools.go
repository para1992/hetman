package main

import (
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"hetman/internal/adapter"
)

// What the client honours in a tool entry, read from its help and from the
// rule checks it applies to permission settings:
//
//   - an entry is Tool or Tool(content) and ends at the closing parenthesis;
//     parentheses inside the content are literal, and empty parentheses are
//     refused;
//   - built-in tool names start with an upper-case letter;
//   - a file path in parentheses is matched only by Edit(...) rules, which
//     cover every tool that changes files (Edit, Write, MultiEdit,
//     NotebookEdit), and by Read(...) rules, which cover the tools that read
//     files; Write(path), MultiEdit(path), NotebookEdit(path) and Glob(path)
//     are accepted by the client but never match, so the call is denied;
//   - the ":*" prefix form belongs to Bash rules only;
//   - tool servers are not loaded (--strict-mcp-config), so their tools,
//     mcp__SERVER__TOOL, do not exist.

// readOnlyNote tells the log what a step with Bash can do beyond its
// entries: the client runs, without any rule, commands it judges read-only
// (ls, cat, head, tail, grep, wc, diff, stat and the like, also chained with
// && or |) whenever the Bash tool is available, and no option of print mode
// turns that off.
const readOnlyNote = "note: with Bash available the client also runs commands it judges read-only (ls, cat, head, tail, grep, find, wc, diff and the like) without a matching entry; the adapter cannot turn this off"

// pathOnlyIn maps a tool whose path rules never match to the tool whose
// rules do.
var pathOnlyIn = map[string]string{"Write": "Edit", "MultiEdit": "Edit", "NotebookEdit": "Edit", "Glob": "Read"}

// fileTools take a path pattern in parentheses.
var fileTools = map[string]bool{"Read": true, "Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true, "Glob": true, "Grep": true}

// splitTool splits an entry into its tool name and content; call is false
// for a bare name. ok is false for an entry that does not end at the
// closing parenthesis of its content.
func splitTool(t string) (name, content string, call, ok bool) {
	t = strings.TrimSpace(t)
	i := strings.IndexByte(t, '(')
	if i < 0 {
		return t, "", false, !strings.Contains(t, ")")
	}
	if !strings.HasSuffix(t, ")") {
		return strings.TrimSpace(t[:i]), "", true, false
	}
	return strings.TrimSpace(t[:i]), t[i+1 : len(t)-1], true, true
}

// toolProblem says why the client would not honour a tools entry, or ""
// when it would.
func toolProblem(t string) string {
	if strings.TrimSpace(t) == "" {
		return "empty entry"
	}
	name, content, call, ok := splitTool(t)
	switch {
	case !ok:
		return "an entry is Tool or Tool(pattern) and must end at the closing parenthesis; a list may have been split at a space inside it"
	case name == "":
		return "no tool name before the parentheses"
	case strings.HasPrefix(name, "mcp__"):
		return "tool servers are not loaded by this adapter, so their tools do not exist"
	case !unicode.IsUpper([]rune(name)[0]):
		return fmt.Sprintf("client tool names start with an upper-case letter, such as %s", upperFirst(name))
	case call && strings.TrimSpace(content) == "":
		return fmt.Sprintf("empty parentheses; write %s alone, or give a pattern", name)
	}
	if !call {
		return ""
	}
	if via, found := pathOnlyIn[name]; found {
		return fmt.Sprintf("the client never matches a path in %s(...) rules, so every call would be denied; write %s(%s), which covers %s", name, via, content, coverage(via))
	}
	if fileTools[name] && strings.Contains(content, ":*") {
		return "the :* form belongs to Bash rules; match files with a glob such as *.go or src/**"
	}
	return ""
}

func coverage(tool string) string {
	if tool == "Edit" {
		return "every tool that changes files, Write included"
	}
	return "the tools that read files"
}

func upperFirst(s string) string {
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// stepProblems lists what in a step the adapter rejects before running the
// client: an effort the client does not know and tools it would not honour.
func stepProblems(_ options, st adapter.Step, _ io.Writer) []adapter.CheckProblem {
	var out []adapter.CheckProblem
	if st.Effort != "" {
		ok := false
		for _, e := range efforts {
			ok = ok || st.Effort == e
		}
		if !ok {
			out = append(out, adapter.CheckProblem{Field: adapter.FieldEffort, Message: fmt.Sprintf("effort %q is not one of %s", st.Effort, strings.Join(efforts, ", "))})
		}
	}
	for i, t := range st.Tools {
		if msg := toolProblem(t); msg != "" {
			out = append(out, adapter.ToolProblem(i, msg))
		}
	}
	return out
}

// rulePath writes an absolute path the way the client's permission rules
// name one: "//" and the path with forward slashes, a Windows drive letter
// as its first folder ("//c/Users/...").
func rulePath(p string) string {
	p = filepath.ToSlash(filepath.Clean(p))
	if runtime.GOOS == "windows" && len(p) >= 2 && p[1] == ':' {
		p = "/" + strings.ToLower(p[:1]) + p[2:]
	}
	return "/" + p
}

// fenceArgs keeps the client's file tools from writing the step's
// protected paths: an Edit deny rule, which covers every tool that changes
// files, for each path and everything inside it. The client's deny rules
// always win over its allow rules, so a protected folder that holds a
// writable one is denied entry by entry around it (see adapter.Fence). A
// writable folder outside the working folder is added as a working folder
// of its own, since the file tools stay inside those.
func fenceArgs(st adapter.Step) []string {
	var a []string
	for _, w := range st.Writable {
		if w != "" && !inside(st.Workdir, w) {
			a = append(a, "--add-dir", w)
		}
	}
	for _, p := range adapter.Fence(st.Protect, st.Writable) {
		a = append(a, "--disallowedTools=Edit("+rulePath(p)+")", "--disallowedTools=Edit("+rulePath(p)+"/**)")
	}
	return a
}

// inside reports whether path is dir or lies in it.
func inside(dir, path string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && filepath.IsLocal(rel) || rel == "."
}
