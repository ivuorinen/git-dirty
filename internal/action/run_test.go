package action

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// No test compares a Kind's argv with a literal: each flag is proven by
// what git does with it.

func run(t *testing.T, dir string, k Kind) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	return Run(ctx, dir, k)
}

func mustRun(t *testing.T, dir string, k Kind) {
	t.Helper()
	if res := run(t, dir, k); res.Err != nil {
		t.Fatalf("%s: %v: %s", k, res.Err, res.Output)
	}
}

// addRemote gives clone c a second remote holding a commit c has not
// fetched, and returns a check that a fetch brought it in: without --all,
// git fetches only origin.
func addRemote(t *testing.T, c string) func() {
	t.Helper()
	other := newRepo(t)
	git(t, c, "remote", "add", "other", other)
	want := git(t, other, "rev-parse", "HEAD")

	return func() {
		t.Helper()
		if git(t, c, "for-each-ref", "--points-at", want, "refs/remotes/other") == "" {
			t.Errorf("no refs/remotes/other points at %s after the fetch", want)
		}
	}
}

func TestRun_Fetch(t *testing.T) {
	up := newRepo(t)
	c := clone(t, up)
	commitFile(t, up, "b.txt", "b\n", "upstream")
	fetchedOther := addRemote(t, c)
	mustRun(t, c, Fetch)
	if got := git(t, c, "rev-list", "--count", "HEAD..@{u}"); got != "1" {
		t.Errorf("commits behind after fetch = %s, want 1", got)
	}
	fetchedOther()
}

func TestRun_FetchPrune(t *testing.T) {
	up := newRepo(t)
	git(t, up, "branch", "x")
	c := clone(t, up)
	git(t, up, "branch", "-D", "x")
	// show-ref exits non-zero for a missing ref: 1 or 128, by git release.
	exists := func() bool {
		_, code := gitCode(t, c, "show-ref", "--verify", "--quiet", "refs/remotes/origin/x")

		return code == 0
	}
	mustRun(t, c, Fetch)
	if !exists() {
		t.Fatal("fetch without --prune deleted origin/x")
	}
	fetchedOther := addRemote(t, c)
	mustRun(t, c, FetchPrune)
	if exists() {
		t.Error("origin/x still exists after fetch --prune")
	}
	fetchedOther()
}

const tenLines = "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"

// autostashFixture returns a clone one commit behind, the upstream having
// changed line 1 of f.txt, with line 10 changed and uncommitted.
func autostashFixture(t *testing.T) string {
	t.Helper()
	up := newRepo(t)
	commitFile(t, up, "f.txt", tenLines, "ten lines")
	c := clone(t, up)
	commitFile(t, up, "f.txt", strings.Replace(tenLines, "1\n", "one\n", 1), "line 1")
	write(t, c, "f.txt", strings.Replace(tenLines, "10\n", "ten\n", 1))

	return c
}

func TestRun_PullAutostash(t *testing.T) {
	// The control: without --autostash, git refuses to pull over the
	// uncommitted change.
	if _, code := gitCode(t, autostashFixture(t), "pull", "--ff-only"); code == 0 {
		t.Fatal("git pull --ff-only succeeded over an uncommitted change to the same file")
	}
	c := autostashFixture(t)
	mustRun(t, c, Pull)
	if head, up := git(t, c, "rev-parse", "HEAD"), git(t, c, "rev-parse", "@{u}"); head != up {
		t.Errorf("HEAD %s is not the upstream %s after pull", head, up)
	}
	got, err := os.ReadFile(filepath.Clean(filepath.Join(c, "f.txt")))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.Replace(tenLines, "1\n", "one\n", 1), "10\n", "ten\n", 1)
	if string(got) != want {
		t.Errorf("f.txt after pull = %q, want both changes %q", got, want)
	}
}

// divergedFixture returns a clone with one commit of its own, its upstream
// having one other.
func divergedFixture(t *testing.T) string {
	t.Helper()
	up := newRepo(t)
	c := clone(t, up)
	commitFile(t, up, "up.txt", "up\n", "upstream")
	commitFile(t, c, "local.txt", "local\n", "local")
	git(t, c, "fetch", "-q")

	return c
}

