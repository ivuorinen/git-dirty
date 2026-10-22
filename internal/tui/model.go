// Package tui is the interactive, read-only tree browser.
package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ivuorinen/git-dirty/internal/render"
	"github.com/ivuorinen/git-dirty/internal/scan"
	"github.com/ivuorinen/git-dirty/internal/tree"
)

// batchWindow is how long results are collected before the tree is redrawn,
// capping redraws at 20 per second while a scan streams in.
const batchWindow = 50 * time.Millisecond

// Config is everything the browser needs from the command line.
type Config struct {
	Root   string
	Scan   scan.Options
	Render render.Options
	Filter tree.Filter
	Color  bool

	// Start begins a scan; nil means scan.Run. Tests substitute a feed.
	Start func(context.Context, scan.Options) <-chan scan.Item
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Run shows the browser until the user quits, then cancels any scan in
// progress and waits for it to stop.
func Run(ctx context.Context, cfg Config, in io.Reader, out io.Writer) error {
	m := New(ctx, cfg)
	final, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
	if fm, ok := final.(*Model); ok {
		fm.stop()
	} else {
		m.stop()
	}

	return err
}

// Model is the bubbletea model.
type Model struct {
	cfg    Config
	parent context.Context

	// The current scan. gen tags its messages so results of a canceled
	// scan are dropped.
	gen      int
	cancel   context.CancelFunc
	items    <-chan scan.Item
	scanning bool
	tree     *tree.Tree
	dirs     int
	start    time.Time
	elapsed  time.Duration

	dirtyOnly bool
	showAll   bool // help expanded

	spinner spinner.Model
	vp      viewport.Model
	help    help.Model
	keys    keyMap
	styles  map[render.Style]lipgloss.Style
	width   int
	height  int
}

// batchMsg carries the results collected during one batch window.
type batchMsg struct {
	gen   int
	items []scan.Item
	done  bool
}

// New returns a model that starts scanning in Init.
func New(ctx context.Context, cfg Config) *Model {
	if cfg.Start == nil {
		cfg.Start = scan.Run
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	vp := viewport.New()
	// f and b are this program's filter and branch toggles.
	vp.KeyMap.PageDown = key.NewBinding(key.WithKeys("pgdown", "space"))
	vp.KeyMap.PageUp = key.NewBinding(key.WithKeys("pgup"))

	m := &Model{
		cfg:     cfg,
		parent:  ctx,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
		vp:      vp,
		help:    help.New(),
		keys:    newKeyMap(),
		styles:  styles(cfg.Color),
		tree:    tree.New(cfg.Root),
		width:   80,
		height:  24,
	}
	if !cfg.Color {
		m.help.Styles = help.Styles{}
	}
	m.layout()

	return m
}

func styles(color bool) map[render.Style]lipgloss.Style {
	plain := lipgloss.NewStyle()
	if !color {
		return map[render.Style]lipgloss.Style{}
	}
	fg := func(c string) lipgloss.Style { return plain.Foreground(lipgloss.Color(c)) }
	// The same ANSI colors as plain mode.
	return map[render.Style]lipgloss.Style{
		render.Clean:  fg("2"),
		render.Dirty:  fg("1"),
		render.NotGit: fg("3"),
		render.Status: fg("3"),
		render.Branch: fg("5"),
		render.Error:  fg("1"),
	}
}

// Init starts the first scan.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.startScan(), m.spinner.Tick)
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()

		return m, nil

	case batchMsg:
		return m, m.addBatch(msg)

	case spinner.TickMsg:
		if !m.scanning {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)

		return m, cmd

	case tea.KeyPressMsg:
		if cmd, ok := m.handleKey(msg); ok {
			return m, cmd
		}
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)

	return m, cmd
}

// View renders the screen.
func (m *Model) View() tea.View {
	title := "git-dirty  " + render.Sanitize(m.cfg.Root)
	if m.cfg.Color {
		title = lipgloss.NewStyle().Bold(true).Render(title)
	}
	v := tea.NewView(strings.Join([]string{title, m.status(), m.vp.View(), m.help.View(m.keys)}, "\n"))
	v.AltScreen = true
	v.WindowTitle = "git-dirty"

	return v
}

// startScan cancels any running scan and begins a new one.
func (m *Model) startScan() tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(m.parent)
	m.cancel = cancel
	m.gen++
	m.items = m.cfg.Start(ctx, m.cfg.Scan)
	m.scanning = true
	m.tree = tree.New(m.cfg.Root)
	m.dirs = 0
	m.start = m.cfg.Now()
	m.elapsed = 0
	m.refresh()

	return waitBatch(m.gen, m.items)
}

