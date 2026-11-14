// Package edit changes an orders file through a fixed set of operations.
//
// An operation edits the text of the file in place: everything outside the
// lines it changes stays byte for byte as it was, comments, blank lines,
// key order, quoting and alignment included. New entries are written in the
// style of their neighbours (a node as a block or on one line, transitions
// in braces or as a block). When a change cannot be made that way (an entry
// written in an unusual form, such as a multi-line string or an anchor),
// the whole file is written out by the YAML library instead, which keeps
// comments and key order but not blank lines, alignment or quoting; Notes
// then says so.
//
// A Doc collects operations in memory. Nothing is written until Save, and
// Save refuses a result that does not pass the orders check unless forced.
package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hetman/internal/orders"
)

// Doc is an orders file being changed.
type Doc struct {
	Path string // absolute path of the orders file
	Dir  string // the folder holding it

	exists bool
	crlf   bool   // the file on disk ends its lines with CR LF
	orig   []byte // the file as read, with LF line ends
	text   []byte // the file as changed
	files  map[string]*File
	notes  []string
}

// File is another file an operation creates or changes: a starter prompt
// template or schema, or a template whose placeholders name a renamed node.
type File struct {
	Path string // absolute
	Old  []byte // nil when the file does not exist yet
	New  []byte
}

