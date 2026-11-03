package git

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// gitSaysGitDir is git's answer: rev-parse --git-dir prints "." only when
// dir itself is a git directory. The ceiling stops git looking above dir.
func gitSaysGitDir(t *testing.T, dir string) bool {
	t.Helper()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	out, code := runCode(t, dir, nil, "-c", "safe.bareRepository=all", "rev-parse", "--git-dir")

	return code == 0 && out == "."
}

// bareRepo initializes an empty bare repository in the given format.
func bareRepo(t *testing.T, f Format) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "b.git")
	run(t, filepath.Dir(dir), "init", "-q", "--bare", "--object-format="+f.Name, dir)

	return dir
}

// bareClone clones a repository with one commit as a bare repository. It
// returns the clone and the commit's ID.
func bareClone(t *testing.T, f Format) (dir, head string) {
	t.Helper()
	src := newRepo(t, f)
	commit(t, src, "a.txt", "a\n", "init")
	dir = filepath.Join(t.TempDir(), "c.git")
	run(t, src, "clone", "-q", "--bare", src, dir)

	return dir, run(t, src, "rev-parse", "HEAD")
}

// bareLayouts builds the plan's bare layouts in a fresh root: plain.git with
// the worktrees plain-main (beside it) and inside (in it); proj (a bare .bare
// and a .git file naming it) with the worktree proj/main; dotgit (a bare
// .git) with the worktree dotgit/main; lonely.git; and nobare.git, a git
// directory whose config says core.bare = false.
func bareLayouts(t *testing.T, f Format) string {
	t.Helper()
	seed := newRepo(t, f)
	commit(t, seed, "a.txt", "a\n", "init")
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	for _, rel := range []string{"plain.git", "proj/.bare", "dotgit/.git", "lonely.git"} {
		run(t, root, "clone", "-q", "--bare", seed, at(rel))
	}
	run(t, at("plain.git"), "worktree", "add", "-q", "--detach", at("plain-main"))
	run(t, at("plain.git"), "worktree", "add", "-q", "--detach", "inside")
	writeFile(t, root, "proj/.git", "gitdir: ./.bare\n")
	run(t, at("proj"), "worktree", "add", "-q", "--detach", "main")
	run(t, at("dotgit"), "worktree", "add", "-q", "--detach", "main")
	run(t, root, "init", "-q", "--bare", "--object-format="+f.Name, at("nobare.git"))
	run(t, at("nobare.git"), "config", "core.bare", "false")
	// git sparse-checkout in a worktree turns on extensions.worktreeConfig
	// and moves core.bare from sparse/.bare/config to config.worktree.
	run(t, root, "clone", "-q", "--bare", seed, at("sparse/.bare"))
	writeFile(t, root, "sparse/.git", "gitdir: ./.bare\n")
	run(t, at("sparse"), "worktree", "add", "-q", "--detach", "main")
	run(t, at("sparse/main"), "sparse-checkout", "set", "foo")

	return root
}

// gitSaysBare is git's answer to whether path has no working tree: there,
// rev-parse --is-inside-work-tree prints false and git status fails.
func gitSaysBare(t *testing.T, path string) bool {
	t.Helper()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(path))
	out, code := runCode(t, path, nil, "rev-parse", "--is-inside-work-tree")

	return code == 0 && out == "false"
}

func TestRepositoryBare(t *testing.T) {
	for _, f := range formats {
		t.Run(f.Name, func(t *testing.T) {
			root := bareLayouts(t, f)
			for _, c := range []struct {
				name string
				path string
				want bool
			}{
				{"plain.git", filepath.Join(root, "plain.git"), true},
				{"lonely.git", filepath.Join(root, "lonely.git"), true},
				{"core.bare false", filepath.Join(root, "nobare.git"), true},
				{"bare .bare named by a .git file", filepath.Join(root, "proj"), true},
				{"bare .git folder", filepath.Join(root, "dotgit"), true},
				{"working tree", newRepo(t, f), false},
				{"worktree beside a bare repository", filepath.Join(root, "plain-main"), false},
				{"worktree inside a .bare container", filepath.Join(root, "proj", "main"), false},
				{"core.bare in config.worktree", filepath.Join(root, "sparse"), true},
				{"worktree with extensions.worktreeConfig", filepath.Join(root, "sparse", "main"), false},
			} {
				t.Run(c.name, func(t *testing.T) {
					if got := gitSaysBare(t, c.path); got != c.want {
						t.Fatalf("git says bare %v, want %v: the fixture is not what the case claims", got, c.want)
					}
					if got := openRepo(t, c.path).Bare; got != c.want {
						t.Errorf("Bare = %v, want %v", got, c.want)
					}
				})
			}
		})
	}
}