// waitBatch blocks for the next result, then gathers whatever else arrives
// within batchWindow.
func waitBatch(gen int, ch <-chan scan.Item) tea.Cmd {
	return func() tea.Msg {
		it, ok := <-ch
		if !ok {
			return batchMsg{gen: gen, done: true}
		}
		items := []scan.Item{it}
		deadline := time.NewTimer(batchWindow)
		defer deadline.Stop()
		for {
			select {
			case it, ok := <-ch:
				if !ok {
					return batchMsg{gen: gen, items: items, done: true}
				}
				items = append(items, it)
			case <-deadline.C:
				return batchMsg{gen: gen, items: items}
			}
		}
	}
}

// stop cancels the scan and waits for its goroutines to finish.
func (m *Model) stop() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.items != nil {
		for range m.items {
		}
	}
}

// addBatch adds one batch of results to the tree and returns the command
// that waits for the next, or nil once the scan is done.
func (m *Model) addBatch(msg batchMsg) tea.Cmd {
	if msg.gen != m.gen {
		return nil // from a scan that was replaced
	}
	for _, it := range msg.items {
		if it.Kind == scan.Dir {
			m.dirs++
		}
		m.tree.Add(it)
	}
	if msg.done {
		m.scanning = false
		m.elapsed = m.cfg.Now().Sub(m.start)
	}
	m.refresh()
	if msg.done {
		return nil
	}

	return waitBatch(m.gen, m.items)
}

// handleKey runs the program's own key bindings. ok is false for any other
// key, which then goes to the viewport.
func (m *Model) handleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, ok bool) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		if m.cancel != nil {
			m.cancel()
		}

		return tea.Quit, true
	case key.Matches(msg, m.keys.Filter):
		m.dirtyOnly = !m.dirtyOnly
		m.refresh()
	case key.Matches(msg, m.keys.Branch):
		m.cfg.Render.ShowBranch = !m.cfg.Render.ShowBranch
		m.refresh()
	case key.Matches(msg, m.keys.Rescan):
		return tea.Batch(m.startScan(), m.spinner.Tick), true
	case key.Matches(msg, m.keys.Help):
		m.showAll = !m.showAll
		m.help.ShowAll = m.showAll
		m.layout()
	case key.Matches(msg, m.keys.Top):
		m.vp.GotoTop()
	case key.Matches(msg, m.keys.Bottom):
		m.vp.GotoBottom()
	default:
		return nil, false
	}

	return nil, true
}

// lines returns the tree as styled text, one string per row.
func (m *Model) lines() []string {
	f := m.cfg.Filter
	f.DirtyOnly = m.dirtyOnly
	tl := m.tree.Lines(f)
	out := make([]string, 0, len(tl))
	for _, l := range tl {
		var b strings.Builder
		for _, s := range render.Line(l, m.cfg.Render) {
			if st, ok := m.styles[s.Style]; ok && s.Text != "" {
				b.WriteString(st.Render(s.Text))
			} else {
				b.WriteString(s.Text)
			}
		}
		out = append(out, b.String())
	}

	return out
}

func (m *Model) refresh() {
	m.vp.SetContentLines(m.lines())
}

// layout sizes the viewport between the two header rows and the help.
func (m *Model) layout() {
	helpRows := lipgloss.Height(m.help.View(m.keys))
	m.vp.SetWidth(m.width)
	m.vp.SetHeight(max(1, m.height-2-helpRows))
}

func (m *Model) status() string {
	repos := m.tree.Repos()
	noun := "repositories"
	if repos == 1 {
		noun = "repository"
	}
	var s string
	if m.scanning {
		s = fmt.Sprintf("%s Scanning… %d %s, %d directories", m.spinner.View(), repos, noun, m.dirs)
	} else {
		s = render.Summary(repos, m.elapsed)
	}
	if m.dirtyOnly {
		s += "  [dirty only]"
	}

	return s
}

type keyMap struct {
	Up, Down, PageUp, PageDown, Top, Bottom key.Binding
	Filter, Branch, Rescan, Help, Quit      key.Binding
}

func newKeyMap() keyMap {
	b := func(keys []string, k, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(k, desc))
	}

	return keyMap{
		Up:       b([]string{"up", "k"}, "↑/k", "up"),
		Down:     b([]string{"down", "j"}, "↓/j", "down"),
		PageUp:   b([]string{"pgup"}, "pgup", "page up"),
		PageDown: b([]string{"pgdown", "space"}, "pgdn", "page down"),
		Top:      b([]string{"g", "home"}, "g", "top"),
		Bottom:   b([]string{"G", "end"}, "G", "bottom"),
		Filter:   b([]string{"f"}, "f", "dirty only"),
		Branch:   b([]string{"b"}, "b", "branch names"),
		Rescan:   b([]string{"r"}, "r", "rescan"),
		Help:     b([]string{"?"}, "?", "help"),
		Quit:     b([]string{"q", "ctrl+c"}, "q", "quit"),
	}
}

// ShortHelp implements help.KeyMap.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Filter, k.Branch, k.Rescan, k.Help, k.Quit}
}

// FullHelp implements help.KeyMap.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown},
		{k.Top, k.Bottom},
		{k.Filter, k.Branch, k.Rescan},
		{k.Help, k.Quit},
	}
}
