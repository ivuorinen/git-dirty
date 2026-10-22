package git

import (
	"bytes"
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
		os.Setenv(k, v)
	}
	for _, k := range []string{"GIT_CONFIG_GLOBAL", "GIT_DIR", "GIT_WORK_TREE", "GIT_DEFAULT_HASH", "GIT_INDEX_FILE"} {
		os.Unsetenv(k)
	}
	gitconfig := "[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n[gc]\n\tauto = 0\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0o644); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// formats lists the object formats every reader is tested with.
var formats = []Format{SHA1, SHA256}

// run runs git in dir and returns trimmed stdout; failure fails the test.
func run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, code := runCode(t, dir, nil, args...)
	if code != 0 {
		t.Fatalf("git %s: exit %d: %s", strings.Join(args, " "), code, out)
	}
	return out
}

// runCode runs git with optional stdin and returns trimmed stdout and the
// exit code.
func runCode(t testing.TB, dir string, stdin []byte, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return strings.TrimSpace(out.String() + errb.String()), ee.ExitCode()
		}
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(out.String()), 0
}

func writeFile(t testing.TB, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t testing.TB, dir, name, body, msg string) {
	t.Helper()
	writeFile(t, dir, name, body)
	run(t, dir, "add", name)
	run(t, dir, "commit", "-q", "-m", msg)
}

func openRepo(t testing.TB, worktree string) *Repository {
	t.Helper()
	r, err := OpenWorktree(worktree)
	if err != nil {
		t.Fatalf("OpenWorktree(%s): %v", worktree, err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// newRepo initializes an empty repository in the given object format.
func newRepo(t testing.TB, f Format) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "--object-format="+f.Name)
	return dir
}
