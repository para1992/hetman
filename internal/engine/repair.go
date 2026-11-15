package engine

import (
	"strconv"
	"strings"
	"time"

	"hetman/internal/adapter"
)

// MaxRepairs is how many times one attempt of an agent step asks its
// conversation to correct an answer that does not match the step's schema
// before the attempt counts as failed with invalid_output. A repair round
// is not an attempt.
const MaxRepairs = 2

// minRepairTime is the least time left of an attempt's timeout for a repair
// round to start; with less, the attempt fails as it is.
var minRepairTime = time.Second

// RepairPromptFile is the message of one repair round of an attempt,
// relative to the campaign folder, with forward slashes.
func RepairPromptFile(node string, attempt, round int) string {
	return "prompts/" + execBase(node, "", attempt) + ".repair" + strconv.Itoa(round) + ".txt"
}

// repairMessage asks the conversation that gave an answer the schema
// rejected to correct only the answer. problems is the validation error,
// its problems joined with "; ".
func repairMessage(problems string) string {
	return "Your answer was not accepted: it does not match the required output schema.\n- " +
		strings.ReplaceAll(problems, "; ", "\n- ") +
		"\nYour work is kept as it is. Do not redo, repeat or change any of it, and do not call any tools. " +
		"Answer again with only the corrected JSON object for the same schema as before, keeping everything else in it as it was, and nothing else.\n"
}

// answered reports whether an adapter process ended with a result, as the
// protocol wants it, so that its output can be judged.
func answered(r adapterRun) bool {
	return r.startErr == nil && !r.timedOut && !r.idleOut && r.protocol == "" && !r.lateExit &&
		r.terminal != nil && r.terminal.Type == adapter.TypeResult && r.exit == 0
}

// mergeDenied lists the denied tool calls of two turns of an attempt, each
// once, in order.
func mergeDenied(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	out := append([]string{}, a...)
	seen := map[string]bool{}
	for _, d := range a {
		seen[d] = true
	}
	for _, d := range b {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}
