package gitstat

import (
	"context"
	"errors"
	"strings"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
)

// Tracking compares a commit with another ref.
type Tracking struct {
	Ref    string // short name of the ref compared against, e.g. "origin/main"
	Ahead  int    // commits only on our side
	Behind int    // commits only on Ref
	Gone   bool   // an upstream is configured but its ref does not exist
}

// DefaultBranch is the comparison with the repository's default branch, made
// only when something other than it is checked out.
type DefaultBranch struct {
	Name   string
	Local  *Tracking // local default branch vs its own upstream; nil without either
	Versus *Tracking // checked-out commit vs the default ref
}

// upstreamRef maps branch.<name>.remote/merge to the ref holding the
// upstream, the way `@{u}` does: through the remote's fetch refspecs, or the
// merge ref itself when remote is ".". configured is false when the branch
// has no upstream; a configured upstream with no mapping returns ref "".
func upstreamRef(cfg *gitrepo.Config, branch string) (ref, remote string, configured bool) {
	remote, _ = cfg.Get("branch", branch, "remote")
	merge, _ := cfg.Get("branch", branch, "merge")
	if remote == "" || merge == "" {
		return "", "", false
	}
	if remote == "." {
		return merge, ".", true
	}
	var specs []gitrepo.RefSpec
	for _, s := range cfg.GetAll("remote", remote, "fetch") {
		if rs, err := gitrepo.ParseRefSpec(s); err == nil {
			specs = append(specs, rs)
		}
	}
	if dst, ok := gitrepo.MapRef(specs, merge); ok {
		return dst, remote, true
	}

	return "", remote, true
}

// resolve returns the commit a ref points at; found is false when it does
// not exist.
func (r *Repo) resolve(name string) (id gitrepo.ObjectID, found bool, err error) {
	id, err = r.repo.Resolve(name)
	if errors.Is(err, gitrepo.ErrRefNotFound) {
		return gitrepo.ObjectID{}, false, nil
	}
	if err != nil {
		return gitrepo.ObjectID{}, false, err
	}
	id, err = r.repo.Peel(id)

	return id, err == nil, err
}

// upstream compares from (branch's tip) with branch's upstream. It returns
// nil when no upstream is configured.
func (r *Repo) upstream(
	ctx context.Context,
	w *commitWalker,
	cfg *gitrepo.Config,
	branch string,
	from gitrepo.ObjectID,
) (*Tracking, error) {
	ref, _, configured := upstreamRef(cfg, branch)
	if !configured {
		return nil, nil
	}
	if ref == "" {
		return &Tracking{Gone: true}, nil
	}

	return r.compare(ctx, w, from, ref, true)
}

// compare counts from against ref. A missing ref is reported as Gone when
// missingIsGone, else as nil.
func (r *Repo) compare(
	ctx context.Context,
	w *commitWalker,
	from gitrepo.ObjectID,
	ref string,
	missingIsGone bool,
) (*Tracking, error) {
	id, found, err := r.resolve(ref)
	if err != nil {
		return nil, err
	}
	t := &Tracking{Ref: shortRefName(ref)}
	if !found {
		if !missingIsGone {
			return nil, nil
		}
		t.Gone = true

		return t, nil
	}
	t.Ahead, t.Behind, err = w.aheadBehind(ctx, from, id)
	if err != nil {
		return nil, err
	}

	return t, nil
}

// defaultBranchName finds the default branch: what refs/remotes/<remote>/HEAD
// points at, else the first of mainBranches that exists locally.
func (r *Repo) defaultBranchName(remote string, mainBranches []string) (string, error) {
	prefix := "refs/remotes/" + remote + "/"
	ref, err := r.repo.ReadRef(prefix + "HEAD")
	switch {
	case err == nil && ref.Target != "":
		if name, ok := strings.CutPrefix(ref.Target, prefix); ok {
			return name, nil
		}
	case err != nil && !errors.Is(err, gitrepo.ErrRefNotFound):
		return "", err
	}
	for _, m := range mainBranches {
		_, found, err := r.resolve("refs/heads/" + m)
		if err != nil {
			return "", err
		}
		if found {
			return m, nil
		}
	}

	return "", nil
}

// defaultBranch compares the checkout with the default branch. It returns
// nil when HEAD is unborn, no default branch is found, or the default branch
// is what is checked out.
func (r *Repo) defaultBranch(
	ctx context.Context,
	w *commitWalker,
	cfg *gitrepo.Config,
	head Head,
	mainBranches []string,
) (*DefaultBranch, error) {
	if head.Unborn {
		return nil, nil
	}
	remote := defaultRemote(cfg, head)
	name, err := r.defaultBranchName(remote, mainBranches)
	if err != nil || name == "" || name == head.Branch {
		return nil, err
	}
	d := &DefaultBranch{Name: name}
	if d.Local, err = r.localTracking(ctx, w, cfg, name); err != nil {
		return nil, err
	}
	versusRef, err := r.versusRef(remote, name)
	if err != nil {
		return nil, err
	}
	if d.Versus, err = r.compare(ctx, w, head.Hash, versusRef, false); err != nil {
		return nil, err
	}

	return d, nil
}

// defaultRemote is the remote whose HEAD names the default branch: the
// checked-out branch's upstream remote, else origin.
func defaultRemote(cfg *gitrepo.Config, head Head) string {
	if !head.Detached() {
		if _, rem, ok := upstreamRef(cfg, head.Branch); ok && rem != "." {
			return rem
		}
	}

	return "origin"
}

// localTracking compares the local branch name with its own upstream. It
// returns nil when the branch or its upstream does not exist.
func (r *Repo) localTracking(
	ctx context.Context,
	w *commitWalker,
	cfg *gitrepo.Config,
	name string,
) (*Tracking, error) {
	id, found, err := r.resolve("refs/heads/" + name)
	if err != nil || !found {
		return nil, err
	}

	return r.upstream(ctx, w, cfg, name, id)
}

// versusRef is the ref the checkout is compared with: the remote-tracking
// default branch when it exists, else the local one. The Local comparison
// already shows how stale the local branch is.
func (r *Repo) versusRef(remote, name string) (string, error) {
	ref := "refs/remotes/" + remote + "/" + name
	_, found, err := r.resolve(ref)
	if err != nil {
		return "", err
	}
	if !found {
		return "refs/heads/" + name, nil
	}

	return ref, nil
}
