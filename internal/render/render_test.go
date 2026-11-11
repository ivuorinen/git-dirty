package render

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
	"github.com/ivuorinen/git-dirty/internal/gitstat"
	"github.com/ivuorinen/git-dirty/internal/scan"
	"github.com/ivuorinen/git-dirty/internal/tree"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	root       = "/home/u/Code"
	mainBranch = "main"
	originMain = "origin/main"
)

var someHash, _ = gitrepo.SHA1.ParseHex("1111111111111111111111111111111111111111")

func onBranch(name string) gitstat.Head {
	return gitstat.Head{Branch: name, Label: name, Hash: someHash}
}

// The fixture's paths are slash-separated on every platform (path, not
// filepath), so the truncate-off golden file holds on Windows too.
func repo(rel string, res gitstat.Result) scan.Item {
	return scan.Item{Kind: scan.Repo, Path: path.Join(root, rel), Rel: strings.Split(rel, "/"), Result: res}
}

// worktree is a linked worktree listed under the repository its rel is in.
// Its directory is outside the root, as most are.
func worktree(rel string, res gitstat.Result) scan.Item {
	it := repo(rel, res)
	it.Worktree = true
	it.Path = path.Join("/home/u/worktrees", path.Base(rel))

	return it
}

// bare is a bare repository: a result with nothing but Bare set.
func bare(rel string) scan.Item {
	return repo(rel, gitstat.Result{Bare: true})
}

func dir(rel string) scan.Item {
	if rel == "" {
		return scan.Item{Kind: scan.Dir, Path: root}
	}

	return scan.Item{Kind: scan.Dir, Path: path.Join(root, rel), Rel: strings.Split(rel, "/")}
}

// fixture covers every branch-segment variant from the plan's output format.
func fixture() *tree.Tree {
	t := tree.New(root)
	for _, it := range []scan.Item{
		dir(""),
		dir("ivuorinen"),
		repo("ivuorinen/git-dirty", gitstat.Result{
			Head:     onBranch("feature/tree"),
			Status:   gitstat.Status{Modified: true, Untracked: true},
			Upstream: &gitstat.Tracking{Ref: "origin/feature/tree", Ahead: 2},
			Default: &gitstat.DefaultBranch{
				Name:   mainBranch,
				Local:  &gitstat.Tracking{Ref: originMain, Behind: 4},
				Versus: &gitstat.Tracking{Ref: originMain, Ahead: 5, Behind: 1},
			},
		}),
		worktree("ivuorinen/git-dirty/feat", gitstat.Result{Head: onBranch("feat")}),
		worktree("ivuorinen/git-dirty/old", gitstat.Result{
			Err: errors.New("worktree: gitdir file points to non-existent location"),
		}),
		repo("ivuorinen/dotfiles", gitstat.Result{
			Head:     onBranch(mainBranch),
			Upstream: &gitstat.Tracking{Ref: originMain, Behind: 3},
		}),
		// A dirty worktree of a clean repository: the dirty filter keeps both.
		worktree("ivuorinen/dotfiles/hotfix", gitstat.Result{
			Head:     onBranch("hotfix"),
			Status:   gitstat.Status{Modified: true},
			Upstream: &gitstat.Tracking{Ref: "origin/hotfix", Ahead: 1},
		}),
		repo("ivuorinen/gh-history", gitstat.Result{
			Head:     onBranch(mainBranch),
			Upstream: &gitstat.Tracking{Ref: originMain, Ahead: 1},
		}),
		repo("ivuorinen/broken", gitstat.Result{
			Head: onBranch(mainBranch),
			Err:  errors.New("index: unknown extension\nsecond line"),
		}),
		repo("scratch", gitstat.Result{
			Head:    onBranch("wip"),
			Default: &gitstat.DefaultBranch{Name: mainBranch, Versus: &gitstat.Tracking{Ref: mainBranch}},
		}),
		repo("gone", gitstat.Result{
			Head:     onBranch("feat"),
			Upstream: &gitstat.Tracking{Gone: true},
			Status:   gitstat.Status{Stashed: true, Staged: true},
		}),
		repo("tagged", gitstat.Result{Head: gitstat.Head{Label: "v1.0", Hash: someHash}}),
		repo("fresh", gitstat.Result{Head: gitstat.Head{Branch: mainBranch, Label: mainBranch, Unborn: true}}),
		dir("empty"),
		dir("deep"),
		dir("deep/a"),
		dir("deep/a/b"),
		repo("a\x1b[2Jb", gitstat.Result{Head: onBranch(mainBranch)}),
		// Bare repositories with 2, 0 and 1 worktrees; plain.git's dirty
		// worktree keeps it in the dirty filter, with the count unchanged.
		bare("ivuorinen/plain.git"),
		worktree("ivuorinen/plain.git/plain-feat", gitstat.Result{
			Head: onBranch("feat"), Status: gitstat.Status{Modified: true},
		}),
		worktree("ivuorinen/plain.git/plain-main", gitstat.Result{Head: onBranch(mainBranch)}),
		bare("lonely.git"),
		// A bare repository whose worktrees could not be listed: the error,
		// not a count of none.
		repo("ivuorinen/locked.git", gitstat.Result{
			Bare: true,
			Err:  errors.New("worktrees: open worktrees: permission denied"),
		}),
		bare("single.git"),
		worktree("single.git/wt", gitstat.Result{Head: onBranch(mainBranch)}),
		// A folder of clean repositories, one behind its upstream: 📁, not ⚠️.
		dir("clean-group"),
		repo("clean-group/ok", gitstat.Result{
			Head:     onBranch(mainBranch),
			Upstream: &gitstat.Tracking{Ref: originMain, Behind: 3},
		}),
	} {
		t.Add(it)
	}

	return t
}

