package tui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/ivuorinen/git-dirty/internal/action"
	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
	"github.com/ivuorinen/git-dirty/internal/gitstat"
	"github.com/ivuorinen/git-dirty/internal/render"
	"github.com/ivuorinen/git-dirty/internal/scan"
	"github.com/ivuorinen/git-dirty/internal/tree"
)

var update = flag.Bool("update", false, "rewrite golden files")

const (
	root       = "/home/u/Code"
	mainBranch = "main"
	bareRepo   = "bare.git"
)

// feeder records every scan the model starts and hands it a channel the
// test controls.
type feeder struct {
	ctxs  []context.Context
	feeds []chan scan.Item
}

func (f *feeder) start(ctx context.Context, _ scan.Options) <-chan scan.Item {
	ch := make(chan scan.Item, 16)
	f.ctxs = append(f.ctxs, ctx)
	f.feeds = append(f.feeds, ch)

	return ch
}

func newTestModel(t *testing.T) (*Model, *feeder) {
	t.Helper()
	f := &feeder{}
	start := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	now := start
	cfg := Config{
		Root:   root,
		Render: render.Options{ShowBranch: true, Truncate: true, MainBranches: []string{mainBranch}},
		Filter: tree.Filter{},
		Start:  f.start,
		Now: func() time.Time {
			now = now.Add(750 * time.Millisecond)

			return now
		},
	}
	m := New(context.Background(), cfg)
	m.Init()

	return m, f
}

func repo(name string, res gitstat.Result) scan.Item {
	res.Path = filepath.Join(root, name)

	return scan.Item{Kind: scan.Repo, Path: res.Path, Rel: []string{name}, Result: res}
}

func results() []scan.Item {
	h, _ := gitrepo.SHA1.ParseHex("1111111111111111111111111111111111111111")

	return []scan.Item{
		{Kind: scan.Dir, Path: root},
		repo("clean", gitstat.Result{Head: gitstat.Head{Branch: mainBranch, Label: mainBranch, Hash: h}}),
		repo(
			"dirty",
			gitstat.Result{
				Head:   gitstat.Head{Branch: "feature", Label: "feature", Hash: h},
				Status: gitstat.Status{Modified: true},
			},
		),
	}
}

// viewResults adds a bare repository with one worktree to results(), for
// the view's golden file. results() itself keeps its three items: the
// counting tests and TestWaitBatch's four-slot channel depend on them.
func viewResults() []scan.Item {
	h, _ := gitrepo.SHA1.ParseHex("1111111111111111111111111111111111111111")
	wt := filepath.Join(root, "bare-wt")

	return append(results(),
		repo(bareRepo, gitstat.Result{Bare: true}),
		scan.Item{
			Kind:     scan.Repo,
			Path:     wt,
			Rel:      []string{bareRepo, "wt"},
			Worktree: true,
			Result:   gitstat.Result{Path: wt, Head: gitstat.Head{Branch: mainBranch, Label: mainBranch, Hash: h}},
		},
	)
}

// TestIconBareWidth: the TUI lays rows out by lipgloss's width, so 🪵 must
// measure two columns, as the other icons do. The golden file cannot show
// it: its rows are stored with the padding trimmed.
func TestIconBareWidth(t *testing.T) {
	if got, want := lipgloss.Width(render.IconBare), lipgloss.Width(render.IconWorktree); got != want || want != 2 {
		t.Errorf("IconBare is %d columns, IconWorktree %d; want both 2", got, want)
	}
}

func press(m *Model, k string) tea.Cmd {
	r := []rune(k)[0]
	_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: k})

	return cmd
}

// runBatch runs a waitBatch command and returns its message.
func runBatch(t *testing.T, cmd tea.Cmd) batchMsg {
	t.Helper()
	msg, ok := cmd().(batchMsg)
	if !ok {
		t.Fatalf("command returned %T, want batchMsg", msg)
	}

	return msg
}

