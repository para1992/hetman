package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"hetman/internal/chronicle"
	"hetman/internal/orders"
	"hetman/internal/runinput"
)

// inputFields are the declared fields of this campaign's run input: the
// input section of its orders; for a child whose segment declares none, the
// fields its parent gives it: the parent's own fields for a segment that
// gets the parent's input, or the fields of each object of the parent's
// list of objects that a for-each node runs over. Nil when nothing
// declares them. The orders are the ones read for this step.
func (e *Engine) inputFields() []*runinput.Field {
	if e.Orders != nil && e.Orders.Input != nil {
		return e.Orders.Input.Fields
	}
	if e.parent == nil || e.parent.Orders == nil {
		return nil
	}
	node := e.parent.Orders.Nodes[e.node]
	if node == nil {
		return nil
	}
	switch node.Type {
	case orders.Segment:
		if node.Input == "" {
			return e.parent.inputFields()
		}
	case orders.ForEach:
		if node.ItemsRef.Kind == orders.RefInput && len(node.ItemsRef.Path) == 1 {
			for _, f := range e.parent.inputFields() {
				if f.Name == node.ItemsRef.Path[0] && f.Kind == runinput.Objects {
					return f.Fields
				}
			}
		}
	}
	return nil
}

// runInput is the run input recorded when the campaign started, as a JSON
// value, with every declared field that is not required and is absent given
// its empty value or default (runinput.Fill). An input recorded before that
// rule, or an item of a list that left a field out, so reads the same as a
// new one.
func (e *Engine) runInput(events []chronicle.Event) (any, error) {
	var v any = map[string]any{}
	if len(events) > 0 && len(events[0].Input) > 0 {
		if err := json.Unmarshal(events[0].Input, &v); err != nil {
			return nil, fmt.Errorf("run input: %v", err)
		}
	}
	runinput.Fill(e.inputFields(), v)
	return v, nil
}

// filledInput is raw with the absent declared fields filled in, or raw as
// it is when nothing is missing.
func (e *Engine) filledInput(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil || !runinput.Fill(e.inputFields(), v) {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// missingInput is the error of a placeholder that names a field of the run
// input the input has not: it says whether the orders declare the field and
// what to do.
func (e *Engine) missingInput(placeholder string, path []string) error {
	name := strings.Join(path, ".")
	key := "engine.input_undeclared"
	if declared(e.inputFields(), path) {
		key = "engine.input_absent"
	}
	return errors.New(e.Text.T(key, "placeholder", "{{"+placeholder+"}}", "field", name))
}

// declared reports whether a path such as tasks or project names a declared
// field; a path into a list of objects counts by its first part.
func declared(fields []*runinput.Field, path []string) bool {
	if len(path) == 0 {
		return false
	}
	for _, f := range fields {
		if f.Name == path[0] {
			return true
		}
	}
	return false
}
