package tui

import (
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ivuorinen/git-dirty/internal/action"
	"github.com/ivuorinen/git-dirty/internal/gitstat"
	"github.com/ivuorinen/git-dirty/internal/scan"
	"github.com/ivuorinen/git-dirty/internal/tree"
)

// massItems is a listing with each case massTargets tells apart: a bare
// repository with two worktrees (one pullable, one detached), a detached
// repository, one without an upstream, an unreadable one and a pullable
// one.
func massItems() []scan.Item {
	upstream := &gitstat.Tracking{Ref: "origin/" + mainBranch}
	onMain := gitstat.Head{Branch: mainBranch, Label: mainBranch}
	worktree := func(name string, res gitstat.Result) scan.Item {
		res.Path = filepath.Join(root, name)

		return scan.Item{Kind: scan.Repo, Path: res.Path, Rel: []string{bareRepo, name}, Worktree: true, Result: res}
	}

	return []scan.Item{
		{Kind: scan.Dir, Path: root},
		repo(bareRepo, gitstat.Result{Bare: true}),
		worktree("wt1", gitstat.Result{Head: onMain, Upstream: upstream}),
		worktree("wt2", gitstat.Result{Head: gitstat.Head{Label: "1111111"}}),
		repo("detached", gitstat.Result{Head: gitstat.Head{Label: "2222222"}}),
		repo("noup", gitstat.Result{Head: onMain, Status: gitstat.Status{Modified: true}}),
		repo("ok", gitstat.Result{Head: onMain, Upstream: upstream}),
		repo("unreadable", gitstat.Result{Err: errors.New("index: unknown extension")}),
	}
}

func massLines(dirtyOnly bool) []tree.Line {
	t := tree.New(root)
	for _, it := range massItems() {
		t.Add(it)
	}

	return t.Lines(tree.Filter{DirtyOnly: dirtyOnly})
}

func names(nodes []*tree.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Name)
	}

	return out
}

func TestMassTargets(t *testing.T) {
	fetch := massFetchOrder
	fetchSkips := map[string]int{skipWorktree: 2}
	pull := []string{"wt1", "ok"}
	pullSkips := map[string]int{
		action.ErrBare.Error():       1,
		action.ErrDetached.Error():   2,
		action.ErrNoUpstream.Error(): 1,
		action.ErrUnreadable.Error(): 1,
	}
	tests := []struct {
		kind    action.Kind
		targets []string
		skipped map[string]int
	}{
		{action.Fetch, fetch, fetchSkips},
		{action.FetchPrune, fetch, fetchSkips},
		{action.Pull, pull, pullSkips},
		{action.PullRebase, pull, pullSkips},
	}
	lines := massLines(false)
	for _, tt := range tests {
		targets, skipped := massTargets(lines, tt.kind)
		if got := names(targets); !slices.Equal(got, tt.targets) {
			t.Errorf("%s targets = %q, want %q", tt.kind, got, tt.targets)
		}
		if !maps.Equal(skipped, tt.skipped) {
			t.Errorf("%s skipped = %v, want %v", tt.kind, skipped, tt.skipped)
		}
	}
}

// newMassModel returns an 80x24 model with a finished scan of massItems
// and a fake runner.
func newMassModel(t *testing.T) (*Model, *feeder, *fakeRun) {
	t.Helper()
	m, f, fr := newRunModel(t, massItems())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	return m, f, fr
}

func TestMass_RefusedWhileScanning(t *testing.T) {
	m, _, _ := newMassModel(t)
	press(m, "r")
	press(m, "m")
	if m.mass != nil {
		t.Fatal("m opened the dialog during a scan")
	}
	if !strings.Contains(m.status(), "mass actions: wait for the scan to finish") {
		t.Errorf("status = %q, want the scanning refusal", m.status())
	}
}

func TestMass_RefusedWhileAnActionRuns(t *testing.T) {
	m, _, fr := newMassModel(t)
	selectRepo(t, m, "ok")
	press(m, "f")
	fr.next(t)
	press(m, "m")
	if m.mass != nil {
		t.Fatal("m opened the dialog while an action runs")
	}
	if !strings.Contains(m.status(), "mass actions: an action is running") {
		t.Errorf("status = %q, want the busy refusal", m.status())
	}
}

func TestMass_MenuCounts(t *testing.T) {
	m, _, _ := newMassModel(t)
	press(m, "m")
	v := m.View().Content
	for _, ak := range m.keys.actionKeys() {
		targets, _ := massTargets(m.rows, ak.kind)
		want := fmt.Sprintf("%-28s %s", ak.kind, count(len(targets)))
		if !strings.Contains(v, want) {
			t.Errorf("menu lacks %q:\n%s", want, v)
		}
	}
	press(m, "esc")
	if m.mass != nil {
		t.Error("esc did not close the menu")
	}
}

