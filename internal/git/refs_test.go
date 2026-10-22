package git

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// refRepo has main, a branch, a lightweight tag v1 and an annotated tag v2.
func refRepo(t *testing.T, f Format) string {
	t.Helper()
	dir := newRepo(t, f)
	commit(t, dir, "a.txt", "a\n", "one")
	run(t, dir, "tag", "v1")
	run(t, dir, "tag", "-a", "-m", "annotated", "v2")
	run(t, dir, "branch", "feature")

	return dir
}

// checkResolve compares Resolve with git rev-parse for each name.
func checkResolve(t *testing.T, dir string, names ...string) {
	t.Helper()
	r := openRepo(t, dir)
	for _, name := range names {
		got, err := r.Resolve(name)
		if err != nil {
			t.Errorf("Resolve(%s): %v", name, err)

			continue
		}
		if want := run(t, dir, "rev-parse", name); got.String() != want {
			t.Errorf("Resolve(%s) = %s, git rev-parse = %s", name, got, want)
		}
	}
}

// checkTags compares Tags with git show-ref --tags -d, peeled entries
// included.
func checkTags(t *testing.T, dir string) {
	t.Helper()
	tags, err := openRepo(t, dir).Tags()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tag := range tags {
		got = append(got, tag.ID.String()+" "+tag.Name)
		if !tag.Peeled.IsZero() {
			got = append(got, tag.Peeled.String()+" "+tag.Name+"^{}")
		}
	}
	want := strings.Split(run(t, dir, "show-ref", "--tags", "-d"), "\n")
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Tags:\n%s\ngit show-ref --tags -d:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRefs_MatchGit(t *testing.T) {
	names := []string{headRef, refMain, "refs/heads/feature", "refs/tags/v1", "refs/tags/v2"}
	for _, f := range formats {
		t.Run(f.Name, func(t *testing.T) {
			t.Run("loose", func(t *testing.T) {
				dir := refRepo(t, f)
				checkResolve(t, dir, names...)
				head, err := openRepo(t, dir).ReadRef(headRef)
				if err != nil || head.Target != run(t, dir, "symbolic-ref", headRef) {
					t.Errorf("HEAD = %+v, %v", head, err)
				}
			})
			t.Run("packed", func(t *testing.T) {
				dir := refRepo(t, f)
				run(t, dir, "pack-refs", "--all")
				for _, loose := range []string{refMain, "refs/tags/v2"} {
					if _, err := os.Stat(filepath.Join(dir, ".git", loose)); err == nil {
						t.Fatalf("fixture: %s still loose after pack-refs", loose)
					}
				}
				checkResolve(t, dir, names...)
				checkTags(t, dir)
			})
			t.Run("loose overrides packed", func(t *testing.T) {
				dir := refRepo(t, f)
				run(t, dir, "pack-refs", "--all")
				commit(t, dir, "b.txt", "b\n", "two")
				run(t, dir, "tag", "-f", "v1")
				checkResolve(t, dir, names...)
				checkTags(t, dir)
			})
			t.Run("symbolic chain", func(t *testing.T) {
				dir := refRepo(t, f)
				run(t, dir, "symbolic-ref", "refs/heads/alias", "refs/heads/feature")
				run(t, dir, "symbolic-ref", headRef, "refs/heads/alias")
				checkResolve(t, dir, headRef, "refs/heads/alias")
			})
		})
	}
}

func TestRefs_Errors(t *testing.T) {
	dir := refRepo(t, SHA1)
	writeFile(t, dir, ".git/refs/heads/loop", "ref: refs/heads/loop\n")
	writeFile(t, dir, ".git/refs/heads/garbage", "not a hash\n")
	r := openRepo(t, dir)

	if _, err := r.Resolve("refs/heads/loop"); err == nil || !strings.Contains(err.Error(), "deeper") {
		t.Errorf("self-referencing symref: err = %v", err)
	}
	if _, code := runCode(t, dir, nil, "rev-parse", "--verify", "-q", "refs/heads/loop"); code == 0 {
		t.Error("fixture: git resolves the loop")
	}
	if _, err := r.Resolve("refs/heads/garbage"); err == nil {
		t.Error("garbage ref content: want error")
	}
	if _, err := r.Resolve("refs/heads/missing"); !errors.Is(err, ErrRefNotFound) {
		t.Errorf("missing ref: err = %v, want ErrRefNotFound", err)
	}
	// A directory where a ref name points (refs/heads/dir/x exists) is an
	// absent ref, not an error.
	run(t, dir, "branch", "dir/x")
	if _, err := openRepo(t, dir).Resolve("refs/heads/dir"); !errors.Is(err, ErrRefNotFound) {
		t.Errorf("ref name that is a directory: err = %v", err)
	}
}

func TestValidRefName(t *testing.T) {
	valid := []string{headRef, refMain, "refs/heads/feat/x-1_2", "refs/remotes/origin/HEAD", "refs/tags/v1.0"}
	invalid := []string{
		"", "main", "refs/", "refs/heads/../../config", "refs/heads/a\x00b", "refs/heads/a b",
		"refs//x", "/refs/heads/x", "refs/heads/x/", "refs/heads/.hidden", "refs/heads/x.lock",
		"refs/heads/x.", "refs/heads/a@{1}", "refs/heads/a~1", "refs/heads/a^", "refs/heads/a:b",
		"refs/heads/a?", "refs/heads/a*", "refs/heads/a[", "refs/heads/a\\b", "refs/heads/a\x7f",
	}
	for _, n := range valid {
		if !ValidRefName(n) {
			t.Errorf("ValidRefName(%q) = false", n)
		}
	}
	for _, n := range invalid {
		if ValidRefName(n) {
			t.Errorf("ValidRefName(%q) = true", n)
		}
	}
	r := openRepo(t, refRepo(t, SHA1))
	if _, err := r.ReadRef("refs/heads/../../config"); err == nil || errors.Is(err, ErrRefNotFound) {
		t.Errorf("traversal name: err = %v, want an invalid-name error", err)
	}
}

func TestOpen_FormatsAndWorktrees(t *testing.T) {
	for _, f := range formats {
		dir := refRepo(t, f)
		r := openRepo(t, dir)
		if r.Format.Name != f.Name {
			t.Errorf("format = %s, want %s", r.Format.Name, f.Name)
		}
		wt := filepath.Join(t.TempDir(), "wt")
		run(t, dir, "worktree", "add", "-q", "-b", "side", wt)
		commit(t, wt, "w.txt", "w\n", "in worktree")
		w := openRepo(t, wt)
		if w.CommonDir == w.GitDir {
			t.Error("worktree commondir not followed")
		}
		checkResolve(t, wt, headRef, refMain, "refs/heads/side")
	}
}
