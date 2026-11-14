package scribe

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSkillHasFrontMatter(t *testing.T) {
	text := string(Skill())
	if !strings.HasPrefix(text, "---\nname: hetman-scribe\ndescription: ") {
		t.Fatalf("skill does not start with the expected front matter:\n%.200s", text)
	}
	if strings.Contains(text, "\r") {
		t.Fatal("skill contains carriage returns")
	}
}

func TestWriteFreshDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	path, changed, err := Write(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, FileName); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if !changed {
		t.Fatal("changed = false for a fresh directory")
	}
	if !bytes.Equal(read(t, path), Skill()) {
		t.Fatal("written file differs from the skill")
	}
}

func TestWriteIdenticalFile(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Write(dir, false); err != nil {
		t.Fatal(err)
	}
	for _, overwrite := range []bool{false, true} {
		path, changed, err := Write(dir, overwrite)
		if err != nil {
			t.Fatalf("overwrite=%v: %v", overwrite, err)
		}
		if changed {
			t.Fatalf("overwrite=%v: changed = true for an identical file", overwrite)
		}
		if !bytes.Equal(read(t, path), Skill()) {
			t.Fatalf("overwrite=%v: file differs from the skill", overwrite)
		}
	}
}

func TestWriteDifferentFileRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	mine := []byte("my own notes\n")
	if err := os.WriteFile(path, mine, 0o644); err != nil {
		t.Fatal(err)
	}
	got, changed, err := Write(dir, false)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if got != path || changed {
		t.Fatalf("got (%q, %v), want (%q, false)", got, changed, path)
	}
	if !bytes.Equal(read(t, path), mine) {
		t.Fatal("existing file was modified")
	}
}

func TestWriteDifferentFileOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("an older version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, changed, err := Write(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != path || !changed {
		t.Fatalf("got (%q, %v), want (%q, true)", got, changed, path)
	}
	if !bytes.Equal(read(t, path), Skill()) {
		t.Fatal("file was not replaced with the skill")
	}
}

func TestWriteTargetIsAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Write(file, true); err == nil {
		t.Fatal("no error when the target directory is a file")
	}
}
