package gitstat

import (
	"os"
	"path/filepath"
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
	if st, err = st.push(dirAbs, nil); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(rel, "/")
	for i := range parts {
		last := i == len(parts)-1
		if st.match(parts[:i+1], !last || isDir) {
			return true
		}
		if !last {
			dirAbs = filepath.Join(dirAbs, parts[i])
			if st, err = st.push(dirAbs, parts[:i+1]); err != nil {
				t.Fatal(err)
			}
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

func TestIgnore_NoSystemConfigRead(t *testing.T) {
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
