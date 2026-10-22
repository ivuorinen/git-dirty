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
		MainBranches:   testMainBranches,
		Excludes:       ex,
	}
}

// wantCounts fails the test unless tr holds the counts
// `git rev-list --left-right --count a...b` prints.
func wantCounts(t *testing.T, dir, what string, tr *Tracking, a, b string) {
	t.Helper()
	if tr == nil {
		t.Errorf("%s = nil, want counts for %s...%s", what, a, b)

		return
	}
	if got, want := countsOf(tr), revListCounts(t, dir, a, b); got != want {
		t.Errorf("%s = %q, git says %q", what, got, want)
	}
}

// wantStageErr fails the test unless res.Err is prefixed with stage.
func wantStageErr(t *testing.T, res Result, stage string) {
	t.Helper()
	if res.Err == nil || !strings.HasPrefix(res.Err.Error(), stage+":") {
		t.Errorf("Err = %v, want a %s: error", res.Err, stage)
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
	if res.Upstream == nil || res.Upstream.Ref != "origin/feature" {
		t.Errorf("Upstream = %+v", res.Upstream)
	}
	wantCounts(t, clone, "Upstream", res.Upstream, "HEAD", "@{u}")
	if res.Default == nil || res.Default.Name != mainBranch {
		t.Fatalf("Default = %+v", res.Default)
	}
	wantCounts(t, clone, "Versus", res.Default.Versus, "HEAD", "origin/main")
	wantCounts(t, clone, "Local", res.Default.Local, mainBranch, "origin/main")
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
		wantStageErr(t, res, "index")
		if res.Err == nil || !strings.Contains(res.Err.Error(), `"sdir"`) {
			t.Errorf("Err = %v, want an unsupported extension \"sdir\" error", res.Err)
		}
		if res.Head.Branch != mainBranch {
			t.Errorf("Head before the failing stage lost: %+v", res.Head)
		}
	})
	t.Run("corrupted HEAD", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, ".git/HEAD", "garbage\n")
		wantStageErr(t, Inspect(context.Background(), dir, inspectOpts(t)), "head")
	})
	t.Run("HEAD names a missing commit", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, ".git/HEAD", strings.Repeat("ab", 20)+"\n")
		wantStageErr(t, Inspect(context.Background(), dir, inspectOpts(t)), "head")
	})
	t.Run("not a repository", func(t *testing.T) {
		wantStageErr(t, Inspect(context.Background(), t.TempDir(), inspectOpts(t)), "open")
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res := Inspect(ctx, newRepo(t), inspectOpts(t))
		if res.Err == nil {
			t.Error("Err = nil for a canceled context")
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

	opts := inspectOpts(t)
	before := countFDs(t)
	for range 300 {
		if res := Inspect(context.Background(), clone, opts); res.Err != nil {
			t.Fatal(res.Err)
		}
	}
	if after := countFDs(t); after > before {
		t.Errorf("descriptors leaked: %d before, %d after 300 inspections", before, after)
	}
}
