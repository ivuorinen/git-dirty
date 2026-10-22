package gitstat

import (
	"context"
	"fmt"
)

// Options selects what Inspect checks.
type Options struct {
	CheckUntracked bool
	CheckStash     bool
	MainBranches   []string  // branch names treated as the default when origin/HEAD is absent
	Excludes       *Excludes // shared core.excludesFile patterns; may be nil
}

// Status holds the dirty-state letters.
type Status struct {
	Modified  bool // M: worktree differs from the index
	Staged    bool // S: index differs from HEAD
	Untracked bool // ?: untracked, unignored files exist
	Stashed   bool // $: refs/stash exists
}

// Result is everything known about one working tree. When Err is set, the
// fields filled before the failing stage are still valid.
type Result struct {
	Path     string
	Head     Head
	Status   Status
	Upstream *Tracking      // nil without a configured upstream
	Default  *DefaultBranch // nil when on the default branch or none is found
	Err      error
}

// Dirty is the bash script's ❌ condition: any status letter, or commits not
// yet pushed to the upstream. Being behind does not make a repository dirty.
func (r Result) Dirty() bool {
	s := r.Status

	return s.Modified || s.Staged || s.Untracked || s.Stashed ||
		(r.Upstream != nil && r.Upstream.Ahead > 0)
}

// Inspect examines the working tree at path. It never returns an error
// directly: failures are recorded in Result.Err, prefixed with the stage.
func Inspect(ctx context.Context, path string, opts Options) Result {
	res := Result{Path: path}
	res.Err = inspect(ctx, path, opts, &res)

	return res
}

func inspect(ctx context.Context, path string, opts Options, res *Result) (err error) {
	r, err := Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() {
		if cerr := r.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close: %w", cerr)
		}
	}()

	if res.Head, err = r.head(); err != nil {
		return fmt.Errorf("head: %w", err)
	}
	if err = r.inspectStatus(ctx, opts, res); err != nil {
		return err
	}
	if res.Head.Unborn {
		return nil
	}

	return r.inspectTracking(ctx, opts, res)
}

// inspectStatus fills res.Status, the dirty-state letters. res.Head must be
// set.
func (r *Repo) inspectStatus(ctx context.Context, opts Options, res *Result) error {
	idx, err := r.repo.ReadIndex()
	if err != nil {
		return fmt.Errorf("index: %w", err)
	}
	cc, err := r.coreConfig()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if res.Status.Modified, err = r.modified(ctx, idx, cc, opts.Excludes); err != nil {
		return fmt.Errorf("modified: %w", err)
	}
	if res.Status.Staged, err = r.staged(ctx, idx, res.Head.Hash); err != nil {
		return fmt.Errorf("staged: %w", err)
	}
	if opts.CheckUntracked {
		if res.Status.Untracked, err = r.untracked(ctx, idx, cc, opts.Excludes); err != nil {
			return fmt.Errorf("untracked: %w", err)
		}
	}
	if opts.CheckStash {
		if res.Status.Stashed, err = r.stashed(); err != nil {
			return fmt.Errorf("stash: %w", err)
		}
	}

	return nil
}

// inspectTracking fills res.Upstream and res.Default. res.Head must be set
// and born.
func (r *Repo) inspectTracking(ctx context.Context, opts Options, res *Result) error {
	w, err := r.newCommitWalker()
	if err != nil {
		return fmt.Errorf("shallow: %w", err)
	}
	cfg := r.repo.Config
	if !res.Head.Detached() {
		if res.Upstream, err = r.upstream(ctx, w, cfg, res.Head.Branch, res.Head.Hash); err != nil {
			return fmt.Errorf("ahead/behind: %w", err)
		}
	}
	if res.Default, err = r.defaultBranch(ctx, w, cfg, res.Head, opts.MainBranches); err != nil {
		return fmt.Errorf("default branch: %w", err)
	}

	return nil
}
