package gitstat

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ignored applies the ignore stack the way the untracked walk does: a path is
// ignored when it, or any directory above it, matches.
func ignored(t *testing.T, r *Repo, ex *Excludes, rel string, isDir bool) bool {
	t.Helper()
	st, err := r.rootIgnores(ex)
	if err != nil {
		t.Fatal(err)
	}
	dirAbs := r.worktree
	st = st.push(dirAbs, nil)
	parts := strings.Split(rel, "/")
	for i := range parts {
		last := i == len(parts)-1
		if st.Ignored(parts[:i+1], !last || isDir) {
			return true
		}
		if !last {
			dirAbs = filepath.Join(dirAbs, parts[i])
			st = st.push(dirAbs, parts[:i+1])
		}
	}

	return false
}

// checkAgainstGit compares every path's verdict with `git check-ignore`.
func checkAgainstGit(t *testing.T, dir string, paths []string) {
	t.Helper()
	ex, err := LoadExcludes()
	if err != nil {
		t.Fatal(err)
	}
	r := open(t, dir)
	for _, p := range paths {
		isDir := strings.HasSuffix(p, "/")
		rel := strings.TrimSuffix(p, "/")
		// The paths need not exist, so git only knows a directory by its
		// trailing slash.
		_, code := gitCode(t, dir, "check-ignore", "-q", p)
		want := code == 0
		if got := ignored(t, r, ex, rel, isDir); got != want {
			t.Errorf("%s: ignored = %v, git check-ignore says %v", p, got, want)
		}
	}
}

func TestIgnore_GlobalExcludesFile(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, home string)
		paths []string
	}{
		{
			name: "XDG default ignore file",
			setup: func(t *testing.T, home string) {
				t.Helper()
				write(t, home, ".config/git/ignore", "*.log\n.ignore\n")
			},
			paths: []string{"x.log", "x.txt", ".ignore"},
		},
		{
			name: "core.excludesFile in ~/.gitconfig",
			setup: func(t *testing.T, home string) {
				t.Helper()
				write(t, home, ".gitconfig", baseGitconfig+"[core]\n\texcludesFile = ~/my-ignore\n")
				write(t, home, "my-ignore", "secret*\n")
			},
			paths: []string{"secret.txt", "public.txt"},
		},
		{
			name: "core.excludesFile in ~/.config/git/config",
			setup: func(t *testing.T, home string) {
				t.Helper()
				write(t, home, ".config/git/config", "[core]\n\texcludesFile = ~/cfg-ignore\n")
				write(t, home, "cfg-ignore", "*.tmp\n")
			},
			paths: []string{"a.tmp", "a.txt"},
		},
		{
			name: "an empty XDG_CONFIG_HOME means ~/.config",
			setup: func(t *testing.T, home string) {
				t.Helper()
				t.Setenv("XDG_CONFIG_HOME", "")
				write(t, home, ".config/git/ignore", "*.empty\n")
			},
			paths: []string{"a.empty", "kept.txt"},
		},
		{
			name: "~/.gitconfig wins over the XDG config",
			setup: func(t *testing.T, home string) {
				t.Helper()
				write(t, home, ".config/git/config", "[core]\n\texcludesFile = ~/xdg-ignore\n")
				write(t, home, "xdg-ignore", "*.xdg\n")
				write(t, home, ".gitconfig", baseGitconfig+"[core]\n\texcludesFile = ~/home-ignore\n")
				write(t, home, "home-ignore", "*.home\n")
			},
			paths: []string{"a.xdg", "a.home"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := isolatedHome(t)
			tt.setup(t, home)
			checkAgainstGit(t, newRepo(t), tt.paths)
		})
	}
}

// A relative core.excludesFile is read from each repository's worktree.
func TestIgnore_RelativeGlobalExcludesFile(t *testing.T) {
	home := isolatedHome(t)
	write(t, home, ".gitconfig", baseGitconfig+"[core]\n\texcludesFile = my-ignore\n")
	dir := newRepo(t)
	write(t, dir, "my-ignore", "*.rel\n")

	checkAgainstGit(t, dir, []string{"a.rel", "kept.txt"})
}

func TestIgnore_MalformedGlobalConfig(t *testing.T) {
	home := isolatedHome(t)
	write(t, home, ".gitconfig", "[bad\n")
	if _, err := LoadExcludes(); err == nil || !strings.Contains(err.Error(), "bad config line") {
		t.Errorf("err = %v, want the parse error", err)
	}
}

func TestIgnore_NoHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	if h, _ := os.UserHomeDir(); h != "" {
		t.Skipf("this platform has a home without HOME: %s", h)
	}
	ex, err := LoadExcludes()
	if err != nil || ex.path != "" {
		t.Errorf("Excludes = %+v, err = %v, want no excludes file", ex, err)
	}
}

