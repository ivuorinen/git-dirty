package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Repository is an opened git directory. It is not safe for concurrent use;
// each goroutine opens its own. Close releases its open files.
type Repository struct {
	GitDir    string // this worktree's git dir: HEAD, index
	CommonDir string // shared dir: config, refs, objects (== GitDir unless a linked worktree)
	Format    Format
	Config    *Config

	packed     map[string]packedRef // packed-refs, loaded on first use
	packedRead bool
	odb        *odb // object database, opened on first use
}

// OpenWorktree opens the repository of the working tree at path, which must
// contain a .git directory or a .git file ("gitdir: <path>") as written for
// linked worktrees and absorbed submodules.
func OpenWorktree(path string) (*Repository, error) {
	gitDir, err := findGitDir(path)
	if err != nil {
		return nil, err
	}
	return Open(gitDir)
}

// Open opens a git directory, following its commondir file if present.
func Open(gitDir string) (*Repository, error) {
	commonDir := gitDir
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	switch {
	case err == nil:
		c := strings.TrimSpace(string(b))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		commonDir = filepath.Clean(c)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
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
	return &Repository{GitDir: gitDir, CommonDir: commonDir, Format: format, Config: cfg}, nil
}

// Close releases the repository's open files: the pack and pack index files
// held open since the first object read.
func (r *Repository) Close() error {
	if r.odb == nil {
		return nil
	}
	return r.odb.close()
}

func findGitDir(worktree string) (string, error) {
	p := filepath.Join(worktree, ".git")
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return p, nil
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: neither a directory nor a gitdir file", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return "", fmt.Errorf("%s: missing \"gitdir:\" line", p)
	}
	dir := strings.TrimSpace(rest)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(worktree, dir)
	}
	return filepath.Clean(dir), nil
}
