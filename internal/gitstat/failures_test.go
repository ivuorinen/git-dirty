package gitstat

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chmodBack restores a directory's mode after the test, so TempDir can
// remove it, and reports whether the mode stops this user: root ignores it.
func chmodBack(t *testing.T, dir string, mode os.FileMode) bool {
	t.Helper()
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a directory has to be searchable
			t.Error(err)
		}
	})
	_, err := os.ReadDir(dir)

	return errors.Is(err, fs.ErrPermission)
}

// A subdirectory that cannot be listed or searched is skipped, as git does
// after warning about it. The .gitignore git cannot reach there has no
// patterns.
func TestUntracked_UnreadableSubdirectory(t *testing.T) {
	for _, mode := range []os.FileMode{0o300, 0} {
		dir := newRepo(t)
		write(t, dir, "sub/hidden.txt", "h\n")
		if !chmodBack(t, filepath.Join(dir, "sub"), mode) {
			t.Skip("the mode does not stop this user")
		}
		out, code := gitCode(t, dir, "ls-files", "--others", "--exclude-standard")
		if code != 0 || strings.Contains(out, "hidden.txt") {
			t.Fatalf("fixture: git ls-files: exit %d: %s", code, out)
		}
		if ourUntracked(t, dir) {
			t.Errorf("mode %o: untracked = true, want the directory skipped", mode)
		}
	}
}