func TestMass_ZeroTargetsCannotStart(t *testing.T) {
	m, _, fr := newMassModel(t)
	press(m, "d") // the dirty filter leaves nothing to pull
	press(m, "m")
	press(m, "p")
	if len(m.mass.paths) != 0 {
		t.Fatalf("pull targets = %q, want none", m.mass.paths)
	}
	press(m, "y")
	fr.none(t)
	if m.mass.page != massWarning || m.busy {
		t.Error("y started a run with no targets")
	}
	if v := m.View().Content; !strings.Contains(v, "Nothing to run") {
		t.Errorf("warning page lacks the nothing-to-run line:\n%s", v)
	}
}

func TestMass_WarningGolden(t *testing.T) {
	m, _, _ := newMassModel(t)
	press(m, "m")
	press(m, "F")
	golden(t, "mass-warning-80x24.golden", m.View().Content)
	press(m, "esc")
	if m.mass == nil || m.mass.page != massMenu {
		t.Error("esc on the warning page did not go back to the menu")
	}
}

// startMassRun opens the dialog, picks the action key k and starts the
// run, returning the command that waits for the first target.
func startMassRun(m *Model, k string) tea.Cmd {
	press(m, "m")
	press(m, k)

	return press(m, "y")
}

// answer answers the runner's next call with res and hands the result to
// the model, returning the model's next command.
func answer(t *testing.T, m *Model, fr *fakeRun, cmd tea.Cmd, res action.Result) (call, tea.Cmd) {
	t.Helper()
	c := fr.next(t)
	fr.none(t) // never two targets at once
	c.answer <- res
	_, next := m.Update(actionResult(t, cmd))

	return c, next
}

var massFetchOrder = []string{bareRepo, "detached", "noup", "ok", "unreadable"}

func TestMass_OneAtATimeInDisplayOrder(t *testing.T) {
	m, _, fr := newMassModel(t)
	cmd := startMassRun(m, "f")
	order := make([]string, 0, len(massFetchOrder))
	for range massFetchOrder {
		var c call
		c, cmd = answer(t, m, fr, cmd, action.Result{})
		order = append(order, filepath.Base(c.path))
	}
	if !slices.Equal(order, massFetchOrder) {
		t.Errorf("run order = %q, want %q", order, massFetchOrder)
	}
	if cmd != nil || !m.mass.ended || m.busy {
		t.Error("the run did not end after the last target")
	}
	fr.none(t)
}

func TestMass_EscStopsAfterTheRunningTarget(t *testing.T) {
	m, _, fr := newMassModel(t)
	cmd := startMassRun(m, "f")
	c := fr.next(t)
	press(m, "esc")
	c.answer <- action.Result{}
	_, cmd = m.Update(actionResult(t, cmd))
	fr.none(t)
	if cmd != nil || !m.mass.ended {
		t.Fatal("the run went on after esc")
	}
	want := []cell{cellDone, cellWaiting, cellWaiting, cellWaiting, cellWaiting}
	if !slices.Equal(m.mass.cells, want) {
		t.Errorf("cells = %v, want %v", m.mass.cells, want)
	}
	if v := m.View().Content; !strings.Contains(v, "stopped: 4 of 5 not run") {
		t.Errorf("grid page lacks the stop line:\n%s", v)
	}
}

func TestMass_GitNotFoundEndsTheRun(t *testing.T) {
	m, _, fr := newMassModel(t)
	cmd := startMassRun(m, "f")
	_, cmd = answer(t, m, fr, cmd, action.Result{Err: &exec.Error{Name: "git", Err: exec.ErrNotFound}})
	fr.none(t)
	if cmd != nil || !m.mass.ended || m.mass.fatal != gitNotFound {
		t.Fatalf("the run did not end on a missing git: %+v", *m.mass)
	}
	if v := m.View().Content; !strings.Contains(v, "stopped: "+gitNotFound) {
		t.Errorf("grid page lacks the reason:\n%s", v)
	}
}

func TestMass_GridGolden(t *testing.T) {
	m, f, fr := newMassModel(t)
	cmd := startMassRun(m, "f")
	_, cmd = answer(t, m, fr, cmd, action.Result{})
	golden(t, "mass-running-80x24.golden", m.View().Content)
	failed := action.Result{Err: errors.New("exit status 128"), Output: "fatal: could not read from remote\n"}
	_, cmd = answer(t, m, fr, cmd, failed)
	for range 3 {
		_, cmd = answer(t, m, fr, cmd, action.Result{})
	}
	if cmd != nil {
		t.Fatal("the run did not end")
	}
	golden(t, "mass-done-80x24.golden", m.View().Content)
	press(m, "esc")
	if m.mass != nil || len(f.ctxs) != 2 {
		t.Errorf("esc after the run: dialog open %v, scans started %d, want closed and 2", m.mass != nil, len(f.ctxs))
	}
}

func TestMass_GridWrapsAtTheWindow(t *testing.T) {
	m, _, _ := newMassModel(t)
	cells := make([]cell, 7)
	m.Update(tea.WindowSizeMsg{Width: 1, Height: 24})
	if rows := m.grid(cells); len(rows) != len(cells) {
		t.Errorf("width 1: %d rows for %d cells, want one per row", len(rows), len(cells))
	}
	m.Update(tea.WindowSizeMsg{Width: 6, Height: 24})
	if rows := m.grid(cells); len(rows) != 3 || rows[0] != "□ □ □ " {
		t.Errorf("width 6: rows %q, want 3 rows of up to 3 cells", rows)
	}
}

