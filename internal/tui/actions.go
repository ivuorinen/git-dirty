package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ivuorinen/git-dirty/internal/action"
	"github.com/ivuorinen/git-dirty/internal/render"
)

// gitNotFound is the failure when git is not on PATH.
const gitNotFound = "git not found on PATH"

// actionMsg is a finished action. gen tags it, as scan batches are tagged,
// so a result from before a quit is dropped.
type actionMsg struct {
	gen  int
	kind action.Kind
	path string
	res  action.Result
}

// confirmation is the page of an action that asks before it runs.
type confirmation struct {
	kind action.Kind
	path string
}

// refusal says why an action cannot start now, or "" when one can. A scan
// and an action never overlap: the rescan after an action would race git.
func (m *Model) refusal() string {
	switch {
	case m.busy:
		return "an action is running"
	case m.scanning:
		return "wait for the scan to finish"
	}

	return ""
}

// requestAction runs k on the line under the cursor, or opens its confirm
// page, or says in the status line why it cannot.
func (m *Model) requestAction(k action.Kind) tea.Cmd {
	if why := m.refusal(); why != "" {
		m.message = k.Verb() + ": " + why

		return nil
	}
	n := m.rows[m.cursor].Node
	if err := k.Check(n.Repo); err != nil {
		m.message = fmt.Sprintf("%s: %v", k.Verb(), err)

		return nil
	}
	if k.Asks() {
		m.confirm = &confirmation{kind: k, path: n.Path}

		return nil
	}

	return m.startAction(k, n.Path)
}

// startAction runs k on one repository.
func (m *Model) startAction(k action.Kind, path string) tea.Cmd {
	m.busy = true
	m.message = ""

	return m.launch(k, path)
}

// launch starts the runner in a goroutine of its own rather than in the
// returned command, so stop can wait for it even when the program quits
// before running the command.
func (m *Model) launch(k action.Kind, path string) tea.Cmd {
	ctx, cancel := context.WithCancel(m.parent)
	m.actGen++
	m.actCancel = cancel
	m.running = k.Verb() + " " + render.Sanitize(path)
	res := make(chan action.Result, 1)
	done := make(chan struct{})
	m.actDone = done
	go func(run func(context.Context, string, action.Kind) action.Result) {
		defer close(done)
		res <- run(ctx, path, k)
	}(m.cfg.Run)
	gen := m.actGen
	wait := func() tea.Msg {
		return actionMsg{gen: gen, kind: k, path: path, res: <-res}
	}

	return tea.Batch(wait, m.spinner.Tick)
}

// finishAction handles a finished action: the next target of a mass run,
// or, for a single action, the status line and a rescan, which also
// re-reads the repository the action changed.
func (m *Model) finishAction(msg actionMsg) tea.Cmd {
	if msg.gen != m.actGen || !m.busy {
		return nil
	}
	m.actCancel()
	if m.mass != nil {
		return m.massDone(msg)
	}
	m.busy = false
	m.message = resultLine(msg.kind, msg.path, msg.res)

	return tea.Batch(m.startScan(), m.spinner.Tick)
}

// resultLine is the status line after an action: ok, or why it failed.
func resultLine(k action.Kind, path string, res action.Result) string {
	head := k.Verb() + " " + render.Sanitize(path)
	if res.Err == nil {
		return head + ": ok"
	}

	return head + ": failed: " + failure(res)
}

// failure says why an action failed: git's last line, sanitized because
// git's output comes from the remote server as well (remote: lines).
func failure(res action.Result) string {
	if errors.Is(res.Err, exec.ErrNotFound) {
		return gitNotFound
	}
	why := res.LastLine()
	if why == "" {
		why = res.Err.Error()
	}

	return render.Sanitize(why)
}

// confirmKey handles the confirm page: y runs the action, n or esc closes
// the page.
func (m *Model) confirmKey(msg tea.KeyPressMsg) tea.Cmd {
	c := m.confirm
	switch msg.String() {
	case keyYes:
		m.confirm = nil

		return m.startAction(c.kind, c.path)
	case keyNo, keyEsc:
		m.confirm = nil
	case keyForceQuit:
		return m.quit()
	}

	return nil
}

// confirmView is the confirm page: the command, the repository and what
// the command changes.
func (m *Model) confirmView() string {
	c := m.confirm
	title := c.kind.String()
	if m.cfg.Color {
		title = lipgloss.NewStyle().Bold(true).Render(title)
	}
	wrap := lipgloss.NewStyle().Width(max(1, m.width))

	return strings.Join([]string{
		"",
		title,
		render.Sanitize(c.path),
		"",
		wrap.Render(c.kind.Warning()),
		"",
		"Run it? y yes • n no",
	}, "\n")
}
