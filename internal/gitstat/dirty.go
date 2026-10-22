package gitstat

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
)

// ctxCheckEvery is how many entries are processed between cancellation checks.
const ctxCheckEvery = 256

// coreConfig holds the core.* settings that change how the worktree is
// compared with the index.
type coreConfig struct {
	fileMode   bool // compare the executable bit
	symlinks   bool // symlinks are checked out as links, not plain files
	ignoreCase bool // the filesystem is case-insensitive
}

func (r *Repo) coreConfig() (coreConfig, error) {
	cfg := r.repo.Config
	return coreConfig{
		fileMode:   cfg.Bool("core", "", "filemode", true),
		symlinks:   cfg.Bool("core", "", "symlinks", true),
		ignoreCase: cfg.Bool("core", "", "ignorecase", false),
	}, nil
}

// modified reports whether the worktree differs from the index, like
// `git diff --quiet` failing. It stops at the first difference. ex is needed
// for submodules, whose untracked files make them modified.
func (r *Repo) modified(ctx context.Context, idx *gitrepo.Index, cc coreConfig, ex *Excludes) (bool, error) {
	for i := range idx.Entries {
		e := &idx.Entries[i]
		if i%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return false, err
			}
		}
		if e.SkipWorktree {
			continue
		}
		if e.Stage != 0 || e.IntentToAdd {
			return true, nil
		}
		abs := filepath.Join(r.worktree, filepath.FromSlash(e.Name))
		if e.Mode == gitrepo.ModeGitlink {
			changed, err := submoduleChanged(ctx, abs, e.ID, ex)
			if err != nil || changed {
				return changed, err
			}
			continue
		}
		fi, err := os.Lstat(abs)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		changed, err := entryChanged(e, abs, fi, idx.ModTime, cc, r.repo.Format)
		if err != nil || changed {
			return changed, err
		}
	}
	return false, nil
}

// entryChanged compares one index entry with the file on disk. Matching size
// and mtime trust the index unless the entry is racily clean (the file's mtime
// is not older than the index file's); otherwise the content is hashed.
func entryChanged(e *gitrepo.IndexEntry, abs string, fi fs.FileInfo, indexTime time.Time, cc coreConfig, f gitrepo.Format) (bool, error) {
	isLink := fi.Mode()&fs.ModeSymlink != 0
	switch e.Mode {
	case gitrepo.ModeSymlink:
		// Without core.symlinks the link is checked out as a plain file
		// holding the target, so either form is accepted.
		if cc.symlinks && !isLink {
			return true, nil
		}
		if !cc.symlinks && !isLink && !fi.Mode().IsRegular() {
			return true, nil
		}
	default:
		if !fi.Mode().IsRegular() {
			return true, nil
		}
		if cc.fileMode && (e.Mode == gitrepo.ModeExecutable) != (fi.Mode().Perm()&0o111 != 0) {
			return true, nil
		}
	}
	// The index stores the size truncated to 32 bits.
	if uint32(fi.Size()) != e.Size {
		return true, nil
	}
	if sameMtime(fi.ModTime(), e.MTime) && fi.ModTime().Before(indexTime) {
		return false, nil
	}
	h, err := hashWorktreeFile(abs, fi, isLink, f)
	if err != nil {
		return false, err
	}
	return h != e.ID, nil
}

// sameMtime compares to the second, and to the nanosecond only when the index
// recorded one (git built without nanosecond support stores zero).
func sameMtime(disk, entry time.Time) bool {
	if disk.Unix() != entry.Unix() {
		return false
	}
	return entry.Nanosecond() == 0 || disk.Nanosecond() == entry.Nanosecond()
}

// hashWorktreeFile computes the blob id git would store for the file: its
// content, or the link target for a symlink.
func hashWorktreeFile(abs string, fi fs.FileInfo, isLink bool, f gitrepo.Format) (gitrepo.ObjectID, error) {
	if isLink {
		target, err := os.Readlink(abs)
		if err != nil {
			return gitrepo.ObjectID{}, err
		}
		return f.HashObject(gitrepo.ObjBlob, []byte(filepath.ToSlash(target))), nil
	}
	file, err := os.Open(abs)
	if err != nil {
		return gitrepo.ObjectID{}, err
	}
	defer file.Close()
	h := f.NewObjectHasher(gitrepo.ObjBlob, fi.Size())
	if _, err := io.Copy(h, file); err != nil {
		return gitrepo.ObjectID{}, err
	}
	return h.Sum(), nil
}

