package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ivuorinen/git-dirty/internal/config"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Flags named by several tests.
const (
	flagPlain = "--plain"
	flagDepth = "-d"
)

// isolateEnv clears every setting git-dirty and git read from the
// environment, so the developer's own configuration cannot change results.
func isolateEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{
		"GIT_DIRTY_DIR", "GIT_DIRTY_DEPTH", "GIT_DIRTY_MAXDEPTH", "GIT_DIRTY_CHECK_STASH",
		"GIT_DIRTY_CHECK_UNTRACKED", "GIT_DIRTY_EXCLUDE", "GIT_DIRTY_COLOR", "GIT_DIRTY_TRUNCATE",
		"GIT_DIRTY_SHOW_BRANCH", "GIT_DIRTY_MAIN_BRANCHES", "VERBOSE", "NO_COLOR", "GIT_CONFIG_GLOBAL",
	} {
		t.Setenv(k, "") // restores the original value after the test
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	gitconfig := "[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0o600); err != nil {
		t.Fatal(err)
	}

	return home
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", args...) //nolint:gosec // G204: tests run git as the oracle (CLAUDE.md)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func repoWithCommit(t *testing.T, dir string) {
	t.Helper()
	gitIn(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "a.txt")
	gitIn(t, dir, "commit", "-q", "-m", "init")
}

// golden compares got with testdata/name, rewriting the file first with
// -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Errorf("output mismatch\n--- got\n%s\n--- want\n%s", got, want)
	}
}

// TestRunPlain_Golden runs the whole program against a fixture tree, as
// `git-dirty --plain <dir> | cat` would, with the timing masked.
func TestRunPlain_Golden(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	isolateEnv(t)
	root := t.TempDir()
	repoWithCommit(t, filepath.Join(root, "alpha"))
	beta := filepath.Join(root, "beta")
	repoWithCommit(t, beta)
	if err := os.WriteFile(filepath.Join(beta, "a.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gamma := filepath.Join(root, "group", "gamma")
	repoWithCommit(t, gamma)
	gitIn(t, gamma, "switch", "-q", "-c", "feature")
	gitIn(t, gamma, "commit", "-q", "--allow-empty", "-m", "feature work")
	gitIn(t, filepath.Join(root, "node_modules", "hidden"), "init", "-q")
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o750); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{flagPlain, root}, nil, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected stderr: %q", errOut.String())
	}
	got := strings.ReplaceAll(out.String(), root, "<root>")
	got = regexp.MustCompile(`in [0-9.]+s\n$`).ReplaceAllString(got, "in <elapsed>\n")
	golden(t, "plain.golden", got)
}

func TestUseTUI(t *testing.T) {
	tests := []struct {
		interactive         bool
		term                string
		stdinTTY, stdoutTTY bool
		want                bool
	}{
		{true, "xterm-256color", true, true, true},
		{false, "xterm-256color", true, true, false}, // --plain or config
		{true, "dumb", true, true, false},
		{true, "xterm", true, false, false}, // piped: git-dirty | cat
		{true, "xterm", false, true, false}, // input redirected
		{true, "", true, true, true},
	}
	for _, tt := range tests {
		if got := useTUI(tt.interactive, tt.term, tt.stdinTTY, tt.stdoutTTY); got != tt.want {
			t.Errorf(
				"useTUI(%v, %q, %v, %v) = %v, want %v",
				tt.interactive,
				tt.term,
				tt.stdinTTY,
				tt.stdoutTTY,
				got,
				tt.want,
			)
		}
	}
}

func TestRunPlain_Interrupted(t *testing.T) {
	isolateEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if code := run(ctx, []string{flagPlain, t.TempDir()}, nil, &out, &errOut); code != exitSignal {
		t.Errorf("exit %d, want %d", code, exitSignal)
	}
	if out.Len() != 0 {
		t.Errorf("report printed after interrupt: %q", out.String())
	}
}