func TestUpdate_BatchAddsResults(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(batchMsg{gen: m.gen, items: results(), done: true})
	if got := m.tree.Repos(); got != 2 {
		t.Errorf("repos = %d, want 2", got)
	}
	if got := len(m.lines()); got != 3 {
		t.Errorf("lines = %d, want 3 (root + 2 repos)", got)
	}
	if m.scanning {
		t.Error("still scanning after the final batch")
	}
}

func TestUpdate_StaleBatchIgnored(t *testing.T) {
	m, f := newTestModel(t)
	press(m, "r")
	if m.gen != 2 || len(f.ctxs) != 2 {
		t.Fatalf("gen = %d, scans started = %d, want 2 and 2", m.gen, len(f.ctxs))
	}
	if f.ctxs[0].Err() == nil {
		t.Error("first scan's context not canceled by rescan")
	}
	m.Update(batchMsg{gen: 1, items: results(), done: true})
	if got := m.tree.Repos(); got != 0 {
		t.Errorf("repos = %d after a stale batch, want 0", got)
	}
	if !m.scanning {
		t.Error("stale done batch ended the current scan")
	}
}

func TestUpdate_FilterToggle(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(batchMsg{gen: m.gen, items: results(), done: true})
	before := len(m.lines())
	press(m, "d")
	after := m.lines()
	if len(after) != before-1 {
		t.Fatalf("dirty-only lines = %d, want %d", len(after), before-1)
	}
	if !strings.Contains(strings.Join(after, "\n"), "dirty") || strings.Contains(strings.Join(after, "\n"), "clean") {
		t.Errorf("dirty-only view = %q", after)
	}
	press(m, "d")
	if got := len(m.lines()); got != before {
		t.Errorf("lines after toggling back = %d, want %d", got, before)
	}
}

// dirtyRepos returns n dirty repositories, enough to scroll a small window;
// the dirty filter keeps every one of them.
func dirtyRepos(n int) []scan.Item {
	items := make([]scan.Item, 0, 1+n)
	items = append(items, scan.Item{Kind: scan.Dir, Path: root})
	for i := range n {
		items = append(items, repo(fmt.Sprintf("r%02d", i), gitstat.Result{
			Head:   gitstat.Head{Branch: mainBranch, Label: mainBranch},
			Status: gitstat.Status{Modified: true},
		}))
	}

	return items
}

// TestUpdate_FilterKey: d is the dirty filter, f is not, and neither d nor
// the viewport's other half-page keys scroll the view.
func TestUpdate_FilterKey(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m.Update(batchMsg{gen: m.gen, items: dirtyRepos(30), done: true})
	press(m, "f")
	if strings.Contains(m.status(), "[dirty only]") {
		t.Error("f turned on the dirty filter")
	}
	press(m, "G")
	offset := m.vp.YOffset()
	if offset == 0 {
		t.Fatal("G did not scroll; the test needs a scrolled view")
	}
	press(m, "d")
	if !strings.Contains(m.status(), "[dirty only]") {
		t.Errorf("status after d = %q, want [dirty only]", m.status())
	}
	if got := m.vp.YOffset(); got != offset {
		t.Errorf("YOffset after d = %d, want %d", got, offset)
	}
	halfPage := []tea.KeyPressMsg{{Code: 'u', Text: "u"}, {Code: 'u', Mod: tea.ModCtrl}, {Code: 'd', Mod: tea.ModCtrl}}
	for _, k := range halfPage {
		m.Update(k)
		if got := m.vp.YOffset(); got != offset {
			t.Errorf("YOffset after %s = %d, want %d", k.String(), got, offset)
		}
	}
	press(m, "d")
	if strings.Contains(m.status(), "[dirty only]") {
		t.Errorf("status after a second d = %q", m.status())
	}
}

