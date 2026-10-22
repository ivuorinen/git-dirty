package gitstat

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
)

// headHash resolves HEAD through the opened repository, which needs both the
// per-worktree dir (HEAD) and the common dir (refs, objects).
func headHash(t *testing.T, r *Repo) gitrepo.ObjectID {
	t.Helper()
	id, err := r.repo.Resolve("HEAD")
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	if _, err := r.repo.ReadCommit(id); err != nil {
		t.Fatalf("read HEAD commit: %v", err)
	}

	return id
}

func TestOpen_PlainRepo(t *testing.T) {
	dir := newRepo(t)
	r := open(t, dir)
	if got, want := headHash(t, r).String(), git(t, dir, "rev-parse", "HEAD"); got != want {
		t.Errorf("HEAD = %s, want %s", got, want)
	}
}

func TestOpen_LinkedWorktree(t *testing.T) {
	dir := newRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	git(t, dir, "worktree", "add", "-q", "-b", "side", wt)
	commitFile(t, wt, "b.txt", "b\n", "side commit")

	r := open(t, wt)
	if r.repo.CommonDir == r.repo.GitDir {
		t.Fatalf("commondir not resolved: gitDir=%s", r.repo.GitDir)
	}
	if got, want := headHash(t, r).String(), git(t, wt, "rev-parse", "HEAD"); got != want {
		t.Errorf("worktree HEAD = %s, want %s", got, want)
	}
	if got, want := headHash(t, r).String(), git(t, dir, "rev-parse", "HEAD"); got == want {
		t.Errorf("worktree HEAD equals the main worktree's HEAD %s", want)
	}
}

func TestOpen_AbsorbedSubmodule(t *testing.T) {
	sub := newRepo(t)
	super := newRepo(t)
	git(t, super, "submodule", "add", "-q", sub, "mod")
	git(t, super, "commit", "-q", "-m", "add submodule")

	path := filepath.Join(super, "mod")
	if fi, err := os.Lstat(filepath.Join(path, ".git")); err != nil || fi.IsDir() {
		t.Fatalf("expected a gitdir file in the submodule, got %v %v", fi, err)
	}
	r := open(t, path)
	if got, want := headHash(t, r).String(), git(t, path, "rev-parse", "HEAD"); got != want {
		t.Errorf("submodule HEAD = %s, want %s", got, want)
	}
}

func TestOpen_Errors(t *testing.T) {
	notRepo := t.TempDir()
	if _, err := Open(notRepo); err == nil {
		t.Error("Open on a plain dir: want error")
	}
	bad := t.TempDir()
	write(t, bad, ".git", "nonsense\n")
	if _, err := Open(bad); err == nil {
		t.Error("Open with a malformed .git file: want error")
	}
}

func TestOpenClose_ReleasesDescriptors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor count via /proc/self/fd is Linux-only")
	}
	dir := newRepo(t)
	for i := range 5 {
		commitFile(t, dir, "a.txt", string(rune('b'+i))+"\n", "more")
	}
	// Move objects into a packfile, which stays open from the first object
	// read until Close.
	git(t, dir, "gc", "-q")

	before := countFDs(t)
	for range 500 {
		r, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		id, err := r.repo.Resolve("HEAD")
		if err != nil {
			t.Fatal(err)
		}
		c, err := r.repo.ReadCommit(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.repo.ReadObject(c.Tree); err != nil {
			t.Fatal(err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if after := countFDs(t); after > before {
		t.Errorf("descriptors leaked: %d before, %d after 500 open/close cycles", before, after)
	}
}
