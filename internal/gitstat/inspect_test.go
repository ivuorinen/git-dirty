package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func inspectOpts(t *testing.T) Options {
	t.Helper()
	ex, err := LoadExcludes()
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		CheckUntracked: true,
		CheckStash:     true,
		MainBranches:   []string{"main", "master", "trunk"},
		Excludes:       ex,
	}
}

func TestInspect_AllFields(t *testing.T) {
	_, seed, clone := remoteAndClone(t)
	git(t, clone, "switch", "-q", "-c", "feature")
	git(t, clone, "push", "-q", "-u", "origin", "feature")
	commitFile(t, clone, "f", "1", "unpushed")
	write(t, clone, "stash-me.txt", "s\n")
	git(t, clone, "stash", "-q", "-u")
	write(t, clone, "a.txt", "modified\n")
	write(t, clone, "staged.txt", "s\n")
	git(t, clone, "add", "staged.txt")
	write(t, clone, "untracked.txt", "u\n")
	commitFile(t, seed, "up", "1", "upstream main moved")
	git(t, seed, "push", "-q")
	git(t, clone, "fetch", "-q")

	res := Inspect(context.Background(), clone, inspectOpts(t))
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Head.Branch != "feature" {
		t.Errorf("Branch = %q", res.Head.Branch)
	}
	want := Status{Modified: true, Staged: true, Untracked: true, Stashed: true}
	if res.Status != want {
		t.Errorf("Status = %+v, want %+v", res.Status, want)
	}
	if res.Upstream == nil || res.Upstream.Ref != "origin/feature" || countsOf(res.Upstream) != git(t, clone, "rev-list", "--left-right", "--count", "HEAD...@{u}") {
		t.Errorf("Upstream = %+v", res.Upstream)
	}
	if res.Default == nil || res.Default.Name != "main" {
		t.Fatalf("Default = %+v", res.Default)
	}
	if got, want := countsOf(res.Default.Versus), git(t, clone, "rev-list", "--left-right", "--count", "HEAD...origin/main"); got != want {
		t.Errorf("Versus = %q, git says %q", got, want)
	}
	if got, want := countsOf(res.Default.Local), git(t, clone, "rev-list", "--left-right", "--count", "main...origin/main"); got != want {
		t.Errorf("Local = %q, git says %q", got, want)
	}
	if !res.Dirty() {
		t.Error("Dirty() = false")
	}
}

func TestInspect_OptionsOff(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "untracked.txt", "u\n")
	write(t, dir, "a.txt", "wip\n")
	git(t, dir, "stash", "-q")
	opts := inspectOpts(t)
	opts.CheckUntracked, opts.CheckStash = false, false
	res := Inspect(context.Background(), dir, opts)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Status.Untracked || res.Status.Stashed {
		t.Errorf("Status = %+v, want untracked and stash unchecked", res.Status)
	}
	if res.Dirty() {
		t.Error("Dirty() = true for a clean worktree with checks off")
	}
}

// TestInspect_SplitIndex: split-index repositories were an error line while
// go-git read the index; the in-tree reader supports them.
func TestInspect_SplitIndex(t *testing.T) {
	dir := newRepo(t)
	git(t, dir, "config", "splitIndex.maxPercentChange", "100")
	git(t, dir, "update-index", "--split-index")
	commitFile(t, dir, "b.txt", "b\n", "on the split index")
	write(t, dir, "a.txt", "modified\n")
	write(t, dir, "staged.txt", "s\n")
	git(t, dir, "add", "staged.txt")
	write(t, dir, "untracked.txt", "u\n")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, ".git", "sharedindex.*")); len(matches) == 0 {
		t.Fatal("fixture: no shared index written")
	}

	res := Inspect(context.Background(), dir, inspectOpts(t))
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	want := Status{
		Modified:  gitVerdict(t, dir, "diff", "--quiet"),
		Staged:    gitVerdict(t, dir, "diff", "--cached", "--quiet"),
		Untracked: git(t, dir, "ls-files", "--others", "--exclude-standard") != "",
	}
	if res.Status != want || !want.Modified || !want.Staged || !want.Untracked {
		t.Errorf("Status = %+v, git says %+v", res.Status, want)
	}
}

func TestInspect_Errors(t *testing.T) {
	t.Run("sparse index", func(t *testing.T) {
		dir := newRepo(t)
		commitFile(t, dir, "A/x.txt", "x\n", "a")
		commitFile(t, dir, "B/y.txt", "y\n", "b")
		git(t, dir, "sparse-checkout", "init", "--cone", "--sparse-index")
		git(t, dir, "sparse-checkout", "set", "A")
		res := Inspect(context.Background(), dir, inspectOpts(t))
		if res.Err == nil || !strings.Contains(res.Err.Error(), `"sdir"`) || !strings.HasPrefix(res.Err.Error(), "index:") {
			t.Errorf("Err = %v, want an index: unsupported extension \"sdir\" error", res.Err)
		}
		if res.Head.Branch != "main" {
			t.Errorf("Head before the failing stage lost: %+v", res.Head)
		}
	})
	t.Run("corrupted HEAD", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, ".git/HEAD", "garbage\n")
		res := Inspect(context.Background(), dir, inspectOpts(t))
		if res.Err == nil || !strings.HasPrefix(res.Err.Error(), "head:") {
			t.Errorf("Err = %v, want a head: error", res.Err)
		}
	})
	t.Run("HEAD names a missing commit", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, ".git/HEAD", strings.Repeat("ab", 20)+"\n")
		res := Inspect(context.Background(), dir, inspectOpts(t))
		if res.Err == nil || !strings.HasPrefix(res.Err.Error(), "head:") {
			t.Errorf("Err = %v, want a head: error", res.Err)
		}
	})
	t.Run("not a repository", func(t *testing.T) {
		res := Inspect(context.Background(), t.TempDir(), inspectOpts(t))
		if res.Err == nil || !strings.HasPrefix(res.Err.Error(), "open:") {
			t.Errorf("Err = %v, want an open: error", res.Err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res := Inspect(ctx, newRepo(t), inspectOpts(t))
		if res.Err == nil {
			t.Error("Err = nil for a cancelled context")
		}
	})
}

func TestInspect_ReleasesDescriptors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor count via /proc/self/fd is Linux-only")
	}
	_, _, clone := remoteAndClone(t)
	git(t, clone, "switch", "-q", "-c", "feature")
	commitFile(t, clone, "f", "1", "f")
	git(t, clone, "gc", "-q")
	git(t, clone, "commit-graph", "write", "--reachable")

	countFDs := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	opts := inspectOpts(t)
	before := countFDs()
	for range 300 {
		if res := Inspect(context.Background(), clone, opts); res.Err != nil {
			t.Fatal(res.Err)
		}
	}
	if after := countFDs(); after > before {
		t.Errorf("descriptors leaked: %d before, %d after 300 inspections", before, after)
	}
}
