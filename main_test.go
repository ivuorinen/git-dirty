package main

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/ivuorinen/git-dirty/internal/gitstat"
	"github.com/ivuorinen/git-dirty/internal/scan"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Flags named by several tests.
const (
	flagPlain  = "--plain"
	flagDepth  = "-d"
	flagHelp   = "-h"
	flagConfig = "-config"

	flagMaxDepth = "-m"
	flagLikeFile = "-d3" // a config file name that looks like flags
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
	alpha := filepath.Join(root, "alpha")
	repoWithCommit(t, alpha)
	// Found by the walk too, but listed once: under alpha, with 🌳.
	gitIn(t, alpha, "worktree", "add", "-q", "-b", "side", filepath.Join(root, "alpha-side"))
	// A bare clone with a worktree the walk also finds: listed once, under it.
	gitIn(t, root, "clone", "-q", "--bare", alpha, filepath.Join(root, "delta.git"))
	gitIn(t, filepath.Join(root, "delta.git"), "worktree", "add", "-q", "-b", "delta-side",
		filepath.Join(root, "delta-wt"))
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

// The terminal counter counts what the summary counts: not worktrees.
func TestProgress_CountsRepositoriesOnly(t *testing.T) {
	var buf bytes.Buffer
	p := progress{w: &buf, counter: true}
	p.report(scan.Item{Kind: scan.Repo, Path: "r"})
	p.report(scan.Item{Kind: scan.Repo, Path: "wt", Worktree: true})
	if want := "\rFound 1 git repositories..."; !strings.HasSuffix(buf.String(), want) {
		t.Errorf("counter output %q, want it to end in %q", buf.String(), want)
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
		{flagConfig + " x", flagConfig + " x"},
		{flagConfig + " -d3 -vac", flagConfig + " -d3 -v -a -c"},
		{"--config -d3 -vac", "--config -d3 -v -a -c"},
		{"-config=x -c", "-config=x -c"},
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
		{"help", flagHelp, func(o *options) { o.help = true }},
		{"version", "--version", func(o *options) { o.version = true }},
		{"-config sets the path only", flagConfig + " x", func(o *options) { o.configPath = "x" }},
		{"--config=", "--config=x", func(o *options) { o.configPath = "x" }},
		{
			"-config after the positional",
			"/tmp/x -config y",
			func(o *options) { o.cfg.Dir, o.configPath = "/tmp/x", "y" },
		},
		{"-c still toggles color", "-c", func(o *options) { o.cfg.Color = false }},
		{"-config after -- is the directory", "-- -config", func(o *options) { o.cfg.Dir = flagConfig }},
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

// TestRun_ConfigFlag: every spelling of -config, before or after the
// directory, loads that file instead of the default one, and -h names it.
func TestRun_ConfigFlag(t *testing.T) {
	home := isolateEnv(t)
	writeFile := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join(home, ".config", "git-dirty", "config.json"), `{"maxDepth": 7}`)
	custom := filepath.Join(t.TempDir(), "custom.json")
	writeFile(custom, `{"maxDepth": 3}`)
	for _, args := range [][]string{
		{flagConfig, custom, flagHelp},
		{"--config", custom, flagHelp},
		{"-config=" + custom, flagHelp},
		{t.TempDir(), flagConfig, custom, flagHelp},
	} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), args, nil, &out, &errOut); code != exitOK {
			t.Fatalf("run(%q) = %d: %s", args, code, errOut.String())
		}
		if !strings.Contains(out.String(), "(default: 3)") || !strings.Contains(out.String(), custom) {
			t.Errorf("run(%q): help does not show the custom file's maxDepth 3 and its path:\n%s", args, out.String())
		}
	}
	var out bytes.Buffer
	if code := run(context.Background(), []string{flagHelp}, nil, &out, &bytes.Buffer{}); code != exitOK ||
		!strings.Contains(out.String(), "(default: 7)") {
		t.Errorf("without -config the default file is not read:\n%s", out.String())
	}
}