func baseOptions() Options {
	return Options{ShowBranch: true, Truncate: true, MainBranches: []string{mainBranch, "master", "trunk"}}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s mismatch\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestWritePlain_Golden(t *testing.T) {
	tests := []struct {
		name   string
		filter tree.Filter
		opts   func(*Options)
	}{
		{name: "default", filter: tree.Filter{}},
		{name: "verbose-depth2", filter: tree.Filter{Verbose: true, Depth: 2}},
		{name: "truncate-off", opts: func(o *Options) { o.Truncate = false }},
		{name: "branch-off", opts: func(o *Options) { o.ShowBranch = false }},
		{name: "dirty-only", filter: tree.Filter{DirtyOnly: true, Verbose: true, Depth: 9}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := baseOptions()
			if tt.opts != nil {
				tt.opts(&o)
			}
			tr := fixture()
			var buf bytes.Buffer
			if err := WritePlain(
				&buf,
				root,
				tr.Lines(tt.filter),
				tr.Repos(),
				210*time.Millisecond,
				o,
				false,
			); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(buf.Bytes(), []byte("\x1b")) {
				t.Error("escape byte in color-off output")
			}
			golden(t, tt.name, buf.Bytes())
		})
	}
}

func TestWritePlain_ColorOn(t *testing.T) {
	tr := fixture()
	var buf bytes.Buffer
	if err := WritePlain(
		&buf,
		root,
		tr.Lines(tree.Filter{}),
		tr.Repos(),
		time.Second,
		baseOptions(),
		true,
	); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{
		"\x1b[32m" + IconClean,
		"\x1b[31m" + IconDirty,
		"\x1b[33m[M?]",
		"\x1b[35m(feature/tree ↑2)",
	} {
		if !strings.Contains(buf.String(), code) {
			t.Errorf("colored output lacks %q", code)
		}
	}
	// The only escape sequences are the ones the renderer writes: the
	// sanitized directory name must not contribute a raw ESC.
	stripped := strings.NewReplacer("\x1b[0m", "", "\x1b[31m", "", "\x1b[32m", "", "\x1b[33m", "", "\x1b[35m", "").
		Replace(buf.String())
	if strings.Contains(stripped, "\x1b") {
		t.Error("raw escape byte from repository data reached the output")
	}
}