func TestGlobalConfigFiles(t *testing.T) {
	const home, xdg, global = "/h", "/x", "/g"
	system := "/etc/gitconfig"
	xdgFile, homeFile := filepath.Join(xdg, "git", "config"), filepath.Join(home, ".gitconfig")
	for _, c := range []struct {
		name             string
		noSystem, global string
		home, xdg        string
		want             []string
	}{
		{"everything", "", "", home, xdg, []string{system, xdgFile, homeFile}},
		{"no system config", "1", "", home, xdg, []string{xdgFile, homeFile}},
		{"GIT_CONFIG_GLOBAL replaces the rest", "", global, home, xdg, []string{system, global}},
		{"no XDG directory", "1", "", home, "", []string{homeFile}},
		{"no home", "1", "", "", xdg, []string{xdgFile}},
	} {
		t.Setenv("GIT_CONFIG_NOSYSTEM", c.noSystem)
		t.Setenv("GIT_CONFIG_GLOBAL", c.global)
		if got := globalConfigFiles(c.home, c.xdg); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExpandHome(t *testing.T) {
	const home, sub = "/h", "~/x"
	for _, c := range []struct{ in, home, want string }{
		{"~", home, filepath.Join(home, "")},
		{sub, home, filepath.Join(home, "x")},
		{"~user/x", home, "~user/x"},
		{"x/~", home, "x/~"},
		{sub, "", sub},
	} {
		if got := expandHome(c.in, c.home); got != c.want {
			t.Errorf("expandHome(%q, %q) = %q, want %q", c.in, c.home, got, c.want)
		}
	}
}

func TestIgnore_RepoRules(t *testing.T) {
	home := isolatedHome(t)
	write(t, home, ".config/git/ignore", "*.log\n")
	dir := newRepo(t)
	write(t, dir, ".git/info/exclude", "local-only/\n")
	write(t, dir, ".gitignore", "build/\n/anchored\n*.o\n!keep.o\r\n# comment\n\n")
	write(t, dir, "sub/.gitignore", "*.md\n!README.md\n")
	write(t, dir, "deep/a/.gitignore", "!*.log\n")

	checkAgainstGit(t, dir, []string{
		"build/", "build/out.bin", "src/build/x", "build",
		"anchored", "sub/anchored",
		"a.o", "keep.o",
		"sub/x.md", "sub/README.md", "x.md",
		"local-only/f", "local-only/",
		"notes.txt", "x.log", "deep/a/x.log", "deep/x.log",
	})
}

func TestIgnore_RepoExcludesFileOverridesGlobal(t *testing.T) {
	home := isolatedHome(t)
	write(t, home, ".config/git/ignore", "*.log\n")
	dir := newRepo(t)
	git(t, dir, "config", "core.excludesFile", "repo-ignore")
	write(t, dir, "repo-ignore", "*.rep\n")

	checkAgainstGit(t, dir, []string{"a.rep", "a.log"})
}

func TestIgnore_AbsoluteRepoExcludesFile(t *testing.T) {
	isolatedHome(t)
	dir := newRepo(t)
	file := filepath.Join(t.TempDir(), "abs-ignore")
	write(t, filepath.Dir(file), filepath.Base(file), "*.abs\n")
	git(t, dir, "config", "core.excludesFile", file)

	checkAgainstGit(t, dir, []string{"a.abs", "other.txt"})
}

func TestIgnore_NoExcludesFile(t *testing.T) {
	st, err := open(t, newRepo(t)).rootIgnores(&Excludes{})
	if err != nil || len(st.Patterns) != 0 {
		t.Errorf("patterns = %v, err = %v, want none", st.Patterns, err)
	}
}

func TestIgnore_LoadsWithNoSystemConfig(t *testing.T) {
	// GIT_CONFIG_NOSYSTEM is set by TestMain; a missing system file must not
	// be an error either way.
	if os.Getenv("GIT_CONFIG_NOSYSTEM") == "" {
		t.Fatal("TestMain did not isolate the system config")
	}
	isolatedHome(t)
	if _, err := LoadExcludes(); err != nil {
		t.Fatal(err)
	}
}

// TestIgnore_GlobalConfigNullDevice: GIT_CONFIG_GLOBAL=/dev/null is git's
// documented way to skip the global config; it must not stop the scan.
func TestIgnore_GlobalConfigNullDevice(t *testing.T) {
	isolatedHome(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	if _, err := LoadExcludes(); err != nil {
		t.Fatalf("GIT_CONFIG_GLOBAL=%s: %v", os.DevNull, err)
	}
}