// TestSession_TUIConfig: the settings page's Reconfigure applies the
// environment and the command line over the edited file layer, as startup
// does, and refuses a missing directory.
func TestSession_TUIConfig(t *testing.T) {
	isolateEnv(t)
	t.Setenv("GIT_DIRTY_CHECK_STASH", "1")
	file := config.Defaults()
	file.Dir = t.TempDir()
	file.MaxDepth = 9
	s := session{args: []string{flagMaxDepth, "4"}, path: "/x/config.json"}
	tc, err := s.tuiConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Scan.MaxDepth != 4 {
		t.Errorf("maxDepth = %d, want the flag's 4 over the file's 9", tc.Scan.MaxDepth)
	}
	eff, err := tc.Overlay(tc.File)
	if err != nil {
		t.Fatal(err)
	}
	if !eff.CheckStash || tc.File.CheckStash {
		t.Errorf("checkStash: effective %v, file %v; want the environment's true over the file's false",
			eff.CheckStash, tc.File.CheckStash)
	}
	if tc.ConfigPath != s.path || tc.Reconfigure == nil {
		t.Error("the config path or Reconfigure is not carried")
	}
	file.Dir = filepath.Join(file.Dir, "missing")
	if _, err := s.tuiConfig(file); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("a missing dir: err = %v", err)
	}
}

