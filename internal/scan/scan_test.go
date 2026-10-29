package scan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ivuorinen/git-dirty/internal/gitstat"
)

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("skipping scan tests: git not on PATH")
		os.Exit(0)
	}
	home, err := os.MkdirTemp("", "scan-home-")
	if err != nil {
		panic(err)
	}
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Test",
		"GIT_AUTHOR_EMAIL":    "test@example.invalid",
		"GIT_COMMITTER_NAME":  "Test",
		"GIT_COMMITTER_EMAIL": "test@example.invalid",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	if err := os.RemoveAll(home); err != nil {
		fmt.Println("removing the test home:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// gitIn runs git in dir; failure fails the test.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	args = append([]string{"-C", dir}, args...)
	//nolint:gosec // G204: tests run git as the oracle (CLAUDE.md)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// mkrepo makes an initialized repository at root/rel (no commits needed).
func mkrepo(t *testing.T, root, rel string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q")
}

func mkdir(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o750); err != nil {
		t.Fatal(err)
	}
}

type collected struct {
	repos, dirs, warnings []string
}

func collect(t *testing.T, opts Options) collected {
	t.Helper()
	var c collected
	for it := range Run(context.Background(), opts) {
		rel := strings.Join(it.Rel, "/")
		switch it.Kind {
		case Repo:
			if it.Result.Err != nil {
				t.Errorf("%s: %v", rel, it.Result.Err)
			}
			c.repos = append(c.repos, rel)
		case Dir:
			c.dirs = append(c.dirs, rel)
		case Warning:
			c.warnings = append(c.warnings, rel+": "+it.Message)
		}
	}
	slices.Sort(c.repos)
	slices.Sort(c.dirs)
	slices.Sort(c.warnings)

	return c
}

func baseOpts(root string) Options {
	return Options{
		Root:     root,
		MaxDepth: 15,
		Exclude:  []string{"node_modules", "vendor"},
		Inspect:  gitstat.Options{MainBranches: []string{"main"}},
	}
}

func TestRun_FindsReposAndSkips(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, root, "a")
	mkrepo(t, root, "group/b")
	mkrepo(t, root, "group/deeper/c")
	mkrepo(t, root, "a/nested-inside-repo") // not reported: discovery stops at a
	mkrepo(t, root, "node_modules/excluded")
	mkrepo(t, root, "custom/excluded-too")
	mkrepo(t, root, "ignored/repo")
	if err := os.WriteFile(filepath.Join(root, "ignored", ".ignore"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mkrepo(t, root, "has-ignore-file")
	if err := os.WriteFile(filepath.Join(root, "has-ignore-file", ".ignore"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mkdir(t, root, "empty/dir")
	if runtime.GOOS != "windows" {
		target := t.TempDir()
		mkrepo(t, target, "via-link")
		if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
	}

	opts := baseOpts(root)
	opts.Exclude = append(opts.Exclude, "custom")
	c := collect(t, opts)

	if want := []string{"a", "group/b", "group/deeper/c"}; !slices.Equal(c.repos, want) {
		t.Errorf("repos = %q, want %q", c.repos, want)
	}
	if want := []string{"", "empty", "empty/dir", "group", "group/deeper"}; !slices.Equal(c.dirs, want) {
		t.Errorf("dirs = %q, want %q", c.dirs, want)
	}
	want := []string{
		"custom: skipped: excluded name",
		"has-ignore-file: skipped: contains .ignore",
		"ignored: skipped: contains .ignore",
		"node_modules: skipped: excluded name",
	}
	if !slices.Equal(c.warnings, want) {
		t.Errorf("warnings = %q, want %q", c.warnings, want)
	}
}

func TestRun_MaxDepth(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, root, "one/two")
	mkrepo(t, root, "one/two-b/three")
	opts := baseOpts(root)
	opts.MaxDepth = 2
	c := collect(t, opts)
	if want := []string{"one/two"}; !slices.Equal(c.repos, want) {
		t.Errorf("repos = %q, want %q", c.repos, want)
	}
	if want := []string{"one/two-b/three: skipped: maximum depth reached"}; !slices.Equal(c.warnings, want) {
		t.Errorf("warnings = %q, want %q", c.warnings, want)
	}
}

func TestRun_RootIsRepo(t *testing.T) {
	root := t.TempDir()
	mkrepo(t, root, ".")
	c := collect(t, baseOpts(root))
	if want := []string{""}; !slices.Equal(c.repos, want) || len(c.dirs) != 0 {
		t.Errorf("repos = %q dirs = %q, want the root as the only repo", c.repos, c.dirs)
	}
}

// worktreesFixture makes base/repo with linked worktrees beside it
// (sibling), nested in it, in an excluded directory (node_modules/inex), in
// a directory of their own (only/wt2), outside base (away) and deleted
// (stale).
func worktreesFixture(t *testing.T) (base, repo string) {
	t.Helper()
	base = t.TempDir()
	repo = filepath.Join(base, "repo")
	mkrepo(t, repo, ".")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	stale := filepath.Join(t.TempDir(), "stale")
	for _, p := range []string{
		filepath.Join(base, "sibling"),
		filepath.Join(repo, ".worktrees", "nested"),
		filepath.Join(base, "node_modules", "inex"),
		filepath.Join(base, "only", "wt2"),
		filepath.Join(t.TempDir(), "away"),
		stale,
	} {
		gitIn(t, repo, "worktree", "add", "-q", "--detach", p)
	}
	if err := os.RemoveAll(stale); err != nil {
		t.Fatal(err)
	}

	return base, repo
}

// underRepo is what a scan of the fixture's base finds: every worktree
// under the repository, sibling and only/wt2 included although the walk
// finds them too.
var underRepo = map[string]bool{
	"repo": false, "repo/sibling": true, "repo/nested": true, "repo/inex": true,
	"repo/wt2": true, "repo/away": true, "repo/stale": true,
}

// repoItems runs a scan and returns its repository items by Rel.
func repoItems(t *testing.T, opts Options) map[string]Item {
	t.Helper()
	got := map[string]Item{}
	for it := range Run(context.Background(), opts) {
		if it.Kind == Repo {
			got[strings.Join(it.Rel, "/")] = it
		}
	}

	return got
}

// checkRepos compares the repository items with want, which maps each Rel to
// whether it is a worktree listed under its repository. A Rel ending in
// "stale" must carry git's error for a deleted worktree; others none.
func checkRepos(t *testing.T, got map[string]Item, want map[string]bool) {
	t.Helper()
	for rel, it := range got {
		wt, ok := want[rel]
		switch {
		case !ok:
			t.Errorf("unexpected item at %q", rel)
		case it.Worktree != wt:
			t.Errorf("%q: Worktree = %v, want %v", rel, it.Worktree, wt)
		case strings.HasSuffix(rel, "stale"):
			if it.Result.Err == nil || !strings.Contains(it.Result.Err.Error(),
				"worktree: gitdir file points to non-existent location") {
				t.Errorf("%q: Err = %v, want the deleted-worktree error", rel, it.Result.Err)
			}
		case it.Result.Err != nil:
			t.Errorf("%q: %v", rel, it.Result.Err)
		}
	}
	for rel := range want {
		if _, ok := got[rel]; !ok {
			t.Errorf("no item at %q", rel)
		}
	}
}

func TestRun_Worktrees(t *testing.T) {
	base, repo := worktreesFixture(t)
	for _, c := range []struct {
		name    string
		root    string
		exclude string
		want    map[string]bool
	}{
		{"base", base, "", underRepo},
		{"repository excluded", base, filepath.Base(repo), map[string]bool{"sibling": false, "only/wt2": false}},
		{"root is the repository", repo, "", map[string]bool{
			"": false, "sibling": true, "nested": true, "inex": true, "wt2": true, "away": true, "stale": true,
		}},
		{"root is a linked worktree", filepath.Join(base, "sibling"), "", map[string]bool{"": false}},
		{"repository outside the root", filepath.Join(base, "only"), "", map[string]bool{"wt2": false}},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := baseOpts(c.root)
			if c.exclude != "" {
				opts.Exclude = append(opts.Exclude, c.exclude)
			}
			checkRepos(t, repoItems(t, opts), c.want)
		})
	}
}

// TestRun_WorktreesSymlinkedRoot reaches the fixture through a symlink. Git
// records worktree paths with symlinks resolved, so the walk's path to
// sibling differs from the listed one; it must still be listed once. macOS
// meets this without the symlink: t.TempDir() is below /var, a symlink to
// /private/var.
func TestRun_WorktreesSymlinkedRoot(t *testing.T) {
	base, _ := worktreesFixture(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(base, link); err != nil {
		t.Skip(err)
	}
	checkRepos(t, repoItems(t, baseOpts(link)), underRepo)
}

func TestRun_CancelClosesPromptly(t *testing.T) {
	root := t.TempDir()
	for i := range 40 {
		mkrepo(t, root, fmt.Sprintf("g%02d/r", i))
		for j := range 20 {
			mkdir(t, root, fmt.Sprintf("g%02d/d%02d/x", i, j))
		}
	}
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	opts := baseOpts(root)
	opts.Workers = 4
	ch := Run(ctx, opts)
	<-ch // the scan is running
	cancel()

	deadline := time.After(time.Second)
	for open := true; open; {
		select {
		case _, open = <-ch:
		case <-deadline:
			t.Fatal("result channel not closed within 1s of cancel")
		}
	}
	// Goroutines exit just after closing the channel; allow them a moment.
	for range 100 {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutines = %d, baseline %d: scan goroutines leaked", runtime.NumGoroutine(), baseline)
}

func TestRun_ConsumerStopsReadingAfterCancel(t *testing.T) {
	// More items than the channel buffers hold, so a sender that ignored
	// cancellation would block forever once the consumer stops.
	root := t.TempDir()
	for i := range 300 {
		mkdir(t, root, fmt.Sprintf("d%03d", i))
	}
	mkrepo(t, root, "r")
	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	ch := Run(ctx, baseOpts(root))
	<-ch
	// Let discovery fill the buffer and block on a send before canceling.
	time.Sleep(200 * time.Millisecond)
	cancel() // and never read again: senders must not block forever
	for range 100 {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutines = %d, baseline %d: senders blocked after cancel", runtime.NumGoroutine(), baseline)
}
