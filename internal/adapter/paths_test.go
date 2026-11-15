package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

// linked makes dir/real and a symbolic link dir/link to it, as the system
// temporary folder of macOS is reached; the test is skipped where links
// cannot be made.
func linked(t *testing.T) (real, link string) {
	t.Helper()
	dir := t.TempDir()
	real, link = filepath.Join(dir, "real"), filepath.Join(dir, "link")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	real, _ = filepath.EvalSymlinks(real) // the temporary folder itself may lie behind a link
	return real, link
}

func TestRealAndRel(t *testing.T) {
	real, link := linked(t)
	if got := Real(filepath.Join(link, "a", "b.md")); got != filepath.Join(real, "a", "b.md") {
		t.Errorf("Real of a file not there yet: %s", got)
	}
	for _, c := range []struct{ dir, path string }{{link, filepath.Join(real, "x.md")}, {real, filepath.Join(link, "x.md")}, {link, filepath.Join(link, "x.md")}} {
		if r, ok := Rel(c.dir, c.path); !ok || r != "x.md" {
			t.Errorf("Rel(%s, %s) = %q %v", c.dir, c.path, r, ok)
		}
	}
	if _, ok := Rel(link, filepath.Dir(real)); ok {
		t.Error("the folder above counted as inside")
	}
}

// Fence keeps the way to a writable folder open when the two are spelled
// differently, one through the link and one resolved.
func TestFenceAcrossALink(t *testing.T) {
	real, link := linked(t)
	state := filepath.Join(link, ".hetman")
	os.MkdirAll(filepath.Join(state, "campaigns", "1", "work"), 0o755)
	os.MkdirAll(filepath.Join(state, "campaigns", "2"), 0o755)
	got := Fence([]string{state}, []string{filepath.Join(real, ".hetman", "campaigns", "1", "work")})
	for _, p := range got {
		if p == state || p == filepath.Join(state, "campaigns") || p == filepath.Join(state, "campaigns", "1") {
			t.Fatalf("the way to the work folder is denied: %v", got)
		}
	}
	if len(got) != 1 || got[0] != filepath.Join(state, "campaigns", "2") {
		t.Fatalf("fence %v", got)
	}
}
