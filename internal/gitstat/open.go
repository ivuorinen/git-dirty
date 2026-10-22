// Package gitstat inspects a git working tree read-only, using the in-tree
// readers in internal/git. It never executes git or any repository hook,
// filter or fsmonitor.
package gitstat

import (
	"strings"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
)

// Repo is an open repository. Close releases its pack files.
type Repo struct {
	worktree string
	repo     *gitrepo.Repository
}

// Open opens the working tree at path. The path must contain a .git
// directory, or a .git file ("gitdir: <path>") as written for linked
// worktrees and absorbed submodules.
func Open(path string) (*Repo, error) {
	r, err := gitrepo.OpenWorktree(path)
	if err != nil {
		return nil, err
	}
	return &Repo{worktree: path, repo: r}, nil
}

// Close releases the repository's open files.
func (r *Repo) Close() error {
	return r.repo.Close()
}

// shortRefName drops the refs/heads/, refs/tags/ or refs/remotes/ prefix,
// as git does when it prints a ref.
func shortRefName(name string) string {
	for _, prefix := range []string{"refs/heads/", "refs/tags/", "refs/remotes/"} {
		if s, ok := strings.CutPrefix(name, prefix); ok {
			return s
		}
	}
	return name
}