// gridModel returns an 80x24 model on the grid page of a run of n targets.
func gridModel(t *testing.T, n int) *Model {
	t.Helper()
	m, _, _ := newMassModel(t)
	m.mass = &massRun{page: massGrid, kind: action.Fetch, paths: make([]string, n), cells: make([]cell, n)}
	m.busy = true

	return m
}

// TestMass_GridStaysInTheWindow: 1,000 targets are 25 rows at 80 columns,
// more than the window has; the running cell and the keys stay on screen.
func TestMass_GridStaysInTheWindow(t *testing.T) {
	m := gridModel(t, 1000)
	m.mass.next = 900
	m.mass.cells[900] = cellRunning
	v := m.View().Content
	if rows := strings.Count(v, "\n") + 1; rows > 24 {
		t.Errorf("the page is %d rows in a 24-row window", rows)
	}
	for _, want := range []string{"▣", "esc stop after this repository", "fetch --all: 0/1000 done"} {
		if !strings.Contains(v, want) {
			t.Errorf("the page lacks %q:\n%s", want, v)
		}
	}
}

// TestMass_EveryFailureIsReachable: 60 failures do not fit 24 rows, and the
// cursor keys reach the first and the last while the keys stay on screen.
func TestMass_EveryFailureIsReachable(t *testing.T) {
	m, _, _ := newRunModel(t, dirtyRepos(60)) // a run's failures are listed repositories
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	r := &massRun{page: massGrid, kind: action.Fetch, cells: make([]cell, 60), ended: true, next: 60}
	m.mass = r
	for i := range 60 {
		r.cells[i] = cellFailed
		r.failures = append(r.failures, fmt.Sprintf("/repo/%02d: fatal: no", i))
	}
	first, last := "/repo/00:", "/repo/59:"
	if rows := strings.Count(m.View().Content, "\n") + 1; rows > 24 {
		t.Errorf("the page is %d rows in a 24-row window", rows)
	}
	wantView(t, m, []string{first, "Failed (1-", "of 60):", "scroll"}, []string{last})
	press(m, "k")
	wantView(t, m, []string{first}, nil)
	press(m, "j")
	wantView(t, m, []string{"/repo/01:"}, []string{first})
	press(m, "G")
	wantView(t, m, []string{last, "enter or esc back"}, []string{first})
	press(m, "g")
	wantView(t, m, []string{first}, []string{last})
}

// wantView fails the test unless the view holds every string of has and
// none of lacks.
func wantView(t *testing.T, m *Model, has, lacks []string) {
	t.Helper()
	v := m.View().Content
	for _, s := range has {
		if !strings.Contains(v, s) {
			t.Errorf("the view lacks %q:\n%s", s, v)
		}
	}
	for _, s := range lacks {
		if strings.Contains(v, s) {
			t.Errorf("the view holds %q:\n%s", s, v)
		}
	}
}

func TestWindow(t *testing.T) {
	lines := []string{"0", "1", "2", "3", "4", "5"}
	tests := []struct {
		room, focus int
		want        []string
	}{
		{10, 3, lines},
		{2, 0, []string{"0", "1"}},
		{2, 3, []string{"2", "3"}},
		{2, 99, []string{"4", "5"}},
	}
	for _, tt := range tests {
		if got := window(lines, tt.room, tt.focus); !slices.Equal(got, tt.want) {
			t.Errorf("window(room %d, focus %d) = %q, want %q", tt.room, tt.focus, got, tt.want)
		}
	}
}

func TestMass_FailedCellIsNotPlainDone(t *testing.T) {
	m, _, _ := newMassModel(t)
	if got := m.glyph(cellFailed); got != "✗" {
		t.Errorf("failed cell without color = %q, want ✗", got)
	}
	m.styles = styles(true)
	if got := m.glyph(cellFailed); got == m.glyph(cellDone) || !strings.Contains(got, "■") {
		t.Errorf("failed cell with color = %q, want a red ■", got)
	}
}

// TestMassTargets_DirtyFilter: only listed repositories are targets.
// unreadable counts as a problem, so the filter keeps it.
func TestMassTargets_DirtyFilter(t *testing.T) {
	lines := massLines(true)
	targets, skipped := massTargets(lines, action.Fetch)
	if got, want := names(targets), []string{"noup", "unreadable"}; !slices.Equal(got, want) {
		t.Errorf("fetch targets with the dirty filter = %q, want %q", got, want)
	}
	if len(skipped) != 0 {
		t.Errorf("fetch skipped with the dirty filter = %v, want none", skipped)
	}
	targets, skipped = massTargets(lines, action.Pull)
	if len(targets) != 0 {
		t.Errorf("pull targets with the dirty filter = %q, want none", names(targets))
	}
	want := map[string]int{action.ErrNoUpstream.Error(): 1, action.ErrUnreadable.Error(): 1}
	if !maps.Equal(skipped, want) {
		t.Errorf("pull skipped with the dirty filter = %v, want %v", skipped, want)
	}
}