// bareWithout is an empty bare repository with its sub folder removed.
func bareWithout(t *testing.T, f Format, sub string) string {
	t.Helper()
	dir := bareRepo(t, f)
	if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
		t.Fatal(err)
	}

	return dir
}

// symlinkHEAD replaces dir/HEAD with a symlink to target. A symlinked HEAD
// is a Unix layout: on Windows, os.Symlink rewrites the target's slashes.
func symlinkHEAD(t *testing.T, dir, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("os.Symlink on Windows rewrites / to \\ in the target")
	}
	head := filepath.Join(dir, "HEAD")
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, head); err != nil {
		t.Skip(err)
	}
}

func TestIsGitDir(t *testing.T) {
	for _, f := range formats {
		t.Run(f.Name, func(t *testing.T) {
			for _, c := range []struct {
				name  string
				build func(t *testing.T) string
				want  bool
			}{
				{"init --bare", func(t *testing.T) string {
					t.Helper()

					return bareRepo(t, f)
				}, true},
				{"clone --bare", func(t *testing.T) string {
					t.Helper()
					dir, _ := bareClone(t, f)

					return dir
				}, true},
				{"working tree .git folder", func(t *testing.T) string {
					t.Helper()

					return filepath.Join(newRepo(t, f), ".git")
				}, true},
				{"detached HEAD", func(t *testing.T) string {
					t.Helper()
					dir, head := bareClone(t, f)
					run(t, dir, "update-ref", "--no-deref", "HEAD", head)

					return dir
				}, true},
				{"HEAD symlink", func(t *testing.T) string {
					t.Helper()
					dir := bareRepo(t, f)
					symlinkHEAD(t, dir, "refs/heads/main")

					return dir
				}, true},
				// git reads 255 bytes of HEAD; the rest is never read.
				{"HEAD over 255 bytes", func(t *testing.T) string {
					t.Helper()
					dir := bareRepo(t, f)
					writeFile(t, dir, "HEAD", "ref: refs/heads/main\n"+strings.Repeat("x", 1<<20))

					return dir
				}, true},
				{"working tree root", func(t *testing.T) string {
					t.Helper()

					return newRepo(t, f)
				}, false},
				{"empty folder", func(t *testing.T) string {
					t.Helper()

					return t.TempDir()
				}, false},
				{"only HEAD", func(t *testing.T) string {
					t.Helper()
					dir := t.TempDir()
					writeFile(t, dir, "HEAD", "ref: refs/heads/main\n")

					return dir
				}, false},
				{"garbage HEAD", func(t *testing.T) string {
					t.Helper()
					dir := bareRepo(t, f)
					writeFile(t, dir, "HEAD", "garbage\n")

					return dir
				}, false},
				{"HEAD symlink outside refs/", func(t *testing.T) string {
					t.Helper()
					dir := bareRepo(t, f)
					symlinkHEAD(t, dir, "../x")

					return dir
				}, false},
				{"HEAD and refs without objects", func(t *testing.T) string {
					t.Helper()

					return bareWithout(t, f, "objects")
				}, false},
				{"HEAD and objects without refs", func(t *testing.T) string {
					t.Helper()

					return bareWithout(t, f, "refs")
				}, false},
				{"ref: not under refs/", func(t *testing.T) string {
					t.Helper()
					dir := bareRepo(t, f)
					writeFile(t, dir, "HEAD", "ref: HEAD\n")

					return dir
				}, false},
				{"ref: and a tab", func(t *testing.T) string {
					t.Helper()
					dir := bareRepo(t, f)
					writeFile(t, dir, "HEAD", "ref:\trefs/heads/main\n")

					return dir
				}, true},
				{"39 hex digits", func(t *testing.T) string {
					t.Helper()
					dir := bareRepo(t, f)
					writeFile(t, dir, "HEAD", strings.Repeat("a", 39)+"g\n")

					return dir
				}, false},
			} {
				t.Run(c.name, func(t *testing.T) {
					dir := c.build(t)
					if got := gitSaysGitDir(t, dir); got != c.want {
						t.Fatalf("git says %v, want %v: the fixture is not what the case claims", got, c.want)
					}
					if got := IsGitDir(dir); got != c.want {
						t.Errorf("IsGitDir(%s) = %v, want %v", dir, got, c.want)
					}
				})
			}
		})
	}
}

