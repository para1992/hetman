package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"hetman/internal/adapter"
)

// The client does not allow or deny tools by name. The model runs shell
// commands and edits files with apply_patch, and the client's sandbox
// decides what those may touch: a permission profile of folders that may be
// read or written, and whether commands reach the network. Command rules
// exist, but only in rules files in the user's or the project's folder,
// which the adapter does not load (--ignore-rules) and cannot pass on the
// command line. So a tools entry names what the sandbox may do, in the
// profile's own words:
//
//   - read: the shell is available; commands and the model may read files
//     (the whole file system, as far as the sandbox lets them) and write
//     nothing;
//   - write: as read, and commands and apply_patch may write in the working
//     folder, the step's writable folders and the system's temporary
//     folders, except the protected paths;
//   - write(FOLDER): as read, and they may write in FOLDER (relative to the
//     working folder, or absolute), the step's writable folders and the
//     temporary folders, except the protected paths. The client's sandbox
//     opens only an existing folder for writing, so FOLDER must exist when
//     the step runs; "FOLDER/**" means the same;
//   - network: commands may reach the network (with read or write);
//   - web_search: the client's own web search tool is available.
//
// Without read or write the shell is switched off and the profile is
// read-only. What the client checked when these were chosen:
//
//   - a profile's write entry takes an exact path or a trailing "/**"; any
//     other glob is refused by the client ("only supports deny access");
//   - a write entry on a file, or on a folder that does not exist, does not
//     make it writable (the Linux sandbox fails or keeps it read-only);
//   - a read entry on a protected path inside a writable folder wins over
//     the folder, also for a path that does not exist yet, and a write entry
//     on a folder inside a protected one wins over the protected one.

// toolset is what a step's tools entries ask of the sandbox.
type toolset struct {
	read, write     bool
	folders         []string // write(FOLDER), as written
	network, search bool
}

// shell reports whether the step has the shell.
func (t toolset) shell() bool { return t.read || t.write || len(t.folders) > 0 }

var (
	toolRule = regexp.MustCompile(`^([^()]*)\((.*)\)$`)
	globChar = regexp.MustCompile(`[*?\[\]{}]`)
)

// vocabulary says the adapter's tools entries in a few words.
const vocabulary = "read, write, write(FOLDER), network and web_search"

// splitEntry splits an entry into its name and pattern; ok is false for an
// entry that does not end at the closing parenthesis of its pattern.
func splitEntry(t string) (name, pattern string, hasPattern, ok bool) {
	t = strings.TrimSpace(t)
	if g := toolRule.FindStringSubmatch(t); g != nil {
		return strings.TrimSpace(g[1]), strings.TrimSpace(g[2]), true, true
	}
	return t, "", false, !strings.ContainsAny(t, "()")
}

// toolProblem says why the client would not enforce a tools entry as
// written, or "".
func toolProblem(t string) string {
	if strings.TrimSpace(t) == "" {
		return "empty entry"
	}
	name, pattern, hasPattern, ok := splitEntry(t)
	switch {
	case !ok:
		return "an entry is a name or name(FOLDER) and must end at the closing parenthesis; a list may have been split at a space inside it"
	case hasPattern && pattern == "":
		return "empty parentheses"
	}
	switch strings.ToLower(name) {
	case "read":
		if hasPattern {
			return "the client's sandbox cannot limit reading to some paths; write read, which lets commands read what the sandbox allows (the whole file system but for what it hides)"
		}
	case "write":
		if hasPattern {
			folder := strings.TrimSuffix(strings.TrimSuffix(pattern, "/**"), `\**`)
			if folder == "" || globChar.MatchString(folder) {
				return "the client's sandbox opens only whole folders for writing (FOLDER or FOLDER/**); a glob such as *.go is refused by it"
			}
		}
	case "network", "web_search":
		if hasPattern {
			return fmt.Sprintf("%s takes no pattern: the client limits domains only through its experimental network proxy, which this adapter does not use", name)
		}
	case "shell", "bash", "exec", "exec_command", "unified_exec", "command":
		if hasPattern {
			return "the client cannot limit commands by pattern from the command line (its command rules live in rules files in the user's or the project's folder, which the adapter does not load); commands are confined by the sandbox instead: use read, write or write(FOLDER)"
		}
		return "the shell is not a tool of its own here: read, write and write(FOLDER) make it available, confined by the client's sandbox"
	case "edit", "apply_patch", "patch", "multiedit":
		return "file edits are governed by the client's sandbox: write lets apply_patch and commands write in the working folder, write(FOLDER) only in FOLDER"
	default:
		if strings.HasPrefix(name, "mcp__") {
			return "tool servers are not loaded by this adapter, so their tools do not exist"
		}
		return fmt.Sprintf("not an entry of this adapter: the client restricts by sandbox, not by named tools; the entries are %s", vocabulary)
	}
	if name != strings.ToLower(name) {
		return fmt.Sprintf("write %s in lower case", strings.ToLower(name))
	}
	return ""
}

