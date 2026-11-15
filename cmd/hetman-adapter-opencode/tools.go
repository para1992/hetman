package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"hetman/internal/adapter"
)

// What the client honours in a tool entry, read from the permission schema
// of its configuration and from its tools:
//
//   - the permission names read, edit, glob, grep, list, bash, task, lsp and
//     skill take an action or patterns; todowrite, webfetch and websearch
//     take an action only, so a pattern on them makes the configuration
//     invalid;
//   - writing files (write, patch, apply_patch, multiedit) is governed by
//     the edit permission alone; an entry under any of those names never
//     matches;
//   - external_directory, doom_loop and question are set by the adapter: the
//     first would open folders outside the working folder, the other two
//     would ask a person who is not there.

// actionOnly are the permissions that take no pattern.
var actionOnly = map[string]bool{"todowrite": true, "webfetch": true, "websearch": true}

// underEdit are tool names whose writes the edit permission governs.
var underEdit = map[string]bool{"write": true, "patch": true, "apply_patch": true, "multiedit": true}

// adapterOwned are the permissions the adapter sets itself.
var adapterOwned = map[string]bool{"external_directory": true, "doom_loop": true, "question": true}

// toolProblem says why an entry would not be honoured as written, or "".
func toolProblem(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return "empty entry"
	}
	name, pattern, hasPattern := t, "", false
	if g := toolRule.FindStringSubmatch(t); g != nil {
		name, pattern, hasPattern = strings.TrimSpace(g[1]), strings.TrimSpace(g[2]), true
	} else if strings.ContainsAny(t, "()") {
		return "an entry is a permission name or name(pattern) and must end at the closing parenthesis; a list may have been split at a space inside it"
	}
	switch {
	case hasPattern && pattern == "":
		return "empty pattern"
	case !toolName.MatchString(name):
		return "not a client permission name such as read, edit, glob, grep, list, bash, webfetch or websearch, optionally with a pattern as in bash(git status)"
	case name == "task":
		return "refused: the subagents it starts run with the client's default permissions, not the step's tools"
	case underEdit[name]:
		return fmt.Sprintf("the client governs writing files with the edit permission, so %s never matches; write edit (or edit(PATTERN))", name)
	case adapterOwned[name]:
		return fmt.Sprintf("%s is set by the adapter: it keeps the agent in its working folder and never asks anybody", name)
	case hasPattern && actionOnly[name]:
		return fmt.Sprintf("the client takes no pattern for %s; write %s alone", name, name)
	}
	return ""
}

// stepProblems lists what in a step the adapter rejects before running the
// client: tools it would not honour and an effort the step's model does not
// offer. Listing the model's variants asks the client for its catalogue,
// which needs no model request.
func stepProblems(o options, st adapter.Step, diag io.Writer) []adapter.CheckProblem {
	var out []adapter.CheckProblem
	for i, t := range st.Tools {
		if msg := toolProblem(t); msg != "" {
			out = append(out, adapter.ToolProblem(i, msg))
		}
	}
	if err := checkEffort(o, st, diag); err != nil {
		out = append(out, adapter.CheckProblem{Field: adapter.FieldEffort, Message: err.Error()})
	}
	return out
}

// pathForms are the ways the client may write a path when it asks for the
// edit permission: relative to its worktree, which is the root of the git
// repository holding the working folder, or the root of the file system
// outside one. The adapter cannot tell which, so it writes the path
// relative to the working folder and to every folder above it; a pattern
// with "/*" after it covers what lies inside. The client's patterns know
// only "*" and "?" as wildcards, so a path holding either matches more.
//
// Either path may be reached through a symbolic link, and the client may
// see either spelling (its worktree comes from the resolved working folder,
// the path from what the model wrote), so every combination is written.
func pathForms(workdir, p string) []string {
	var out []string
	seen := map[string]bool{}
	for _, wd := range adapter.Spellings(workdir) {
		for _, path := range adapter.Spellings(p) {
			for dir := wd; ; {
				if rel, err := filepath.Rel(dir, path); err == nil {
					rel = filepath.ToSlash(rel)
					for _, f := range []string{rel, rel + "/*"} {
						if !seen[f] {
							seen[f] = true
							out = append(out, f)
						}
					}
				}
				up := filepath.Dir(dir)
				if up == dir {
					break
				}
				dir = up
			}
		}
	}
	return out
}

// fence adds the step's protection to its permission rules. When the step
// may edit everywhere, every protected path is denied after the step's own
// rules, and the writable folders are allowed again after that; the client
// applies the last rule that matches. When the step may edit only some
// patterns, the protected paths are denied entry by entry around the
// writable folders instead (see adapter.Fence), so the step's patterns are
// not widened. Writable folders outside the working folder are opened as
// external directories, for reading and writing.
func fence(st adapter.Step, rules []rule) []rule {
	if st.Workdir == "" {
		return rules
	}
	for i := range rules {
		if rules[i].tool != "edit" {
			continue
		}
		deny := st.Protect
		if !rules[i].all {
			deny = adapter.Fence(st.Protect, st.Writable)
		}
		for _, p := range deny {
			rules[i].deny = append(rules[i].deny, pathForms(st.Workdir, p)...)
		}
		if rules[i].all {
			for _, w := range st.Writable {
				rules[i].reallow = append(rules[i].reallow, pathForms(st.Workdir, w)...)
			}
		}
	}
	var outside []string
	for _, w := range st.Writable {
		if _, in := adapter.Rel(st.Workdir, w); !in {
			for _, q := range adapter.Spellings(w) {
				outside = append(outside, filepath.ToSlash(q), filepath.ToSlash(q)+"/*")
			}
		}
	}
	if len(outside) > 0 && len(rules) > 0 {
		sort.Strings(outside)
		rules = append(rules, rule{tool: "external_directory", patterns: outside})
	}
	return rules
}
