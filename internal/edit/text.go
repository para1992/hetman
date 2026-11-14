package edit

import (
	"bytes"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// errInPlace means a change cannot be made by editing the text in place; the
// document is then written out whole by the YAML library instead.
var errInPlace = errors.New("cannot edit in place")

// tree is a parsed orders file together with its text. Every change is made
// twice: to the node tree, which is what the file must mean afterwards, and
// as a text edit at the original position, which leaves every byte outside
// the edit as it was. The edited text is accepted only if it parses to the
// changed tree.
type tree struct {
	src    []byte
	doc    *yaml.Node // the document node
	lines  []int      // byte offset where each line starts
	inFlow map[*yaml.Node]bool
	was    map[*yaml.Node]*yaml.Node // a new node and the node in the text it replaced
	edits  []textEdit
	failed error // why the text could not be edited in place, if it cannot
	indent int   // indentation step of the file
}

type textEdit struct {
	start, end int
	text       string
	seq        int
}

func parseTree(src []byte) (*tree, error) {
	t := &tree{src: src, inFlow: map[*yaml.Node]bool{}, was: map[*yaml.Node]*yaml.Node{}, indent: 2}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("the orders file is not a YAML mapping")
	}
	t.doc = &doc
	t.lines = []int{0}
	for i, c := range src {
		if c == '\n' {
			t.lines = append(t.lines, i+1)
		}
	}
	var walk func(n *yaml.Node, flow bool)
	walk = func(n *yaml.Node, flow bool) {
		flow = flow || n.Style&yaml.FlowStyle != 0
		t.inFlow[n] = flow
		for _, c := range n.Content {
			walk(c, flow)
		}
	}
	walk(&doc, false)
	t.indent = t.detectIndent()
	return t, nil
}

func (t *tree) root() *yaml.Node { return t.doc.Content[0] }

// detectIndent finds how far the file indents a nested block mapping.
func (t *tree) detectIndent() int {
	var find func(n *yaml.Node) int
	find = func(n *yaml.Node) int {
		if n.Kind != yaml.MappingNode || n.Style&yaml.FlowStyle != 0 {
			return 0
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle == 0 && v.Line > k.Line && v.Column > k.Column {
				return v.Column - k.Column
			}
			if d := find(v); d > 0 {
				return d
			}
		}
		return 0
	}
	if d := find(t.root()); d > 0 && d <= 8 {
		return d
	}
	return 2
}

// off is the byte offset of a line and column; the column counts characters.
func (t *tree) off(line, col int) int {
	if line < 1 || line > len(t.lines) {
		return -1
	}
	i := t.lines[line-1]
	for c := 1; c < col && i < len(t.src) && t.src[i] != '\n'; c++ {
		_, size := utf8.DecodeRune(t.src[i:])
		i += size
	}
	return i
}

// at is the node in the text that n stands for: n itself, or the node it
// replaced.
func (t *tree) at(n *yaml.Node) *yaml.Node {
	if old, ok := t.was[n]; ok {
		return old
	}
	return n
}

func (t *tree) start(n *yaml.Node) int {
	n = t.at(n)
	return t.off(n.Line, n.Column)
}

// lineStart is the offset of the start of the line holding offset i.
func (t *tree) lineStart(i int) int {
	j := sort.Search(len(t.lines), func(k int) bool { return t.lines[k] > i })
	return t.lines[j-1]
}