// parseTools reads a step's tools entries and lists those the client would
// not enforce as written.
func parseTools(tools []string) (toolset, []adapter.CheckProblem) {
	var ts toolset
	var problems []adapter.CheckProblem
	network := -1
	for i, t := range tools {
		if msg := toolProblem(t); msg != "" {
			problems = append(problems, adapter.ToolProblem(i, msg))
			continue
		}
		name, pattern, hasPattern, _ := splitEntry(t)
		switch name {
		case "read":
			ts.read = true
		case "write":
			if hasPattern {
				pattern = strings.TrimSuffix(strings.TrimSuffix(pattern, "/**"), `\**`)
				ts.folders = append(ts.folders, pattern)
			} else {
				ts.write = true
			}
		case "network":
			ts.network = true
			network = i
		case "web_search":
			ts.search = true
		}
	}
	if network >= 0 && !ts.shell() && len(problems) == 0 {
		problems = append(problems, adapter.ToolProblem(network, "network lets shell commands reach the network, and without read or write the step has no shell; add read or write"))
	}
	return ts, problems
}

// sandbox is the client configuration of a step's tools.
type sandbox struct {
	tools   toolset
	extends string   // the built-in profile the step's profile builds on
	entries []access // filesystem entries of the profile, in order
	// writes lists the folders the step may write, for the prompt and
	// the log; empty when it may write nothing.
	writes  []string
	protect []string // protected paths, for the prompt
	notes   []string // log lines about the sandbox
}

// access is one filesystem entry of a permission profile.
type access struct {
	path, mode string // mode is read or write; path is absolute or a special path
}

// profileName is the name of the profile the adapter defines for a run. The
// user's configuration is not loaded, so no profile of the same name can
// add to it.
const profileName = "hetman"

// sandboxFor builds the permission profile of a step. Writable folders and
// protected paths are written in both spellings when a symbolic link is on
// the way (a sandbox may check either).
func sandboxFor(st adapter.Step, ts toolset) (sandbox, error) {
	sb := sandbox{tools: ts, extends: ":read-only"}
	writes := ts.write || len(ts.folders) > 0
	if !writes {
		if len(st.Writable) > 0 && ts.shell() {
			sb.notes = append(sb.notes, "note: the step's tools write nothing, so its writable folders stay read-only")
		}
		return sb, nil
	}
	seen := map[string]bool{}
	add := func(p, mode string) {
		for _, q := range adapter.Spellings(p) {
			if !seen[q] {
				seen[q] = true
				sb.entries = append(sb.entries, access{q, mode})
			}
		}
	}
	if ts.write {
		sb.extends = ":workspace"
		add(st.Workdir, "write")
		sb.writes = append(sb.writes, st.Workdir)
	} else {
		for _, f := range ts.folders {
			p := f
			if !filepath.IsAbs(p) {
				p = filepath.Join(st.Workdir, p)
			}
			p = filepath.Clean(p)
			fi, err := os.Stat(p)
			if err != nil || !fi.IsDir() {
				return sb, fmt.Errorf("tools entry %q: %s is not an existing folder; the client's sandbox opens only an existing folder for writing, so create it before the step", "write("+f+")", p)
			}
			add(p, "write")
			sb.writes = append(sb.writes, p)
		}
		// The client's workspace mode lets commands write the temporary
		// folders; tools such as compilers and test runners need them.
		sb.entries = append(sb.entries, access{":slash_tmp", "write"}, access{":tmpdir", "write"})
	}
	writable := map[string]bool{}
	for _, w := range st.Writable {
		if w == "" {
			continue
		}
		if fi, err := os.Stat(w); err != nil || !fi.IsDir() {
			sb.notes = append(sb.notes, fmt.Sprintf("note: writable folder %s does not exist, so the client's sandbox cannot open it for writing", w))
			continue
		}
		for _, q := range adapter.Spellings(w) {
			writable[q] = true
		}
		add(w, "write")
		sb.writes = append(sb.writes, w)
	}
	for _, p := range st.Protect {
		if p == "" || writable[filepath.Clean(p)] {
			continue
		}
		for _, q := range adapter.Spellings(p) {
			if !writable[q] && !seen[q] {
				seen[q] = true
				sb.entries = append(sb.entries, access{q, "read"})
			}
		}
		sb.protect = append(sb.protect, p)
	}
	return sb, nil
}

