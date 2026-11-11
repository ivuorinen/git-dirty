package gitstat

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResult_Dirty(t *testing.T) {
	for name, c := range map[string]struct {
		r    Result
		want bool
	}{
		"clean":          {Result{}, false},
		"M":              {Result{Status: Status{Modified: true}}, true},
		"S":              {Result{Status: Status{Staged: true}}, true},
		"?":              {Result{Status: Status{Untracked: true}}, true},
		"$":              {Result{Status: Status{Stashed: true}}, true},
		"ahead":          {Result{Upstream: &Tracking{Ahead: 1}}, true},
		"only behind":    {Result{Upstream: &Tracking{Behind: 1}}, false},
		"upstream gone":  {Result{Upstream: &Tracking{Gone: true}}, false},
		"a failed stage": {Result{Err: errors.New("x")}, false},
	} {
		if got := c.r.Dirty(); got != c.want {
			t.Errorf("%s: Dirty() = %v, want %v", name, got, c.want)
		}
	}
}

// A branch with no commits has a status and nothing to compare.
func TestInspect_Unborn(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "new.txt", "n\n")
	res := Inspect(context.Background(), dir, inspectOpts(t))
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if !res.Head.Unborn || !res.Status.Untracked || res.Upstream != nil || res.Default != nil {
		t.Errorf("Result = %+v, want unborn, untracked, nothing to compare", res)
	}
}

// removeObject deletes the loose object rev names.
func removeObject(t *testing.T, dir, rev string) {
	t.Helper()
	hex := git(t, dir, "rev-parse", rev)
	remove(t, filepath.Join(dir, ".git", "objects", hex[:2], hex[2:]))
}

// missingHash is a well-formed id of the right length that names no object.
func missingHash(t *testing.T, dir string) string {
	t.Helper()

	return strings.Repeat("ab", len(git(t, dir, "rev-parse", "HEAD"))/2)
}

// garbage overwrites a file of dir's git dir with what is not a ref.
func garbage(t *testing.T, dir, name string) {
	t.Helper()
	write(t, dir, ".git/"+name, "garbage\n")
}

// behindClone is a clone two commits behind origin/main, with those commits
// loose.
func behindClone(t *testing.T) string {
	t.Helper()
	_, seed, clone := remoteAndClone(t)
	commitFile(t, seed, "up", "1", "up 1")
	commitFile(t, seed, "up", "2", "up 2")
	git(t, seed, "push", "-q")
	git(t, clone, "fetch", "-q")

	return clone
}

// The stage names Result.Err starts with.
const (
	stageAheadBehind = "ahead/behind"
	stageDefault     = "default branch"
	stageHead        = "head"
	stageModified    = "modified"
	stageStaged      = "staged"
	stageUntracked   = "untracked"
)

// wantGitRefuses fails the test unless git cannot list dir's untracked files.
// Its callers make an exclude file a directory, which only git for Windows
// reads as having no patterns, so there they are skipped.
func wantGitRefuses(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == goosWindows {
		t.Skip("git for Windows reads a directory as an exclude file with no patterns")
	}
	if out, code := gitCode(t, dir, "ls-files", "--others", "--exclude-standard"); code == 0 {
		t.Fatalf("fixture: git accepts it: %s", out)
	}
}