// TestIsGitDir_BoundedHEAD: IsGitDir reads at most maxHeadRef bytes of HEAD
// however large the file is, as git's validate_headref does. The 64 MiB
// HEAD is sparse, so it costs no disk; reading it whole would allocate at
// least that much.
func TestIsGitDir_BoundedHEAD(t *testing.T) {
	dir := bareRepo(t, SHA1)
	writeFile(t, dir, "HEAD", "ref: refs/heads/main\n")
	if err := os.Truncate(filepath.Join(dir, "HEAD"), 64<<20); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	ok := IsGitDir(dir)
	runtime.ReadMemStats(&after)
	if !ok {
		t.Fatal("IsGitDir = false for a HEAD that starts with a valid ref")
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Errorf("IsGitDir allocated %d bytes for a 64 MiB HEAD; it reads at most %d", n, maxHeadRef)
	}
}

// TestIsGitDir_WorktreeAdminDir: git accepts a linked worktree's admin dir,
// whose objects and refs it finds through the commondir file. IsGitDir does
// not follow commondir, so the admin dir is not a repository to list; the
// walk never reaches one anyway, because it stops at the repository holding
// it.
func TestIsGitDir_WorktreeAdminDir(t *testing.T) {
	dir := worktreeRepo(t)
	addWorktree(t, dir, filepath.Join(t.TempDir(), "wt"))
	admin := filepath.Join(dir, ".git", "worktrees", "wt")
	if !gitSaysGitDir(t, admin) {
		t.Fatal("git does not take the admin dir for a git directory")
	}
	if IsGitDir(admin) {
		t.Error("IsGitDir(admin dir) = true, want false: commondir is not followed")
	}
}

// unsearchableDir returns a path inside a directory that cannot be searched
// (mode 0), so os.Stat on it fails with a permission error instead of finding
// it missing. Windows does not apply the mode to directories and root ignores
// it, so the test is skipped wherever that Stat is not refused.
func unsearchableDir(t *testing.T) string {
	t.Helper()
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0); err != nil {
		t.Fatal(err)
	}
	// No chmod back for cleanup: rmdir of an empty directory needs only the
	// parent's permissions, so TempDir's RemoveAll removes it as it is.
	p := filepath.Join(locked, "gitdir")
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrPermission) {
		t.Skipf("mode 0 does not stop this user searching a directory: %v", err)
	}

	return p
}

// A .git file naming a directory in a parent that cannot be searched fails
// with the permission error, not "not a git repository": the directory may
// exist, and calling it missing would misname the cause.
func TestOpenWorktree_UnsearchableGitDir(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, wt, ".git", "gitdir: "+unsearchableDir(t)+"\n")
	_, err := OpenWorktree(wt)
	if !errors.Is(err, fs.ErrPermission) || errors.Is(err, errNotGitRepo) {
		t.Errorf("OpenWorktree: %v; want a permission error, not %q", err, errNotGitRepo)
	}
}

// gitNotARepo reports whether out is git's error for a .git file at path that
// names target, a directory that is not a git directory. git up to 2.55 says
// "not a git repository: <target>"; 2.56 reworded it to "gitfile does not
// point to a valid repository: <path>/.git", with path as git resolved it
// (symlinks and Windows short names expanded), so only its last element is
// compared.
func gitNotARepo(out, path, target string) bool {
	return strings.Contains(out, "not a git repository: "+target) ||
		(strings.Contains(out, "gitfile does not point to a valid repository: ") &&
			strings.Contains(out, filepath.Base(path)+"/.git"))
}

// A .git file naming a git directory that does not exist is git's "not a
// git repository" error, whichever wording the installed git uses. Open would
// otherwise read the missing commondir and config as absent, and the first
// failure would be HEAD, misnaming the cause. Cases: a worktree moved after
// `git worktree prune` deleted its admin dir, a .git file naming a path that
// never existed, and one naming a path under a regular file (ENOTDIR).
func TestOpenWorktree_MissingGitDir(t *testing.T) {
	dir := worktreeRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	addWorktree(t, dir, wt)
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(wt, moved); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "worktree", "prune")
	never := t.TempDir()
	writeFile(t, never, ".git", "gitdir: "+filepath.Join(never, "nowhere")+"\n")
	underFile := t.TempDir()
	writeFile(t, underFile, "file", "")
	writeFile(t, underFile, ".git", "gitdir: "+filepath.Join(underFile, "file", "nowhere")+"\n")

	for _, path := range []string{moved, never, underFile} {
		target, err := readLinkFile(filepath.Join(path, ".git"), "gitdir:")
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(path))
		out, code := runCode(t, path, nil, "rev-parse", "--git-dir")
		if code == 0 || !gitNotARepo(out, path, target) {
			t.Fatalf("git rev-parse: exit %d, %q; want git's error for a .git file naming %s", code, out, target)
		}
		want := "not a git repository: " + filepath.Clean(target)
		if _, err := OpenWorktree(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("OpenWorktree(%s): %v; want %q", filepath.Base(path), err, want)
		}
	}
}
