// Package tui is the interactive tree browser. Its fetch and pull keys run
// git through internal/action, and its settings page writes the config
// file; everything else only reads.
package tui

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ivuorinen/git-dirty/internal/action"
	"github.com/ivuorinen/git-dirty/internal/config"
	"github.com/ivuorinen/git-dirty/internal/render"
	"github.com/ivuorinen/git-dirty/internal/scan"
	"github.com/ivuorinen/git-dirty/internal/tree"
)

// batchWindow is how long results are collected before the tree is redrawn,
// capping redraws at 20 per second while a scan streams in.
const batchWindow = 50 * time.Millisecond

// The keys of the y/n questions, which the help does not list, and the
// key that quits from every page.
const (
	keyYes       = "y"
	keyNo        = "n"
	keyEsc       = "esc"
	keyEnter     = "enter"
	keyForceQuit = "ctrl+c"
)

// Config is everything the browser needs from the command line.
type Config struct {
	Root   string
	Scan   scan.Options
	Render render.Options
	Filter tree.Filter
	Color  bool

	// Start begins a scan; nil means scan.Run. Tests substitute a feed.
	Start func(context.Context, scan.Options) <-chan scan.Item
	// Run runs an action in a repository; nil means action.Run. Tests
	// substitute a fake.
	Run func(context.Context, string, action.Kind) action.Result
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	// The settings page. ConfigPath is the config file in use. File is its
	// layer: the defaults and the file, without the environment or flags.
	// Overlay applies both to a file layer, before ~ is expanded, to show
	// which rows they override. Reconfigure rebuilds the Config from an
	// edited file layer as main does at startup. Save writes the file
	// layer; nil means config.Save.
	ConfigPath  string
	File        config.Config
	Overlay     func(config.Config) (config.Config, error)
	Reconfigure func(config.Config) (Config, error)
	Save        func(string, config.Config) error
}

// Run shows the browser until the user quits, then cancels any scan in
// progress and waits for it to stop.
func Run(ctx context.Context, cfg Config, in io.Reader, out io.Writer) error {
	m := New(ctx, cfg)
	_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)).Run()
	m.stop() // Update returns m every time, so m is the final model

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

	// The listing as last drawn: the tree lines and their styled text,
	// without the cursor gutter, so a cursor move redraws only the gutter.
	rows   []tree.Line
	styled []string
	cursor int // index into rows
	// selected is the path of the line the cursor keys last chose. The
	// cursor returns to it whenever the listing changes, so lines added
	// above it or a filter that hid it for a while do not move it.
	selected string

	// The running action. actGen tags its result, and actDone is closed
	// when its runner returns, so stop can wait for git.
	busy      bool
	running   string // what runs, for the status line: "fetch /path"
	actGen    int
	actCancel context.CancelFunc
	actDone   chan struct{}

	message  string        // the last refusal or action result, until the next key
	confirm  *confirmation // an action's confirm page, when open
	mass     *massRun      // the mass-action dialog, when open
	settings *settingsPage // the settings page, when open
	askQuit  bool          // q during an action: quitting interrupts git

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
	if cfg.Run == nil {
		cfg.Run = action.Run
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	vp := viewport.New()
	// The viewport keeps only its horizontal scrolling. Its vertical keys
	// would scroll without the cursor (d, u, ctrl+d and ctrl+u half-page),
	// and some are this program's own (f, b, d).
	km := viewport.DefaultKeyMap()
	vp.KeyMap = viewport.KeyMap{Left: km.Left, Right: km.Right}

	m := &Model{
		cfg:     cfg,
		parent:  ctx,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
		vp:      vp,
		help:    help.New(),
		keys:    newKeyMap(),
		tree:    tree.New(cfg.Root),
		width:   80,
		height:  24,
	}
	m.applyColor()
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

	case actionMsg:
		return m, m.finishAction(msg)

	case spinner.TickMsg:
		if !m.scanning && !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)

		return m, cmd

	case tea.KeyPressMsg:
		if cmd, ok := m.handleKey(msg); ok {
			return m, cmd
		}

	case tea.PasteMsg:
		m.paste(msg.Content)

		return m, nil
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)

	return m, cmd
}

// View renders the screen.
func (m *Model) View() tea.View {
	title := m.bold("git-dirty  " + render.Sanitize(m.cfg.Root))
	body, help := m.vp.View(), m.help.View(m.keys)
	switch {
	case m.confirm != nil:
		body, help = m.confirmView(), ""
	case m.mass != nil:
		body, help = m.massView(), ""
	case m.settings != nil:
		body, help = m.settingsView(), ""
	}
	v := tea.NewView(strings.Join([]string{title, m.status(), body, help}, "\n"))
	v.AltScreen = true
	v.WindowTitle = "git-dirty"

	return v
}