func TestRun_PullDiverged(t *testing.T) {
	// The control: a pull that may merge does merge here.
	ctl := divergedFixture(t)
	git(t, ctl, "pull", "-q", "--no-rebase", "--no-edit")
	if git(t, ctl, "rev-list", "--merges", "HEAD") == "" {
		t.Fatal("git pull --no-rebase made no merge on a diverged branch")
	}
	c := divergedFixture(t)
	before := git(t, c, "rev-parse", "HEAD")
	if res := run(t, c, Pull); res.Err == nil {
		t.Errorf("pull succeeded on a diverged branch: %s", res.Output)
	}
	if after := git(t, c, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved from %s to %s", before, after)
	}
	if merges := git(t, c, "rev-list", "--merges", "HEAD"); merges != "" {
		t.Errorf("pull made a merge: %s", merges)
	}
}

func TestRun_PullRebase(t *testing.T) {
	c := divergedFixture(t)
	mustRun(t, c, PullRebase)
	if parent, up := git(t, c, "rev-parse", "HEAD~1"), git(t, c, "rev-parse", "@{u}"); parent != up {
		t.Errorf("HEAD~1 %s is not the upstream %s after pull --rebase", parent, up)
	}
	if merges := git(t, c, "rev-list", "--merges", "HEAD"); merges != "" {
		t.Errorf("pull --rebase made a merge: %s", merges)
	}
}

// TestRun_IgnoresExportedGitDir: an exported GIT_DIR would make git fetch
// into that repository instead of the working directory's.
func TestRun_IgnoresExportedGitDir(t *testing.T) {
	up := newRepo(t)
	c := clone(t, up)
	other := clone(t, up)
	commitFile(t, up, "b.txt", "b\n", "upstream")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	res := run(t, c, Fetch)
	if err := os.Unsetenv("GIT_DIR"); err != nil {
		t.Fatal(err)
	}
	if res.Err != nil {
		t.Fatalf("fetch: %v: %s", res.Err, res.Output)
	}
	if got := git(t, c, "rev-list", "--count", "HEAD..@{u}"); got != "1" {
		t.Errorf("target behind by %s after fetch, want 1", got)
	}
	if got := git(t, other, "rev-list", "--count", "HEAD..@{u}"); got != "0" {
		t.Errorf("the GIT_DIR repository was fetched into: behind by %s", got)
	}
}

// remoteRepo returns a repository whose origin is url.
func remoteRepo(t *testing.T, url string) string {
	t.Helper()
	dir := newRepo(t)
	git(t, dir, "remote", "add", "origin", url)

	return dir
}

// TestRun_NoPrompt: a remote asking for a password fails at once instead of
// waiting for an answer nobody can type.
func TestRun_NoPrompt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	dir := remoteRepo(t, srv.URL+"/repo.git")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := Run(ctx, dir, Fetch)
	if res.Err == nil {
		t.Fatalf("fetch from a 401 remote succeeded: %s", res.Output)
	}
	if ctx.Err() != nil {
		t.Errorf("fetch waited until the 10 s deadline: %s", res.Output)
	}
}

func TestRun_RespectsSafeBareRepository(t *testing.T) {
	up := newRepo(t)
	bare := filepath.Join(t.TempDir(), "bare.git")
	git(t, up, "clone", "-q", "--bare", up, bare)
	mustRun(t, bare, Fetch)
	git(t, bare, "fetch", "--all")

	globalConfig(t, "[safe]\n\tbareRepository = explicit\n")
	if _, code := gitCode(t, bare, "fetch", "--all"); code == 0 {
		t.Fatal("git fetch --all ran in a bare repository with safe.bareRepository=explicit")
	}
	if res := run(t, bare, Fetch); res.Err == nil {
		t.Errorf("fetch ran in a bare repository with safe.bareRepository=explicit: %s", res.Output)
	}
}

