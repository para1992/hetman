package orders

import (
	"os"

	"go.yaml.in/yaml/v3"
)

// Position finds where a key of a node is written in an orders file: its
// value, or item i of it when the value is a list and i is not negative.
// It falls back to the key, then to the node, then to the start of the file.
func Position(path, node, key string, item int) (line, col int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
		return 0, 0
	}
	at := func(n *yaml.Node) (int, int) { return n.Line, n.Column }
	root := resolve(doc.Content[0])
	line, col = at(root)
	nodes := valueOf(root, "nodes")
	if nodes == nil {
		return line, col
	}
	n := valueOf(nodes, node)
	if n == nil {
		return line, col
	}
	line, col = at(n)
	if k := keyOf(n, key); k != nil {
		line, col = at(k)
	}
	v := valueOf(n, key)
	if v == nil {
		return line, col
	}
	line, col = at(v)
	if item >= 0 && v.Kind == yaml.SequenceNode && item < len(v.Content) {
		line, col = at(resolve(v.Content[item]))
	}
	return line, col
}

func keyOf(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := resolve(m.Content[i]); k.Value == key {
			return k
		}
	}
	return nil
}

func valueOf(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if k := resolve(m.Content[i]); k.Value == key {
			return resolve(m.Content[i+1])
		}
	}
	return nil
}