// nextLine is the offset just after the line holding offset i.
func (t *tree) nextLine(i int) int {
	if j := bytes.IndexByte(t.src[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(t.src)
}

func (t *tree) lineText(start int) string {
	end := t.nextLine(start)
	return strings.TrimRight(string(t.src[start:end]), "\r\n")
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

func isComment(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "#") }

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

// scalarEnd is the offset just after the text of a scalar. The text found
// must mean the scalar's value; otherwise the scalar is not edited in place.
func (t *tree) scalarEnd(n *yaml.Node) (int, error) {
	n = t.at(n)
	s := t.start(n)
	if s < 0 {
		return 0, errInPlace
	}
	src := t.src
	end := -1
	switch {
	case n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		return 0, errInPlace
	case n.Style&yaml.DoubleQuotedStyle != 0:
		for i := s + 1; i < len(src) && src[i] != '\n'; i++ {
			if src[i] == '\\' {
				i++
				continue
			}
			if src[i] == '"' {
				end = i + 1
				break
			}
		}
	case n.Style&yaml.SingleQuotedStyle != 0:
		for i := s + 1; i < len(src) && src[i] != '\n'; i++ {
			if src[i] == '\'' {
				if i+1 < len(src) && src[i+1] == '\'' {
					i++
					continue
				}
				end = i + 1
				break
			}
		}
	default:
		flow := t.inFlow[n]
		i := s
		for i < len(src) {
			c := src[i]
			if c == '\n' || c == '\r' {
				break
			}
			if c == '#' && i > s && (src[i-1] == ' ' || src[i-1] == '\t') {
				break
			}
			if c == ':' && (i+1 == len(src) || strings.IndexByte(" \t\r\n", src[i+1]) >= 0 || flow && strings.IndexByte(",[]{}", src[i+1]) >= 0) {
				break
			}
			if flow && strings.IndexByte(",[]{}", c) >= 0 {
				break
			}
			i++
		}
		for i > s && (src[i-1] == ' ' || src[i-1] == '\t') {
			i--
		}
		if string(src[s:i]) != n.Value && !(n.ShortTag() == "!!null" && i == s) {
			return 0, errInPlace
		}
		return i, nil
	}
	if end < 0 {
		return 0, errInPlace
	}
	var v string
	if yaml.Unmarshal(src[s:end], &v) != nil || v != n.Value {
		return 0, errInPlace
	}
	return end, nil
}

// closing is the offset just after the bracket that closes the flow
// collection starting at offset s.
func (t *tree) closing(s int) (int, error) {
	src := t.src
	if s < 0 || s >= len(src) || (src[s] != '{' && src[s] != '[') {
		return 0, errInPlace
	}
	depth := 0
	for i := s; i < len(src); i++ {
		switch c := src[i]; c {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1, nil
			}
		case '"':
			for i++; i < len(src) && src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case '\'':
			for i++; i < len(src); i++ {
				if src[i] == '\'' {
					if i+1 < len(src) && src[i+1] == '\'' {
						i++
						continue
					}
					break
				}
			}
		case '#':
			if i > 0 && (src[i-1] == ' ' || src[i-1] == '\t') {
				i = t.nextLine(i) - 1
			}
		}
	}
	return 0, errInPlace
}

// end is the offset just after the text of a node.
func (t *tree) end(n *yaml.Node) (int, error) {
	n = t.at(n)
	switch n.Kind {
	case yaml.ScalarNode:
		return t.scalarEnd(n)
	case yaml.MappingNode, yaml.SequenceNode:
		if n.Line == 0 {
			return 0, errInPlace
		}
		if n.Style&yaml.FlowStyle != 0 {
			return t.closing(t.start(n))
		}
		if len(n.Content) == 0 {
			return 0, errInPlace
		}
		return t.end(n.Content[len(n.Content)-1])
	}
	return 0, errInPlace
}

// lead is where the line content of an item of a block collection begins:
// its key, or the dash of a sequence item. Only text before it on its line
// that is indentation is accepted.
func (t *tree) lead(item *yaml.Node, seq bool) (int, error) {
	s := t.start(item)
	if s < 0 {
		return 0, errInPlace
	}
	if seq {
		i := s - 1
		for i >= 0 && t.src[i] == ' ' {
			i--
		}
		if i < 0 || t.src[i] != '-' {
			return 0, errInPlace
		}
		s = i
	}
	ls := t.lineStart(s)
	if strings.TrimLeft(string(t.src[ls:s]), " ") != "" {
		return 0, errInPlace
	}
	return s, nil
}

// span is the text of one item of a block collection: from the comment
// lines directly above it, at its indentation, to the end of the line where
// its value ends.
func (t *tree) span(first, last *yaml.Node, seq bool) (int, int, error) {
	l, err := t.lead(first, seq)
	if err != nil {
		return 0, 0, err
	}
	e, err := t.end(last)
	if err != nil {
		return 0, 0, err
	}
	begin := t.lineStart(l)
	col := l - begin
	for begin > 0 {
		prev := t.lineStart(begin - 1)
		s := t.lineText(prev)
		if !isComment(s) || indentOf(s) != col {
			break
		}
		begin = prev
	}
	return begin, t.nextLine(e), nil
}

// separated reports whether the items of a block collection are separated
// by blank lines, judged by the line above its second item.
func (t *tree) separated(items [][2]*yaml.Node, seq bool) bool {
	for i := 1; i < len(items); i++ {
		if t.at(items[i][0]).Line == 0 {
			continue
		}
		b, _, err := t.span(items[i][0], items[i][1], seq)
		if err != nil || b == 0 {
			return false
		}
		return isBlank(t.lineText(t.lineStart(b - 1)))
	}
	return false
}

func (t *tree) edit(start, end int, text string) {
	t.edits = append(t.edits, textEdit{start: start, end: end, text: text, seq: len(t.edits)})
}

func (t *tree) fail(err error) {
	if t.failed == nil && err != nil {
		t.failed = err
	}
}

// items lists the entries of a mapping as key and value, or the items of a
// sequence twice.
func items(n *yaml.Node) [][2]*yaml.Node {
	var out [][2]*yaml.Node
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, [2]*yaml.Node{n.Content[i], n.Content[i+1]})
		}
		return out
	}
	for _, c := range n.Content {
		out = append(out, [2]*yaml.Node{c, c})
	}
	return out
}