// cursorVisible fails the test unless the cursor line is inside the view
// and drawn with the cursor gutter.
func cursorVisible(t *testing.T, m *Model) {
	t.Helper()
	top, h := m.vp.YOffset(), m.vp.Height()
	if m.cursor < top || m.cursor >= top+h {
		t.Fatalf("cursor %d outside the view, rows %d-%d", m.cursor, top, top+h-1)
	}
	row := strings.Split(m.vp.View(), "\n")[m.cursor-top]
	if !strings.HasPrefix(row, "> ") {
		t.Errorf("cursor row %q lacks the cursor gutter", row)
	}
}

func TestCursor_ClampsAtBothEnds(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(batchMsg{gen: m.gen, items: results(), done: true})
	press(m, "k")
	if m.cursor != 0 {
		t.Errorf("cursor after k on the first line = %d, want 0", m.cursor)
	}
	press(m, "j")
	press(m, "j")
	press(m, "j")
	if want := len(m.rows) - 1; m.cursor != want {
		t.Errorf("cursor after j past the last line = %d, want %d", m.cursor, want)
	}
	press(m, "g")
	if m.cursor != 0 {
		t.Errorf("cursor after g = %d, want 0", m.cursor)
	}
	press(m, "G")
	if want := len(m.rows) - 1; m.cursor != want {
		t.Errorf("cursor after G = %d, want %d", m.cursor, want)
	}
	cursorVisible(t, m)
}

func TestCursor_FollowedByTheView(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	m.Update(batchMsg{gen: m.gen, items: dirtyRepos(30), done: true})
	h := m.vp.Height()
	for range h + 2 {
		press(m, "j")
	}
	if m.cursor != h+2 {
		t.Fatalf("cursor = %d, want %d", m.cursor, h+2)
	}
	cursorVisible(t, m)
	if got, want := m.vp.YOffset(), m.cursor-h+1; got != want {
		t.Errorf("YOffset = %d, want %d: one line per step, cursor on the last row", got, want)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.cursor != 2*h+2 {
		t.Errorf("cursor after pgdown = %d, want %d", m.cursor, 2*h+2)
	}
	cursorVisible(t, m)
	press(m, " ")
	press(m, "G")
	cursorVisible(t, m)
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if want := len(m.rows) - 1 - h; m.cursor != want {
		t.Errorf("cursor after pgup = %d, want %d", m.cursor, want)
	}
	cursorVisible(t, m)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 7})
	if m.vp.Height() >= h {
		t.Fatalf("viewport height %d did not shrink from %d", m.vp.Height(), h)
	}
	cursorVisible(t, m)
}

// selectRepo moves the cursor down to the repository called name.
func selectRepo(t *testing.T, m *Model, name string) {
	t.Helper()
	want := filepath.Join(root, name)
	press(m, "g")
	for range m.rows {
		if m.rows[m.cursor].Node.Path == want {
			return
		}
		press(m, "j")
	}
	t.Fatalf("%s is not listed", name)
}

// cursorPath returns the path of the line under the cursor.
func cursorPath(m *Model) string {
	return m.rows[m.cursor].Node.Path
}

func TestSelection_KeptWhenLinesAreAddedAbove(t *testing.T) {
	m, _ := newTestModel(t)
	items := results() // root, clean, dirty
	m.Update(batchMsg{gen: m.gen, items: []scan.Item{items[0], items[2]}})
	selectRepo(t, m, "dirty")
	before := m.cursor
	m.Update(batchMsg{gen: m.gen, items: []scan.Item{items[1]}, done: true})
	if want := filepath.Join(root, "dirty"); cursorPath(m) != want {
		t.Errorf("cursor on %s after a batch, want %s", cursorPath(m), want)
	}
	if m.cursor != before+1 {
		t.Errorf("cursor index = %d, want %d: one line was added above", m.cursor, before+1)
	}
}

