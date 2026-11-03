package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// errWorktreeMissing is git's wording for a worktree whose directory is gone
// (`git worktree list` marks it prunable).
var errWorktreeMissing = errors.New("gitdir file points to non-existent location")

// errWorktreesNotDir is returned for a worktrees entry that is not a
// directory. Callers prefix it with what they were listing.
var errWorktreesNotDir = errors.New("not a directory")

// ErrOtherGitDir is a listed worktree's Err when the worktree's .git file does
// not lead back to the admin dir that lists it: a stale record whose folder
// now belongs to another repository, or a crafted gitdir file. The folder is
// not this repository's worktree, so the scan must not claim it.
var ErrOtherGitDir = errors.New("worktree .git file names another git dir")

// Worktree is a linked worktree recorded in a repository's worktrees/ dir.
type Worktree struct {
	ID   string // <id> under worktrees/: git's name for it
	Path string // the working tree the gitdir file names, without "/.git"
	Err  error  // why it cannot be inspected; Path may then be ""
}

// ListWorktrees lists the linked worktrees of the repository at path, a
// working tree or a folder that is itself a git directory, in ID order.
// linked is true, with none listed, when path is itself a linked worktree
// (its git dir has a commondir file). A worktree that cannot be inspected is
// listed with Err set; errors name no path.
func ListWorktrees(path string) (wts []Worktree, linked bool, err error) {
	gitDir, _, err := resolveGitDir(path)
	if err != nil {
		return nil, false, err
	}
	_, err = os.Lstat(filepath.Join(gitDir, "commondir"))
	switch {
	case err == nil:
		return nil, true, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, false, err
	}
	dir := filepath.Join(gitDir, "worktrees")
	// Stat, not Lstat: git follows a worktrees symlink. Checked before
	// ReadDir because Go's ReadDir on Windows reads a file as an empty dir.
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		return nil, false, errWorktreesNotDir
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	for _, e := range entries { // ReadDir sorts by name
		if e.IsDir() {
			wts = append(wts, readWorktree(filepath.Join(dir, e.Name()), e.Name()))
		}
	}

	return wts, false, nil
}

// readWorktree reads the admin dir worktrees/<id>/ of one linked worktree.
func readWorktree(admin, id string) Worktree {
	wt := Worktree{ID: id}
	p, err := readLinkFile(filepath.Join(admin, "gitdir"), "")
	if err != nil {
		wt.Err = fmt.Errorf("invalid gitdir file: %w", err)

		return wt
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(admin, p)
	}
	p = filepath.Clean(p)
	if filepath.Base(p) != ".git" {
		wt.Err = errors.New("invalid gitdir file: does not name a .git file")

		return wt
	}
	wt.Path = filepath.Dir(p)
	back, err := findGitDir(wt.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		wt.Err = errWorktreeMissing

		return wt
	case err != nil:
		// findGitDir wraps its cause with the .git path; Unwrap drops it.
		wt.Err = fmt.Errorf("invalid .git file: %w", errors.Unwrap(err))

		return wt
	}
	// The worktree's own .git must lead back here. The paths differ in
	// form (git records them with symlinks resolved), so compare the files.
	// Without this, a crafted gitdir file could point the scan at any
	// directory.
	ai, aerr := os.Stat(admin)
	bi, berr := os.Stat(back)
	if aerr != nil || berr != nil || !os.SameFile(ai, bi) {
		wt.Err = ErrOtherGitDir
	}

	return wt
}
