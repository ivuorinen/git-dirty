package gitstat

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// history builds a repository and names the two revisions to compare.
type history func(t *testing.T) (dir, a, b string)

func branchFrom(t *testing.T, dir, name string) {
	t.Helper()
	git(t, dir, "branch", name)
}

func histories() map[string]history {
	return map[string]history{
		"equal tips": func(t *testing.T) (string, string, string) {
			dir := newRepo(t)
			branchFrom(t, dir, "other")
			return dir, "main", "other"
		},
		"linear ahead": func(t *testing.T) (string, string, string) {
			dir := newRepo(t)
			branchFrom(t, dir, "base")
			commitFile(t, dir, "b", "1", "1")
			commitFile(t, dir, "b", "2", "2")
			return dir, "main", "base"
		},
		"linear behind": func(t *testing.T) (string, string, string) {
			dir := newRepo(t)
			branchFrom(t, dir, "old")
			commitFile(t, dir, "b", "1", "1")
			commitFile(t, dir, "b", "2", "2")
			commitFile(t, dir, "b", "3", "3")
			return dir, "old", "main"
		},
		"diverged": func(t *testing.T) (string, string, string) {
			dir := newRepo(t)
			git(t, dir, "switch", "-q", "-c", "side")
			commitFile(t, dir, "s", "1", "s1")
			commitFile(t, dir, "s", "2", "s2")
			git(t, dir, "switch", "-q", "main")
			for i := range 3 {
				commitFile(t, dir, "m", fmt.Sprint(i), "m")
			}
			return dir, "side", "main"
		},
		"merge on one side": func(t *testing.T) (string, string, string) {
			dir := newRepo(t)
			git(t, dir, "switch", "-q", "-c", "feature")
			commitFile(t, dir, "f", "1", "f1")
			git(t, dir, "switch", "-q", "main")
			commitFile(t, dir, "m", "1", "m1")
			commitFile(t, dir, "m", "2", "m2")
			git(t, dir, "switch", "-q", "feature")
			git(t, dir, "merge", "-q", "--no-edit", "main")
			commitFile(t, dir, "f", "2", "f2")
			git(t, dir, "switch", "-q", "main")
			commitFile(t, dir, "m", "3", "m3")
			return dir, "feature", "main"
		},
		"criss-cross merges": func(t *testing.T) (string, string, string) {
			dir := newRepo(t)
			git(t, dir, "switch", "-q", "-c", "x")
			commitFile(t, dir, "x", "1", "x1")
			git(t, dir, "switch", "-q", "main")
			commitFile(t, dir, "m", "1", "m1")
			git(t, dir, "branch", "m-snap")
			git(t, dir, "merge", "-q", "--no-edit", "x")
			git(t, dir, "switch", "-q", "x")
			git(t, dir, "merge", "-q", "--no-edit", "m-snap")
			commitFile(t, dir, "x", "2", "x2")
			git(t, dir, "switch", "-q", "main")
			commitFile(t, dir, "m", "2", "m2")
			return dir, "x", "main"
		},
		"skewed commit dates": func(t *testing.T) (string, string, string) {
			dir := t.TempDir()
			git(t, dir, "init", "-q")
			future := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			past := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
			commitAt(t, dir, "root", past)
			commitAt(t, dir, "base dated in the future", future)
			git(t, dir, "switch", "-q", "-c", "side")
			commitAt(t, dir, "s1", past.Add(time.Hour))
			commitAt(t, dir, "s2", past.Add(2*time.Hour))
			git(t, dir, "switch", "-q", "main")
			commitAt(t, dir, "m1", past.Add(3*time.Hour))
			git(t, dir, "merge", "-q", "--no-edit", "side")
			commitAt(t, dir, "m2", past)
			git(t, dir, "switch", "-q", "side")
			commitAt(t, dir, "s3", past.Add(time.Minute))
			return dir, "side", "main"
		},
	}
}

func revListCounts(t *testing.T, dir, a, b string) string {
	t.Helper()
	return git(t, dir, "rev-list", "--left-right", "--count", a+"..."+b)
}

func ourCounts(t *testing.T, dir, a, b string, wantGraph bool) string {
	t.Helper()
	r := open(t, dir)
	w, err := r.newCommitWalker()
	if err != nil {
		t.Fatal(err)
	}
	ha := hashOf(t, dir, a)
	hb := hashOf(t, dir, b)
	ahead, behind, err := w.aheadBehind(context.Background(), ha, hb)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%d\t%d", ahead, behind)
}

func TestAheadBehind(t *testing.T) { forEachFormat(t, testAheadBehind) }

func testAheadBehind(t *testing.T) {
	for name, build := range histories() {
		for _, withGraph := range []bool{false, true} {
			label := name + "/objects"
			if withGraph {
				label = name + "/commit-graph"
			}
			t.Run(label, func(t *testing.T) {
				dir, a, b := build(t)
				if withGraph {
					git(t, dir, "commit-graph", "write", "--reachable")
				}
				want := revListCounts(t, dir, a, b)
				if got := ourCounts(t, dir, a, b, withGraph); got != want {
					t.Errorf("ahead/behind %s...%s = %q, git rev-list says %q", a, b, got, want)
				}
			})
		}
	}
}

func TestAheadBehind_ShallowClone(t *testing.T) { forEachFormat(t, testAheadBehind_ShallowClone) }

func testAheadBehind_ShallowClone(t *testing.T) {
	origin := newRepo(t)
	for i := range 8 {
		commitFile(t, origin, "h", fmt.Sprint(i), "history")
	}
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, origin, "clone", "-q", "--depth", "3", "file://"+origin, clone)
	commitFile(t, clone, "local", "1", "local 1")
	commitFile(t, clone, "local", "2", "local 2")
	commitFile(t, origin, "h", "new1", "upstream 1")
	commitFile(t, origin, "h", "new2", "upstream 2")
	git(t, clone, "fetch", "-q")

	want := revListCounts(t, clone, "main", "origin/main")
	if got := ourCounts(t, clone, "main", "origin/main", false); got != want {
		t.Errorf("shallow ahead/behind = %q, git rev-list says %q", got, want)
	}
}

func TestAheadBehind_Cancelled(t *testing.T) {
	dir, a, b := histories()["diverged"](t)
	w, err := open(t, dir).newCommitWalker()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := w.aheadBehind(ctx, hashOf(t, dir, a), hashOf(t, dir, b)); err == nil {
		t.Error("want context error")
	}
}
