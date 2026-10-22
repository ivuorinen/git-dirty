package gitstat

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// testMainBranches is the MainBranches list the tests inspect with.
var testMainBranches = []string{mainBranch, "master", "trunk"}

// remoteAndClone returns a bare "remote", a seed clone used to push to it, and
// a working clone (with refs/remotes/origin/HEAD) to inspect.
func remoteAndClone(t *testing.T) (bare, seed, clone string) {
	t.Helper()
	bare = filepath.Join(t.TempDir(), "remote.git")
	git(t, t.TempDir(), "init", "-q", "--bare", bare)
	seed = newRepo(t)
	git(t, seed, "remote", "add", "origin", bare)
	git(t, seed, "push", "-q", "-u", "origin", mainBranch)
	clone = filepath.Join(t.TempDir(), "clone")
	git(t, seed, "clone", "-q", bare, clone)

	return bare, seed, clone
}

func trackingFor(t *testing.T, dir string) (*Tracking, *DefaultBranch) {
	t.Helper()
	r := open(t, dir)
	w, err := r.newCommitWalker()
	if err != nil {
		t.Fatal(err)
	}
	cfg := r.repo.Config
	head, err := r.head()
	if err != nil {
		t.Fatal(err)
	}
	up, err := r.upstream(context.Background(), w, cfg, head.Branch, head.Hash)
	if err != nil {
		t.Fatal(err)
	}
	def, err := r.defaultBranch(context.Background(), w, cfg, head, testMainBranches)
	if err != nil {
		t.Fatal(err)
	}

	return up, def
}

func countsOf(tr *Tracking) string {
	return fmt.Sprintf("%d\t%d", tr.Ahead, tr.Behind)
}

// wantGitUpstream fails the test unless up names, and counts against, the
// upstream git resolves as HEAD@{u}.
func wantGitUpstream(t *testing.T, dir string, up *Tracking) {
	t.Helper()
	if up == nil || up.Gone {
		t.Fatalf("upstream = %+v", up)
	}
	if want := git(t, dir, "rev-parse", "--abbrev-ref", "@{u}"); up.Ref != want {
		t.Errorf("Ref = %q, git says %q", up.Ref, want)
	}
	wantCounts(t, dir, "counts", up, "HEAD", "@{u}")
}

func TestUpstream(t *testing.T) { forEachFormat(t, testUpstream) }

func testUpstream(t *testing.T) {
	t.Helper()
	t.Run("tracking branch ahead and behind", func(t *testing.T) {
		_, seed, clone := remoteAndClone(t)
		commitFile(t, clone, "local", "1", "local")
		commitFile(t, seed, "up", "1", "up 1")
		commitFile(t, seed, "up", "2", "up 2")
		git(t, seed, "push", "-q")
		git(t, clone, "fetch", "-q")

		up, _ := trackingFor(t, clone)
		wantGitUpstream(t, clone, up)
	})

	t.Run("no upstream", func(t *testing.T) {
		_, _, clone := remoteAndClone(t)
		git(t, clone, "switch", "-q", "-c", "feature")
		if _, code := gitCode(t, clone, "rev-parse", "@{u}"); code == 0 {
			t.Fatal("fixture: git found an upstream")
		}
		if up, _ := trackingFor(t, clone); up != nil {
			t.Errorf("upstream = %+v, want nil", up)
		}
	})

	t.Run("upstream gone", func(t *testing.T) {
		_, seed, clone := remoteAndClone(t)
		git(t, clone, "switch", "-q", "-c", "feat")
		git(t, clone, "push", "-q", "-u", "origin", "feat")
		git(t, seed, "push", "-q", "origin", "--delete", "feat")
		git(t, clone, "fetch", "-q", "--prune")
		if out, code := gitCode(t, clone, "rev-parse", "@{u}"); code == 0 {
			t.Fatalf("fixture: upstream still resolves: %s", out)
		}
		up, _ := trackingFor(t, clone)
		if up == nil || !up.Gone {
			t.Errorf("upstream = %+v, want Gone", up)
		}
	})

	t.Run("remote dot tracks a local branch", func(t *testing.T) {
		_, _, clone := remoteAndClone(t)
		git(t, clone, "switch", "-q", "-c", "topic", "--track", mainBranch)
		commitFile(t, clone, "t", "1", "topic")
		up, _ := trackingFor(t, clone)
		wantGitUpstream(t, clone, up)
	})
}

func TestDefaultBranch(t *testing.T) { forEachFormat(t, testDefaultBranch) }

func testDefaultBranch(t *testing.T) {
	t.Helper()
	t.Run("from origin/HEAD, both comparisons", testDefaultBranchFromOriginHEAD)
	t.Run("no origin/HEAD falls back to the main list", testDefaultBranchFromMainList)

	t.Run("on the default branch", func(t *testing.T) {
		_, _, clone := remoteAndClone(t)
		if _, def := trackingFor(t, clone); def != nil {
			t.Errorf("default = %+v, want nil when it is checked out", def)
		}
	})

	t.Run("unborn", func(t *testing.T) {
		dir := t.TempDir()
		git(t, dir, "init", "-q")
		if _, def := trackingFor(t, dir); def != nil {
			t.Errorf("default = %+v, want nil", def)
		}
	})
}

func testDefaultBranchFromOriginHEAD(t *testing.T) {
	_, seed, clone := remoteAndClone(t)
	git(t, clone, "switch", "-q", "-c", "feature")
	commitFile(t, clone, "f", "1", "f1")
	commitFile(t, seed, "up", "1", "up 1")
	git(t, seed, "push", "-q")
	git(t, clone, "fetch", "-q")

	_, def := trackingFor(t, clone)
	if def == nil {
		t.Fatal("no default branch")
	}
	if want := git(t, clone, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); "origin/"+def.Name != want {
		t.Errorf("Name = %q, git origin/HEAD = %q", def.Name, want)
	}
	if def.Versus == nil || def.Versus.Ref != "origin/main" {
		t.Fatalf("Versus = %+v", def.Versus)
	}
	wantCounts(t, clone, "Versus counts", def.Versus, "feature", "origin/main")
	wantCounts(t, clone, "Local counts", def.Local, mainBranch, "origin/main")
}

func testDefaultBranchFromMainList(t *testing.T) {
	const fallback = "master" // in testMainBranches, after main
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", fallback)
	commitFile(t, dir, "a", "1", "init")
	git(t, dir, "switch", "-q", "-c", "feature")
	commitFile(t, dir, "f", "1", "f1")

	_, def := trackingFor(t, dir)
	if def == nil || def.Name != fallback {
		t.Fatalf("default = %+v, want %s", def, fallback)
	}
	if def.Local != nil {
		t.Errorf("Local = %+v, want nil (%s has no upstream)", def.Local, fallback)
	}
	if def.Versus == nil || def.Versus.Ref != fallback {
		t.Fatalf("Versus = %+v", def.Versus)
	}
	wantCounts(t, dir, "Versus counts", def.Versus, "feature", fallback)
}
