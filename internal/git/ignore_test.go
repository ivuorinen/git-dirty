package git

import (
	"path/filepath"
	"strings"
	"testing"
)

// buildDir is a directory-only pattern and the path it is checked on.
const buildDir = "build/"

// ignoredLikeWalk applies the per-directory .gitignore stack the way a tree
// walk does: a path is ignored when it, or any directory above it, is.
func ignoredLikeWalk(t *testing.T, dir, rel string, isDir, ignoreCase bool) bool {
	t.Helper()
	info, err := ReadPatterns(filepath.Join(dir, ".git", "info", "exclude"), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := Matcher{Patterns: info, IgnoreCase: ignoreCase}
	parts := strings.Split(rel, "/")
	cur := dir
	for i := range parts {
		ps, err := ReadPatterns(filepath.Join(cur, ".gitignore"), parts[:i])
		if err != nil {
			t.Fatal(err)
		}
		m.Patterns = append(m.Patterns[:len(m.Patterns):len(m.Patterns)], ps...)
		last := i == len(parts)-1
		if m.Ignored(parts[:i+1], !last || isDir) {
			return true
		}
		cur = filepath.Join(cur, parts[i])
	}

	return false
}

func checkIgnore(t *testing.T, dir string, ignoreCase bool, paths []string) {
	t.Helper()
	for _, p := range paths {
		isDir := strings.HasSuffix(p, "/")
		_, code := runCode(t, dir, nil, "check-ignore", "-q", p)
		want := code == 0
		if got := ignoredLikeWalk(t, dir, strings.TrimSuffix(p, "/"), isDir, ignoreCase); got != want {
			t.Errorf("%s: ignored = %v, git check-ignore says %v", p, got, want)
		}
	}
}

func TestMatcher_MatchesGit(t *testing.T) {
	dir := newRepo(t, SHA1)
	writeFile(t, dir, ".git/info/exclude", "local-only/\n")
	writeFile(t, dir, ".gitignore", strings.Join([]string{
		"# comment",
		`\#hash`,
		`\!bang`,
		"*.o",
		"!keep.o",
		buildDir,
		"/anchored",
		"**/logs",
		"docs/**/tmp",
		"cache/**",
		"x[0-9].txt",
		"y[!a-c].txt",
		"trailing\\ ",
		"spaces   ",
		"lit?.md",
		"",
	}, "\n"))
	writeFile(t, dir, "sub/.gitignore", "*.md\n!README.md\n/only-here\n")

	checkIgnore(t, dir, false, []string{
		"#hash", "!bang", "hash",
		"a.o", "keep.o", "sub/b.o",
		buildDir, "build", "src/build/", "src/build/x",
		"anchored", "sub/anchored",
		"logs/", "a/b/logs/", "logs/x", "a/logs/y",
		"docs/tmp", "docs/a/b/tmp", "docs/a/tmpx",
		"cache/x", "cache/a/b", "cache/", "cache",
		"x1.txt", "xa.txt", "yd.txt", "ya.txt",
		"trailing ", "trailing", "spaces", "spaces ",
		"lit1.md", "sub/x.md", "sub/README.md", "x.md",
		"sub/only-here", "only-here", "sub/deeper/only-here",
		"local-only/f", "local-only/",
	})
}

func TestMatcher_IgnoreCase(t *testing.T) {
	dir := newRepo(t, SHA1)
	run(t, dir, "config", "core.ignorecase", "true")
	writeFile(t, dir, ".gitignore", "*.LOG\nBuild/\nsub/Deep.txt\n")
	checkIgnore(t, dir, true, []string{"x.log", "X.LOG", buildDir, "BUILD/", "sub/deep.txt", "SUB/DEEP.TXT", "x.txt"})
}

func TestParsePattern_Skips(t *testing.T) {
	for _, line := range []string{"", "# c", "   ", "\r", "/", "!"} {
		if _, ok := ParsePattern(line, nil); ok {
			t.Errorf("ParsePattern(%q): want skipped", line)
		}
	}
	if p, ok := ParsePattern("a\r", nil); !ok || p.segments[0] != "a" {
		t.Errorf("trailing CR not dropped: %+v", p)
	}
}