func isFlow(n *yaml.Node) bool { return n.Style&yaml.FlowStyle != 0 }

// insert adds an entry (a key and value of a mapping, or an item of a
// sequence, given as item, item) at index i of a collection.
func (t *tree) insert(coll *yaml.Node, i int, k, v *yaml.Node) {
	seq := coll.Kind == yaml.SequenceNode
	old := items(coll)
	if seq {
		coll.Content = append(coll.Content[:i], append([]*yaml.Node{v}, coll.Content[i:]...)...)
	} else {
		coll.Content = append(coll.Content[:2*i], append([]*yaml.Node{k, v}, coll.Content[2*i:]...)...)
	}
	if t.failed != nil {
		return
	}
	if coll.Line == 0 {
		t.fail(errInPlace)
		return
	}
	// The nearest entries that are in the text, before and after i.
	next, prev := -1, -1
	for j := i; j < len(old); j++ {
		if t.at(old[j][0]).Line > 0 {
			next = j
			break
		}
	}
	for j := i - 1; j >= 0; j-- {
		if t.at(old[j][0]).Line > 0 {
			prev = j
			break
		}
	}
	if isFlow(coll) {
		text, err := flowEntry(seq, k, v)
		if err != nil {
			t.fail(err)
			return
		}
		switch {
		case next >= 0:
			t.edit(t.start(old[next][0]), t.start(old[next][0]), text+", ")
		case prev >= 0:
			e, err := t.end(old[prev][1])
			t.fail(err)
			t.edit(e, e, ", "+text)
		default:
			e, err := t.closing(t.start(coll))
			t.fail(err)
			if i > 0 {
				text = ", " + text
			}
			t.edit(e-1, e-1, text)
		}
		return
	}
	var anchor [2]*yaml.Node
	switch {
	case next >= 0:
		anchor = old[next]
	case prev >= 0:
		anchor = old[prev]
	default:
		t.fail(errInPlace)
		return
	}
	l, err := t.lead(anchor[0], seq)
	if err != nil {
		t.fail(err)
		return
	}
	col := l - t.lineStart(l)
	text, err := blockEntry(seq, k, v, col, t.indent)
	if err != nil {
		t.fail(err)
		return
	}
	sep := t.separated(old, seq)
	b, e, err := t.span(anchor[0], anchor[1], seq)
	if err != nil {
		t.fail(err)
		return
	}
	if next >= 0 {
		// The new entry keeps the blank line above the one it is put
		// before, and gets one of its own.
		if sep || b > 0 && isBlank(t.lineText(t.lineStart(b-1))) {
			text += "\n"
		}
		t.edit(b, b, text)
		return
	}
	if e == len(t.src) && (e == 0 || t.src[e-1] != '\n') {
		text = "\n" + text
	}
	if sep {
		text = "\n" + text
	}
	t.edit(e, e, text)
}

