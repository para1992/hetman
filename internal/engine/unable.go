package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"hetman/internal/orders"
)

// An agent step that cannot do its work at all says so with the reserved
// outcome orders.Unable and its reason in orders.UnableField. The engine
// adds both to the schema the adapter gets and tells the model about them.
// Such an answer is not retried: with a transition for the outcome the flow
// follows it, else the campaign stops in the error state with the reason
// and waits for a person.

// unableSchema adds the reserved outcome and its reason field to a schema
// sent to the adapter. The other fields stay required, so the client still
// holds an ordinary answer to the whole schema; the engine checks only the
// reason of an answer that cannot proceed.
func unableSchema(schema []byte) ([]byte, error) {
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, err
	}
	props, _ := s["properties"].(map[string]any)
	out, _ := props[orders.OutcomeField].(map[string]any)
	if props == nil || out == nil {
		return nil, errors.New("the schema has no outcome property")
	}
	enum, _ := out["enum"].([]any)
	out["enum"] = append(append([]any{}, enum...), orders.Unable)
	props[orders.UnableField] = map[string]any{"type": "string"}
	return json.Marshal(s)
}

// unableInstruction tells an agent step how to say it cannot proceed.
func unableInstruction() string {
	return fmt.Sprintf(`

---
If you cannot do this step at all, for example because a tool or file you need is denied or out of reach, or the task contradicts itself, do not guess and do not pretend it is done: reply with a JSON object whose "outcome" is %[1]q and whose %[2]q says in a sentence or two what stops you, naming the tool or file. Fill any other required field with an empty value. A person reads the reason; this step is not retried by itself.
`, orders.Unable, orders.UnableField)
}

// deniedError says which tool calls the client denied, one per line after
// the first, so that deniedOf can take them apart for the next prompt.
func deniedError(denied []string) string {
	return fmt.Sprintf("the client denied %d tool call(s), and the node fails an attempt after any:\n%s", len(denied), strings.Join(denied, "\n"))
}

// deniedOf returns the calls a deniedError lists.
func deniedOf(msg string) []string {
	lines := strings.Split(msg, "\n")
	if len(lines) > 1 {
		return lines[1:]
	}
	return lines
}

// unableReason reads an answer that reports the step cannot proceed. ok is
// false for any other answer.
func unableReason(v any) (reason string, ok bool, err error) {
	obj, isObj := v.(map[string]any)
	if !isObj || obj[orders.OutcomeField] != orders.Unable {
		return "", false, nil
	}
	reason, _ = obj[orders.UnableField].(string)
	if strings.TrimSpace(reason) == "" {
		return "", true, fmt.Errorf("an answer with outcome %q must say why in %q", orders.Unable, orders.UnableField)
	}
	return strings.TrimSpace(reason), true, nil
}
