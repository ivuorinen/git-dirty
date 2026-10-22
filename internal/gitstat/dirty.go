package gitstat

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
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
		if i%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return false, err
			}
		}
		changed, err := r.entryModified(ctx, &idx.Entries[i], idx.ModTime, cc, ex)
		if err != nil || changed {
			return changed, err
		}
	}

	return false, nil
}

// entryModified reports whether the worktree differs from one index entry.
// indexTime is the index file's mtime, for the racy-git check.
func (r *Repo) entryModified(
	ctx context.Context,
	e *gitrepo.IndexEntry,
	indexTime time.Time,
	cc coreConfig,
	ex *Excludes,
) (bool, error) {
	if e.SkipWorktree {
		return false, nil
	}
	if e.Stage != 0 || e.IntentToAdd {
		return true, nil
	}
	abs := filepath.Join(r.worktree, filepath.FromSlash(e.Name))
	if e.Mode == gitrepo.ModeGitlink {
		return submoduleChanged(ctx, abs, e.ID, ex)
	}
	fi, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	return entryChanged(e, abs, fi, indexTime, cc, r.repo.Format)
}

// entryChanged compares one index entry with the file on disk. Matching size
// and mtime trust the index unless the entry is racily clean (the file's mtime
// is not older than the index file's); otherwise the content is hashed.
func entryChanged(
	e *gitrepo.IndexEntry,
	abs string,
	fi fs.FileInfo,
	indexTime time.Time,
	cc coreConfig,
	f gitrepo.Format,
) (bool, error) {
	isLink := fi.Mode()&fs.ModeSymlink != 0
	if modeChanged(e, fi, isLink, cc) {
		return true, nil
	}
	if indexSize(fi.Size()) != e.Size {
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

// modeChanged reports whether the file's type, or its executable bit when
// core.fileMode is set, no longer matches the entry's mode.
func modeChanged(e *gitrepo.IndexEntry, fi fs.FileInfo, isLink bool, cc coreConfig) bool {
	if e.Mode == gitrepo.ModeSymlink {
		// Without core.symlinks the link is checked out as a plain file
		// holding the target, so either form is accepted.
		if cc.symlinks {
			return !isLink
		}

		return !isLink && !fi.Mode().IsRegular()
	}
	if !fi.Mode().IsRegular() {
		return true
	}

	return cc.fileMode && (e.Mode == gitrepo.ModeExecutable) != (fi.Mode().Perm()&0o111 != 0)
}

// indexSize is a file size as the index stores it: truncated to its low 32
// bits, since git keeps it in an unsigned int (ce_stat_data's sd_size).
func indexSize(size int64) uint32 {
	return uint32(size & math.MaxUint32)
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
	file, err := os.Open(filepath.Clean(abs))
	if err != nil {
		return gitrepo.ObjectID{}, err
	}
	defer func() { _ = file.Close() }() // read-only: a close error carries no information
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
		return hasStagedEntry(idx), nil
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
	inTree, err := r.treeBlobs(ctx, commit.Tree)
	if err != nil {
		return false, err
	}

	return indexDiffersFromTree(idx, inTree), nil
}

// hasStagedEntry reports whether the index holds an entry with content, one
// not only marked intent-to-add.
func hasStagedEntry(idx *gitrepo.Index) bool {
	for _, e := range idx.Entries {
		if !e.IntentToAdd {
			return true
		}
	}

	return false
}

// treeBlob is a non-tree entry of a commit's tree.
type treeBlob struct {
	id   gitrepo.ObjectID
	mode uint32
}

// treeBlobs reads every non-tree entry under tree, keyed by its path.
func (r *Repo) treeBlobs(ctx context.Context, tree gitrepo.ObjectID) (map[string]treeBlob, error) {
	blobs := make(map[string]treeBlob)
	n := 0
	err := r.repo.WalkTree(tree, func(path string, e gitrepo.TreeEntry) error {
		if n++; n%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		blobs[path] = treeBlob{e.ID, e.Mode}

		return nil
	})

	return blobs, err
}

// indexDiffersFromTree reports whether the index entries differ from the
// tree's blobs: a conflict, a changed or added entry, or a removed one.
func indexDiffersFromTree(idx *gitrepo.Index, inTree map[string]treeBlob) bool {
	matched := 0
	for _, e := range idx.Entries {
		if e.IntentToAdd {
			continue // not staged content; `git diff --cached` ignores it
		}
		if e.Stage != 0 {
			return true
		}
		b, ok := inTree[e.Name]
		if !ok || b.id != e.ID || b.mode != e.Mode {
			return true
		}
		matched++
	}

	return matched != len(inTree)
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
		found, err := w.visit(dirAbs, rel, de, st)
		if err != nil || found {
			return found, err
		}
	}

	return false, nil
}

// visit reports whether the entry de of the directory dirAbs (rel inside the
// worktree) is an untracked file or holds one.
func (w *untrackedWalker) visit(dirAbs string, rel []string, de fs.DirEntry, st ignoreStack) (bool, error) {
	name := de.Name()
	if name == ".git" {
		return false, nil
	}
	path := append(slices.Clip(rel), name) // a fresh slice: siblings must not share it
	_, isTracked := w.tracked[w.key(strings.Join(path, "/"))]
	if !de.IsDir() { // files and symlinks, including symlinks to directories
		return !isTracked && !st.match(path, false), nil
	}
	if isTracked || st.match(path, true) { // a submodule, or ignored
		return false, nil
	}
	sub := filepath.Join(dirAbs, name)
	if _, err := os.Lstat(filepath.Join(sub, ".git")); err == nil {
		return true, nil // nested repository
	}

	return w.walk(sub, path, st)
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
	defer func() { _ = sub.Close() }() // read-only pack files: a close error carries no information
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