func TestSanitize(t *testing.T) {
	tests := map[string]string{
		"plain":           "plain",
		"ünïcödé/ブランチ":    "ünïcödé/ブランチ",
		"a\x1b[2Jb":       `a\x1b[2Jb`,
		"tab\there":       `tab\x09here`,
		"del\x7f":         `del\x7f`,
		"c1\u009bx":       `c1\xc2\x9bx`,
		"bad\xffutf8":     `bad\xffutf8`,
		"newline\nsecond": `newline\x0asecond`,
	}
	for in, want := range tests {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		210 * time.Millisecond: "0.21s",
		59 * time.Second:       "59.00s",
		61 * time.Second:       "1m 01s",
		10 * time.Minute:       "10m 00s",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestDefaultSegments(t *testing.T) {
	behind := &gitstat.Tracking{Behind: 1}
	versus := &gitstat.Tracking{Ahead: 1, Behind: 2}
	for _, c := range []struct {
		name string
		d    *gitstat.DefaultBranch
		want string
	}{
		{"on the default branch", nil, ""},
		{"local behind", &gitstat.DefaultBranch{Name: mainBranch, Local: behind}, " · main ↓1"},
		{"local ahead", &gitstat.DefaultBranch{Name: mainBranch, Local: &gitstat.Tracking{Ahead: 2}}, " · main ↑2"},
		{"local level", &gitstat.DefaultBranch{Name: mainBranch, Local: &gitstat.Tracking{}}, ""},
		{"local gone", &gitstat.DefaultBranch{Name: mainBranch, Local: &gitstat.Tracking{Gone: true, Ahead: 3}}, ""},
		{"versus", &gitstat.DefaultBranch{Name: mainBranch, Versus: versus}, " · vs main ↑1 ↓2"},
		{"both", &gitstat.DefaultBranch{Name: mainBranch, Local: behind, Versus: versus}, " · main ↓1 · vs main ↑1 ↓2"},
	} {
		var got strings.Builder
		for _, s := range defaultSegments(c.d) {
			got.WriteString(s.Text)
		}
		if got.String() != c.want {
			t.Errorf("%s: %q, want %q", c.name, got.String(), c.want)
		}
	}
}

func TestSanitize_KeepsAReplacementCharacter(t *testing.T) {
	if got := Sanitize("\uFFFD"); got != "\uFFFD" {
		t.Errorf("Sanitize(U+FFFD) = %q; only an invalid byte is escaped", got)
	}
}

func TestWriteSegments_EmptyTextGetsNoColor(t *testing.T) {
	var b bytes.Buffer
	if err := WriteSegments(&b, []Segment{{"", Dirty}}, true); err != nil {
		t.Fatal(err)
	}
	if b.String() != "\n" {
		t.Errorf("output = %q, want only the newline", b.String())
	}
}

func TestSummary_Singular(t *testing.T) {
	if got := Summary(1, 0); !strings.Contains(got, "1 git repository in") {
		t.Errorf("Summary(1) = %q", got)
	}
}

// failingWriter accepts left writes, then fails.
type failingWriter struct{ left int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.left == 0 {
		return 0, errors.New("disk full")
	}
	w.left--

	return len(p), nil
}

func TestWritePlain_WriteErrors(t *testing.T) {
	lines := fixture().Lines(tree.Filter{})
	o := baseOptions()
	header := len(Header(root, o))
	for name, left := range map[string]int{"header": 0, "tree": header, "summary": header + len(lines)} {
		if err := WritePlain(&failingWriter{left}, root, lines, 1, 0, o, false); err == nil {
			t.Errorf("a failed write in the %s: want error", name)
		}
	}
}