// TestSelection_ReturnsAfterTheFilterHidIt: clean is the fourth line; the
// filter leaves two, so the index alone would come back on bare.git.
func TestSelection_ReturnsAfterTheFilterHidIt(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(batchMsg{gen: m.gen, items: viewResults(), done: true})
	selectRepo(t, m, "clean")
	press(m, "d")
	if cursorPath(m) == filepath.Join(root, "clean") {
		t.Fatal("the dirty filter still lists the clean repository")
	}
	cursorVisible(t, m)
	press(m, "d")
	if want := filepath.Join(root, "clean"); cursorPath(m) != want {
		t.Errorf("cursor on %s after d twice, want %s", cursorPath(m), want)
	}
}

func TestUpdate_BranchToggle(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(batchMsg{gen: m.gen, items: results(), done: true})
	if !strings.Contains(strings.Join(m.lines(), "\n"), "(feature") {
		t.Fatal("branch name missing before toggle")
	}
	press(m, "b")
	if strings.Contains(strings.Join(m.lines(), "\n"), "(feature") {
		t.Error("branch name still shown after b")
	}
}

func TestUpdate_Quit(t *testing.T) {
	m, f := newTestModel(t)
	cmd := press(m, "q")
	if cmd == nil {
		t.Fatal("no command from q")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q did not quit")
	}
	if f.ctxs[0].Err() == nil {
		t.Error("scan not canceled on quit")
	}
}

// call is one action the fake runner was asked to run. The runner returns
// what the test sends on answer, or the context's error.
type call struct {
	ctx    context.Context
	path   string
	kind   action.Kind
	answer chan action.Result
}

// fakeRun stands in for action.Run.
type fakeRun struct {
	calls chan call
}

func (f *fakeRun) run(ctx context.Context, path string, k action.Kind) action.Result {
	c := call{ctx: ctx, path: path, kind: k, answer: make(chan action.Result)}
	f.calls <- c
	select {
	case r := <-c.answer:
		return r
	case <-ctx.Done():
		return action.Result{Err: ctx.Err()}
	}
}

// next returns the runner's next call.
func (f *fakeRun) next(t *testing.T) call {
	t.Helper()
	select {
	case c := <-f.calls:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("the runner was not called")
	}

	return call{}
}

// none fails the test if the runner is called.
func (f *fakeRun) none(t *testing.T) {
	t.Helper()
	select {
	case c := <-f.calls:
		t.Fatalf("the runner was called: %s %s", c.kind, c.path)
	case <-time.After(50 * time.Millisecond):
	}
}

// actionItems is results() plus a repository that can be pulled.
func actionItems() []scan.Item {
	h, _ := gitrepo.SHA1.ParseHex("1111111111111111111111111111111111111111")

	return append(results(), repo("tracked", gitstat.Result{
		Head:     gitstat.Head{Branch: mainBranch, Label: mainBranch, Hash: h},
		Upstream: &gitstat.Tracking{Ref: "origin/" + mainBranch},
	}))
}

// newActionModel returns a model with a finished scan of actionItems and
// a fake runner.
func newActionModel(t *testing.T) (*Model, *feeder, *fakeRun) {
	t.Helper()

	return newRunModel(t, actionItems())
}

// newRunModel returns a model with a finished scan of items and a fake
// runner.
func newRunModel(t *testing.T, items []scan.Item) (*Model, *feeder, *fakeRun) {
	t.Helper()
	m, f := newTestModel(t)
	fr := &fakeRun{calls: make(chan call, 4)}
	m.cfg.Run = fr.run
	m.Update(batchMsg{gen: m.gen, items: items, done: true})
	close(f.feeds[0]) // a finished scan closes its channel
	t.Cleanup(func() { m.quit() })

	return m, f, fr
}

// actionResult runs the command an action key returned, which waits for
// the runner, and returns the action's result message.
func actionResult(t *testing.T, cmd tea.Cmd) actionMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatal("the command is not a batch")
	}
	for _, c := range batch {
		if c == nil {
			continue
		}
		if msg, ok := c().(actionMsg); ok {
			return msg
		}
	}
	t.Fatal("no action result in the batch")

	return actionMsg{}
}