// TestInspect_StageErrors: every failure lands in Result.Err, prefixed with
// the stage that failed.
func TestInspect_StageErrors(t *testing.T) {
	for _, c := range []struct {
		name, stage string
		setup       func(t *testing.T) string
	}{
		{"HEAD's commit is gone", stageStaged, func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			removeObject(t, dir, "HEAD")

			return dir
		}},
		{"HEAD's tree is gone", stageStaged, func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			write(t, dir, "b.txt", "b\n")
			git(t, dir, "add", "b.txt")
			removeObject(t, dir, "HEAD^{tree}")

			return dir
		}},
		{"refs/stash is garbage", "stash", func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			garbage(t, dir, "refs/stash")

			return dir
		}},
		{"shallow is a directory", "shallow", func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			mkdirAll(t, filepath.Join(dir, ".git", "shallow"))

			return dir
		}},
		{"the upstream ref is garbage", stageAheadBehind, func(t *testing.T) string {
			t.Helper()
			_, _, clone := remoteAndClone(t)
			garbage(t, clone, "refs/remotes/origin/main")

			return clone
		}},
		{"the upstream names no object", stageAheadBehind, func(t *testing.T) string {
			t.Helper()
			_, _, clone := remoteAndClone(t)
			write(t, clone, ".git/refs/remotes/origin/main", missingHash(t, clone)+"\n")

			return clone
		}},
		{"a commit behind the upstream is missing", stageAheadBehind, func(t *testing.T) string {
			t.Helper()
			clone := behindClone(t)
			removeObject(t, clone, "origin/main~1")

			return clone
		}},
		{"a commit behind the default branch is missing", stageDefault, func(t *testing.T) string {
			t.Helper()
			clone := behindClone(t)
			git(t, clone, "switch", "-q", "-c", "feature")
			git(t, clone, "branch", "-q", "-D", mainBranch)
			removeObject(t, clone, "origin/main~1")

			return clone
		}},
		{"core.excludesFile is a directory", stageUntracked, func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			mkdirAll(t, filepath.Join(dir, "patterns"))
			git(t, dir, "config", "core.excludesFile", "patterns")
			wantGitRefuses(t, dir)

			return dir
		}},
		{"info/exclude is a directory", stageUntracked, func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			exclude := filepath.Join(dir, ".git", "info", "exclude")
			mkdirAll(t, filepath.Dir(exclude))
			if err := os.RemoveAll(exclude); err != nil {
				t.Fatal(err)
			}
			mkdirAll(t, exclude)
			wantGitRefuses(t, dir)

			return dir
		}},
		{"origin/HEAD is garbage", stageDefault, func(t *testing.T) string {
			t.Helper()
			clone := onFeature(t)
			garbage(t, clone, "refs/remotes/origin/HEAD")

			return clone
		}},
		{"a branch from the list is garbage", stageDefault, func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			git(t, dir, "switch", "-q", "-c", "feature")
			garbage(t, dir, "refs/heads/main")

			return dir
		}},
		{"the local default branch is garbage", stageDefault, func(t *testing.T) string {
			t.Helper()
			clone := onFeature(t)
			garbage(t, clone, "refs/heads/main")

			return clone
		}},
		{"the remote default branch is garbage", stageDefault, func(t *testing.T) string {
			t.Helper()
			clone := onFeature(t)
			git(t, clone, "branch", "-q", "-D", mainBranch)
			garbage(t, clone, "refs/remotes/origin/main")

			return clone
		}},
		{"a submodule's .git leads nowhere", stageModified, func(t *testing.T) string {
			t.Helper()
			super := superWithSubmodule(t)
			write(t, super, "mod/.git", "gitdir: /nonexistent\n")

			return super
		}},
		{"a submodule's HEAD is garbage", stageModified, func(t *testing.T) string {
			t.Helper()
			super := superWithSubmodule(t)
			garbage(t, super, "modules/mod/HEAD")

			return super
		}},
		{"a submodule's index is garbage", stageModified, func(t *testing.T) string {
			t.Helper()
			super := superWithSubmodule(t)
			garbage(t, super, "modules/mod/index")

			return super
		}},
		{"HEAD is the zero id", stageHead, func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			write(t, dir, ".git/HEAD", strings.Repeat("0", len(git(t, dir, "rev-parse", "HEAD")))+"\n")

			return dir
		}},
		{"HEAD's branch is garbage", stageHead, func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			garbage(t, dir, "refs/heads/main")

			return dir
		}},
		{"a tag is garbage on a detached HEAD", stageHead, func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			git(t, dir, "switch", "-q", "--detach")
			garbage(t, dir, "refs/tags/bad")

			return dir
		}},
		{"a tag names no object on a detached HEAD", stageHead, func(t *testing.T) string {
			t.Helper()
			dir := newRepo(t)
			git(t, dir, "switch", "-q", "--detach")
			write(t, dir, ".git/refs/tags/gone", missingHash(t, dir)+"\n")

			return dir
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantStageErr(t, Inspect(context.Background(), c.setup(t), inspectOpts(t)), c.stage)
		})
	}
}

func TestInspect_CanceledNamesTheStage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Inspect(ctx, newRepo(t), inspectOpts(t))
	wantStageErr(t, res, stageModified)
	if !errors.Is(res.Err, context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", res.Err)
	}
}

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

func TestInspect_UnreadableWorktree(t *testing.T) {
	dir := newRepo(t)
	if !chmodBack(t, dir, 0o300) {
		t.Skip("the mode does not stop this user")
	}
	wantStageErr(t, Inspect(context.Background(), dir, inspectOpts(t)), "untracked")
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

// A global excludes file that is a directory stops git, and the scan.
func TestLoadExcludes_Directory(t *testing.T) {
	home := isolatedHome(t)
	dir := newRepo(t)
	mkdirAll(t, filepath.Join(home, "patterns"))
	write(t, home, ".gitconfig", baseGitconfig+"[core]\n\texcludesFile = ~/patterns\n")
	wantGitRefuses(t, dir)
	if _, err := LoadExcludes(); err == nil {
		t.Error("want error")
	}
}

// unreadable makes the tracked file p something the status has to hash and
// cannot, or skips the test where mode 0 stops nothing.
func unreadable(t *testing.T, p string) {
	t.Helper()
	chtimes(t, p, time.Now().Add(time.Hour))
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Clean(p)); !errors.Is(err, fs.ErrPermission) {
		t.Skipf("mode 0 does not stop this user reading a file: %v", err)
	}
}

