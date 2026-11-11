package tree

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ivuorinen/git-dirty/internal/gitstat"
	"github.com/ivuorinen/git-dirty/internal/scan"
)

func item(rel string, res gitstat.Result, worktree bool) scan.Item {
	return scan.Item{Kind: scan.Repo, Rel: strings.Split(rel, "/"), Result: res, Worktree: worktree}
}

var (
	clean   = gitstat.Result{}
	dirty   = gitstat.Result{Status: gitstat.Status{Modified: true}}
	failed  = gitstat.Result{Err: errors.New("index: unknown extension")}
	behind  = gitstat.Result{Upstream: &gitstat.Tracking{Ref: "origin/main", Behind: 3}}
	bareRes = gitstat.Result{Bare: true}
)

// TestNodeProblem: a folder has a problem when a repository or worktree
// anywhere below it is dirty or could not be read; being behind is not one,
// as it does not make a repository dirty either.
func TestNodeProblem(t *testing.T) {
	for _, c := range []struct {
		name  string
		items []scan.Item
		want  bool
	}{
		{"clean repositories", []scan.Item{item("g/a", clean, false), item("g/b", clean, false)}, false},
		{"behind its upstream", []scan.Item{item("g/a", behind, false)}, false},
		{"a dirty repository", []scan.Item{item("g/a", clean, false), item("g/b", dirty, false)}, true},
		{"an unreadable repository", []scan.Item{item("g/a", failed, false)}, true},
		{"a dirty worktree of a clean repository", []scan.Item{
			item("g/a", clean, false), item("g/a/wt", dirty, true),
		}, true},
		{"a dirty worktree of a bare repository", []scan.Item{
			item("g/b.git", bareRes, false), item("g/b.git/wt", dirty, true),
		}, true},
		{"a clean bare repository", []scan.Item{
			item("g/b.git", bareRes, false), item("g/b.git/wt", clean, true),
		}, false},
		{"a dirty repository two folders down", []scan.Item{item("g/x/y/a", dirty, false)}, true},
		{"no repository", []scan.Item{{Kind: scan.Dir, Rel: []string{"g"}}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr := New("/root")
			for _, it := range c.items {
				tr.Add(it)
			}
			if got := tr.node([]string{"g"}).Problem(); got != c.want {
				t.Errorf("folder g: Problem() = %v, want %v", got, c.want)
			}
			if got := tr.root.Problem(); got != c.want {
				t.Errorf("root: Problem() = %v, want %v", got, c.want)
			}
		})
	}
}

const treeRoot = "/root"

// shown renders a filter's lines the way the tree is drawn.
func shown(tr *Tree, f Filter) []string {
	lines := tr.Lines(f)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Prefix + l.Node.Name
	}

	return out
}

func filterFixture() *Tree {
	tr := New(treeRoot)
	tr.Add(scan.Item{Kind: scan.Dir, Path: treeRoot})
	tr.Add(scan.Item{Kind: scan.Dir, Rel: []string{"empty"}})
	tr.Add(scan.Item{Kind: scan.Warning, Rel: []string{"skipped"}, Message: "skipped: excluded name"})
	for _, it := range []scan.Item{
		item("deep/x/y", dirty, false),
		item("clean/z", clean, false),
		item("b", failed, false),
		item("a", clean, false),
		item("a/wt", dirty, true),
	} {
		tr.Add(it)
	}

	return tr
}

func TestLines_Filters(t *testing.T) {
	tr := filterFixture()
	top := []string{treeRoot, "├── a", "│   └── wt", "├── b"}
	cleanLines := []string{"├── clean", "│   └── z"}
	deepLast := []string{"└── deep", "    └── x", "        └── y"}
	for _, c := range []struct {
		name string
		f    Filter
		want []string
	}{
		{"repositories and what holds them", Filter{}, slices.Concat(top, cleanLines, deepLast)},
		{"verbose lists an empty folder within the depth", Filter{Verbose: true, Depth: 1}, slices.Concat(
			top, cleanLines, []string{"├── deep", "│   └── x", "│       └── y", "└── empty"},
		)},
		{"verbose stops at the depth", Filter{Verbose: true}, slices.Concat(top, cleanLines, deepLast)},
		{"dirty only keeps a clean repository with a dirty worktree", Filter{DirtyOnly: true}, slices.Concat(top, deepLast)},
		{"dirty only hides empty folders even when verbose", Filter{DirtyOnly: true, Verbose: true, Depth: 9},
			slices.Concat(top, deepLast)},
	} {
		if got := shown(tr, c.f); !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestAdd_Bookkeeping(t *testing.T) {
	tr := filterFixture()
	if got := tr.Repos(); got != 4 {
		t.Errorf("Repos() = %d, want 4: worktrees are not counted", got)
	}
	tr.Add(item("a", clean, false))
	if got := tr.Repos(); got != 4 {
		t.Errorf("Repos() = %d after a repository came again, want 4", got)
	}
	if got := tr.node([]string{"a"}).Worktrees(); got != 1 {
		t.Errorf("Worktrees() = %d, want 1", got)
	}
	if got := tr.node([]string{"clean"}).Worktrees(); got != 0 {
		t.Errorf("Worktrees() of a folder holding a repository = %d, want 0", got)
	}
	if _, ok := tr.nodes["skipped"]; ok {
		t.Error("a warning created a node")
	}
	if got, want := tr.node([]string{"empty"}).Path, filepath.Join(treeRoot, "empty"); got != want {
		t.Errorf("Path of an item without one = %q, want %q", got, want)
	}
}