func TestAction_RunsOnTheSelectedRepository(t *testing.T) {
	m, f, fr := newActionModel(t)
	selectRepo(t, m, "clean")
	cmd := press(m, "f")
	c := fr.next(t)
	if want := filepath.Join(root, "clean"); c.path != want || c.kind != action.Fetch {
		t.Fatalf("runner called with %s %s, want fetch %s", c.kind, c.path, want)
	}
	if !strings.Contains(m.status(), "fetch "+filepath.Join(root, "clean")+"…") {
		t.Errorf("status while running = %q", m.status())
	}
	c.answer <- action.Result{}
	m.Update(actionResult(t, cmd))
	if len(f.ctxs) != 2 {
		t.Errorf("scans started = %d, want 2: a finished action rescans", len(f.ctxs))
	}
	if !strings.Contains(m.status(), "fetch "+filepath.Join(root, "clean")+": ok") {
		t.Errorf("status after the action = %q", m.status())
	}
}

func TestAction_RefusedWithTheReason(t *testing.T) {
	m, _, fr := newActionModel(t)
	press(m, "f") // the cursor is on the root directory
	if !strings.Contains(m.status(), "fetch: not a repository") {
		t.Errorf("status = %q, want the directory refusal", m.status())
	}
	selectRepo(t, m, "clean")
	press(m, "p")
	if !strings.Contains(m.status(), "pull: no upstream") {
		t.Errorf("status = %q, want the no-upstream refusal", m.status())
	}
	fr.none(t)
}

func TestAction_ConfirmFirst(t *testing.T) {
	m, _, fr := newActionModel(t)
	selectRepo(t, m, "tracked")
	press(m, "P")
	fr.none(t)
	if m.confirm == nil {
		t.Fatal("P did not open the confirm page")
	}
	if v := m.View().Content; !strings.Contains(v, "pull --rebase=true") ||
		!strings.Contains(v, filepath.Join(root, "tracked")) {
		t.Errorf("confirm page lacks the command or the path:\n%s", v)
	}
	press(m, "n")
	fr.none(t)
	if m.confirm != nil {
		t.Fatal("n left the confirm page open")
	}
	press(m, "P")
	press(m, "y")
	if c := fr.next(t); c.kind != action.PullRebase || c.path != filepath.Join(root, "tracked") {
		t.Errorf("runner called with %s %s, want pull --rebase on tracked", c.kind, c.path)
	}
}

func TestAction_RefusedWhileOneRuns(t *testing.T) {
	m, f, fr := newActionModel(t)
	selectRepo(t, m, "clean")
	press(m, "f")
	fr.next(t) // left unanswered: the action keeps running
	selectRepo(t, m, "tracked")
	press(m, "p")
	if !strings.Contains(m.status(), "pull: an action is running") {
		t.Errorf("status = %q, want the busy refusal", m.status())
	}
	press(m, "F")
	if m.confirm != nil {
		t.Error("F opened a confirm page while an action runs")
	}
	press(m, "r")
	if len(f.ctxs) != 1 {
		t.Errorf("r started a scan while an action runs")
	}
	fr.none(t)
}

func TestAction_RefusedWhileScanning(t *testing.T) {
	m, _, fr := newActionModel(t)
	selectRepo(t, m, "clean")
	press(m, "r")
	press(m, "f")
	if !strings.Contains(m.status(), "fetch: wait for the scan to finish") {
		t.Errorf("status = %q, want the scanning refusal", m.status())
	}
	fr.none(t)
}

func TestAction_OutputIsSanitized(t *testing.T) {
	m, _, fr := newActionModel(t)
	selectRepo(t, m, "clean")
	cmd := press(m, "f")
	fr.next(t).answer <- action.Result{Err: errors.New("exit status 1"), Output: "remote: \x1b[31mdenied\n"}
	m.Update(actionResult(t, cmd))
	s := m.status()
	if strings.Contains(s, "\x1b") || !strings.Contains(s, render.Sanitize("\x1b[31m")+"denied") {
		t.Errorf("status = %q, want git's line with the escape escaped", s)
	}
}