// TestSession_ReconfigureChecksTheFileLayer: a flag does not hide an invalid
// value from a save, but startup with the same flag still runs, and a file
// dir that a directory argument replaces need not exist.
func TestSession_ReconfigureChecksTheFileLayer(t *testing.T) {
	isolateEnv(t)
	file := config.Defaults()
	file.Dir = t.TempDir()
	file.MaxDepth = 0
	s := session{args: []string{flagMaxDepth, "4"}}
	tc, err := s.tuiConfig(file)
	if err != nil {
		t.Fatalf("startup with -m 4 over maxDepth 0: %v", err)
	}
	if _, err := tc.Reconfigure(file); err == nil || !strings.Contains(err.Error(), "maxDepth") {
		t.Errorf("saving maxDepth 0 under -m 4: err = %v, want a maxDepth error", err)
	}
	file.MaxDepth = 9
	file.Dir = filepath.Join(file.Dir, "missing")
	if _, err := (session{args: []string{t.TempDir()}}).reconfigure(file); err != nil {
		t.Errorf("a missing file dir under a directory argument: %v", err)
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
		{[]string{flagConfig}, exitUsage, "", "flag needs an argument"},
		{[]string{flagConfig, flagLikeFile, flagHelp}, exitOK, "Configuration: " + flagLikeFile, ""},
		{[]string{"--config", flagLikeFile, flagHelp}, exitOK, "Configuration: " + flagLikeFile, ""},
		{[]string{flagHelp}, exitOK, "Usage: git-dirty", ""},
		{[]string{"--version"}, exitOK, "dev", ""},
		{[]string{flagMaxDepth, "0", t.TempDir()}, exitError, "", "maxDepth"},
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

// failingWriter accepts nothing.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestRun_MalformedConfigFile(t *testing.T) {
	isolateEnv(t)
	bad := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	code := run(context.Background(), []string{flagConfig, bad}, nil, &bytes.Buffer{}, &errOut)
	if code != exitError || !strings.Contains(errOut.String(), bad) {
		t.Errorf("code %d, stderr %q, want the error naming the file", code, errOut.String())
	}
}

func TestRun_MalformedGlobalGitConfig(t *testing.T) {
	home := isolateEnv(t)
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	code := run(context.Background(), []string{flagPlain, t.TempDir()}, nil, &bytes.Buffer{}, &errOut)
	if code != exitError || !strings.Contains(errOut.String(), "git ignore configuration") {
		t.Errorf("code %d, stderr %q, want the git configuration error", code, errOut.String())
	}
}

func TestRun_NoWayToFindTheConfigFile(t *testing.T) {
	isolateEnv(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if h, err := os.UserHomeDir(); err == nil {
		t.Skipf("this platform has a home without HOME: %s", h)
	}
	if code := run(context.Background(), []string{flagHelp}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != exitError {
		t.Errorf("code %d, want %d", code, exitError)
	}
}

func TestRun_ReportCannotBeWritten(t *testing.T) {
	isolateEnv(t)
	var errOut bytes.Buffer
	code := run(context.Background(), []string{flagPlain, t.TempDir()}, nil, failingWriter{}, &errOut)
	if code != exitError || !strings.Contains(errOut.String(), "disk full") {
		t.Errorf("code %d, stderr %q, want the write error", code, errOut.String())
	}
}

func TestRunTUI_FailsBeforeTheBrowser(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	bad, good := filepath.Join(dir, "bad.json"), filepath.Join(dir, "good.json")
	for name, body := range map[string]string{bad: "{", good: "{}"} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, s := range map[string]session{
		"malformed config file":   {path: bad},
		"bad flag at reconfigure": {path: good, args: []string{flagDepth, "x"}},
		"root is missing":         {path: good, args: []string{filepath.Join(dir, "missing")}},
		"bad value":               {path: good, args: []string{flagMaxDepth, "0"}},
	} {
		var errOut bytes.Buffer
		if code := runTUI(context.Background(), s, nil, &bytes.Buffer{}, &errOut); code != exitError {
			t.Errorf("%s: code %d, want %d", name, code, exitError)
		}
		if errOut.Len() == 0 {
			t.Errorf("%s: nothing on stderr", name)
		}
	}
}

func TestSession_Overlay_BadEnvironment(t *testing.T) {
	isolateEnv(t)
	t.Setenv("GIT_DIRTY_DEPTH", "x")
	if _, err := (session{}).overlay(config.Defaults()); err == nil {
		t.Error("a bad variable: want error")
	}
}

func TestCheckDir_Unreadable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkDir(filepath.Join(file, "below")); err == nil {
		t.Error("a path below a file: want error")
	}
}

func TestProgress_Report(t *testing.T) {
	failed := scan.Item{Kind: scan.Repo, Path: "bad", Result: gitstat.Result{Err: errors.New("index: broken")}}
	warning := scan.Item{Kind: scan.Warning, Path: "skipped", Message: "skipped: excluded name"}
	clean := scan.Item{Kind: scan.Repo, Path: "ok"}
	const erase = "\r\x1b[K"
	for _, c := range []struct {
		name             string
		verbose, counter bool
		it               scan.Item
		want             string
	}{
		{"a warning, quiet", false, false, warning, ""},
		{"a warning, verbose", true, false, warning, "git-dirty: skipped: skipped: excluded name\n"},
		{"a warning clears the counter", true, true, warning, erase + "git-dirty: skipped: skipped: excluded name\n"},
		{"a failed repository, quiet", false, false, failed, ""},
		{"a failed repository, verbose", true, false, failed, "git-dirty: bad: index: broken\n"},
		{"a clean repository, verbose", true, false, clean, ""},
	} {
		var b bytes.Buffer
		p := progress{w: &b, verbose: c.verbose, counter: c.counter}
		p.report(c.it)
		if b.String() != c.want {
			t.Errorf("%s: %q, want %q", c.name, b.String(), c.want)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }() // read back by no one
	if isTerminal(&bytes.Buffer{}) || isTerminal(file) {
		t.Error("a buffer or a regular file counted as a terminal")
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = null.Close() }() // read-only
	if !isTerminal(null) {
		t.Errorf("%s is a character device", os.DevNull)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if isTerminal(file) {
		t.Error("a closed file counted as a terminal")
	}
}

func TestOnOff(t *testing.T) {
	if onOff(true) != "on" || onOff(false) != "off" {
		t.Errorf("onOff: %q, %q", onOff(true), onOff(false))
	}
}

func TestRun_NoColorWritesNoEscapes(t *testing.T) {
	isolateEnv(t)
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	if code := run(context.Background(), []string{flagPlain, t.TempDir()}, nil, &out, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("code %d", code)
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("escape codes with NO_COLOR: %q", out.String())
	}
}

// A signal before the browser has drawn anything still ends the run as one.
func TestRunTUI_Canceled(t *testing.T) {
	isolateEnv(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "config.json")
	if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ex, err := gitstat.LoadExcludes()
	if err != nil {
		t.Fatal(err)
	}
	s := session{path: file, args: []string{dir}, ex: ex}
	if code := runTUI(ctx, s, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); code != exitSignal {
		t.Errorf("code %d, want %d", code, exitSignal)
	}
}
