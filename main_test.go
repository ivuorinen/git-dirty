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
	"strings"
	"testing"

	"github.com/ivuorinen/git-dirty/internal/config"
)

var update = flag.Bool("update", false, "rewrite golden files")

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
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	gitconfig := "[init]\n\tdefaultBranch = main\n[commit]\n\tgpgsign = false\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitconfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func repoWithCommit(t *testing.T, dir string) {
	t.Helper()
	gitIn(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "a.txt")
	gitIn(t, dir, "commit", "-q", "-m", "init")
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
	if err := os.WriteFile(filepath.Join(beta, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gamma := filepath.Join(root, "group", "gamma")
	repoWithCommit(t, gamma)
	gitIn(t, gamma, "switch", "-q", "-c", "feature")
	gitIn(t, gamma, "commit", "-q", "--allow-empty", "-m", "feature work")
	gitIn(t, filepath.Join(root, "node_modules", "hidden"), "init", "-q")
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--plain", root}, nil, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected stderr: %q", errOut.String())
	}
	got := strings.ReplaceAll(out.String(), root, "<root>")
	got = regexp.MustCompile(`in [0-9.]+s\n$`).ReplaceAllString(got, "in <elapsed>\n")

	path := filepath.Join("testdata", "plain.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Errorf("output mismatch\n--- got\n%s\n--- want\n%s", got, want)
	}
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
			t.Errorf("useTUI(%v, %q, %v, %v) = %v, want %v", tt.interactive, tt.term, tt.stdinTTY, tt.stdoutTTY, got, tt.want)
		}
	}
}

func TestRunPlain_Interrupted(t *testing.T) {
	isolateEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errOut bytes.Buffer
	if code := run(ctx, []string{"--plain", t.TempDir()}, nil, &out, &errOut); code != exitSignal {
		t.Errorf("exit %d, want %d", code, exitSignal)
	}
	if out.Len() != 0 {
		t.Errorf("report printed after interrupt: %q", out.String())
	}
}

func TestExpandShortFlags(t *testing.T) {
	tests := []struct {
		in, want []string
	}{
		{[]string{"-vac"}, []string{"-v", "-a", "-c"}},
		{[]string{"-d3"}, []string{"-d", "3"}},
		{[]string{"-vd3"}, []string{"-v", "-d", "3"}},
		{[]string{"-e", "-x"}, []string{"-e", "-x"}},
		{[]string{"-ve", "-x"}, []string{"-v", "-e", "-x"}},
		{[]string{"-plain"}, []string{"-plain"}},
		{[]string{"--plain", "-version"}, []string{"--plain", "-version"}},
		{[]string{"-vz"}, []string{"-vz"}},
		{[]string{"--", "-vac"}, []string{"--", "-vac"}},
		{[]string{"dir", "-tb"}, []string{"dir", "-t", "-b"}},
	}
	for _, tt := range tests {
		if got := expandShortFlags(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("expandShortFlags(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseArgs(t *testing.T) {
	base := config.Defaults()
	tests := []struct {
		name  string
		args  []string
		check func(t *testing.T, o options)
	}{
		{"cluster -vac", []string{"-vac"}, func(t *testing.T, o options) {
			c := o.cfg
			if !c.Verbose || !c.CheckStash || !c.CheckUntracked || c.Color {
				t.Errorf("verbose=%v stash=%v untracked=%v color=%v", c.Verbose, c.CheckStash, c.CheckUntracked, c.Color)
			}
		}},
		{"attached value -d3", []string{"-d3"}, func(t *testing.T, o options) {
			if o.cfg.Depth != 3 {
				t.Errorf("depth=%d", o.cfg.Depth)
			}
		}},
		{"-m overrides config", []string{"-m", "4"}, func(t *testing.T, o options) {
			if o.cfg.MaxDepth != 4 {
				t.Errorf("maxDepth=%d", o.cfg.MaxDepth)
			}
		}},
		{"-e appends, repeatable", []string{"-e", "a,b", "-e", "c,,"}, func(t *testing.T, o options) {
			want := append(append([]string(nil), base.Exclude...), "a", "b", "c")
			if !reflect.DeepEqual(o.cfg.Exclude, want) {
				t.Errorf("exclude=%q", o.cfg.Exclude)
			}
		}},
		{"flags after the positional", []string{"/tmp/x", "-v"}, func(t *testing.T, o options) {
			if o.cfg.Dir != "/tmp/x" || !o.cfg.Verbose {
				t.Errorf("dir=%q verbose=%v", o.cfg.Dir, o.cfg.Verbose)
			}
		}},
		{"toggle twice restores", []string{"-cc", "-b"}, func(t *testing.T, o options) {
			if !o.cfg.Color || o.cfg.ShowBranch {
				t.Errorf("color=%v showBranch=%v", o.cfg.Color, o.cfg.ShowBranch)
			}
		}},
		{"-t toggles truncate", []string{"-t"}, func(t *testing.T, o options) {
			if o.cfg.Truncate {
				t.Error("truncate still on")
			}
		}},
		{"--plain and -p", []string{"--plain", "-p"}, func(t *testing.T, o options) {
			if o.cfg.Interactive {
				t.Error("interactive still on")
			}
		}},
		{"dash-prefixed dir after --", []string{"-v", "--", "-weird"}, func(t *testing.T, o options) {
			if o.cfg.Dir != "-weird" {
				t.Errorf("dir=%q", o.cfg.Dir)
			}
		}},
		{"-h", []string{"-h"}, func(t *testing.T, o options) {
			if !o.help {
				t.Error("help not set")
			}
		}},
		{"--version", []string{"--version"}, func(t *testing.T, o options) {
			if !o.version {
				t.Error("version not set")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, err := parseArgs(tt.args, base)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, o)
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
		{"-d"},
		{"-d", "abc"},
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
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		args       []string
		code       int
		wantStdout string
		wantStderr string
	}{
		{[]string{"-x"}, exitUsage, "", "-h' for usage"},
		{[]string{"-d"}, exitUsage, "", "flag needs an argument"},
		{[]string{"-d", "abc"}, exitUsage, "", "invalid value"},
		{[]string{"-h"}, exitOK, "Usage: git-dirty", ""},
		{[]string{"--version"}, exitOK, "dev", ""},
		{[]string{"-m", "0", t.TempDir()}, exitError, "", "maxDepth"},
		{[]string{"--plain", filepath.Join(t.TempDir(), "missing")}, exitError, "", "no such file"},
		{[]string{"--plain", file}, exitError, "", "not a directory"},
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
