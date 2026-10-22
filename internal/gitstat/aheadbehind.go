package gitstat

import (
	"bufio"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
)

// commitWalker counts ahead/behind for one repository, reading commits from
// the object store.
type commitWalker struct {
	repo    *gitrepo.Repository
	shallow map[gitrepo.ObjectID]bool
}

func (r *Repo) newCommitWalker() (*commitWalker, error) {
	shallow, err := readShallow(filepath.Join(r.repo.CommonDir, "shallow"), r.repo.Format)
	if err != nil {
		return nil, err
	}

	return &commitWalker{repo: r.repo, shallow: shallow}, nil
}

func readShallow(path string, f gitrepo.Format) (map[gitrepo.ObjectID]bool, error) {
	file, err := os.Open(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }() // read-only: a close error carries no information
	set := make(map[gitrepo.ObjectID]bool)
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		if id, err := f.ParseHex(sc.Text()); err == nil {
			set[id] = true
		}
	}

	return set, sc.Err()
}

// walkSlop is how many common commits are still visited after the walk could
// stop, the same allowance git's limit_list makes (SLOP) for commit-time
// ordering under clock skew.
const walkSlop = 5

const (
	fromA uint8 = 1 << iota
	fromB
	fromBoth = fromA | fromB
)

// commitNode is the part of a commit the walk needs.
type commitNode struct {
	id      gitrepo.ObjectID
	parents []gitrepo.ObjectID
	when    time.Time
}

func (w *commitWalker) node(id gitrepo.ObjectID) (commitNode, error) {
	c, err := w.repo.ReadCommit(id)
	if err != nil {
		return commitNode{}, err
	}

	return commitNode{id: id, parents: c.Parents, when: c.CommitTime}, nil
}

// aheadBehind counts commits reachable from a but not from b (ahead) and from
// b but not from a (behind), like `git rev-list --left-right --count a...b`.
//
// Commits are visited newest first by commit time, each carrying the tips it
// is reachable from. The walk ends once every queued commit is reachable from
// both tips (plus walkSlop more): everything below them is common. Clock skew,
// or commits with equal timestamps, can deliver a tip's bit to a commit after
// it was counted; such a commit is uncounted, queued again and its parents
// updated, so the counts stay exact.
func (w *commitWalker) aheadBehind(ctx context.Context, a, b gitrepo.ObjectID) (ahead, behind int, err error) {
	if a == b {
		return 0, 0, nil
	}
	s := newWalkState()
	for _, tip := range []struct {
		id gitrepo.ObjectID
		f  uint8
	}{{a, fromA}, {b, fromB}} {
		n, err := w.node(tip.id)
		if err != nil {
			return 0, 0, err
		}
		s.add(n, tip.f)
	}

	for steps := 0; s.q.Len() > 0 && s.keepWalking(); steps++ {
		if steps%ctxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return 0, 0, err
			}
		}
		n, f := s.pop()
		if err := w.visitParents(s, n, f); err != nil {
			return 0, 0, err
		}
	}

	return s.ahead, s.behind, nil
}

// visitParents propagates bits f from n to its parents, reading each parent
// that gains a bit. The parents of a shallow boundary commit are not visited.
func (w *commitWalker) visitParents(s *walkState, n commitNode, f uint8) error {
	if w.shallow[n.id] {
		return nil
	}
	for _, p := range n.parents {
		if s.flags[p]|f == s.flags[p] {
			continue // nothing new to propagate
		}
		pn, err := w.node(p)
		if err != nil {
			return err
		}
		s.add(pn, f)
	}

	return nil
}

type walkState struct {
	q        commitQueue
	flags    map[gitrepo.ObjectID]uint8
	queued   map[gitrepo.ObjectID]bool
	done     map[gitrepo.ObjectID]bool
	nonStale int // queued commits not yet reachable from both tips
	late     int // queued commits that gained a bit after being counted
	slop     int // common commits still to visit once the queue is all common
	ahead    int
	behind   int
}

func newWalkState() *walkState {
	return &walkState{
		flags:  make(map[gitrepo.ObjectID]uint8),
		queued: make(map[gitrepo.ObjectID]bool),
		done:   make(map[gitrepo.ObjectID]bool),
		slop:   walkSlop,
	}
}

// add merges bits f into the commit's flags and queues it when that changes
// them.
func (s *walkState) add(n commitNode, f uint8) {
	old := s.flags[n.id]
	nf := old | f
	if nf == old {
		return
	}
	s.flags[n.id] = nf
	if s.queued[n.id] {
		if nf == fromBoth {
			s.nonStale-- // it was counted as non-stale when queued
		}

		return
	}
	late := s.done[n.id]
	if late {
		switch old { // undo the count made with the incomplete bits
		case fromA:
			s.ahead--
		case fromB:
			s.behind--
		}
		s.done[n.id] = false
		s.late++
	}
	s.queued[n.id] = true
	if nf != fromBoth {
		s.nonStale++
	}
	heap.Push(&s.q, queueItem{node: n, late: late})
}

// keepWalking reports whether the walk goes on. While every queued commit is
// common to both tips and none is late, each step spends one unit of slop;
// any other commit restores the full allowance.
func (s *walkState) keepWalking() bool {
	if s.nonStale != 0 || s.late != 0 {
		s.slop = walkSlop

		return true
	}
	if s.slop == 0 {
		return false
	}
	s.slop--

	return true
}

// pop removes the newest queued commit, counts it on the side its bits put it
// and returns it with those bits.
func (s *walkState) pop() (commitNode, uint8) {
	it := s.q[0] // heap.Pop removes the root, q[0]
	heap.Pop(&s.q)
	id := it.node.id
	s.queued[id] = false
	f := s.flags[id]
	if f != fromBoth {
		s.nonStale--
	}
	if it.late {
		s.late--
	}
	s.done[id] = true
	switch f {
	case fromA:
		s.ahead++
	case fromB:
		s.behind++
	}

	return it.node, f
}

type queueItem struct {
	node commitNode
	late bool
}

// commitQueue is a max-heap on commit time.
type commitQueue []queueItem

func (q commitQueue) Len() int           { return len(q) }
func (q commitQueue) Less(i, j int) bool { return q[i].node.when.After(q[j].node.when) }
func (q commitQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }

// Push implements heap.Interface; the walk pushes only queueItem values.
func (q *commitQueue) Push(x any) {
	it, ok := x.(queueItem)
	if !ok {
		panic(fmt.Sprintf("commitQueue: pushed %T, want queueItem", x))
	}
	*q = append(*q, it)
}

func (q *commitQueue) Pop() any {
	old := *q
	it := old[len(old)-1]
	*q = old[:len(old)-1]

	return it
}
