package git

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxLinkFile caps a .git file or a worktrees/<id>/gitdir file: each holds
// one path.
const maxLinkFile = 4096

// maxHeadRef is how much of HEAD IsGitDir reads, as git's validate_headref
// does.
const maxHeadRef = 255

// errNotRegular refuses a file that is not a regular file.
var errNotRegular = errors.New("not a regular file")

// Repository is an opened git directory. It is not safe for concurrent use;
// each goroutine opens its own. Close releases its open files.
type Repository struct {
	GitDir    string // this worktree's git dir: HEAD, index
	CommonDir string // shared dir: config, refs, objects (== GitDir unless a linked worktree)
	Format    Format
	Config    *Config
	Bare      bool // no working tree: the git dir itself, or core.bare without commondir

	packed     map[string]packedRef // packed-refs, loaded on first use
	packedRead bool
	odb        *odb // object database, opened on first use
}

// OpenWorktree opens the repository at path: a working tree holding a .git
// directory or a .git file ("gitdir: <path>"), as written for linked
// worktrees and absorbed submodules, or a folder that is itself a git
// directory, which has no working tree and is opened as bare.
func OpenWorktree(path string) (*Repository, error) {
	gitDir, self, err := resolveGitDir(path)
	if err != nil {
		return nil, err
	}
	r, err := Open(gitDir)
	if err != nil {
		return nil, err
	}
	if self {
		r.Bare = true
	}

	return r, nil
}

// Open opens a git directory, following its commondir file if present. The
// commondir file is read as a link file: regular, at most maxLinkFile bytes.
func Open(gitDir string) (*Repository, error) {
	commonDir := gitDir
	linked := false
	c, err := readLinkFile(filepath.Join(gitDir, "commondir"), "")
	switch {
	case err == nil:
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		commonDir = filepath.Clean(c)
		linked = true
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("commondir: %w", err)
	}
	cfg, err := ReadConfigFile(filepath.Join(commonDir, "config"))
	if err != nil {
		return nil, err
	}
	version, _ := cfg.Get("core", "", "repositoryformatversion")
	ext := map[string]string{}
	for _, k := range cfg.Keys("extensions", "") {
		ext[k], _ = cfg.Get("extensions", "", k)
	}
	format, err := checkFormat(version, ext)
	if err != nil {
		return nil, err
	}
	bare, err := isBare(cfg, gitDir, linked)
	if err != nil {
		return nil, err
	}

	return &Repository{GitDir: gitDir, CommonDir: commonDir, Format: format, Config: cfg, Bare: bare}, nil
}

// isBare is core.bare as git's check_repository_format_gently applies it.
// Without extensions.worktreeConfig, the shared config's core.bare is
// ignored in a linked worktree (has_common), which is why the worktrees of a
// bare repository have working trees. With it, this git dir's
// config.worktree may set core.bare, overriding the shared config, and the
// result applies in a linked worktree too; git sparse-checkout moves a bare
// repository's core.bare there when it turns the extension on.
func isBare(cfg *Config, gitDir string, linked bool) (bool, error) {
	bare := cfg.Bool("core", "", "bare", false)
	if !cfg.Bool("extensions", "", "worktreeconfig", false) {
		return bare && !linked, nil
	}
	wt, err := ReadConfigFile(filepath.Join(gitDir, "config.worktree"))
	if err != nil {
		return false, err
	}
	if _, ok := wt.Get("core", "", "bare"); ok {
		bare = wt.Bool("core", "", "bare", false)
	}

	return bare, nil
}

// Close releases the repository's open files: the pack and pack index files
// held open since the first object read.
func (r *Repository) Close() error {
	if r.odb == nil {
		return nil
	}

	return r.odb.close()
}

// errNotGitRepo is git's wording up to 2.55 (read_gitfile_gently) for a .git
// file that names a directory that is not there. git 2.56 reworded it to
// "gitfile does not point to a valid repository" and names the .git file;
// the error findGitDir returns names both.
var errNotGitRepo = errors.New("not a git repository")

// findGitDir returns the git dir of the working tree at worktree: its .git
// directory, or the directory its .git file names. An error from a .git file
// is "<.git path>: <cause>", and errors.Unwrap gives the cause without a path,
// as readWorktree reports it. A missing .git is the fs.ErrNotExist from
// Lstat, and only that: a .git file naming a missing directory fails with
// errNotGitRepo, followed by the directory, as git up to 2.55 does. It is not
// fs.ErrNotExist, which readWorktree takes for a missing worktree. Without
// the check, Open read the missing dir's commondir and config as absent, and
// the first error was HEAD: reference not found. Any other failure to stat
// the directory, such as a parent that cannot be searched or an I/O error, is
// returned as itself, without its path: git reports that as not a git
// repository too, but the directory may be there, and calling it missing
// would misname the cause.
func findGitDir(worktree string) (string, error) {
	p := filepath.Join(worktree, ".git")
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return p, nil
	}
	dir, err := readLinkFile(p, "gitdir:")
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(worktree, dir)
	}
	dir = filepath.Clean(dir)
	// Stat follows symlinks, as git's access(2) does.
	fi, err = os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR), err == nil && !fi.IsDir():
		return "", fmt.Errorf("%s: %w: %s", p, errNotGitRepo, dir)
	case err != nil:
		return "", fmt.Errorf("%s: %w", p, pathless(err))
	}

	return dir, nil
}