// remove deletes the entry at index i of a collection.
func (t *tree) remove(coll *yaml.Node, i int) {
	seq := coll.Kind == yaml.SequenceNode
	old := items(coll)
	if seq {
		coll.Content = append(coll.Content[:i:i], coll.Content[i+1:]...)
	} else {
		coll.Content = append(coll.Content[:2*i:2*i], coll.Content[2*i+2:]...)
	}
	if t.failed != nil {
		return
	}
	it := old[i]
	if t.at(it[0]).Line == 0 {
		t.fail(errInPlace)
		return
	}
	if isFlow(coll) {
		s := t.start(it[0])
		switch {
		case i+1 < len(old) && t.at(old[i+1][0]).Line > 0:
			t.edit(s, t.start(old[i+1][0]), "")
		case i > 0 && t.at(old[i-1][0]).Line > 0:
			pe, err := t.end(old[i-1][1])
			t.fail(err)
			e, err := t.end(it[1])
			t.fail(err)
			t.edit(pe, e, "")
		case len(old) == 1:
			e, err := t.end(it[1])
			t.fail(err)
			t.edit(s, e, "")
		default:
			t.fail(errInPlace)
		}
		return
	}
	if len(old) == 1 {
		t.fail(errInPlace) // an empty block collection has no text of its own
		return
	}
	b, e, err := t.span(it[0], it[1], seq)
	if err != nil {
		t.fail(err)
		return
	}
	// Between blank lines, or first and followed by one, the entry takes a
	// blank line with it.
	if e < len(t.src) && isBlank(t.lineText(e)) && (i == 0 || b > 0 && isBlank(t.lineText(t.lineStart(b-1)))) {
		e = t.nextLine(e)
	}
	t.edit(b, e, "")
}

// replace puts value v in place of the value at index i of a mapping, or
// of the item at index i of a sequence.
func (t *tree) replace(coll *yaml.Node, i int, v *yaml.Node) {
	var old *yaml.Node
	if coll.Kind == yaml.SequenceNode {
		old, coll.Content[i] = coll.Content[i], v
	} else {
		old, coll.Content[2*i+1] = coll.Content[2*i+1], v
	}
	t.was[v] = old
	if t.failed != nil {
		return
	}
	if old.Line == 0 {
		t.fail(errInPlace)
		return
	}
	s := t.start(old)
	switch {
	case old.Kind == yaml.ScalarNode && (v.Kind == yaml.ScalarNode || isFlow(v)):
		e, err := t.scalarEnd(old)
		if err != nil {
			t.fail(err)
			return
		}
		text, err := inline(v, t.inFlow[old])
		t.fail(err)
		t.edit(s, e, text)
	case old.Kind != yaml.ScalarNode && isFlow(old):
		e, err := t.closing(s)
		if err != nil {
			t.fail(err)
			return
		}
		if v.Kind != yaml.ScalarNode {
			v.Style |= yaml.FlowStyle
		}
		text, err := inline(v, t.inFlow[old])
		t.fail(err)
		t.edit(s, e, text)
	case old.Kind != yaml.ScalarNode && v.Kind == old.Kind && !isFlow(v) && len(old.Content) > 0:
		seq := old.Kind == yaml.SequenceNode
		l, err := t.lead(old.Content[0], seq)
		if err != nil {
			t.fail(err)
			return
		}
		_, e, err := t.span(old.Content[0], old.Content[len(old.Content)-1], seq)
		if err != nil {
			t.fail(err)
			return
		}
		col := l - t.lineStart(l)
		var b strings.Builder
		for _, it := range items(v) {
			text, err := blockEntry(seq, it[0], it[1], col, t.indent)
			if err != nil {
				t.fail(err)
				return
			}
			b.WriteString(text)
		}
		t.edit(t.lineStart(l), e, b.String())
	default:
		t.fail(errInPlace)
	}
}