// applyColor styles the tree and the help for the color setting.
func (m *Model) applyColor() {
	m.styles = styles(m.cfg.Color)
	m.help.Styles = help.New().Styles
	if !m.cfg.Color {
		m.help.Styles = help.Styles{}
	}
}

// paste adds a terminal paste to the settings row being edited; elsewhere
// a paste does nothing.
func (m *Model) paste(s string) {
	if m.settings != nil && m.settings.editing {
		m.settings.insert(s)
	}
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

// stop cancels the scan and any running action, and waits for the scan's
// goroutines and for git to finish.
func (m *Model) stop() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.actCancel != nil {
		m.actCancel()
	}
	if m.items != nil {
		for range m.items {
		}
	}
	if m.actDone != nil {
		<-m.actDone
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

// handleKey runs the program's own key bindings, by page. ok is false for
// any other key, which then goes to the viewport.
func (m *Model) handleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, ok bool) {
	switch {
	case m.askQuit:
		return m.quitKey(msg), true
	case m.confirm != nil:
		return m.confirmKey(msg), true
	case m.mass != nil:
		return m.massKey(msg), true
	case m.settings != nil:
		return m.settingsKey(msg), true
	}

	return m.browseKey(msg)
}

// browseKey handles the tree page.
func (m *Model) browseKey(msg tea.KeyPressMsg) (cmd tea.Cmd, ok bool) {
	m.message = ""
	if n, ok := m.cursorStep(msg); ok {
		m.move(n)

		return nil, true
	}
	if k, ok := m.actionKind(msg); ok {
		return m.requestAction(k), true
	}
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.requestQuit(msg), true
	case key.Matches(msg, m.keys.Filter):
		m.dirtyOnly = !m.dirtyOnly
		m.refresh()
	case key.Matches(msg, m.keys.Branch):
		m.cfg.Render.ShowBranch = !m.cfg.Render.ShowBranch
		m.refresh()
	case key.Matches(msg, m.keys.Rescan):
		return m.rescan(), true
	case key.Matches(msg, m.keys.Mass):
		m.requestMass()
	case key.Matches(msg, m.keys.Settings):
		m.requestSettings()
	case key.Matches(msg, m.keys.Help):
		m.showAll = !m.showAll
		m.help.ShowAll = m.showAll
		m.layout()
	default:
		return nil, false
	}

	return nil, true
}

// requestQuit quits, except that q asks first while an action runs:
// quitting interrupts git, which can leave a rebase in progress. ctrl+c
// quits at once.
func (m *Model) requestQuit(msg tea.KeyPressMsg) tea.Cmd {
	if m.busy && msg.String() != keyForceQuit {
		m.askQuit = true

		return nil
	}

	return m.quit()
}

// quitKey answers the quit question.
func (m *Model) quitKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case keyYes, keyForceQuit:
		return m.quit()
	case keyNo, keyEsc:
		m.askQuit = false
	}

	return nil
}

// quit cancels the scan and any running action, and ends the program.
// stop then waits for both.
func (m *Model) quit() tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	if m.actCancel != nil {
		m.actCancel()
	}

	return tea.Quit
}

// rescan starts a new scan, unless an action runs: its own rescan follows.
func (m *Model) rescan() tea.Cmd {
	if m.busy {
		m.message = "rescan: an action is running"

		return nil
	}

	return tea.Batch(m.startScan(), m.spinner.Tick)
}

// actionKind returns the action an action key runs; ok is false for any
// other key.
func (m *Model) actionKind(msg tea.KeyPressMsg) (k action.Kind, ok bool) {
	for _, ak := range m.keys.actionKeys() {
		if key.Matches(msg, ak.binding) {
			return ak.kind, true
		}
	}

	return 0, false
}

// cursorStep returns how many lines a cursor key moves the cursor; ok is
// false for any other key.
func (m *Model) cursorStep(msg tea.KeyPressMsg) (n int, ok bool) {
	switch {
	case key.Matches(msg, m.keys.Up):
		return -1, true
	case key.Matches(msg, m.keys.Down):
		return 1, true
	case key.Matches(msg, m.keys.PageUp):
		return -m.vp.Height(), true
	case key.Matches(msg, m.keys.PageDown):
		return m.vp.Height(), true
	case key.Matches(msg, m.keys.Top):
		return -len(m.rows), true
	case key.Matches(msg, m.keys.Bottom):
		return len(m.rows), true
	}

	return 0, false
}