// TestInspect_UnreadableFile: a tracked file whose stat data no longer
// matches has to be hashed, and one that cannot be read is an error.
func TestInspect_UnreadableFile(t *testing.T) {
	dir := newRepo(t)
	unreadable(t, filepath.Join(dir, "a.txt"))
	wantStageErr(t, Inspect(context.Background(), dir, inspectOpts(t)), stageModified)
}

func TestInspect_UnreadableFileInASubmodule(t *testing.T) {
	super := superWithSubmodule(t)
	unreadable(t, filepath.Join(super, "mod", "a.txt"))
	wantStageErr(t, Inspect(context.Background(), super, inspectOpts(t)), stageModified)
}

// A tracked file below a directory that cannot be searched is an error, not
// a deleted file.
func TestInspect_UnsearchableTrackedDirectory(t *testing.T) {
	dir := newRepo(t)
	commitFile(t, dir, "sub/f.txt", "f\n", "nested")
	if !chmodBack(t, filepath.Join(dir, "sub"), 0) {
		t.Skip("the mode does not stop this user")
	}
	wantStageErr(t, Inspect(context.Background(), dir, inspectOpts(t)), stageModified)
}

func TestInspect_UnreadableShallowFile(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, ".git/shallow", "")
	p := filepath.Join(dir, ".git", "shallow")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(filepath.Clean(p)); !errors.Is(err, fs.ErrPermission) {
		t.Skipf("mode 0 does not stop this user reading a file: %v", err)
	}
	wantStageErr(t, Inspect(context.Background(), dir, inspectOpts(t)), "shallow")
}

// manyFiles is a committed repository with more entries than one
// cancellation check covers.
func manyFiles(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	for i := range ctxCheckEvery {
		write(t, dir, fmt.Sprintf("sub/f%03d", i), "x") // the walk has to stop one level down too
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "many files")

	return dir
}

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return ctx
}

func TestTreeBlobs_ChecksTheContext(t *testing.T) {
	dir := manyFiles(t)
	r := open(t, dir)
	c, err := r.repo.ReadCommit(hashOf(t, dir, "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.treeBlobs(canceled(), c.Tree); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v", err)
	}
	if blobs, err := r.treeBlobs(context.Background(), c.Tree); err != nil || len(blobs) <= ctxCheckEvery {
		t.Errorf("live context: %d blobs, err = %v", len(blobs), err)
	}
}

func TestUntrackedWalk_ChecksTheContext(t *testing.T) {
	r := open(t, manyFiles(t))
	idx, err := r.repo.ReadIndex()
	if err != nil {
		t.Fatal(err)
	}
	ex, err := LoadExcludes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.untracked(canceled(), idx, r.coreConfig(), ex); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: err = %v", err)
	}
	if found, err := r.untracked(context.Background(), idx, r.coreConfig(), ex); err != nil || found {
		t.Errorf("live context: found = %v, err = %v", found, err)
	}
}

func TestAheadBehind_MissingCommit(t *testing.T) {
	dir, a, b := histories()["diverged"](t)
	removeObject(t, dir, b+"~1")
	w, err := open(t, dir).newCommitWalker()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.aheadBehind(context.Background(), hashOf(t, dir, a), hashOf(t, dir, b)); err == nil {
		t.Error("a commit in the walk is missing: want error")
	}
	id, err := open(t, dir).repo.Format.ParseHex(missingHash(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.aheadBehind(context.Background(), id, hashOf(t, dir, b)); err == nil {
		t.Error("the tip is missing: want error")
	}
}

func TestReadShallow_SkipsGarbage(t *testing.T) {
	dir := newRepo(t)
	head := hashOf(t, dir, "HEAD")
	write(t, dir, "shallow", "garbage\n"+head.String()+"\n")
	r := open(t, dir)
	set, err := readShallow(filepath.Join(dir, "shallow"), r.repo.Format)
	if err != nil || len(set) != 1 || !set[head] {
		t.Errorf("set = %v, err = %v, want only HEAD", set, err)
	}
}
