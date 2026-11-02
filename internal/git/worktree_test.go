package git

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// worktreeRepo makes a repository with one commit, alone in a fresh root so
// that a sibling worktree can sit next to it.
func worktreeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run(t, root, "init", "-q", "main")
	dir := filepath.Join(root, "main")
	commit(t, dir, "a.txt", "a\n", "init")

	return dir
}

func addWorktree(t *testing.T, dir, path string) {
	t.Helper()
	run(t, dir, "worktree", "add", "-q", "--detach", path)
}

// porcelain maps each linked worktree in `git worktree list --porcelain` to
// whether git marks it prunable. The main worktree, listed first, is left
// out.
func porcelain(t *testing.T, dir string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for i, block := range strings.Split(run(t, dir, "worktree", "list", "--porcelain"), "\n\n") {
		if i == 0 {
			continue
		}
		var path string
		prunable := false
		for line := range strings.SplitSeq(block, "\n") {
			if p, ok := strings.CutPrefix(line, "worktree "); ok {
				path = filepath.Clean(filepath.FromSlash(p))
			}
			prunable = prunable || strings.HasPrefix(line, "prunable")
		}
		out[path] = prunable
	}

	return out
}

// worktreesFixture makes a repository with worktrees outside it (two named
// feat), nested in it, beside it, and one whose directory is deleted.
func worktreesFixture(t *testing.T) string {
	t.Helper()
	dir := worktreeRepo(t)
	gone := filepath.Join(t.TempDir(), "gone")
	for _, p := range []string{
		filepath.Join(t.TempDir(), "feat"),
		filepath.Join(t.TempDir(), "feat"), // same name: git makes the ID feat1
		filepath.Join(dir, ".worktrees", "nested"),
		filepath.Join(filepath.Dir(dir), "sibling"),
		gone,
	} {
		addWorktree(t, dir, p)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	return dir
}

// checkListed checks one listed worktree against git's list and reports
// whether git marks it prunable.
func checkListed(t *testing.T, wt Worktree, want map[string]bool) bool {
	t.Helper()
	prunable, ok := want[wt.Path]
	switch {
	case !ok:
		t.Errorf("%s: path %q is not one git lists", wt.ID, wt.Path)
	case prunable:
		if !errors.Is(wt.Err, errWorktreeMissing) {
			t.Errorf("%s: git marks it prunable, Err = %v", wt.ID, wt.Err)
		}
	case wt.Err != nil:
		t.Errorf("%s: %v", wt.ID, wt.Err)
	default:
		if got := filepath.Base(run(t, wt.Path, "rev-parse", "--git-dir")); got != wt.ID {
			t.Errorf("ID %s, git dir %s", wt.ID, got)
		}
	}

	return prunable
}

func TestListWorktrees(t *testing.T) {
	dir := worktreesFixture(t)
	wts, linked, err := ListWorktrees(dir)
	if err != nil || linked {
		t.Fatalf("ListWorktrees: linked %v, err %v", linked, err)
	}
	want := porcelain(t, dir)
	if len(wts) != len(want) {
		t.Fatalf("listed %d worktrees, git lists %d: %+v", len(wts), len(want), wts)
	}
	ids := make([]string, 0, len(wts))
	pruned := 0
	for _, wt := range wts {
		ids = append(ids, wt.ID)
		if checkListed(t, wt, want) {
			pruned++
		}
	}
	if pruned != 1 {
		t.Errorf("%d listed worktrees are prunable, want 1", pruned)
	}
	if wantIDs := []string{"feat", "feat1", "gone", "nested", "sibling"}; !slices.Equal(ids, wantIDs) {
		t.Errorf("IDs %v, want %v in this order", ids, wantIDs)
	}
}

// TestListWorktrees_BareRepository lists from a folder that is itself the
// git directory, including a worktree whose files are inside that folder.
func TestListWorktrees_BareRepository(t *testing.T) {
	for _, f := range formats {
		t.Run(f.Name, func(t *testing.T) {
			plain := filepath.Join(bareLayouts(t, f), "plain.git")
			wts, linked, err := ListWorktrees(plain)
			if err != nil || linked {
				t.Fatalf("ListWorktrees: linked %v, err %v", linked, err)
			}
			want := porcelain(t, plain)
			if len(want) != 2 || len(wts) != len(want) {
				t.Fatalf("listed %d worktrees, git lists %d (want 2): %+v", len(wts), len(want), wts)
			}
			for _, wt := range wts {
				if checkListed(t, wt, want) {
					t.Errorf("%s: git marks it prunable", wt.ID)
				}
			}
		})
	}
}

// A file named worktrees is the same error on every platform: os.ReadDir
// fails on it with ENOTDIR on Linux and macOS, but reads it as an empty
// directory on Windows, which listed the repository with no worktrees.
func TestListWorktrees_WorktreesFile(t *testing.T) {
	dir := newRepo(t, SHA1)
	if err := os.WriteFile(filepath.Join(dir, ".git", "worktrees"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if wts, _, err := ListWorktrees(dir); !errors.Is(err, errWorktreesNotDir) {
		t.Errorf("got %+v, %v; want %v", wts, err, errWorktreesNotDir)
	}
}

// A worktrees symlink to a directory is followed, as `git worktree list`
// follows it, so the not-a-directory check must not reject it. Only the
// listing is compared: git cannot open the worktree itself, because the
// admin dir's relative commondir resolves from the symlink's target.
func TestListWorktrees_WorktreesSymlink(t *testing.T) {
	dir := worktreeRepo(t)
	addWorktree(t, dir, filepath.Join(filepath.Dir(dir), "wt"))
	admin := filepath.Join(dir, ".git", "worktrees")
	moved := filepath.Join(t.TempDir(), "worktrees")
	if err := os.Rename(admin, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, admin); err != nil {
		t.Skip(err)
	}
	wts, _, err := ListWorktrees(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := porcelain(t, dir)
	if len(want) != 1 || len(wts) != 1 {
		t.Fatalf("listed %+v, git lists %v (want 1)", wts, want)
	}
	if _, ok := want[wts[0].Path]; !ok {
		t.Errorf("listed %q, git lists %v", wts[0].Path, want)
	}
}

func TestListWorktrees_Linked(t *testing.T) {
	if wts, linked, err := ListWorktrees(newRepo(t, SHA1)); len(wts) != 0 || linked || err != nil {
		t.Errorf("no worktrees: got %+v, %v, %v", wts, linked, err)
	}
	dir := worktreeRepo(t)
	sibling := filepath.Join(filepath.Dir(dir), "sibling")
	addWorktree(t, dir, sibling)
	for _, c := range []struct {
		path   string
		linked bool
	}{{dir, false}, {sibling, true}} {
		gitDir := run(t, c.path, "rev-parse", "--absolute-git-dir")
		common := run(t, c.path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if (gitDir != common) != c.linked {
			t.Fatalf("%s: git dir %s, common dir %s", c.path, gitDir, common)
		}
		wts, linked, err := ListWorktrees(c.path)
		if err != nil || linked != c.linked || (linked && len(wts) != 0) {
			t.Errorf("%s: got %+v, linked %v, err %v; want linked %v", c.path, wts, linked, err, c.linked)
		}
	}
}

// relativeWorktrees makes a repository with two linked worktrees, beside it
// and nested in it, that git records with relative paths: git 2.48's
// `git worktree add --relative-paths`, which also sets
// extensions.relativeWorktrees. dir has its symlinks resolved, as the paths
// git records do. The test is skipped on a git without --relative-paths.
func relativeWorktrees(t *testing.T) (dir string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(worktreeRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(filepath.Dir(dir), "sibling"), filepath.Join(dir, ".worktrees", "nested")} {
		out, code := runCode(t, dir, nil, "worktree", "add", "-q", "--detach", "--relative-paths", p)
		switch {
		case code != 0 && strings.Contains(out, "unknown option"):
			t.Skipf("git before 2.48 has no --relative-paths: %s", out)
		case code != 0:
			t.Fatalf("git worktree add --relative-paths: exit %d: %s", code, out)
		}
		if got, err := readLinkFile(filepath.Join(p, ".git"), "gitdir:"); err != nil || filepath.IsAbs(got) {
			t.Fatalf("%s/.git names %q, %v; want a relative path", p, got, err)
		}
	}
	got := run(t, dir, "config", "--bool", "--get", "extensions.relativeWorktrees")
	if on, err := strconv.ParseBool(got); err != nil || !on {
		t.Fatalf("extensions.relativeWorktrees = %q, want true", got)
	}

	return dir
}

// TestListWorktrees_RelativePaths: worktrees git records with relative paths
// are listed where git lists them, and they and their repository open, which
// needs extensions.relativeWorktrees to be a known extension.
func TestListWorktrees_RelativePaths(t *testing.T) {
	dir := relativeWorktrees(t)
	wts, linked, err := ListWorktrees(dir)
	if err != nil || linked {
		t.Fatalf("ListWorktrees: linked %v, err %v", linked, err)
	}
	want := porcelain(t, dir)
	if len(want) != 2 || len(wts) != len(want) {
		t.Fatalf("listed %d worktrees, git lists %d (want 2): %+v", len(wts), len(want), wts)
	}
	paths := append(make([]string, 0, 1+len(wts)), dir)
	for _, wt := range wts {
		if checkListed(t, wt, want) {
			t.Errorf("%s: git marks it prunable", wt.ID)
		}
		paths = append(paths, wt.Path)
	}
	for _, p := range paths {
		r, err := OpenWorktree(p)
		if err != nil {
			t.Errorf("OpenWorktree(%s): %v", p, err)

			continue
		}
		_ = r.Close() // read-only
	}
}

// oneWorktree makes a repository with one linked worktree, wt, outside it.
// It returns the repository, the worktree's admin dir and the worktree.
func oneWorktree(t *testing.T) (dir, admin, wt string) {
	t.Helper()
	dir = worktreeRepo(t)
	wt = filepath.Join(t.TempDir(), "wt")
	addWorktree(t, dir, wt)

	return dir, filepath.Join(dir, ".git", "worktrees", "wt"), wt
}

// listOne lists the single worktree of the repository at dir.
func listOne(t *testing.T, dir string) Worktree {
	t.Helper()
	wts, _, err := ListWorktrees(dir)
	if err != nil || len(wts) != 1 {
		t.Fatalf("ListWorktrees(%s) = %+v, %v; want one worktree", dir, wts, err)
	}

	return wts[0]
}

// The setups below damage a worktree in ways git never does.

func gitdirIsDir(t *testing.T, admin, _ string) {
	t.Helper()
	p := filepath.Join(admin, "gitdir")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o750); err != nil {
		t.Fatal(err)
	}
}

func gitdirTooLarge(t *testing.T, admin, _ string) {
	t.Helper()
	writeFile(t, admin, "gitdir", strings.Repeat("a", maxLinkFile+1))
}

// gitdirDevZero makes the gitdir file endless. The listing must refuse it,
// not hang reading it.
func gitdirDevZero(t *testing.T, admin, _ string) {
	t.Helper()
	if _, err := os.Stat("/dev/zero"); err != nil {
		t.Skip("no /dev/zero")
	}
	p := filepath.Join(admin, "gitdir")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", p); err != nil {
		t.Skip(err)
	}
}

func gitdirNotDotGit(t *testing.T, admin, wt string) {
	t.Helper()
	writeFile(t, admin, "gitdir", filepath.Join(wt, "a.txt")+"\n")
}

// dotGitElsewhere points the worktree's .git file at another repository's
// worktree admin dir, as a crafted gitdir file would.
func dotGitElsewhere(t *testing.T, _, wt string) {
	t.Helper()
	other, _, _ := oneWorktree(t)
	writeFile(t, wt, ".git", "gitdir: "+filepath.Join(other, ".git", "worktrees", "wt")+"\n")
}

// dotGitMissing points the worktree's .git file at a directory that does
// not exist: git's "not a git repository", without the path, and not the
// missing-worktree error.
func dotGitMissing(t *testing.T, _, wt string) {
	t.Helper()
	writeFile(t, wt, ".git", "gitdir: "+filepath.Join(t.TempDir(), "nowhere")+"\n")
}

func TestListWorktrees_Hostile(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, admin, wt string)
		want  string
	}{
		{"gitdir is a directory", gitdirIsDir, "invalid gitdir file: not a regular file"},
		{"gitdir too large", gitdirTooLarge, "invalid gitdir file: larger than"},
		{"gitdir links to /dev/zero", gitdirDevZero, "invalid gitdir file: not a regular file"},
		{"gitdir names no .git file", gitdirNotDotGit, "invalid gitdir file: does not name a .git file"},
		{"worktree .git leads to another repository", dotGitElsewhere, "worktree .git file names another git dir"},
		{"worktree .git names a missing directory", dotGitMissing, "invalid .git file: not a git repository"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, admin, wt := oneWorktree(t)
			c.setup(t, admin, wt)
			got := listOne(t, dir)
			if got.Err == nil || !strings.Contains(got.Err.Error(), c.want) {
				t.Fatalf("Err = %v, want %q", got.Err, c.want)
			}
			if strings.ContainsAny(got.Err.Error(), `/\`) {
				t.Errorf("Err names a path: %v", got.Err)
			}
		})
	}
}

// TestListWorktrees_RelativeGitdir rewrites the gitdir file relative to the
// admin dir, as worktree.useRelativePaths does; git 2.43 cannot write one.
func TestListWorktrees_RelativeGitdir(t *testing.T) {
	dir, admin, wt := oneWorktree(t)
	rel, err := filepath.Rel(admin, filepath.Join(wt, ".git"))
	if err != nil {
		t.Skip(err)
	}
	writeFile(t, admin, "gitdir", filepath.ToSlash(rel)+"\n")
	got := listOne(t, dir)
	if got.Err != nil {
		t.Fatalf("Err = %v", got.Err)
	}
	gi, err := os.Stat(got.Path)
	if err != nil {
		t.Fatal(err)
	}
	wi, err := os.Stat(wt)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gi, wi) {
		t.Errorf("Path = %q, want %q", got.Path, wt)
	}
}
