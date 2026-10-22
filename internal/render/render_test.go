package render

import (
	"bytes"
	"errors"
	"flag"
	"os"
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

const root = "/home/u/Code"

var someHash, _ = gitrepo.SHA1.ParseHex("1111111111111111111111111111111111111111")

func onBranch(name string) gitstat.Head {
	return gitstat.Head{Branch: name, Label: name, Hash: someHash}
}

func repo(rel string, res gitstat.Result) scan.Item {
	parts := strings.Split(rel, "/")
	res.Path = filepath.Join(root, rel)
	return scan.Item{Kind: scan.Repo, Path: res.Path, Rel: parts, Result: res}
}

func dir(rel string) scan.Item {
	if rel == "" {
		return scan.Item{Kind: scan.Dir, Path: root}
	}
	return scan.Item{Kind: scan.Dir, Path: filepath.Join(root, rel), Rel: strings.Split(rel, "/")}
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
				Name:   "main",
				Local:  &gitstat.Tracking{Ref: "origin/main", Behind: 4},
				Versus: &gitstat.Tracking{Ref: "origin/main", Ahead: 5, Behind: 1},
			},
		}),
		repo("ivuorinen/dotfiles", gitstat.Result{Head: onBranch("main"), Upstream: &gitstat.Tracking{Ref: "origin/main", Behind: 3}}),
		repo("ivuorinen/gh-history", gitstat.Result{Head: onBranch("main"), Upstream: &gitstat.Tracking{Ref: "origin/main", Ahead: 1}}),
		repo("ivuorinen/broken", gitstat.Result{Head: onBranch("main"), Err: errors.New("index: unknown extension\nsecond line")}),
		repo("scratch", gitstat.Result{
			Head:    onBranch("wip"),
			Default: &gitstat.DefaultBranch{Name: "main", Versus: &gitstat.Tracking{Ref: "main"}},
		}),
		repo("gone", gitstat.Result{Head: onBranch("feat"), Upstream: &gitstat.Tracking{Gone: true}, Status: gitstat.Status{Stashed: true, Staged: true}}),
		repo("tagged", gitstat.Result{Head: gitstat.Head{Label: "v1.0", Hash: someHash}}),
		repo("fresh", gitstat.Result{Head: gitstat.Head{Branch: "main", Label: "main", Unborn: true}}),
		dir("empty"),
		dir("deep"),
		dir("deep/a"),
		dir("deep/a/b"),
		repo("a\x1b[2Jb", gitstat.Result{Head: onBranch("main")}),
	} {
		t.Add(it)
	}
	return t
}

func baseOptions() Options {
	return Options{ShowBranch: true, Truncate: true, MainBranches: []string{"main", "master", "trunk"}}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
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
			if err := WritePlain(&buf, root, tr.Lines(tt.filter), tr.Repos(), 210*time.Millisecond, o, false); err != nil {
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
	if err := WritePlain(&buf, root, tr.Lines(tree.Filter{}), tr.Repos(), time.Second, baseOptions(), true); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"\x1b[32m" + IconClean, "\x1b[31m" + IconDirty, "\x1b[33m[M?]", "\x1b[35m(feature/tree ↑2)"} {
		if !strings.Contains(buf.String(), code) {
			t.Errorf("colored output lacks %q", code)
		}
	}
	// The only escape sequences are the ones the renderer writes: the
	// sanitized directory name must not contribute a raw ESC.
	stripped := strings.NewReplacer("\x1b[0m", "", "\x1b[31m", "", "\x1b[32m", "", "\x1b[33m", "", "\x1b[35m", "").Replace(buf.String())
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