// move moves the cursor n lines, stopping at the first and last line, and
// selects the line it lands on.
func (m *Model) move(n int) {
	m.cursor = min(max(m.cursor+n, 0), len(m.rows)-1)
	m.selected = m.rows[m.cursor].Node.Path
	m.draw()
}

// refresh rebuilds the listing from the tree and the filters, and puts the
// cursor on the selected line, or keeps its index, within the listing, when
// that line is not listed.
func (m *Model) refresh() {
	f := m.cfg.Filter
	f.DirtyOnly = m.dirtyOnly
	m.rows = m.tree.Lines(f)
	m.styled = make([]string, len(m.rows))
	for i, l := range m.rows {
		var b strings.Builder
		for _, s := range render.Line(l, m.cfg.Render) {
			if st, ok := m.styles[s.Style]; ok && s.Text != "" {
				b.WriteString(st.Render(s.Text))
			} else {
				b.WriteString(s.Text)
			}
		}
		m.styled[i] = b.String()
	}
	m.cursor = min(m.cursor, len(m.rows)-1)
	if i := slices.IndexFunc(m.rows, func(l tree.Line) bool { return l.Node.Path == m.selected }); i >= 0 {
		m.cursor = i
	}
	m.draw()
}

// draw puts the listing into the viewport behind the cursor gutter and
// scrolls the cursor into view.
func (m *Model) draw() {
	content := make([]string, len(m.styled))
	for i, s := range m.styled {
		gutter := "  "
		if i == m.cursor {
			gutter = "> "
		}
		content[i] = gutter + s
	}
	m.vp.SetContentLines(content)
	m.follow()
}

// follow scrolls the view the least distance that shows the cursor line.
// viewport.EnsureVisible is not used: it puts a line below the view at the
// top, a page jump on every step past the bottom, and it resets the
// horizontal scroll.
func (m *Model) follow() {
	switch h := m.vp.Height(); {
	case m.cursor < m.vp.YOffset():
		m.vp.SetYOffset(m.cursor)
	case m.cursor >= m.vp.YOffset()+h:
		m.vp.SetYOffset(m.cursor - h + 1)
	}
}

// layout sizes the viewport between the two header rows and the help, and
// keeps the cursor line in view.
func (m *Model) layout() {
	helpRows := lipgloss.Height(m.help.View(m.keys))
	m.vp.SetWidth(m.width)
	m.vp.SetHeight(max(1, m.height-2-helpRows))
	m.follow()
}

func (m *Model) status() string {
	repos := m.tree.Repos()
	var s string
	switch {
	case m.busy:
		s = fmt.Sprintf("%s %s…", m.spinner.View(), m.running)
	case m.scanning:
		s = fmt.Sprintf("%s Scanning… %s, %d directories", m.spinner.View(), count(repos), m.dirs)
	default:
		s = render.Summary(repos, m.elapsed)
	}
	if m.dirtyOnly {
		s += "  [dirty only]"
	}
	if m.message != "" {
		s += "  " + m.message
	}
	if m.askQuit {
		s += "  Quit and interrupt git? y yes • n no"
	}

	return s
}

type keyMap struct {
	Up, Down, PageUp, PageDown, Top, Bottom key.Binding
	Filter, Branch, Rescan, Help, Quit      key.Binding
	Fetch, FetchPrune, Pull, PullRebase     key.Binding
	Mass, Settings                          key.Binding
}

// actionKey pairs an action with its key.
type actionKey struct {
	kind    action.Kind
	binding key.Binding
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
		Filter:   b([]string{"d"}, "d", "dirty only"),
		Branch:   b([]string{"b"}, "b", "branch names"),
		Rescan:   b([]string{"r"}, "r", "rescan"),
		Help:     b([]string{"?"}, "?", "help"),
		Quit:     b([]string{"q", keyForceQuit}, "q", "quit"),

		Fetch:      b([]string{"f"}, "f", "fetch"),
		FetchPrune: b([]string{"F"}, "F", "fetch, prune"),
		Pull:       b([]string{"p"}, "p", "pull, fast-forward"),
		PullRebase: b([]string{"P"}, "P", "pull, rebase"),
		Mass:       b([]string{"m"}, "m", "mass actions"),
		Settings:   b([]string{"s"}, "s", "settings"),
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
		{k.Fetch, k.FetchPrune, k.Pull, k.PullRebase},
		{k.Mass, k.Settings, k.Help, k.Quit},
	}
}

// actionKeys lists the actions with their keys, in menu order.
func (k keyMap) actionKeys() []actionKey {
	return []actionKey{
		{action.Fetch, k.Fetch},
		{action.FetchPrune, k.FetchPrune},
		{action.Pull, k.Pull},
		{action.PullRebase, k.PullRebase},
	}
}