// profileArgs are the client's configuration overrides that define the
// step's permission profile and select it, together with the tools that
// depend on the step's entries.
func profileArgs(sb sandbox) []string {
	a := []string{"-c", "default_permissions=" + tomlString(profileName),
		"-c", "permissions." + profileName + ".extends=" + tomlString(sb.extends)}
	if len(sb.entries) > 0 {
		var parts []string
		for _, e := range sb.entries {
			parts = append(parts, tomlString(e.path)+"="+tomlString(e.mode))
		}
		a = append(a, "-c", "permissions."+profileName+".filesystem={"+strings.Join(parts, ",")+"}")
	}
	if sb.tools.network {
		a = append(a, "-c", "permissions."+profileName+".network.enabled=true")
	}
	search := "disabled"
	if sb.tools.search {
		search = "live"
	}
	a = append(a, "-c", "web_search="+tomlString(search))
	if !sb.tools.shell() {
		a = append(a, "--disable", "shell_tool", "--disable", "unified_exec")
	}
	return a
}

// tomlString writes a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// scopeNote tells the model what the step's sandbox allows. The client's
// message for a refused write names neither the folders nor the reason, and
// a model may take it for the shell being unavailable.
func scopeNote(st adapter.Step, sb sandbox) string {
	var b strings.Builder
	b.WriteString("\n\n---\nWhat this step may do; the client's sandbox enforces it, and anything else fails without asking anybody:\n")
	if !sb.tools.shell() {
		b.WriteString("- no shell commands and no file edits\n")
	} else {
		b.WriteString("- run shell commands and read files\n")
		if len(sb.writes) == 0 {
			b.WriteString("- write nothing: a write fails with a read-only file system error\n")
		} else {
			fmt.Fprintf(&b, "- write files, with apply_patch or commands, only in %s and the system's temporary folders\n", strings.Join(sb.writes, ", "))
			if len(sb.protect) > 0 {
				fmt.Fprintf(&b, "- never write in %s (protected; a write there fails)\n", strings.Join(sb.protect, ", "))
			}
		}
		if sb.tools.network {
			b.WriteString("- commands may reach the network\n")
		} else {
			b.WriteString("- commands cannot reach the network\n")
		}
	}
	if sb.tools.search {
		b.WriteString("- search the web with the web search tool\n")
	}
	b.WriteString("A failed write does not mean other writes fail: a write the list above allows still works.\n")
	return b.String()
}

// toolsText lists the step's entries for the log.
func toolsText(tools []string) string {
	if len(tools) == 0 {
		return "none"
	}
	return strings.Join(tools, ", ")
}

// stepProblems lists what in a step the adapter rejects before running the
// client: tools entries it would not enforce, and the model and effort as
// the client's catalogue judges them (see modelProblems).
func stepProblems(o options, st adapter.Step, diag io.Writer) []adapter.CheckProblem {
	_, out := parseTools(st.Tools)
	return append(out, modelProblems(o, st, diag)...)
}