func TestRun_CancelStopsAStalledFetch(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer srv.Close()
	defer close(stop)
	dir := remoteRepo(t, srv.URL+"/repo.git")
	if runtime.GOOS == "windows" {
		// Windows kills git alone. The git fetch it runs for the remote, and
		// that fetch's remote helper, outlive it with the repository as their
		// working directory, which Windows does not let t.TempDir remove. The
		// deferred calls end the stall; this waits for them to exit.
		t.Cleanup(func() {
			for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
				if os.RemoveAll(dir) == nil {
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	res := Run(ctx, dir, Fetch)
	if res.Err == nil {
		t.Errorf("a canceled fetch succeeded: %s", res.Output)
	}
	if d := time.Since(start); d > waitDelay+2*time.Second {
		t.Errorf("Run returned %v after the cancel, want at most %v", d, waitDelay+2*time.Second)
	}
}

// TestRun_CancelKillsWhatIgnoresSIGINT: a process in git's group that
// ignores SIGINT, as a hook can, does not outlive a cancel. The fake
// upload-pack writes a heartbeat, because a killed process can stay a
// zombie that kill -0 still finds.
func TestRun_CancelKillsWhatIgnoresSIGINT(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no process groups or SIGINT to ignore")
	}
	dir := clone(t, newRepo(t))
	tmp := t.TempDir()
	beat := filepath.Join(tmp, "beat")
	script := filepath.Join(tmp, "upload-pack")
	body := "#!/bin/sh\ntrap '' INT\nexec 2>/dev/null\ni=0\n" +
		"while :; do i=$((i+1)); echo $i > '" + beat + "'; sleep 0.05; done\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o700); err != nil { //nolint:gosec // G302: the fake upload-pack must be executable
		t.Fatal(err)
	}
	git(t, dir, "config", "remote.origin.uploadpack", script)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if _, err := os.Stat(beat); err == nil {
				cancel()

				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	Run(ctx, dir, Fetch)
	first, err := os.ReadFile(filepath.Clean(beat))
	if err != nil {
		t.Fatalf("the fake upload-pack never ran: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	second, err := os.ReadFile(filepath.Clean(beat))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("the process that ignores SIGINT still runs: heartbeat went from %q to %q", first, second)
	}
}

func TestRun_GitNotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	if res := Run(context.Background(), dir, Fetch); !errors.Is(res.Err, exec.ErrNotFound) {
		t.Errorf("Run without git on PATH: %v, want exec.ErrNotFound", res.Err)
	}
}

// TestLocalEnv_CoversGit: localEnv holds every variable this git calls
// repository-local. The list grows between releases, so it is a superset
// check.
func TestLocalEnv_CoversGit(t *testing.T) {
	for _, name := range strings.Fields(git(t, t.TempDir(), "rev-parse", "--local-env-vars")) {
		if !slices.Contains(localEnv, name) {
			t.Errorf("localEnv lacks %s", name)
		}
	}
}

func TestEnviron(t *testing.T) {
	got := environ([]string{"PATH=/bin", "GIT_DIR=/x/.git", "GIT_TERMINAL_PROMPT=1", "GIT_AUTHOR_NAME=A"})
	want := []string{"PATH=/bin", "GIT_AUTHOR_NAME=A", "GIT_TERMINAL_PROMPT=0"}
	if !slices.Equal(got, want) {
		t.Errorf("environ = %q, want %q", got, want)
	}
}

func TestTail_KeepsTheEnd(t *testing.T) {
	var all bytes.Buffer
	tl := &tail{}
	for i := range 100 << 10 / 100 {
		chunk := bytes.Repeat([]byte{byte('a' + i%26)}, 100+i%7)
		all.Write(chunk)
		if n, err := tl.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	want := all.Bytes()[all.Len()-outputTail:]
	if !bytes.Equal(tl.buf, want) {
		t.Errorf("kept %d bytes, not the last %d written", len(tl.buf), outputTail)
	}
	big := bytes.Repeat([]byte("xyz"), outputTail)
	if _, err := tl.Write(big); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(tl.buf, big[len(big)-outputTail:]) {
		t.Error("one large write did not leave its last bytes")
	}
}

func TestResult_LastLine(t *testing.T) {
	r := Result{Output: "remote: counting\nfatal: not possible to fast-forward\n\n  \n"}
	if got, want := r.LastLine(), "fatal: not possible to fast-forward"; got != want {
		t.Errorf("LastLine = %q, want %q", got, want)
	}
	if got := (Result{}).LastLine(); got != "" {
		t.Errorf("LastLine of no output = %q", got)
	}
}
