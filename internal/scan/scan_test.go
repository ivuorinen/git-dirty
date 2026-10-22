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
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// mkrepo makes an initialized repository at root/rel (no commits needed).
func mkrepo(t *testing.T, root, rel string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

func mkdir(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
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
	if err := os.WriteFile(filepath.Join(root, "ignored", ".ignore"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mkrepo(t, root, "has-ignore-file")
	if err := os.WriteFile(filepath.Join(root, "has-ignore-file", ".ignore"), nil, 0o644); err != nil {
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
	// Let discovery fill the buffer and block on a send before cancelling.
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