// Open reads an orders file. A file that does not exist gives an empty Doc,
// which only the new operation accepts.
func Open(path string) (*Doc, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	d := &Doc{Path: abs, Dir: filepath.Dir(abs), files: map[string]*File{}}
	data, err := os.ReadFile(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	d.exists = true
	if bytes.Contains(data, []byte("\r\n")) {
		d.crlf = true
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	}
	d.orig, d.text = data, data
	return d, nil
}

// Exists reports whether the orders file existed when it was opened.
func (d *Doc) Exists() bool { return d.exists }

// Text is the orders file as changed so far.
func (d *Doc) Text() []byte { return d.text }

// Files lists the other files the operations create or change, by path.
func (d *Doc) Files() []*File {
	var out []*File
	for _, f := range d.files {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Notes are remarks about how the changes were made.
func (d *Doc) Notes() []string { return d.notes }

// Changed reports whether anything differs from the files on disk.
func (d *Doc) Changed() bool {
	if !bytes.Equal(d.orig, d.text) || !d.exists && len(d.text) > 0 {
		return true
	}
	for _, f := range d.files {
		if !bytes.Equal(f.Old, f.New) || f.Old == nil {
			return true
		}
	}
	return false
}

// Clone returns an independent copy, so that operations can be tried and
// thrown away.
func (d *Doc) Clone() *Doc {
	c := *d
	c.files = map[string]*File{}
	for k, f := range d.files {
		cf := *f
		c.files[k] = &cf
	}
	c.notes = append([]string{}, d.notes...)
	return &c
}

// Apply makes one operation in memory. On an error the Doc is unchanged.
func (d *Doc) Apply(op Op) error {
	if err := op.Validate(); err != nil {
		return err
	}
	c := d.Clone()
	e := &editing{d: c, op: op}
	if op.Op == OpNew {
		if c.exists || len(c.text) > 0 {
			return fmt.Errorf("%s already exists; new only starts a flow from nothing", c.Path)
		}
		if err := e.create(); err != nil {
			return err
		}
	} else {
		if !c.exists && len(c.text) == 0 {
			return fmt.Errorf("%s does not exist yet; start the flow with the new operation", c.Path)
		}
		t, err := parseTree(c.text)
		if err != nil {
			return fmt.Errorf("%s: %v", c.Path, err)
		}
		e.t = t
		if err := e.apply(); err != nil {
			return err
		}
		out, inPlace, err := t.result()
		if err != nil {
			return err
		}
		if !inPlace {
			c.notes = append(c.notes, fmt.Sprintf("%s could not be changed in place, so the whole file was written out again: "+
				"comments and key order are kept, blank lines, alignment and quoting are not", op.Op))
		}
		c.text = out
	}
	*d = *c
	return nil
}

// overlay is the content of every file created or changed so far.
func (d *Doc) overlay() map[string][]byte {
	m := map[string][]byte{}
	for p, f := range d.files {
		m[p] = f.New
	}
	return m
}

// Check runs the orders check on the changed text, reading created and
// changed files from memory. It returns the checked orders or the
// problems as orders.Problems.
func (d *Doc) Check() (*orders.Orders, error) {
	return orders.ParseOverlay(d.Path, d.text, d.overlay())
}

// Warnings checks the programs of the run lists of checked orders. None of
// them stops a change: a script may be written after the step that runs it.
func Warnings(o *orders.Orders) []string {
	errs, warnings := o.CheckPrograms()
	var out []string
	for _, p := range append(errs, warnings...) {
		msg := strings.TrimPrefix(p.Msg, "warning: ")
		out = append(out, fmt.Sprintf("%s:%d:%d: %s", filepath.Base(p.File), p.Line, p.Col, msg))
	}
	return out
}

// read returns a file relative to the orders folder, as changed so far.
func (d *Doc) read(rel string) ([]byte, bool) {
	path := d.abs(rel)
	if f, ok := d.files[path]; ok {
		return f.New, true
	}
	data, err := os.ReadFile(path)
	return data, err == nil
}

func (d *Doc) abs(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(d.Dir, filepath.FromSlash(rel))
}

// create adds a file that does not exist yet. It is never written over an
// existing one.
func (d *Doc) create(rel string, content []byte) bool {
	path := d.abs(rel)
	if _, ok := d.files[path]; ok {
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return false
	}
	d.files[path] = &File{Path: path, New: content}
	return true
}

// change replaces the content of an existing file.
func (d *Doc) change(path string, old, content []byte) {
	if f, ok := d.files[path]; ok {
		f.New = content
		return
	}
	d.files[path] = &File{Path: path, Old: old, New: content}
}

// ErrNotChecked is wrapped by Save when the result does not pass the check.
var ErrNotChecked = errors.New("the changed orders do not pass the check; nothing was written")

// Save writes the changes: first the other files, then the orders file.
// Unless force is set, nothing is written when the result does not pass the
// orders check; the error then wraps ErrNotChecked and the orders.Problems.
func (d *Doc) Save(force bool) error {
	if !force {
		if _, err := d.Check(); err != nil {
			return fmt.Errorf("%w\n%v", ErrNotChecked, err)
		}
	}
	for _, f := range d.Files() {
		if f.Old != nil && bytes.Equal(f.Old, f.New) {
			continue
		}
		if f.Old == nil {
			if _, err := os.Stat(f.Path); err == nil {
				return fmt.Errorf("%s appeared meanwhile; it is not written over", f.Path)
			}
		}
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
			return err
		}
		if err := writeFile(f.Path, f.New); err != nil {
			return err
		}
	}
	text := d.text
	if d.crlf {
		text = bytes.ReplaceAll(text, []byte("\n"), []byte("\r\n"))
	}
	if err := writeFile(d.Path, text); err != nil {
		return err
	}
	d.exists, d.orig = true, d.text
	for _, f := range d.files {
		f.Old = f.New
	}
	return nil
}

// writeFile replaces a file by renaming a complete new one over it, so that
// a reader never sees half of it.
func writeFile(path string, data []byte) error {
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), mode)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// Diff shows what the changes do to every file, as a unified diff.
func (d *Doc) Diff() string {
	var b strings.Builder
	name := filepath.Base(d.Path)
	old := d.orig
	if !d.exists {
		old = nil
	}
	b.WriteString(unified(name, old, d.text, d.exists))
	for _, f := range d.Files() {
		rel, err := filepath.Rel(d.Dir, f.Path)
		if err != nil {
			rel = f.Path
		}
		b.WriteString(unified(filepath.ToSlash(rel), f.Old, f.New, f.Old != nil))
	}
	return b.String()
}
