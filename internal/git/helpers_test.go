package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain isolates the tests from the developer's git configuration; the
// fixtures are built and checked with the real git binary.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("skipping git package tests: git not on PATH")
		os.Exit(0)
	}
	home, err := os.MkdirTemp("", "git-home-")
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
	for _, k := range []string{"GIT_CONFIG_GLOBAL", "GIT_DIR", "GIT_WORK_TREE", "GIT_DEFAULT_HASH", "GIT_INDEX_FILE"} {
		if err := os.Unsetenv(k); err != nil {
			panic(err)
		}
	}
	gitconfig := "[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n" +
		"[tag]\n\tgpgsign = false\n[gc]\n\tauto = 0\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0o600); err != nil {
		panic(err)
	}
	code := m.Run()
	if err := os.RemoveAll(home); err != nil {
		fmt.Fprintln(os.Stderr, "removing the test home:", err)
	}
	os.Exit(code)
}

// formats lists the object formats every reader is tested with.
var formats = []Format{SHA1, SHA256}

// run runs git in dir and returns trimmed stdout; failure fails the test.
func run(tb testing.TB, dir string, args ...string) string {
	tb.Helper()
	out, code := runCode(tb, dir, nil, args...)
	if code != 0 {
		tb.Fatalf("git %s: exit %d: %s", strings.Join(args, " "), code, out)
	}

	return out
}

// runCode runs git with optional stdin and returns trimmed stdout and the
// exit code.
func runCode(tb testing.TB, dir string, stdin []byte, args ...string) (string, int) {
	tb.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: git is the test oracle; the tests choose its arguments
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return strings.TrimSpace(out.String() + errb.String()), ee.ExitCode()
		}
		tb.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}

	return strings.TrimSpace(out.String()), 0
}

func writeFile(tb testing.TB, dir, name, body string) {
	tb.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
}

func commit(tb testing.TB, dir, name, body, msg string) {
	tb.Helper()
	writeFile(tb, dir, name, body)
	run(tb, dir, "add", name)
	run(tb, dir, "commit", "-q", "-m", msg)
}

func openRepo(tb testing.TB, worktree string) *Repository {
	tb.Helper()
	r, err := OpenWorktree(worktree)
	if err != nil {
		tb.Fatalf("OpenWorktree(%s): %v", worktree, err)
	}
	tb.Cleanup(func() {
		if err := r.Close(); err != nil {
			tb.Errorf("Close(%s): %v", worktree, err)
		}
	})

	return r
}

// newRepo initializes an empty repository in the given object format.
func newRepo(tb testing.TB, f Format) string {
	tb.Helper()
	dir := tb.TempDir()
	run(tb, dir, "init", "-q", "--object-format="+f.Name)

	return dir
}
