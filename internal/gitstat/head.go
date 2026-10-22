package gitstat

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
)

// shortHashLen matches git's minimum abbreviation.
const shortHashLen = 7

// Head describes what is checked out.
type Head struct {
	Branch string           // short branch name; empty when HEAD is detached
	Hash   gitrepo.ObjectID // checked-out commit; zero when Unborn
	Unborn bool             // on a branch that has no commits yet
	Label  string           // display name: branch, else exact tag, else short hash
}

// Detached reports whether HEAD points at a commit rather than a branch.
func (h Head) Detached() bool { return h.Branch == "" }

func (r *Repo) head() (Head, error) {
	ref, err := r.repo.ReadRef("HEAD")
	if err != nil {
		return Head{}, err
	}
	if ref.Target != "" {
		name := shortRefName(ref.Target)
		h := Head{Branch: name, Label: name}
		id, err := r.repo.Resolve(ref.Target)
		if errors.Is(err, gitrepo.ErrRefNotFound) {
			h.Unborn = true

			return h, nil
		}
		if err != nil {
			return h, err
		}
		h.Hash = id

		return h, nil
	}

	if ref.ID.IsZero() {
		return Head{}, errors.New("HEAD is neither a branch reference nor a commit id")
	}
	h := Head{Hash: ref.ID, Label: ref.ID.String()[:shortHashLen]}
	if _, err := r.repo.ReadCommit(ref.ID); err != nil {
		return h, fmt.Errorf("HEAD commit %s: %w", h.Label, err)
	}
	tag, err := r.exactTag(ref.ID)
	if err != nil {
		return h, err
	}
	if tag != "" {
		h.Label = tag
	}

	return h, nil
}

// exactTag returns the name of a tag pointing at commit, lightweight or
// annotated, like `git describe --tags --exact-match`. With several, the
// lexicographically first is returned.
func (r *Repo) exactTag(commit gitrepo.ObjectID) (string, error) {
	tags, err := r.repo.Tags()
	if err != nil {
		return "", err
	}
	var names []string
	for _, t := range tags {
		target := t.Peeled
		if target.IsZero() {
			if target, err = r.repo.Peel(t.ID); err != nil {
				return "", err
			}
		}
		if target == commit {
			names = append(names, strings.TrimPrefix(t.Name, "refs/tags/"))
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	sort.Strings(names)

	return names[0], nil
}