// resolveGitDir returns the git dir of the working tree at path, or path
// itself (self true) when path has no .git entry and is itself a git
// directory. findGitDir stays strict: readWorktree's back-link check needs a
// missing .git to read as missing, in git's wording.
func resolveGitDir(path string) (gitDir string, self bool, err error) {
	gitDir, err = findGitDir(path)
	if errors.Is(err, fs.ErrNotExist) && IsGitDir(path) {
		return path, true, nil
	}

	return gitDir, false, err
}

// IsGitDir reports whether dir is itself a git directory, by git's test
// (is_git_directory in setup.c): a valid HEAD, and objects and refs
// directories. HEAD is checked first, so a directory without one costs one
// Lstat. A symlinked HEAD is judged by its target and not followed; a regular
// file is read up to maxHeadRef bytes; anything else, and any error, fails.
// Unlike git, IsGitDir does not follow a commondir file, so a linked
// worktree's admin dir is not a git directory here.
func IsGitDir(dir string) bool {
	head := filepath.Join(dir, "HEAD")
	fi, err := os.Lstat(head)
	if err != nil {
		return false
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(head)
		if err != nil || !strings.HasPrefix(target, "refs/") {
			return false
		}
	case fi.Mode().IsRegular():
		f, err := OpenRegular(head)
		if err != nil {
			return false
		}
		b, err := io.ReadAll(io.LimitReader(f, maxHeadRef))
		_ = f.Close() // read-only
		if err != nil || !validHeadRef(b) {
			return false
		}
	default:
		return false
	}

	return isDir(filepath.Join(dir, "objects")) && isDir(filepath.Join(dir, "refs"))
}

// validHeadRef reports whether b, the start of a HEAD file, is a symbolic
// ref ("ref:", whitespace, "refs/...") or starts with an object ID. As git's
// get_oid_hex, it reads 40 hex digits, which a SHA-256 ID also starts with.
func validHeadRef(b []byte) bool {
	if rest, ok := strings.CutPrefix(string(b), "ref:"); ok {
		return strings.HasPrefix(strings.TrimLeft(rest, " \t\n\r"), "refs/")
	}
	if len(b) < 40 {
		return false
	}
	for _, c := range b[:40] {
		if !isHexDigit(c) {
			return false
		}
	}

	return true
}

func isHexDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// isDir follows symlinks, as git's access(2) does.
func isDir(p string) bool {
	fi, err := os.Stat(p)

	return err == nil && fi.IsDir()
}

// OpenRegular opens p for reading if the opened file is a regular file. A
// caller's Lstat can be raced: the path can be swapped for a FIFO, whose open
// waits for a writer, or for a link to a device, whose read can block.
// O_NONBLOCK returns the FIFO open at once, the handle's Stat then refuses
// both, and a regular file reads the same with it. Windows ignores the flag:
// syscall.Open passes only the high 12 flag bits on to CreateFile.
func OpenRegular(p string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Clean(p), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = &fs.PathError{Op: "open", Path: p, Err: errNotRegular}
	}
	if err != nil {
		_ = f.Close() // read-only

		return nil, err
	}

	return f, nil
}

// readLinkFile reads a file that holds one path: a .git file ("gitdir:
// <path>") or a worktrees/<id>/gitdir file (no prefix). Only a regular file
// of at most maxLinkFile bytes is read, so a FIFO or a link to a device
// cannot block or flood the scan: the Lstat refuses a symlink, which
// OpenRegular would follow, and OpenRegular a file swapped in after the
// Lstat. The error does not name p; callers do.
func readLinkFile(p, prefix string) (string, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return "", pathless(err)
	}
	if !fi.Mode().IsRegular() {
		return "", errNotRegular
	}
	f, err := OpenRegular(p)
	if err != nil {
		return "", pathless(err)
	}
	defer func() { _ = f.Close() }() // read-only
	b, err := io.ReadAll(io.LimitReader(f, maxLinkFile+1))
	if err != nil {
		return "", pathless(err)
	}
	if len(b) > maxLinkFile {
		return "", fmt.Errorf("larger than %d bytes", maxLinkFile)
	}

	return parseLinkFile(b, prefix)
}

// parseLinkFile returns the path on the first line of b, after prefix.
func parseLinkFile(b []byte, prefix string) (string, error) {
	line, _, _ := strings.Cut(string(b), "\n")
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
	if !ok {
		return "", fmt.Errorf("missing %q line", prefix)
	}
	dir := strings.TrimSpace(rest)
	if dir == "" {
		return "", errors.New("empty path")
	}

	return dir, nil
}

// pathless drops the path an *fs.PathError carries, keeping its cause.
func pathless(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}

	return err
}
