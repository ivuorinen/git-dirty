package action

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain isolates every test from the developer's git configuration:
// GIT_CONFIG_GLOBAL names a test file and GIT_CONFIG_NOSYSTEM drops the
// system file, so no setting of the machine running the tests (pull.rebase,
// a credential helper, safe.bareRepository) can change a verdict.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("skipping action tests: git not on PATH")
		os.Exit(0)
	}
	home, err := os.MkdirTemp("", "action-home-")
	if err != nil {
		panic(err)
	}
	if err := isolateGitEnv(home); err != nil {
		_ = os.RemoveAll(home)
		panic(err)
	}
	code := m.Run()
	if err := os.RemoveAll(home); err != nil {
		fmt.Fprintf(os.Stderr, "removing %s: %v\n", home, err)
	}
	os.Exit(code)
}

// mainBranch is the initial branch of every repository the tests create.
const mainBranch = "main"

const baseGitconfig = "[init]\n\tdefaultBranch = " + mainBranch + "\n" +
	"[protocol \"file\"]\n\tallow = always\n" +
	"[commit]\n\tgpgsign = false\n"

func isolateGitEnv(home string) error {
	global := filepath.Join(home, "gitconfig")
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL":   global,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "Test",
		"GIT_AUTHOR_EMAIL":    "test@example.invalid",
		"GIT_COMMITTER_NAME":  "Test",
		"GIT_COMMITTER_EMAIL": "test@example.invalid",
		"GIT_TERMINAL_PROMPT": "0",
	} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	// The repository-local variables, and the askpass programs an editor's
	// terminal exports, which would answer a test's credential prompt.
	unset := append([]string{"GIT_ASKPASS", "SSH_ASKPASS"}, localEnv...)
	for _, k := range unset {
		if err := os.Unsetenv(k); err != nil {
			return err
		}
	}

	return os.WriteFile(global, []byte(baseGitconfig), 0o600)
}

// globalConfig points GIT_CONFIG_GLOBAL at a file holding the base
// configuration plus extra, for one test.
func globalConfig(t *testing.T, extra string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(p, []byte(baseGitconfig+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", p)
}

// git runs git in dir and returns trimmed stdout; a non-zero exit fails the
// test.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, code := gitCode(t, dir, args...)
	if code != 0 {
		t.Fatalf("git %s: exit %d: %s", strings.Join(args, " "), code, out)
	}

	return out
}

// gitCode runs git in dir and returns trimmed combined output and the exit
// code.
func gitCode(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: tests run git as their oracle, with fixture arguments
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return strings.TrimSpace(buf.String()), ee.ExitCode()
		}
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}

	return strings.TrimSpace(buf.String()), 0
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitFile(t *testing.T, dir, name, body, msg string) {
	t.Helper()
	write(t, dir, name, body)
	git(t, dir, "add", name)
	git(t, dir, "commit", "-q", "-m", msg)
}

// newRepo creates a repository with one committed file.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	commitFile(t, dir, "a.txt", "a\n", "init")

	return dir
}

// clone clones src into a new directory, which tracks src as origin.
func clone(t *testing.T, src string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, src, "clone", "-q", src, dir)

	return dir
}