func TestExpandShortFlags(t *testing.T) {
	tests := []struct {
		in, want string // arguments separated by spaces
	}{
		{"-vac", "-v -a -c"},
		{"-d3", "-d 3"},
		{"-vd3", "-v -d 3"},
		{"-e -x", "-e -x"},
		{"-ve -x", "-v -e -x"},
		{"-plain", "-plain"},
		{"--plain -version", "--plain -version"},
		{"-vz", "-vz"},
		{"-- -vac", "-- -vac"},
		{"dir -tb", "dir -t -b"},
	}
	for _, tt := range tests {
		in, want := strings.Fields(tt.in), strings.Fields(tt.want)
		if got := expandShortFlags(in); !reflect.DeepEqual(got, want) {
			t.Errorf("expandShortFlags(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseArgs compares the whole result, so a test also fails when a flag
// changes a setting it should not touch.
func TestParseArgs(t *testing.T) {
	base := config.Defaults()
	tests := []struct {
		name string
		args string         // arguments separated by spaces
		want func(*options) // applied to the result of parsing no arguments
	}{
		{"cluster -vac", "-vac", func(o *options) {
			o.cfg.Verbose, o.cfg.CheckStash, o.cfg.CheckUntracked, o.cfg.Color = true, true, true, false
		}},
		{"attached value -d3", "-d3", func(o *options) { o.cfg.Depth = 3 }},
		{"-m overrides config", "-m 4", func(o *options) { o.cfg.MaxDepth = 4 }},
		{"-e appends, repeatable", "-e a,b -e c,,", func(o *options) {
			o.cfg.Exclude = append(o.cfg.Exclude, "a", "b", "c")
		}},
		{"flags after the positional", "/tmp/x -v", func(o *options) { o.cfg.Dir, o.cfg.Verbose = "/tmp/x", true }},
		{"toggle twice restores", "-cc -b", func(o *options) { o.cfg.ShowBranch = false }},
		{"-t toggles truncate", "-t", func(o *options) { o.cfg.Truncate = false }},
		{"--plain and -p", "--plain -p", func(o *options) { o.cfg.Interactive = false }},
		{"dash-prefixed dir after --", "-v -- -weird", func(o *options) { o.cfg.Dir, o.cfg.Verbose = "-weird", true }},
		{"help", "-h", func(o *options) { o.help = true }},
		{"version", "--version", func(o *options) { o.version = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(strings.Fields(tt.args), base)
			if err != nil {
				t.Fatal(err)
			}
			want := options{cfg: base}
			want.cfg.Exclude = slices.Clone(base.Exclude)
			tt.want(&want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestParseArgs_DoesNotMutateCallerExclude(t *testing.T) {
	base := config.Defaults()
	base.Exclude = make([]string, 1, 10) // spare capacity would expose aliasing
	base.Exclude[0] = "x"
	if _, err := parseArgs([]string{"-e", "y"}, base); err != nil {
		t.Fatal(err)
	}
	if got := base.Exclude[:2][1]; got != "" {
		t.Errorf("caller's backing array was written: %q", got)
	}
}

func TestParseArgs_Errors(t *testing.T) {
	for _, args := range [][]string{
		{"-x"},
		{flagDepth},
		{flagDepth, "abc"},
		{"a", "b"},
		{"--nope"},
	} {
		if _, err := parseArgs(args, config.Defaults()); err == nil {
			t.Errorf("parseArgs(%q): want error", args)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	isolateEnv(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		args       []string
		code       int
		wantStdout string
		wantStderr string
	}{
		{[]string{"-x"}, exitUsage, "", "-h' for usage"},
		{[]string{flagDepth}, exitUsage, "", "flag needs an argument"},
		{[]string{flagDepth, "abc"}, exitUsage, "", "invalid value"},
		{[]string{"-h"}, exitOK, "Usage: git-dirty", ""},
		{[]string{"--version"}, exitOK, "dev", ""},
		{[]string{"-m", "0", t.TempDir()}, exitError, "", "maxDepth"},
		{[]string{flagPlain, filepath.Join(t.TempDir(), "missing")}, exitError, "", "no such file"},
		{[]string{flagPlain, file}, exitError, "", "not a directory"},
	}
	for _, tt := range tests {
		var out, errOut bytes.Buffer
		code := run(context.Background(), tt.args, nil, &out, &errOut)
		if code != tt.code {
			t.Errorf("run(%q) = %d, want %d (stderr %q)", tt.args, code, tt.code, errOut.String())
		}
		if !strings.Contains(out.String(), tt.wantStdout) {
			t.Errorf("run(%q) stdout = %q, want %q", tt.args, out.String(), tt.wantStdout)
		}
		if !strings.Contains(errOut.String(), tt.wantStderr) {
			t.Errorf("run(%q) stderr = %q, want %q", tt.args, errOut.String(), tt.wantStderr)
		}
	}
}
