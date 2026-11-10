package tui

import (
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ivuorinen/git-dirty/internal/action"
	"github.com/ivuorinen/git-dirty/internal/render"
	"github.com/ivuorinen/git-dirty/internal/tree"
)

// skipWorktree is why a mass fetch skips a worktree listed under its
// repository: worktrees share their repository's remote-tracking refs, so
// the repository's fetch updates them.
const skipWorktree = "worktree, fetched with its repository"

// massChrome is the window rows the grid page uses besides the grid and the
// failures: title and status, then a blank line, the counts and a blank line
// above the grid, a blank line and the progress line under it, a blank line
// and the keys, and the empty help row View adds.
const massChrome = 10

// massPage is the page the mass-action dialog shows.
type massPage int

const (
	massMenu    massPage = iota // the four actions and their target counts
	massWarning                 // one action's warning, before it starts
	massGrid                    // the run's progress, then its result
)

// cell is the state of one target of a mass run.
type cell int

const (
	cellWaiting cell = iota
	cellRunning
	cellDone
	cellFailed
)

// massRun is the mass-action dialog, which m opens on the menu.
type massRun struct {
	page     massPage
	kind     action.Kind
	paths    []string // the targets, in display order
	bare     int      // how many targets are bare repositories
	skipped  map[string]int
	cells    []cell
	failures []string // "path: git's last line", in run order
	failTop  int      // the first failure shown, when they do not all fit
	next     int      // the target running, or the next to run
	stopping bool     // esc during the run: start nothing more
	ended    bool
	fatal    string // why the run ended early: git is missing
}

// massTargets returns the listed repositories k runs on in a mass run, in
// display order, and how many listed repositories it skips, by reason.
// What is listed is what runs: the dirty filter narrows the targets.
func massTargets(lines []tree.Line, k action.Kind) (targets []*tree.Node, skipped map[string]int) {
	skipped = map[string]int{}
	for _, l := range lines {
		n := l.Node
		if n.Repo == nil {
			continue // a directory
		}
		if err := k.Check(n.Repo); err != nil {
			skipped[err.Error()]++

			continue
		}
		if n.Worktree && k.Verb() == "fetch" {
			skipped[skipWorktree]++

			continue
		}
		targets = append(targets, n)
	}

	return targets, skipped
}

// requestMass opens the mass-action menu. Its targets are the listing, so
// it waits for the scan to finish.
func (m *Model) requestMass() {
	if why := m.refusal(); why != "" {
		m.message = "mass actions: " + why

		return
	}
	m.mass = &massRun{page: massMenu}
}

// massKey handles the mass-action pages.
func (m *Model) massKey(msg tea.KeyPressMsg) tea.Cmd {
	if key.Matches(msg, m.keys.Quit) {
		return m.requestQuit(msg)
	}
	switch m.mass.page {
	case massMenu:
		m.massMenuKey(msg)
	case massWarning:
		return m.massWarningKey(msg)
	case massGrid:
		return m.massGridKey(msg)
	}

	return nil
}

// massMenuKey: an action's key opens its warning page; esc closes the
// dialog.
func (m *Model) massMenuKey(msg tea.KeyPressMsg) {
	if msg.String() == keyEsc {
		m.mass = nil

		return
	}
	k, ok := m.actionKind(msg)
	if !ok {
		return
	}
	targets, skipped := massTargets(m.rows, k)
	r := &massRun{page: massWarning, kind: k, skipped: skipped}
	for _, n := range targets {
		r.paths = append(r.paths, n.Path)
		if n.Repo.Bare {
			r.bare++
		}
	}
	m.mass = r
}

// massWarningKey: y starts the run, when it has targets; esc goes back to
// the menu.
func (m *Model) massWarningKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case keyYes:
		if len(m.mass.paths) > 0 {
			return m.startMass()
		}
	case keyEsc:
		m.mass = &massRun{page: massMenu}
	}

	return nil
}

