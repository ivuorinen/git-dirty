package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// gitVerdict maps `git <args> --quiet`-style exit codes to a boolean:
// 1 means "differs", 0 means "same"; anything else fails the test.
func gitVerdict(t *testing.T, dir string, args ...string) bool {
	t.Helper()
	out, code := gitCode(t, dir, args...)
	switch code {
	case 0:
		return false
	case 1:
		return true
	}
	t.Fatalf("git %v: exit %d: %s", args, code, out)

	return false
}

func ourModified(t *testing.T, dir string) bool {
	t.Helper()
	r := open(t, dir)
	idx, err := r.repo.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	cc, err := r.coreConfig()
	if err != nil {
		t.Fatal(err)
	}
	ex, err := LoadExcludes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.modified(context.Background(), idx, cc, ex)
	if err != nil {
		t.Fatal(err)
	}

	return got
}

// racyFixture leaves a.txt with the same size and mtime as its index entry,
// with the index file itself written at that same instant: git cannot trust
// the stat data and must compare content.
func racyFixture(t *testing.T, newContent string) string {
	t.Helper()
	dir := newRepo(t)
	stamp := time.Now().Add(-time.Minute).Truncate(time.Second)
	p := filepath.Join(dir, "a.txt")
	write(t, dir, "a.txt", "aaaa\n")
	chtimes(t, p, stamp)
	git(t, dir, "add", "a.txt")
	write(t, dir, "a.txt", newContent)
	chtimes(t, p, stamp)
	chtimes(t, filepath.Join(dir, ".git", "index"), stamp)

	return dir
}

func chtimes(t *testing.T, p string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, p string) {
	t.Helper()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
}

// makeExecutable gives the file the executable bits that core.fileMode
// compares.
func makeExecutable(t *testing.T, p string) {
	t.Helper()
	//nolint:gosec // G302: the fixture is about the executable bit, so the mode must exceed 0600
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatal(err)
	}
}

// mergeConflict leaves a.txt conflicted between main and another branch.
func mergeConflict(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	git(t, dir, "switch", "-q", "-c", "other")
	commitFile(t, dir, "a.txt", "other\n", "other")
	git(t, dir, "switch", "-q", mainBranch)
	commitFile(t, dir, "a.txt", "main\n", "main")
	if _, code := gitCode(t, dir, "merge", "-q", "other"); code == 0 {
		t.Fatal("fixture: merge did not conflict")
	}

	return dir
}

func TestModified(t *testing.T) { forEachFormat(t, testModified) }

func testModified(t *testing.T) {
	t.Helper()
	type fixture func(t *testing.T) string
	unix := runtime.GOOS != "windows"
	tests := []struct {
		name  string
		setup fixture
		unix  bool // needs POSIX modes or symlinks
	}{
		{name: "clean", setup: newRepo},
		{name: "content changed", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			write(t, dir, "a.txt", "changed\n")

			return dir
		}},
		{name: "deleted", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			remove(t, filepath.Join(dir, "a.txt"))

			return dir
		}},
		{name: "racy same-size edit", setup: func(t *testing.T) string {
			t.Helper()

			return racyFixture(t, "bbbb\n")
		}},
		{name: "racy but unchanged", setup: func(t *testing.T) string {
			t.Helper()

			return racyFixture(t, "aaaa\n")
		}},
		{name: "touch without content change", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			chtimes(t, filepath.Join(dir, "a.txt"), time.Now().Add(time.Hour))

			return dir
		}},
		{name: "chmod +x with core.fileMode=true", unix: true, setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			makeExecutable(t, filepath.Join(dir, "a.txt"))

			return dir
		}},
		{name: "chmod +x with core.fileMode=false", unix: true, setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			git(t, dir, "config", "core.fileMode", "false")
			makeExecutable(t, filepath.Join(dir, "a.txt"))

			return dir
		}},
		{name: "file replaced by symlink", unix: true, setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			remove(t, filepath.Join(dir, "a.txt"))
			if err := os.Symlink("elsewhere", filepath.Join(dir, "a.txt")); err != nil {
				t.Fatal(err)
			}

			return dir
		}},
		{name: "committed symlink unchanged", unix: true, setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			git(t, dir, "add", "link")
			git(t, dir, "commit", "-q", "-m", "link")

			return dir
		}},
		{name: "committed symlink retargeted", unix: true, setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			link := filepath.Join(dir, "link")
			if err := os.Symlink("a.txt", link); err != nil {
				t.Fatal(err)
			}
			git(t, dir, "add", "link")
			git(t, dir, "commit", "-q", "-m", "link")
			remove(t, link)
			if err := os.Symlink("b.txt", link); err != nil {
				t.Fatal(err)
			}

			return dir
		}},
		{name: "intent to add", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			write(t, dir, "new.txt", "n\n")
			git(t, dir, "add", "-N", "new.txt")

			return dir
		}},
		{name: "merge conflict", setup: mergeConflict},
		{name: "skip-worktree entry missing on disk", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			git(t, dir, "update-index", "--skip-worktree", "a.txt")
			remove(t, filepath.Join(dir, "a.txt"))

			return dir
		}},
		{name: "submodule moved to another commit", setup: func(t *testing.T) string {
			t.Helper()
			super := superWithSubmodule(t)
			commitFile(t, filepath.Join(super, "mod"), "extra.txt", "x\n", "move submodule")

			return super
		}},
		{name: "submodule not initialized", setup: func(t *testing.T) string {
			t.Helper()
			super := superWithSubmodule(t)
			clone := filepath.Join(t.TempDir(), "clone")
			git(t, super, "clone", "-q", super, clone)

			return clone
		}},
		{name: "submodule clean", setup: superWithSubmodule},
		{name: "submodule with a modified file", setup: func(t *testing.T) string {
			t.Helper()
			super := superWithSubmodule(t)
			write(t, filepath.Join(super, "mod"), "a.txt", "changed\n")

			return super
		}},
		{name: "submodule with a staged file", setup: func(t *testing.T) string {
			t.Helper()
			super := superWithSubmodule(t)
			mod := filepath.Join(super, "mod")
			write(t, mod, "staged.txt", "s\n")
			git(t, mod, "add", "staged.txt")

			return super
		}},
		{name: "submodule with an untracked file", setup: func(t *testing.T) string {
			t.Helper()
			super := superWithSubmodule(t)
			write(t, filepath.Join(super, "mod"), "untracked.txt", "u\n")

			return super
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unix && !unix {
				t.Skip("needs POSIX file modes and symlinks")
			}
			dir := tt.setup(t)
			got := ourModified(t, dir)
			want := gitVerdict(t, dir, "diff", "--quiet")
			if got != want {
				t.Errorf("modified = %v, git diff --quiet says %v", got, want)
			}
		})
	}
}