// isQuit reports whether cmd ends the program.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)

	return ok
}

// TestAction_QuitAsksFirst: q during an action asks, because quitting
// interrupts git; y quits, cancels git and stop waits for the runner.
func TestAction_QuitAsksFirst(t *testing.T) {
	m, _, _ := newActionModel(t)
	returned := make(chan struct{})
	var seen context.Context
	called := make(chan struct{})
	m.cfg.Run = func(ctx context.Context, _ string, _ action.Kind) action.Result {
		seen = ctx
		close(called)
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // git cleaning up after SIGINT
		close(returned)

		return action.Result{Err: ctx.Err()}
	}
	selectRepo(t, m, "clean")
	press(m, "f")
	<-called
	if isQuit(press(m, "q")) {
		t.Fatal("q quit at once while an action runs")
	}
	if !strings.Contains(m.status(), "Quit and interrupt git?") {
		t.Errorf("status = %q, want the quit question", m.status())
	}
	press(m, "n")
	if m.askQuit || seen.Err() != nil {
		t.Fatal("n did not keep the action running")
	}
	press(m, "q")
	if !isQuit(press(m, "y")) {
		t.Fatal("y did not quit")
	}
	stopped := make(chan struct{})
	go func() { m.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not return")
	}
	select {
	case <-returned:
	default:
		t.Error("stop returned before the runner did")
	}
}

func TestAction_CtrlCQuitsAtOnce(t *testing.T) {
	m, _, fr := newActionModel(t)
	selectRepo(t, m, "clean")
	press(m, "f")
	c := fr.next(t)
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); !isQuit(cmd) {
		t.Fatal("ctrl+c did not quit during an action")
	}
	if c.ctx.Err() == nil {
		t.Error("ctrl+c did not cancel the action")
	}
}

func TestWaitBatch_CollectsUntilClosed(t *testing.T) {
	ch := make(chan scan.Item, 4)
	for _, it := range results() {
		ch <- it
	}
	close(ch)
	msg := runBatch(t, waitBatch(7, ch))
	if msg.gen != 7 || len(msg.items) != 3 || !msg.done {
		t.Errorf("batch = gen %d, %d items, done %v", msg.gen, len(msg.items), msg.done)
	}
}

func TestWaitBatch_ReturnsAfterWindow(t *testing.T) {
	ch := make(chan scan.Item, 1)
	ch <- results()[0]
	start := time.Now()
	msg := runBatch(t, waitBatch(1, ch))
	if msg.done || len(msg.items) != 1 {
		t.Errorf("batch = %d items, done %v", len(msg.items), msg.done)
	}
	if d := time.Since(start); d < batchWindow || d > 10*batchWindow {
		t.Errorf("returned after %v, want about %v", d, batchWindow)
	}
}

func TestStop_DrainsTheScan(t *testing.T) {
	m, f := newTestModel(t)
	go func() {
		<-f.ctxs[0].Done()
		close(f.feeds[0])
	}()
	done := make(chan struct{})
	go func() { m.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop did not return after the scan closed")
	}
}

func TestView_Golden(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(batchMsg{gen: m.gen, items: viewResults(), done: true})
	v := m.View()
	if !v.AltScreen {
		t.Error("view does not use the alternate screen")
	}
	golden(t, "view-80x24.golden", v.Content)
}

// golden compares a view of the 80x24 window with testdata/name, which
// -update rewrites.
func golden(t *testing.T, name, content string) {
	t.Helper()
	rows := strings.Split(content, "\n")
	if len(rows) > 24 {
		t.Errorf("view is %d rows, taller than the 24-row window", len(rows))
	}
	// The viewport pads rows to the window width; the golden file stores
	// them trimmed so it satisfies .editorconfig.
	for i, r := range rows {
		rows[i] = strings.TrimRight(r, " ")
	}
	got := strings.Join(rows, "\n") + "\n"
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Errorf("view mismatch\n--- got\n%s\n--- want\n%s", got, want)
	}
}
