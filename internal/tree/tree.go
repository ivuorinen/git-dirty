// Package tree arranges scan results into the directory tree that both the
// plain output and the TUI display.
package tree

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/ivuorinen/git-dirty/internal/gitstat"
	"github.com/ivuorinen/git-dirty/internal/scan"
)

// Node is a directory or a repository below the scan root.
type Node struct {
	Name     string
	Path     string
	Rel      []string
	Repo     *gitstat.Result // nil for a plain directory
	Worktree bool            // a linked worktree listed under its repository
	children []*Node
	sorted   bool
}

// Tree is built from scan items by a single goroutine; it is not safe for
// concurrent use.
type Tree struct {
	root  *Node
	nodes map[string]*Node
	repos int
}

// New returns an empty tree rooted at rootPath.
func New(rootPath string) *Tree {
	root := &Node{Name: rootPath, Path: rootPath}

	return &Tree{root: root, nodes: map[string]*Node{"": root}}
}

// Repos returns how many repositories were added. Worktrees listed under
// their repository are not repositories and are not counted.
func (t *Tree) Repos() int { return t.repos }

// Add records a directory or repository item; warnings are ignored.
func (t *Tree) Add(it scan.Item) {
	if it.Kind == scan.Warning {
		return
	}
	n := t.node(it.Rel)
	if it.Path != "" {
		n.Path = it.Path
	}
	if it.Kind == scan.Repo {
		res := it.Result
		if n.Repo == nil && !it.Worktree {
			t.repos++
		}
		n.Repo = &res
		n.Worktree = it.Worktree
	}
}

// Filter selects which nodes Lines returns.
type Filter struct {
	Verbose   bool // list directories without repositories too
	Depth     int  // with Verbose, such directories only down to this depth
	DirtyOnly bool // list only dirty or failed repositories
}

// Line is one row of the rendered tree.
type Line struct {
	Prefix string // tree-drawing characters, empty for the root
	Node   *Node
}

// Lines flattens the visible tree in display order: siblings sorted by name
// in byte order (the script's LC_ALL=C sort), each directory before its
// contents.
func (t *Tree) Lines(f Filter) []Line {
	out := []Line{{Node: t.root}} // the root line is always shown
	t.appendChildren(&out, t.root, "", f)

	return out
}

// node returns the node for rel, creating it and any missing ancestors.
func (t *Tree) node(rel []string) *Node {
	key := strings.Join(rel, "/")
	if n, ok := t.nodes[key]; ok {
		return n
	}
	parent := t.node(rel[:len(rel)-1])
	n := &Node{
		Name: rel[len(rel)-1],
		Path: filepath.Join(parent.Path, rel[len(rel)-1]),
		Rel:  slices.Clone(rel),
	}
	parent.children = append(parent.children, n)
	parent.sorted = false
	t.nodes[key] = n

	return n
}

func (t *Tree) appendChildren(out *[]Line, n *Node, indent string, f Filter) {
	if !n.sorted {
		slices.SortFunc(n.children, func(a, b *Node) int { return strings.Compare(a.Name, b.Name) })
		n.sorted = true
	}
	var shown []*Node
	for _, c := range n.children {
		if t.visible(c, f) {
			shown = append(shown, c)
		}
	}
	for i, c := range shown {
		branch, next := "├── ", "│   "
		if i == len(shown)-1 {
			branch, next = "└── ", "    "
		}
		*out = append(*out, Line{Prefix: indent + branch, Node: c})
		t.appendChildren(out, c, indent+next, f)
	}
}

// visible decides whether n is listed: repositories pass the dirty filter;
// repositories and directories are listed when something below them is (a
// repository's children are its worktrees); directories also, in verbose
// mode without the dirty filter, when within the depth limit.
func (t *Tree) visible(n *Node, f Filter) bool {
	if n.Repo != nil && (!f.DirtyOnly || n.Repo.Dirty() || n.Repo.Err != nil) {
		return true
	}
	for _, c := range n.children {
		if t.visible(c, f) {
			return true
		}
	}

	return n.Repo == nil && f.Verbose && !f.DirtyOnly && len(n.Rel) <= f.Depth
}