// massGridKey: esc during the run stops it after the running target;
// after the run, enter or esc goes back to the tree and rescans, and the
// cursor keys scroll the failures.
func (m *Model) massGridKey(msg tea.KeyPressMsg) tea.Cmd {
	r := m.mass
	k := msg.String()
	switch {
	case !r.ended && k == keyEsc:
		r.stopping = true
	case r.ended && (k == keyEsc || k == keyEnter):
		m.mass = nil

		return tea.Batch(m.startScan(), m.spinner.Tick)
	case r.ended:
		if n, ok := m.cursorStep(msg); ok {
			r.failTop = m.clampFailTop(r.failTop + n)
		}
	}

	return nil
}

// clampFailTop keeps the first failure shown within the list for the
// current window, so a resize cannot leave the list scrolled past its end.
func (m *Model) clampFailTop(top int) int {
	r := m.mass
	_, room := m.massRooms(len(m.grid(r.cells)))

	return min(max(top, 0), max(len(r.failures)-room, 0))
}

// startMass starts the run: one target at a time, in display order.
// Worktrees of one repository share its refs and lock files, so
// concurrent fetches would fail with "cannot lock ref".
func (m *Model) startMass() tea.Cmd {
	r := m.mass
	r.page = massGrid
	r.cells = make([]cell, len(r.paths))
	m.busy = true

	return m.massNext()
}

// massNext starts the next target, or ends the run.
func (m *Model) massNext() tea.Cmd {
	r := m.mass
	if r.stopping || r.fatal != "" || r.next >= len(r.paths) {
		r.ended = true
		m.busy = false

		return nil
	}
	r.cells[r.next] = cellRunning

	return m.launch(r.kind, r.paths[r.next])
}

// massDone records the running target's result and starts the next. git
// missing ends the run: every other target would fail the same way.
func (m *Model) massDone(msg actionMsg) tea.Cmd {
	r := m.mass
	switch {
	case msg.res.Err == nil:
		r.cells[r.next] = cellDone
	case errors.Is(msg.res.Err, exec.ErrNotFound):
		r.cells[r.next] = cellFailed
		r.fatal = gitNotFound
	default:
		r.cells[r.next] = cellFailed
		r.failures = append(r.failures, render.Sanitize(msg.path)+": "+failure(msg.res))
	}
	r.next++

	return m.massNext()
}

// massView renders the mass-action page.
func (m *Model) massView() string {
	switch m.mass.page {
	case massMenu:
		return m.massMenuView()
	case massWarning:
		return m.massWarningView()
	case massGrid:
		return m.massGridView()
	}

	return ""
}

func (m *Model) massMenuView() string {
	lines := []string{"", m.bold("Mass actions: one action on every listed repository"), ""}
	for _, ak := range m.keys.actionKeys() {
		targets, _ := massTargets(m.rows, ak.kind)
		lines = append(lines, fmt.Sprintf("  %-2s %-28s %s", ak.binding.Help().Key, ak.kind, count(len(targets))))
	}

	return strings.Join(append(lines, "", "Choose an action • esc back"), "\n")
}

func (m *Model) massWarningView() string {
	r := m.mass
	wrap := lipgloss.NewStyle().Width(max(1, m.width))
	lines := []string{
		"",
		m.bold(fmt.Sprintf("%s on %s", r.kind, count(len(r.paths)))),
		"",
		wrap.Render(r.kind.Warning()),
	}
	if len(r.paths) == 0 {
		lines = append(lines, "", "Nothing to run: no listed repository fits.")
	} else {
		lines = append(lines, "",
			"One repository at a time, in the order listed; this can take a long time.",
			wrap.Render("git runs each repository's hooks and helpers, as it does in a terminal."))
		if r.bare > 0 {
			lines = append(lines, fmt.Sprintf("Bare repositories: %d.", r.bare))
		}
	}
	if len(r.skipped) > 0 {
		reasons := make([]string, 0, len(r.skipped))
		for _, why := range slices.Sorted(maps.Keys(r.skipped)) {
			reasons = append(reasons, fmt.Sprintf("%d %s", r.skipped[why], why))
		}
		lines = append(lines, wrap.Render("Skipped: "+strings.Join(reasons, "; ")+"."))
	}
	prompt := "Start? y yes • esc back"
	if len(r.paths) == 0 {
		prompt = "esc back"
	}

	return strings.Join(append(lines, "", prompt), "\n")
}

