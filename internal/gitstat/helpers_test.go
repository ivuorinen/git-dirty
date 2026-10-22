package gitstat

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
)

// TestMain isolates every test from the developer's git configuration: git
// and this package both read HOME, XDG_CONFIG_HOME and the system config, and
// any of them could change a verdict.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("skipping gitstat tests: git not on PATH")
		os.Exit(0)
	}
	home, err := os.MkdirTemp("", "gitstat-home-")
	if err != nil {
		panic(err)
	}
	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   "",
		"GIT_AUTHOR_NAME":     "Test",
		"GIT_AUTHOR_EMAIL":    "test@example.invalid",
		"GIT_COMMITTER_NAME":  "Test",
		"GIT_COMMITTER_EMAIL": "test@example.invalid",
		"GIT_TERMINAL_PROMPT": "0",
	} {
		os.Setenv(k, v)
	}
	os.Unsetenv("GIT_CONFIG_GLOBAL")
	os.Unsetenv("GIT_DIR")
	os.Unsetenv("GIT_WORK_TREE")
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(baseGitconfig), 0o644); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

const baseGitconfig = "[init]\n\tdefaultBranch = main\n[protocol \"file\"]\n\tallow = always\n[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n"

// isolatedHome gives one test its own HOME and XDG_CONFIG_HOME, for tests
// that write global git configuration.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	write(t, home, ".gitconfig", baseGitconfig)
	return home
}

// forEachFormat runs body once for SHA-1 and once for SHA-256 repositories:
// GIT_DEFAULT_HASH sets the object format of every repository git init or
// git clone creates in the subtest.
func forEachFormat(t *testing.T, body func(t *testing.T)) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			t.Setenv("GIT_DEFAULT_HASH", format)
			probe := t.TempDir()
			git(t, probe, "init", "-q")
			if got := git(t, probe, "rev-parse", "--show-object-format"); got != format {
				t.Fatalf("GIT_DEFAULT_HASH=%s created a %s repository", format, got)
			}
			body(t)
		})
	}
}

// git runs git in dir and returns trimmed stdout; a non-zero exit fails the test.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, code := gitCode(t, dir, args...)
	if code != 0 {
		t.Fatalf("git %s: exit %d: %s", strings.Join(args, " "), code, out)
	}
	return out
}

// gitCode runs git in dir and returns trimmed combined output and exit code.
func gitCode(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return strings.TrimSpace(buf.String()), ee.ExitCode()
		}
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(buf.String()), 0
}

// commitAt commits an empty change with fixed author and committer dates, to
// build histories whose dates disagree with their topology.
func commitAt(t *testing.T, dir, msg string, at time.Time) {
	t.Helper()
	cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-m", msg)
	cmd.Dir = dir
	stamp := at.Format(time.RFC3339)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
}

// newRepo creates an empty repository with one committed file.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "a.txt", "a\n")
	git(t, dir, "add", "a.txt")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitFile(t *testing.T, dir, name, body, msg string) {
	t.Helper()
	write(t, dir, name, body)
	git(t, dir, "add", name)
	git(t, dir, "commit", "-q", "-m", msg)
}

func hashOf(t *testing.T, dir, rev string) gitrepo.ObjectID {
	t.Helper()
	hex := git(t, dir, "rev-parse", rev+"^{commit}")
	f := gitrepo.SHA1
	if len(hex) == 2*gitrepo.SHA256.Size {
		f = gitrepo.SHA256
	}
	id, err := f.ParseHex(hex)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func open(t *testing.T, dir string) *Repo {
	t.Helper()
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}
