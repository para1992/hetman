// Package scribe holds the parts of the setup assistant that involve a model.
//
// With a model named by the orders, a sentence is mapped to exactly one
// operation of package edit or one query of package explain; the program
// checks the answer, shows what it means and acts only on confirmation.
//
// The package also holds an agent skill, SKILL.md, for a coding agent that
// sets a flow up through the same operations. It is kept next to this
// source and embedded into the binary so that it can be written into any
// project.
package scribe

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileName is the name of the skill file written into the target folder.
const FileName = "SKILL.md"

//go:embed SKILL.md
var skill []byte

// ErrExists is returned when the target folder already holds a different skill
// file and overwriting was not requested.
var ErrExists = errors.New("a different file already exists")

// Skill returns the text of the skill file.
func Skill() []byte { return bytes.Clone(skill) }

// Write writes the skill file into dir, creating dir if needed, and returns
// the path of the file. changed is false when an identical file was already
// there. A different existing file is replaced only when overwrite is true;
// otherwise Write returns the path and an error wrapping ErrExists.
func Write(dir string, overwrite bool) (path string, changed bool, err error) {
	path = filepath.Join(dir, FileName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, false, err
	}
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		if bytes.Equal(old, skill) {
			return path, false, nil
		}
		if !overwrite {
			return path, false, fmt.Errorf("%s: %w", path, ErrExists)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return path, false, err
	}
	if err := os.WriteFile(path, skill, 0o644); err != nil {
		return path, false, err
	}
	return path, true, nil
}
