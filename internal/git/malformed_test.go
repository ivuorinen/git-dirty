package git

import (
	"os"
	"path/filepath"
	"testing"
)

// Everything read here may come from a hostile clone, and git never writes
// these bytes, so the git CLI cannot be the oracle. Each case is one way of
// breaking a format and names the error it must produce, so a case cannot
// pass by failing for another reason.

// locked sets path's mode to 0 until the test ends, and skips the test where
// that stops nothing: for root, and on Windows.
func locked(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // G302: a directory has to be searchable
			t.Error(err)
		}
	})
	f, err := os.Open(filepath.Clean(path))
	if err == nil {
		_ = f.Close() // read-only
		t.Skip("mode 0 does not stop this user")
	}
}

func TestReadPatterns_Directory(t *testing.T) {
	if _, err := ReadPatterns(t.TempDir(), nil); err == nil {
		t.Error("a directory: want the read error")
	}
}

func TestReadPatterns_NothingToRead(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "file", "*.log\n")
	for name, path := range map[string]string{
		"missing":      filepath.Join(dir, "absent"),
		"below a file": filepath.Join(dir, "file", "below"),
		"null device":  os.DevNull,
	} {
		if ps, err := ReadPatterns(path, nil); err != nil || len(ps) != 0 {
			t.Errorf("%s: %v, %v, want no patterns and no error", name, ps, err)
		}
	}
	locked(t, filepath.Join(dir, "file"))
	if ps, err := ReadPatterns(filepath.Join(dir, "file"), nil); err != nil || len(ps) != 0 {
		t.Errorf("unreadable: %v, %v, want no patterns and no error", ps, err)
	}
}

func TestReadGitignore(t *testing.T) {
	dir := t.TempDir()
	if ps := ReadGitignore(filepath.Join(dir, ".gitignore"), nil); len(ps) != 0 {
		t.Errorf("missing: %v", ps)
	}
	writeFile(t, dir, ".gitignore", "*.log\n")
	if ps := ReadGitignore(filepath.Join(dir, ".gitignore"), nil); len(ps) != 1 {
		t.Errorf("regular file: %v, want one pattern", ps)
	}
	if ps := ReadGitignore(dir, nil); len(ps) != 0 {
		t.Errorf("a directory: %v", ps)
	}
	locked(t, filepath.Join(dir, ".gitignore"))
	if ps := ReadGitignore(filepath.Join(dir, ".gitignore"), nil); len(ps) != 0 {
		t.Errorf("unreadable: %v", ps)
	}
}
