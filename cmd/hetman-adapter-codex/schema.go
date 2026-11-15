package main

import (
	"bytes"
	"encoding/json"
)

// The client's --output-schema holds the final message to a JSON Schema in
// the server's strict mode, which refuses a schema unless every object in
// it says "additionalProperties": false and lists every property as
// required (the client fails the turn with "invalid_json_schema" before the
// model writes anything). A step's schema is usually not written that way:
// properties are optional and other properties are allowed. So the adapter
// gives the client a strict form of it, when it can make one:
//
//   - every object gets "additionalProperties": false, and every property
//     becomes required; an optional one may then also be null;
//   - after the turn, a null the strict form allowed for an optional
//     property is removed again, so the answer means what the step's own
//     schema expects.
//
// The strict form is narrower than the step's schema: other properties are
// no longer possible. A schema with an object that allows them on purpose
// ("additionalProperties" true or a schema), or whose properties are not
// listed, or with constructs strict mode does not take ($ref, oneOf, allOf,
// not, if, patternProperties), gets no strict form; the client then gets no
// schema, and the answer format in the prompt asks for the object instead.
// When the server refuses a strict form anyway, the adapter runs the turn
// again the same way. Hetman checks every answer against the step's schema.

// strictSchema returns the strict form of a step's schema, or false when it
// has none.
func strictSchema(raw json.RawMessage) (json.RawMessage, bool) {
	var root map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&root) != nil || root == nil {
		return nil, false
	}
	if t, _ := root["type"].(string); t != "object" {
		return nil, false
	}
	if !strictify(root) {
		return nil, false
	}
	data, err := json.Marshal(root)
	if err != nil {
		return nil, false
	}
	return data, true
}

// unsupported are keywords strict mode does not take.
var unsupported = []string{"$ref", "oneOf", "allOf", "not", "if", "then", "else", "patternProperties", "dependentSchemas", "dependentRequired", "unevaluatedProperties"}

// strictify turns one schema into its strict form in place.
func strictify(s map[string]any) bool {
	for _, k := range unsupported {
		if _, ok := s[k]; ok {
			return false
		}
	}
	props, hasProps := s["properties"].(map[string]any)
	isObject := hasProps
	switch t := s["type"].(type) {
	case string:
		isObject = isObject || t == "object"
	case []any:
		for _, x := range t {
			isObject = isObject || x == "object"
		}
	}
	if isObject {
		if ap, ok := s["additionalProperties"]; ok && ap != false {
			return false
		}
		if !hasProps {
			return false
		}
		required := map[string]bool{}
		if list, ok := s["required"].([]any); ok {
			for _, r := range list {
				if name, ok := r.(string); ok {
					required[name] = true
				}
			}
		}
		var all []any
		for _, name := range sortedKeys(props) {
			sub, ok := props[name].(map[string]any)
			if !ok || !strictify(sub) {
				return false
			}
			if !required[name] {
				props[name] = map[string]any{"anyOf": []any{sub, map[string]any{"type": "null"}}}
			}
			all = append(all, name)
		}
		if all == nil {
			all = []any{}
		}
		s["required"] = all
		s["additionalProperties"] = false
	}
	if items, ok := s["items"]; ok {
		sub, ok := items.(map[string]any)
		if !ok || !strictify(sub) {
			return false
		}
	}
	if list, ok := s["anyOf"].([]any); ok {
		for _, x := range list {
			sub, ok := x.(map[string]any)
			if !ok || !strictify(sub) {
				return false
			}
		}
	}
	return true
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// dropNulls removes from an answer given under the strict form the nulls of
// properties that the step's own schema does not require.
func dropNulls(answer, schema json.RawMessage) json.RawMessage {
	var s map[string]any
	if json.Unmarshal(schema, &s) != nil {
		return answer
	}
	dec := json.NewDecoder(bytes.NewReader(answer))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return answer
	}
	v = prune(v, s)
	data, err := json.Marshal(v)
	if err != nil {
		return answer
	}
	return data
}

func prune(v any, s map[string]any) any {
	switch x := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		required := map[string]bool{}
		if list, ok := s["required"].([]any); ok {
			for _, r := range list {
				if name, ok := r.(string); ok {
					required[name] = true
				}
			}
		}
		for k, val := range x {
			sub, _ := props[k].(map[string]any)
			if val == nil && !required[k] && sub != nil {
				delete(x, k)
				continue
			}
			if sub != nil {
				x[k] = prune(val, sub)
			}
		}
		return x
	case []any:
		if sub, ok := s["items"].(map[string]any); ok {
			for i := range x {
				x[i] = prune(x[i], sub)
			}
		}
		return x
	}
	return v
}
