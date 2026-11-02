package tree

import (
	"errors"
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