// result applies the text edits. ok is false when they cannot be made in
// place or do not mean the changed tree; the text is then the changed tree
// written out by the YAML library.
func (t *tree) result() (out []byte, inPlace bool, err error) {
	if t.failed == nil {
		edits := append([]textEdit{}, t.edits...)
		sort.SliceStable(edits, func(i, j int) bool {
			if edits[i].start != edits[j].start {
				return edits[i].start > edits[j].start
			}
			return edits[i].seq > edits[j].seq
		})
		text := append([]byte{}, t.src...)
		last := len(text)
		overlap := false
		for _, e := range edits {
			if e.start < 0 || e.end < e.start || e.end > last {
				overlap = true
				break
			}
			text = append(text[:e.start], append([]byte(e.text), text[e.end:]...)...)
			last = e.start
		}
		if !overlap {
			var got yaml.Node
			if yaml.Unmarshal(text, &got) == nil && same(&got, t.doc) {
				return text, true, nil
			}
		}
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(t.indent)
	if err := enc.Encode(t.doc); err != nil {
		return nil, false, err
	}
	return b.Bytes(), false, nil
}

// same reports whether two node trees hold the same data, whatever their
// style.
func same(a, b *yaml.Node) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case yaml.ScalarNode:
		return a.Value == b.Value && a.ShortTag() == b.ShortTag()
	case yaml.AliasNode:
		return a.Value == b.Value
	}
	if len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		if !same(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

// inline renders a scalar or a flow collection to stand where a value is
// written, inside a flow collection or not.
func inline(v *yaml.Node, inFlow bool) (string, error) {
	var data []byte
	var err error
	if inFlow || v.Kind != yaml.ScalarNode {
		data, err = yaml.Marshal(&yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle, Content: []*yaml.Node{v}})
		if err == nil {
			s := strings.TrimSuffix(string(data), "\n")
			if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") || strings.Contains(s, "\n") {
				return "", errInPlace
			}
			return s[1 : len(s)-1], nil
		}
		return "", err
	}
	data, err = yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	s := strings.TrimSuffix(string(data), "\n")
	if strings.Contains(s, "\n") {
		return "", errInPlace
	}
	return s, nil
}

// flowEntry renders an entry of a flow collection.
func flowEntry(seq bool, k, v *yaml.Node) (string, error) {
	if seq {
		return inline(v, true)
	}
	m := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle, Content: []*yaml.Node{k, v}}
	data, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	s := strings.TrimSuffix(string(data), "\n")
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") || strings.Contains(s, "\n") {
		return "", errInPlace
	}
	return s[1 : len(s)-1], nil
}

// blockEntry renders an entry of a block collection whose lines start at
// column col, ending with a newline.
func blockEntry(seq bool, k, v *yaml.Node, col, indent int) (string, error) {
	var n *yaml.Node
	if seq {
		n = &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{v}}
	} else {
		n = &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{k, v}}
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(indent)
	if err := enc.Encode(n); err != nil {
		return "", err
	}
	pad := strings.Repeat(" ", col)
	var out strings.Builder
	for _, line := range strings.SplitAfter(b.String(), "\n") {
		if line == "" {
			continue
		}
		if line != "\n" {
			out.WriteString(pad)
		}
		out.WriteString(line)
	}
	return out.String(), nil
}