func ourStaged(t *testing.T, dir string) bool {
	t.Helper()
	r := open(t, dir)
	idx, err := r.repo.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	h, err := r.head()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.staged(context.Background(), idx, h.Hash)
	if err != nil {
		t.Fatal(err)
	}

	return got
}

// cacheTreeValid reports whether the index's root cache-tree entry is valid
// and equal to HEAD's tree: the precondition for the no-tree-read shortcut.
func cacheTreeValid(t *testing.T, dir string) bool {
	t.Helper()
	idx, err := open(t, dir).repo.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	root := idx.Tree
	if root == nil {
		return false
	}

	return root.Entries == len(idx.Entries) && root.ID.String() == git(t, dir, "rev-parse", "HEAD^{tree}")
}

func TestStaged(t *testing.T) { forEachFormat(t, testStaged) }

func testStaged(t *testing.T) {
	t.Helper()
	tests := []struct {
		name      string
		setup     func(t *testing.T) string
		wantCache *bool // when set, assert the cache-tree shortcut precondition
	}{
		{name: "clean after commit (cache-tree valid)", setup: newRepo, wantCache: ptr(true)},
		{name: "staged add", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			write(t, dir, "b.txt", "b\n")
			git(t, dir, "add", "b.txt")

			return dir
		}, wantCache: ptr(false)},
		{name: "staged modify", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			write(t, dir, "a.txt", "changed\n")
			git(t, dir, "add", "a.txt")

			return dir
		}},
		{name: "staged delete", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			git(t, dir, "rm", "-q", "a.txt")

			return dir
		}},
		{name: "staged mode change", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			git(t, dir, "update-index", "--chmod=+x", "a.txt")

			return dir
		}},
		{name: "unstaged edit only", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			write(t, dir, "a.txt", "changed\n")

			return dir
		}},
		{name: "re-added unchanged file invalidates cache-tree", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			commitFile(t, dir, "sub/c.txt", "c\n", "nested")
			write(t, dir, "sub/c.txt", "c\n")
			git(t, dir, "add", "sub/c.txt")

			return dir
		}},
		{name: "intent to add only", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			write(t, dir, "new.txt", "n\n")
			git(t, dir, "add", "-N", "new.txt")

			return dir
		}},
		{name: "unborn with entries", setup: func(t *testing.T) string {
			t.Helper()
			dir := t.TempDir()
			git(t, dir, "init", "-q")
			write(t, dir, "a.txt", "a\n")
			git(t, dir, "add", "a.txt")

			return dir
		}},
		{name: "unborn without entries", setup: func(t *testing.T) string {
			t.Helper()
			dir := t.TempDir()
			git(t, dir, "init", "-q")

			return dir
		}},
		{name: "unborn with intent to add only", setup: func(t *testing.T) string {
			t.Helper()
			dir := t.TempDir()
			git(t, dir, "init", "-q")
			write(t, dir, "a.txt", "a\n")
			git(t, dir, "add", "-N", "a.txt")

			return dir
		}},
		{name: "merge conflict", setup: mergeConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			if tt.wantCache != nil {
				if got := cacheTreeValid(t, dir); got != *tt.wantCache {
					t.Fatalf("fixture precondition: cache-tree valid = %v, want %v", got, *tt.wantCache)
				}
			}
			got := ourStaged(t, dir)
			want := gitVerdict(t, dir, "diff", "--cached", "--quiet")
			if got != want {
				t.Errorf("staged = %v, git diff --cached --quiet says %v", got, want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func ourUntracked(t *testing.T, dir string) bool {
	t.Helper()
	ex, err := LoadExcludes()
	if err != nil {
		t.Fatal(err)
	}
	r := open(t, dir)
	idx, err := r.repo.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	cc, err := r.coreConfig()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.untracked(context.Background(), idx, cc, ex)
	if err != nil {
		t.Fatal(err)
	}

	return got
}

func TestUntracked(t *testing.T) { forEachFormat(t, testUntracked) }

func testUntracked(t *testing.T) {
	t.Helper()
	tests := []struct {
		name  string
		setup func(t *testing.T) string
		unix  bool
	}{
		{name: "none", setup: newRepo},
		{name: "file at root", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			write(t, dir, "new.txt", "n\n")

			return dir
		}},
		{name: "file in a tracked dir", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			commitFile(t, dir, "src/main.go", "package main\n", "src")
			write(t, dir, "src/extra.go", "package main\n")

			return dir
		}},
		{name: "file in a new dir", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			write(t, dir, "fresh/deep/file.txt", "f\n")

			return dir
		}},
		{name: "empty untracked dir", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			mkdirAll(t, filepath.Join(dir, "empty", "nested"))

			return dir
		}},
		{name: "ignored file only", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			commitFile(t, dir, ".gitignore", "*.log\n", "ignore")
			write(t, dir, "x.log", "l\n")

			return dir
		}},
		{name: "re-include under an ignored dir stays ignored", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			commitFile(t, dir, ".gitignore", "build/\n!build/keep.txt\n", "ignore")
			write(t, dir, "build/keep.txt", "k\n")

			return dir
		}},
		{name: "nested repository", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			nested := filepath.Join(dir, "nested")
			mkdirAll(t, nested)
			git(t, nested, "init", "-q")

			return dir
		}},
		{name: "symlink to a dir", unix: true, setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			if err := os.Symlink(t.TempDir(), filepath.Join(dir, "linkdir")); err != nil {
				t.Fatal(err)
			}

			return dir
		}},
		{name: "globally ignored marker file", setup: func(t *testing.T) string {
			t.Helper()
			home := isolatedHome(t)
			write(t, home, ".config/git/ignore", ".ignore\n")
			dir := newRepo(t)
			write(t, dir, ".ignore", "")

			return dir
		}},
		{name: "case-renamed file with core.ignorecase", setup: func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			git(t, dir, "config", "core.ignorecase", "true")
			if err := os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "A.txt")); err != nil {
				t.Fatal(err)
			}

			return dir
		}},
		{name: "submodule is not untracked", setup: superWithSubmodule},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unix && runtime.GOOS == "windows" {
				t.Skip("needs symlinks")
			}
			dir := tt.setup(t)
			got := ourUntracked(t, dir)
			want := git(t, dir, "ls-files", "--others", "--exclude-standard") != ""
			if got != want {
				t.Errorf("untracked = %v, git ls-files --others --exclude-standard non-empty = %v", got, want)
			}
		})
	}
}

