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

// The bare repositories bareFixture makes.
const (
	plainGit  = "plain.git"
	projDir   = "proj"
	dotgitDir = "dotgit"
	lonelyGit = "lonely.git"
)

// bareFixture makes the plan's bare layouts in base: plain.git with the
// worktrees plain-main (beside it), inside (in it) and away (outside base);
// proj (a bare .bare and a .git file naming it) with proj/main; dotgit (a
// bare .git) with dotgit/main; lonely.git; and ignored.git, holding .ignore.
func bareFixture(t *testing.T) (base string) {
	t.Helper()
	seed := t.TempDir()
	mkrepo(t, seed, ".")
	gitIn(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	base = t.TempDir()
	at := func(rel string) string { return filepath.Join(base, filepath.FromSlash(rel)) }
	for _, rel := range []string{plainGit, projDir + "/.bare", dotgitDir + "/.git", lonelyGit, "ignored.git"} {
		gitIn(t, base, "clone", "-q", "--bare", seed, at(rel))
	}
	gitIn(t, at(plainGit), "worktree", "add", "-q", "--detach", at("plain-main"))
	gitIn(t, at(plainGit), "worktree", "add", "-q", "--detach", "inside")
	gitIn(t, at(plainGit), "worktree", "add", "-q", "--detach", filepath.Join(t.TempDir(), "away"))
	for name, body := range map[string]string{projDir + "/.git": "gitdir: ./.bare\n", "ignored.git/.ignore": ""} {
		if err := os.WriteFile(at(name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, at(projDir), "worktree", "add", "-q", "--detach", "main")
	gitIn(t, at(dotgitDir), "worktree", "add", "-q", "--detach", "main")

	return base
}

// underBare is what a scan of bareFixture's base finds: each bare
// repository with its worktrees under it, plain-main included although the
// walk finds it too, and inside although the walk does not enter plain.git.
var underBare = map[string]bool{
	plainGit: false, plainGit + "/plain-main": true, plainGit + "/inside": true,
	plainGit + "/away": true, projDir: false, projDir + "/main": true,
	dotgitDir: false, dotgitDir + "/main": true, lonelyGit: false,
}

func TestRun_BareRepositories(t *testing.T) {
	base := bareFixture(t)
	for _, c := range []struct {
		name    string
		root    string
		exclude string
		want    map[string]bool
	}{
		{"base", base, "", underBare},
		{"root is the bare repository", filepath.Join(base, plainGit), "", map[string]bool{
			"": false, "plain-main": true, "inside": true, "away": true,
		}},
		{"bare repository excluded", base, plainGit, map[string]bool{
			"plain-main": false, projDir: false, projDir + "/main": true,
			dotgitDir: false, dotgitDir + "/main": true, lonelyGit: false,
		}},
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

// TestRun_BareRepositoriesInspected: only the bare repositories are bare,
// the walk does not enter one, and .ignore in one skips it.
func TestRun_BareRepositoriesInspected(t *testing.T) {
	base := bareFixture(t)
	bare := []string{plainGit, projDir, dotgitDir, lonelyGit}
	for rel, it := range repoItems(t, baseOpts(base)) {
		if want := slices.Contains(bare, rel); it.Result.Bare != want {
			t.Errorf("%q: Bare = %v, want %v", rel, it.Result.Bare, want)
		}
	}
	c := collect(t, baseOpts(base))
	for _, d := range c.dirs {
		if strings.HasPrefix(d, plainGit+"/") {
			t.Errorf("the walk entered the bare repository: %q", d)
		}
	}
	if w := "ignored.git: skipped: contains .ignore"; !slices.Contains(c.warnings, w) {
		t.Errorf("warnings %q, want %q", c.warnings, w)
	}
}

// TestRun_BareWorktreesUnreadable: a bare repository whose worktrees/ cannot
// be listed carries the error, so its line does not show a count of none.
// A file in place of the directory fails the listing on every platform, as
// an unreadable directory would.
func TestRun_BareWorktreesUnreadable(t *testing.T) {
	seed := t.TempDir()
	mkrepo(t, seed, ".")
	gitIn(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	base := t.TempDir()
	plain := filepath.Join(base, plainGit)
	gitIn(t, base, "clone", "-q", "--bare", seed, plain)
	if err := os.WriteFile(filepath.Join(plain, "worktrees"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	it, ok := repoItems(t, baseOpts(base))[plainGit]
	switch {
	case !ok:
		t.Fatal("no item for the bare repository")
	case !it.Result.Bare || it.Result.Err == nil || !strings.HasPrefix(it.Result.Err.Error(), "worktrees: "):
		t.Errorf("Bare %v, Err %v; want bare with a worktrees: error", it.Result.Bare, it.Result.Err)
	}
}

// brokenWorktreesFixture makes a repository, main, with two linked worktrees
// whose .git files name a missing directory: a-before, which the walk reaches
// before main, and z-after, which it reaches after. orphan has the same .git
// file and no repository lists it.
func brokenWorktreesFixture(t *testing.T) (base string) {
	t.Helper()
	base = t.TempDir()
	mkrepo(t, base, "main")
	repo := filepath.Join(base, "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	for _, name := range []string{"a-before", "z-after"} {
		gitIn(t, repo, "worktree", "add", "-q", "--detach", filepath.Join(base, name))
	}
	mkdir(t, base, "orphan")
	broken := []byte("gitdir: " + filepath.Join(base, "nowhere") + "\n")
	for _, name := range []string{"a-before", "z-after", "orphan"} {
		if err := os.WriteFile(filepath.Join(base, name, ".git"), broken, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return base
}

// TestRun_BrokenWorktreeListedOnce: main still lists both broken worktrees,
// with an error, and the walk must not list their folders again, whether it
// reaches them before main or after it. orphan is still listed on its own.
func TestRun_BrokenWorktreeListedOnce(t *testing.T) {
	checkErrs(t, repoItems(t, baseOpts(brokenWorktreesFixture(t))), map[string]string{
		"main":          "",
		"main/a-before": "worktree: invalid .git file: not a git repository",
		"main/z-after":  "worktree: invalid .git file: not a git repository",
		"orphan":        "not a git repository",
	})
}

// TestRun_BrokenWorktreeWarnings: the "cannot list worktrees" warning is sent
// for orphan alone. A folder that main lists is shown under main with its
// error, and its own listing failure is not reported again.
func TestRun_BrokenWorktreeWarnings(t *testing.T) {
	var warned []string
	for it := range Run(context.Background(), baseOpts(brokenWorktreesFixture(t))) {
		if it.Kind == Warning {
			warned = append(warned, strings.Join(it.Rel, "/")+": "+it.Message)
		}
	}
	if len(warned) != 1 || !strings.HasPrefix(warned[0], "orphan: cannot list worktrees: ") {
		t.Errorf("warnings %q, want one for orphan", warned)
	}
}

// TestRun_OtherRepositorysWorktree: a's record for w is stale, because w was
// deleted without `git worktree prune` and is now a worktree of b, which is
// outside the scan. a lists w with the back-link error but must not claim it,
// so the walk still lists w on its own, as the valid worktree it is.
func TestRun_OtherRepositorysWorktree(t *testing.T) {
	base := t.TempDir()
	mkrepo(t, base, "a")
	repo := filepath.Join(base, "a")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	w := filepath.Join(base, "w")
	gitIn(t, repo, "worktree", "add", "-q", "--detach", w)
	if err := os.RemoveAll(w); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	mkrepo(t, other, "b")
	b := filepath.Join(other, "b")
	gitIn(t, b, "commit", "-q", "--allow-empty", "-m", "init")
	gitIn(t, b, "worktree", "add", "-q", "--detach", w)

	checkErrs(t, repoItems(t, baseOpts(base)), map[string]string{
		"a":   "",
		"a/w": "worktree: worktree .git file names another git dir",
		"w":   "",
	})
}

// checkErrs checks that got has an item at each rel in want and nowhere
// else, and that each item's error contains want[rel], or that it has none
// when want[rel] is "".
func checkErrs(t *testing.T, got map[string]Item, want map[string]string) {
	t.Helper()
	for rel, it := range got {
		w, ok := want[rel]
		switch {
		case !ok:
			t.Errorf("unexpected item at %q: %v", rel, it.Result.Err)
		case w == "" && it.Result.Err != nil:
			t.Errorf("%q: %v", rel, it.Result.Err)
		case w != "" && (it.Result.Err == nil || !strings.Contains(it.Result.Err.Error(), w)):
			t.Errorf("%q: Err = %v, want %q", rel, it.Result.Err, w)
		}
	}
	for rel := range want {
		if _, ok := got[rel]; !ok {
			t.Errorf("no item at %q", rel)
		}
	}
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
