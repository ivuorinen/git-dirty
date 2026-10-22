package tui

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
	"github.com/ivuorinen/git-dirty/internal/gitstat"
	"github.com/ivuorinen/git-dirty/internal/render"
	"github.com/ivuorinen/git-dirty/internal/scan"
	"github.com/ivuorinen/git-dirty/internal/tree"
)

var update = flag.Bool("update", false, "rewrite golden files")

const root = "/home/u/Code"

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
		Render: render.Options{ShowBranch: true, Truncate: true, MainBranches: []string{"main"}},
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
		repo("clean", gitstat.Result{Head: gitstat.Head{Branch: "main", Label: "main", Hash: h}}),
		repo("dirty", gitstat.Result{Head: gitstat.Head{Branch: "feature", Label: "feature", Hash: h}, Status: gitstat.Status{Modified: true}}),
	}
}

func press(m *Model, k string) tea.Cmd {
	r := []rune(k)[0]
	_, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: k})
	return cmd
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
		t.Error("first scan's context not cancelled by rescan")
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
	press(m, "f")
	after := m.lines()
	if len(after) != before-1 {
		t.Fatalf("dirty-only lines = %d, want %d", len(after), before-1)
	}
	if !strings.Contains(strings.Join(after, "\n"), "dirty") || strings.Contains(strings.Join(after, "\n"), "clean") {
		t.Errorf("dirty-only view = %q", after)
	}
	press(m, "f")
	if got := len(m.lines()); got != before {
		t.Errorf("lines after toggling back = %d, want %d", got, before)
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
		t.Error("scan not cancelled on quit")
	}
}

func TestWaitBatch_CollectsUntilClosed(t *testing.T) {
	ch := make(chan scan.Item, 4)
	for _, it := range results() {
		ch <- it
	}
	close(ch)
	msg := waitBatch(7, ch)().(batchMsg)
	if msg.gen != 7 || len(msg.items) != 3 || !msg.done {
		t.Errorf("batch = gen %d, %d items, done %v", msg.gen, len(msg.items), msg.done)
	}
}

func TestWaitBatch_ReturnsAfterWindow(t *testing.T) {
	ch := make(chan scan.Item, 1)
	ch <- results()[0]
	start := time.Now()
	msg := waitBatch(1, ch)().(batchMsg)
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
	m.Update(batchMsg{gen: m.gen, items: results(), done: true})
	v := m.View()
	if !v.AltScreen {
		t.Error("view does not use the alternate screen")
	}
	rows := strings.Split(v.Content, "\n")
	if len(rows) > 24 {
		t.Errorf("view is %d rows, taller than the 24-row window", len(rows))
	}
	// The viewport pads rows to the window width; the golden file stores
	// them trimmed so it satisfies .editorconfig.
	for i, r := range rows {
		rows[i] = strings.TrimRight(r, " ")
	}
	got := strings.Join(rows, "\n") + "\n"
	path := filepath.Join("testdata", "view-80x24.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Errorf("view mismatch\n--- got\n%s\n--- want\n%s", got, want)
	}
}