// massGridView is the progress grid, one cell per target, and after the
// run the failures, which scroll when they do not fit: the list is gone
// once the page is left, so every failure has to be reachable on it.
func (m *Model) massGridView() string {
	r := m.mass
	done, failed := 0, 0
	for _, c := range r.cells {
		switch c {
		case cellDone:
			done++
		case cellFailed:
			done++
			failed++
		case cellWaiting, cellRunning:
		}
	}
	rows := m.grid(r.cells)
	gridRoom, failRoom := m.massRooms(len(rows))
	top := []string{"", m.bold(fmt.Sprintf("%s: %d/%d done, %d failed", r.kind, done, len(r.cells), failed)), ""}
	top = append(top, window(rows, gridRoom, r.next/m.cellsPerRow())...)
	top = append(top, "", m.massProgress())
	bottom := []string{"", "enter or esc back"}
	if !r.ended {
		bottom = []string{"", "esc stop after this repository"}
	}
	if failRoom > 0 {
		from, head := m.clampFailTop(r.failTop), "Failed:"
		if len(r.failures) > failRoom {
			head = fmt.Sprintf("Failed (%d-%d of %d):", from+1, from+failRoom, len(r.failures))
			bottom[1] = "↑/↓ scroll • enter or esc back"
		}
		top = append(top, "", head)
		top = append(top, r.failures[from:from+failRoom]...)
	}

	return strings.Join(append(top, bottom...), "\n")
}

// massRooms divides the window between the grid and the failure list, so
// the progress line and the keys stay on screen however many targets a run
// has. During a run the grid may use all of it; after a run with failures
// the list gets what it needs up to half the window, and more when the
// grid is short.
func (m *Model) massRooms(gridRows int) (gridRoom, failRoom int) {
	r := m.mass
	avail := max(1, m.height-massChrome)
	if !r.ended || len(r.failures) == 0 {
		return min(gridRows, avail), 0
	}
	avail = max(2, avail-2) // the blank line and the "Failed:" heading
	failRoom = min(len(r.failures), max(1, avail-min(gridRows, avail/2)))

	return min(gridRows, avail-failRoom), failRoom
}

// cellsPerRow is how many two-column cells fit the window's width.
func (m *Model) cellsPerRow() int {
	return max(1, m.width/2)
}

// window returns at most room of lines, centered on the line at focus as
// far as the ends allow.
func window(lines []string, room, focus int) []string {
	if len(lines) <= room {
		return lines
	}
	start := min(max(focus-room/2, 0), len(lines)-room)

	return lines[start : start+room]
}

// grid lays the cells out in rows that fit the window, two columns each.
func (m *Model) grid(cells []cell) []string {
	perRow := m.cellsPerRow()
	var rows []string
	var b strings.Builder
	for i, c := range cells {
		b.WriteString(m.glyph(c))
		b.WriteString(" ")
		if (i+1)%perRow == 0 || i == len(cells)-1 {
			rows = append(rows, b.String())
			b.Reset()
		}
	}

	return rows
}

// glyph draws one cell. A failure is a red done cell with color, and a
// glyph of its own without, so it is never shown as plain done.
func (m *Model) glyph(c cell) string {
	switch c {
	case cellRunning:
		return "▣"
	case cellDone:
		return "■"
	case cellFailed:
		if st, ok := m.styles[render.Error]; ok {
			return st.Render("■")
		}

		return "✗"
	case cellWaiting:
	}

	return "□"
}

// massProgress is the line under the grid: the running repository, or how
// the run ended.
func (m *Model) massProgress() string {
	r := m.mass
	switch {
	case !r.ended && r.stopping:
		return "stopping after " + m.running
	case !r.ended:
		return "running " + m.running
	case r.fatal != "":
		return "stopped: " + r.fatal
	case r.stopping:
		return fmt.Sprintf("stopped: %d of %d not run", len(r.cells)-r.next, len(r.cells))
	}

	return "finished"
}

// count is "1 repository" or "N repositories".
func count(n int) string {
	if n == 1 {
		return "1 repository"
	}

	return fmt.Sprintf("%d repositories", n)
}

// bold is s in bold, with color on.
func (m *Model) bold(s string) string {
	if !m.cfg.Color {
		return s
	}

	return lipgloss.NewStyle().Bold(true).Render(s)
}