func TestStashed(t *testing.T) { forEachFormat(t, testStashed) }

// stashedRepo returns a repository with refs/stash, loose.
func stashedRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	write(t, dir, "a.txt", "wip\n")
	// git 2.43 cannot stash in a SHA-256 repository; refs/stash existing
	// is all stashed() checks, so create the ref directly there.
	if _, code := gitCode(t, dir, "stash", "-q"); code != 0 {
		git(t, dir, "update-ref", "refs/stash", "HEAD")
	}

	return dir
}

func testStashed(t *testing.T) {
	t.Helper()
	tests := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{"no stash", newRepo},
		{"loose stash ref", stashedRepo},
		{"packed stash ref", func(t *testing.T) string {
			t.Helper()
			dir := stashedRepo(t)
			git(t, dir, "pack-refs", "--all")
			if _, err := os.Stat(filepath.Join(dir, ".git", "refs", "stash")); err == nil {
				t.Fatal("fixture: refs/stash still loose after pack-refs")
			}

			return dir
		}},
		{"stash cleared", func(t *testing.T) string {
			t.Helper()
			dir := stashedRepo(t)
			git(t, dir, "stash", "clear")

			return dir
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			got, err := open(t, dir).stashed()
			if err != nil {
				t.Fatal(err)
			}
			_, code := gitCode(t, dir, "rev-parse", "-q", "--verify", "refs/stash")
			if want := code == 0; got != want {
				t.Errorf("stashed = %v, git rev-parse --verify refs/stash says %v", got, want)
			}
		})
	}
}

// superWithSubmodule returns a committed superproject with a checked-out
// submodule at "mod".
func superWithSubmodule(t *testing.T) string {
	t.Helper()
	sub := newRepo(t)
	super := newRepo(t)
	git(t, super, "submodule", "add", "-q", sub, "mod")
	git(t, super, "commit", "-q", "-m", "add submodule")

	return super
}
