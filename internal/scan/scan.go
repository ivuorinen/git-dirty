// Package scan walks a directory tree for git working trees and inspects each
// one concurrently.
package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
	"github.com/ivuorinen/git-dirty/internal/gitstat"
)

// Options configures a scan.
type Options struct {
	Root     string
	MaxDepth int      // directories deeper than this below Root are not entered
	Exclude  []string // directory basenames never entered
	Workers  int      // concurrent inspections; 0 means GOMAXPROCS
	Inspect  gitstat.Options
}

// Kind tells what an Item describes.
type Kind int

// Item kinds.
const (
	Dir     Kind = iota // a directory that is not a working tree
	Repo                // an inspected working tree; Result is set
	Warning             // a directory that was skipped; Message says why
)

// Item is one scan event. Rel is the path below Root as components; the root
// itself has an empty Rel.
type Item struct {
	Kind     Kind
	Path     string
	Rel      []string
	Result   gitstat.Result
	Message  string
	Worktree bool // a linked worktree listed under its repository; Rel ends in its ID
}

// Run scans in the background and returns the event stream, closed when the
// scan finishes or ctx is canceled. Directories are reported as they are
// discovered; repositories as their inspections complete, in any order.
func Run(ctx context.Context, opts Options) <-chan Item {
	out := make(chan Item, 64)
	jobs := make(chan Item, 64)
	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// After cancellation Inspect returns at once and send drops the
			// item; the loop keeps draining jobs so discovery can finish.
			for j := range jobs {
				j.Kind = Repo
				j.Result = gitstat.Inspect(ctx, j.Path, opts.Inspect)
				send(ctx, out, j)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(jobs)
		d := discoverer{ctx: ctx, opts: opts, out: out, jobs: jobs}
		if d.walk(opts.Root, nil) {
			d.sendLinked()
		}
	}()
	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}

// send delivers it unless ctx is canceled first.
func send(ctx context.Context, ch chan<- Item, it Item) bool {
	select {
	case ch <- it:
		return true
	case <-ctx.Done():
		return false
	}
}

// discoverer is used by the discovery goroutine alone.
type discoverer struct {
	ctx  context.Context
	opts Options
	out  chan<- Item
	jobs chan<- Item

	claimed []os.FileInfo // directories of the worktrees repositories listed
	linked  []Item        // linked worktrees the walk found, held until it ends
}

// walk mirrors the bash script: a directory containing a .git entry is a
// working tree and is not descended into; otherwise its subdirectories are
// visited in name order, skipping symlinks, excluded names and directories
// holding an .ignore file.
func (d *discoverer) walk(dir string, rel []string) bool {
	if d.ctx.Err() != nil {
		return false
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return d.repo(dir, rel)
	}
	if !send(d.ctx, d.out, Item{Kind: Dir, Path: dir, Rel: rel}) {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return d.warn(dir, rel, fmt.Sprintf("cannot read: %v", err))
	}
	for _, e := range entries {
		if !e.IsDir() { // files, and symlinks even to directories
			continue
		}
		if !d.visit(dir, rel, e.Name()) {
			return false
		}
	}

	return true
}

// visit walks the subdirectory name of dir, or reports why it is skipped.
func (d *discoverer) visit(dir string, rel []string, name string) bool {
	child := filepath.Join(dir, name)
	// A new array per child: items keep their Rel, so siblings must not share one.
	childRel := slices.Concat(rel, []string{name})
	if reason := d.skipReason(child, name, len(childRel)); reason != "" {
		return d.warn(child, childRel, reason)
	}

	return d.walk(child, childRel)
}

// repo queues the working tree at dir, then the linked worktrees it records
// as its children. A linked worktree found by the walk is held instead: the
// repository that lists it may come later in the walk.
func (d *discoverer) repo(dir string, rel []string) bool {
	wts, linked, err := gitrepo.ListWorktrees(dir)
	if linked {
		d.linked = append(d.linked, Item{Path: dir, Rel: rel})

		return true
	}
	if !send(d.ctx, d.jobs, Item{Path: dir, Rel: rel}) {
		return false
	}
	if err != nil {
		return d.warn(dir, rel, fmt.Sprintf("cannot list worktrees: %v", err))
	}
	for _, wt := range wts {
		if !d.worktree(rel, wt) {
			return false
		}
	}

	return true
}

// worktree queues one listed worktree, or reports it uninspected when it
// cannot be inspected.
func (d *discoverer) worktree(rel []string, wt gitrepo.Worktree) bool {
	it := Item{Path: wt.Path, Rel: slices.Concat(rel, []string{wt.ID}), Worktree: true}
	if wt.Err != nil {
		it.Kind = Repo
		it.Result = gitstat.Result{Path: wt.Path, Err: fmt.Errorf("worktree: %w", wt.Err)}

		return send(d.ctx, d.out, it)
	}
	if fi, err := os.Stat(wt.Path); err == nil {
		d.claimed = append(d.claimed, fi)
	}

	return send(d.ctx, d.jobs, it)
}

// sendLinked queues each held linked worktree that no repository in the scan
// listed, at its own place in the walk.
func (d *discoverer) sendLinked() {
	for _, it := range d.linked {
		if d.isClaimed(it.Path) {
			continue
		}
		if !send(d.ctx, d.jobs, it) {
			return
		}
	}
}

// isClaimed reports whether dir is a worktree some repository listed. The
// listed paths have symlinks resolved and the walk's do not, so files are
// compared, not names.
func (d *discoverer) isClaimed(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		return false
	}

	return slices.ContainsFunc(d.claimed, func(c os.FileInfo) bool { return os.SameFile(c, fi) })
}

// skipReason is the warning for a subdirectory that is not entered, or "".
func (d *discoverer) skipReason(child, name string, depth int) string {
	switch {
	case slices.Contains(d.opts.Exclude, name):
		return "skipped: excluded name"
	case exists(filepath.Join(child, ".ignore")):
		return "skipped: contains .ignore"
	case depth > d.opts.MaxDepth:
		return "skipped: maximum depth reached"
	default:
		return ""
	}
}

func (d *discoverer) warn(path string, rel []string, msg string) bool {
	return send(d.ctx, d.out, Item{Kind: Warning, Path: path, Rel: rel, Message: msg})
}

func exists(p string) bool {
	_, err := os.Lstat(p)

	return err == nil
}