// staged reports whether the index differs from the HEAD commit's tree, like
// `git diff --cached --quiet` failing. head is zero for an unborn branch, in
// which case any entry counts as staged.
func (r *Repo) staged(ctx context.Context, idx *gitrepo.Index, head gitrepo.ObjectID) (bool, error) {
	if head.IsZero() {
		for _, e := range idx.Entries {
			if !e.IntentToAdd {
				return true, nil
			}
		}
		return false, nil
	}
	commit, err := r.repo.ReadCommit(head)
	if err != nil {
		return false, err
	}
	// A valid root cache-tree entry covering every index entry records the
	// tree the index would write; equal to HEAD's tree means nothing staged,
	// decided without reading a single tree object.
	if t := idx.Tree; t != nil && t.Entries == len(idx.Entries) && t.ID == commit.Tree {
		return false, nil
	}

	type blob struct {
		id   gitrepo.ObjectID
		mode uint32
	}
	inTree := make(map[string]blob)
	n := 0
	err = r.repo.WalkTree(commit.Tree, func(path string, e gitrepo.TreeEntry) error {
		if n++; n%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		inTree[path] = blob{e.ID, e.Mode}
		return nil
	})
	if err != nil {
		return false, err
	}

	matched := 0
	for _, e := range idx.Entries {
		if e.IntentToAdd {
			continue // not staged content; `git diff --cached` ignores it
		}
		if e.Stage != 0 {
			return true, nil
		}
		b, ok := inTree[e.Name]
		if !ok || b.id != e.ID || b.mode != e.Mode {
			return true, nil
		}
		matched++
	}
	return matched != len(inTree), nil
}

// untracked reports whether the worktree holds a file that is neither in the
// index nor ignored, like `git ls-files --others --exclude-standard` printing
// anything. Ignored directories are not entered, symlinks are not followed,
// and a nested repository that is not a submodule counts as untracked.
func (r *Repo) untracked(ctx context.Context, idx *gitrepo.Index, cc coreConfig, ex *Excludes) (bool, error) {
	key := func(s string) string { return s }
	if cc.ignoreCase {
		key = strings.ToLower
	}
	tracked := make(map[string]struct{}, len(idx.Entries))
	for _, e := range idx.Entries {
		tracked[key(e.Name)] = struct{}{}
	}
	st, err := r.rootIgnores(ex)
	if err != nil {
		return false, err
	}
	w := untrackedWalker{ctx: ctx, tracked: tracked, key: key}
	return w.walk(r.worktree, nil, st)
}

type untrackedWalker struct {
	ctx     context.Context
	tracked map[string]struct{}
	key     func(string) string
	seen    int
}

func (w *untrackedWalker) walk(dirAbs string, rel []string, st ignoreStack) (bool, error) {
	st, err := st.push(dirAbs, rel)
	if err != nil {
		return false, err
	}
	entries, err := os.ReadDir(dirAbs)
	if err != nil {
		if rel == nil {
			return false, err
		}
		return false, nil // git warns and skips unreadable subdirectories
	}
	for _, de := range entries {
		if w.seen++; w.seen%ctxCheckEvery == 0 {
			if err := w.ctx.Err(); err != nil {
				return false, err
			}
		}
		name := de.Name()
		if name == ".git" {
			continue
		}
		path := append(rel[:len(rel):len(rel)], name)
		_, isTracked := w.tracked[w.key(strings.Join(path, "/"))]
		if !de.IsDir() { // files and symlinks, including symlinks to directories
			if !isTracked && !st.match(path, false) {
				return true, nil
			}
			continue
		}
		if isTracked || st.match(path, true) { // a submodule, or ignored
			continue
		}
		sub := filepath.Join(dirAbs, name)
		if _, err := os.Lstat(filepath.Join(sub, ".git")); err == nil {
			return true, nil // nested repository
		}
		found, err := w.walk(sub, path, st)
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}

// stashed reports whether refs/stash exists, loose or packed.
func (r *Repo) stashed() (bool, error) {
	_, err := r.repo.ReadRef("refs/stash")
	if errors.Is(err, gitrepo.ErrRefNotFound) {
		return false, nil
	}
	return err == nil, err
}

// submoduleChanged reports whether a checked-out submodule differs from what
// the superproject's index records: its HEAD moved, or its own tracked
// content is modified or staged (git's "-dirty"). Untracked files inside it
// do not count; `git diff --quiet` ignores them. A submodule that is not
// checked out (no .git inside) is unchanged, as in git.
func submoduleChanged(ctx context.Context, abs string, want gitrepo.ObjectID, ex *Excludes) (bool, error) {
	if _, err := os.Lstat(filepath.Join(abs, ".git")); err != nil {
		return false, nil
	}
	sub, err := Open(abs)
	if err != nil {
		return false, err
	}
	defer sub.Close()
	h, err := sub.head()
	if err != nil {
		return false, err
	}
	if h.Hash != want {
		return true, nil
	}
	idx, err := sub.repo.ReadIndex()
	if err != nil {
		return false, err
	}
	cc, err := sub.coreConfig()
	if err != nil {
		return false, err
	}
	if changed, err := sub.modified(ctx, idx, cc, ex); err != nil || changed {
		return changed, err
	}
	return sub.staged(ctx, idx, h.Hash)
}
