// Package action runs git fetch and git pull in one repository at a time.
// It is the TUI's only way to change a repository, and the only code in
// git-dirty that runs a program (CLAUDE.md).
package action

import (
	"errors"
	"strings"

	"github.com/ivuorinen/git-dirty/internal/gitstat"
)

// Kind is one of the actions the TUI offers.
type Kind int

// The actions. The uppercase key is the destructive variant of the
// lowercase one, and asks first.
const (
	Fetch      Kind = iota // f: git fetch --all
	FetchPrune             // F: git fetch --all --prune
	Pull                   // p: git pull --ff-only --autostash
	PullRebase             // P: git pull --rebase=true
)

// Why Check refuses a line.
var (
	ErrNotRepository = errors.New("not a repository")
	ErrBare          = errors.New("bare repository, no working tree")
	ErrUnreadable    = errors.New("repository could not be read")
	ErrDetached      = errors.New("HEAD is detached")
	ErrUnborn        = errors.New("the branch has no commits yet")
	ErrNoUpstream    = errors.New("no upstream")
	ErrUpstreamGone  = errors.New("upstream gone")
)

const (
	verbFetch = "fetch"
	verbPull  = "pull"
)

// String is the git command line without "git": "fetch --all".
func (k Kind) String() string {
	return strings.Join(k.args(), " ")
}

// Verb is the git command: "fetch" or "pull".
func (k Kind) Verb() string {
	if k.fetches() {
		return verbFetch
	}

	return verbPull
}

// Asks reports whether the TUI asks before running the action: the
// destructive variants do.
func (k Kind) Asks() bool {
	return k == FetchPrune || k == PullRebase
}

// Warning says what the action changes, for the confirm and mass-run pages.
func (k Kind) Warning() string {
	switch k {
	case Fetch:
		return "Downloads new commits from every remote and updates the remote-tracking branches. " +
			"Local branches and the working tree are not changed."
	case FetchPrune:
		return "Downloads new commits from every remote, updates the remote-tracking branches, " +
			"and deletes each remote-tracking branch whose branch the remote no longer has."
	case Pull:
		return "Fast-forwards the current branch to its upstream, stashing uncommitted changes first " +
			"and reapplying them after. A branch that has diverged from its upstream is left as it is."
	case PullRebase:
		return "Rebases the current branch's own commits onto its upstream, rewriting them. " +
			"Uncommitted changes make it fail unless rebase.autoStash is set."
	}

	return ""
}

// Check reports why the action cannot run on the line's repository, or nil
// when it can. res is nil for a directory. Fetch needs only a repository;
// git can read formats internal/git cannot, so an unreadable one is
// fetched too. Pull needs a working tree on a branch with an upstream.
func (k Kind) Check(res *gitstat.Result) error {
	switch {
	case res == nil:
		return ErrNotRepository
	case k.fetches():
		return nil
	case res.Bare:
		return ErrBare
	case res.Err != nil:
		return ErrUnreadable
	case res.Head.Detached():
		return ErrDetached
	case res.Head.Unborn:
		// gitstat does not read the upstream of a branch with no commits.
		return ErrUnborn
	case res.Upstream == nil:
		return ErrNoUpstream
	case res.Upstream.Gone:
		return ErrUpstreamGone
	}

	return nil
}

// args is git's argv after "git". It never holds anything taken from a
// repository: the repository is the working directory.
func (k Kind) args() []string {
	switch k {
	case Fetch:
		return []string{verbFetch, "--all"}
	case FetchPrune:
		return []string{verbFetch, "--all", "--prune"}
	case Pull:
		return []string{verbPull, "--ff-only", "--autostash"}
	case PullRebase:
		return []string{verbPull, "--rebase=true"}
	}

	return nil
}

func (k Kind) fetches() bool {
	return k == Fetch || k == FetchPrune
}
