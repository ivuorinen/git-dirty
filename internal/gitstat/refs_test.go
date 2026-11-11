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
	head, err := r.head()
	if err != nil {
		t.Fatal(err)
	}
	up, err := r.upstream(context.Background(), w, head.Branch, head.Hash)
	if err != nil {
		t.Fatal(err)
	}
	def, err := r.defaultBranch(context.Background(), w, head, testMainBranches)
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

// onFeature is a clone with "feature" checked out and no upstream for it,
// and origin/HEAD pointing at main.
func onFeature(t *testing.T) string {
	t.Helper()
	_, _, clone := remoteAndClone(t)
	git(t, clone, "switch", "-q", "-c", "feature")

	return clone
}

func TestUpstream_Unmapped(t *testing.T) {
	t.Run("a remote without a merge ref is no upstream", func(t *testing.T) {
		dir := newRepo(t)
		git(t, dir, "config", "branch.main.remote", "origin")
		if _, code := gitCode(t, dir, "rev-parse", "@{u}"); code == 0 {
			t.Fatal("fixture: git found an upstream")
		}
		if up, _ := trackingFor(t, dir); up != nil {
			t.Errorf("upstream = %+v, want nil", up)
		}
	})

	for name, fetch := range map[string]string{
		"a malformed fetch refspec maps nothing": "no-colon",
		"a fetch refspec for other branches":     "+refs/heads/other:refs/remotes/origin/other",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, clone := remoteAndClone(t)
			git(t, clone, "config", "remote.origin.fetch", fetch)
			if out, code := gitCode(t, clone, "rev-parse", "@{u}"); code == 0 {
				t.Fatalf("fixture: git mapped the upstream: %s", out)
			}
			up, _ := trackingFor(t, clone)
			if up == nil || !up.Gone || up.Ref != "" {
				t.Errorf("upstream = %+v, want Gone with no ref", up)
			}
		})
	}

}

func TestDefaultBranch_Corners(t *testing.T) { forEachFormat(t, testDefaultBranchCorners) }

func testDefaultBranchCorners(t *testing.T) {
	t.Helper()
	for _, c := range []struct {
		name          string
		setup         func(t *testing.T) string
		want          string // the default branch, "" for none
		local, versus bool   // the comparisons it should have
	}{
		{"no candidate exists", func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			git(t, dir, "branch", "-m", "feature")

			return dir
		}, "", false, false},
		{"origin/HEAD is not symbolic", func(t *testing.T) string {
			t.Helper()
			clone := onFeature(t)
			git(t, clone, "update-ref", "--no-deref", "refs/remotes/origin/HEAD", "HEAD")

			return clone
		}, mainBranch, true, true},
		{"origin/HEAD leads to another remote", func(t *testing.T) string {
			t.Helper()
			clone := onFeature(t)
			git(t, clone, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/other/trunk")

			return clone
		}, mainBranch, true, true},
		{"no local default branch", func(t *testing.T) string {
			t.Helper()
			clone := onFeature(t)
			git(t, clone, "branch", "-q", "-D", mainBranch)

			return clone
		}, mainBranch, false, true},
		{"the default branch exists nowhere", func(t *testing.T) string {
			t.Helper()
			clone := onFeature(t)
			git(t, clone, "branch", "-q", "-D", mainBranch)
			git(t, clone, "update-ref", "-d", "refs/remotes/origin/main")

			return clone
		}, mainBranch, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, def := trackingFor(t, c.setup(t))
			if c.want == "" {
				if def != nil {
					t.Errorf("default = %+v, want nil", def)
				}

				return
			}
			if def == nil || def.Name != c.want || (def.Local != nil) != c.local || (def.Versus != nil) != c.versus {
				t.Errorf("default = %+v, want %s, local %v, versus %v", def, c.want, c.local, c.versus)
			}
		})
	}
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
