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

const (
	Dir     Kind = iota // a directory that is not a working tree
	Repo                // an inspected working tree; Result is set
	Warning             // a directory that was skipped; Message says why
)

// Item is one scan event. Rel is the path below Root as components; the root
// itself has an empty Rel.
type Item struct {
	Kind    Kind
	Path    string
	Rel     []string
	Result  gitstat.Result
	Message string
}

// Run scans in the background and returns the event stream, closed when the
// scan finishes or ctx is cancelled. Directories are reported as they are
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
		d.walk(opts.Root, nil)
	}()
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// send delivers it unless ctx is cancelled first.
func send(ctx context.Context, ch chan<- Item, it Item) bool {
	select {
	case ch <- it:
		return true
	case <-ctx.Done():
		return false
	}
}

type discoverer struct {
	ctx  context.Context
	opts Options
	out  chan<- Item
	jobs chan<- Item
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
		return send(d.ctx, d.jobs, Item{Path: dir, Rel: rel})
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
		name := e.Name()
		child := filepath.Join(dir, name)
		childRel := append(rel[:len(rel):len(rel)], name)
		switch {
		case slices.Contains(d.opts.Exclude, name):
			if !d.warn(child, childRel, "skipped: excluded name") {
				return false
			}
			continue
		case exists(filepath.Join(child, ".ignore")):
			if !d.warn(child, childRel, "skipped: contains .ignore") {
				return false
			}
			continue
		case len(childRel) > d.opts.MaxDepth:
			if !d.warn(child, childRel, "skipped: maximum depth reached") {
				return false
			}
			continue
		}
		if !d.walk(child, childRel) {
			return false
		}
	}
	return true
}

func (d *discoverer) warn(path string, rel []string, msg string) bool {
	return send(d.ctx, d.out, Item{Kind: Warning, Path: path, Rel: rel, Message: msg})
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}
