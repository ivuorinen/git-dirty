package tui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ivuorinen/git-dirty/internal/action"
	"github.com/ivuorinen/git-dirty/internal/config"
	"github.com/ivuorinen/git-dirty/internal/scan"
)

func ctrlC() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} }

func backspace(m *Model) { m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace}) }

func TestNew_FillsTheDefaults(t *testing.T) {
	m := New(context.Background(), Config{})
	if m.cfg.Start == nil || m.cfg.Run == nil || m.cfg.Now == nil {
		t.Error("New left a default unset")
	}
	if m = New(context.Background(), Config{Run: (&fakeRun{}).run}); m.cfg.Run == nil {
		t.Error("New dropped the runner it was given")
	}
}

func TestStopAndQuit_BeforeAnyScan(t *testing.T) {
	m := New(context.Background(), Config{})
	m.stop()
	if !isQuit(m.quit()) {
		t.Error("quit before a scan did not quit")
	}
}

func TestWaitBatch_ClosedAtOnce(t *testing.T) {
	ch := make(chan scan.Item)
	close(ch)
	if msg := runBatch(t, waitBatch(1, ch)); !msg.done || len(msg.items) != 0 {
		t.Errorf("msg = %+v, want done and empty", msg)
	}
}

func TestUpdate_SpinnerOnlyWhileWorking(t *testing.T) {
	m, _, _ := newActionModel(t)
	if _, cmd := m.Update(m.spinner.Tick()); cmd != nil {
		t.Error("the spinner kept ticking with nothing running")
	}
	m.scanning = true
	if _, cmd := m.Update(m.spinner.Tick()); cmd == nil {
		t.Error("the spinner stopped during a scan")
	}
	m.scanning, m.busy = false, true
	if _, cmd := m.Update(m.spinner.Tick()); cmd == nil {
		t.Error("the spinner stopped during an action")
	}
}

func TestUpdate_OtherMessagesReachTheViewport(t *testing.T) {
	m, _ := newTestModel(t)
	m.Update(struct{}{}) // must not panic, and changes nothing
	if m.confirm != nil || m.mass != nil || m.settings != nil {
		t.Error("an unknown message opened a page")
	}
}

func TestUpdate_BranchToggleTwice(t *testing.T) {
	m, _ := newTestModel(t)
	press(m, "b")
	press(m, "b")
	if !m.cfg.Render.ShowBranch {
		t.Error("b twice did not turn the branch back on")
	}
}

func TestUpdate_HelpKey(t *testing.T) {
	m, _ := newTestModel(t)
	press(m, "?")
	if !m.showAll || !m.help.ShowAll {
		t.Error("? did not expand the help")
	}
	press(m, "?")
	if m.showAll || m.help.ShowAll {
		t.Error("? again did not fold the help")
	}
}

func TestPaste_OnlyIntoARowBeingEdited(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	m.Update(tea.PasteMsg{Content: "x"}) // no page
	press(m, "s")
	m.Update(tea.PasteMsg{Content: "x"}) // a page, no row being edited
	if len(m.settings.buf) != 0 {
		t.Errorf("buf = %q, want the paste dropped", string(m.settings.buf))
	}
}

func TestColor_AppliesToTitleAndRows(t *testing.T) {
	m, _ := newTestModel(t)
	if got := m.bold("x"); got != "x" {
		t.Errorf("bold without color = %q", got)
	}
	m.cfg.Color = true
	m.applyColor()
	if got := m.bold("x"); got == "x" || !strings.Contains(got, "x") {
		t.Errorf("bold with color = %q", got)
	}
	m.Update(batchMsg{gen: m.gen, items: results()})
	if !strings.Contains(strings.Join(m.styled, ""), "\x1b[") {
		t.Error("no styled row with color on")
	}
}

func TestFailure(t *testing.T) {
	err := errors.New("exit status 1")
	for _, c := range []struct {
		name string
		res  action.Result
		want string
	}{
		{"git is missing", action.Result{Err: &exec.Error{Name: "git", Err: exec.ErrNotFound}}, gitNotFound},
		{"no output", action.Result{Err: err}, err.Error()},
		{"git's last line", action.Result{Err: err, Output: "a\nfatal: \x1bb\n"}, `fatal: \x1bb`},
	} {
		if got := failure(c.res); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestFinishAction_IgnoresAStaleResult(t *testing.T) {
	m, _, fr := newActionModel(t)
	if _, cmd := m.Update(actionMsg{gen: m.actGen}); cmd != nil {
		t.Error("a result with nothing running did something")
	}
	selectRepo(t, m, "clean")
	press(m, "f")
	c := fr.next(t)
	if _, cmd := m.Update(actionMsg{gen: m.actGen + 1}); cmd != nil || !m.busy {
		t.Error("a result of another generation ended the running action")
	}
	c.answer <- action.Result{}
}

func TestConfirm_Keys(t *testing.T) {
	m, _, _ := newActionModel(t)
	selectRepo(t, m, "tracked")
	press(m, "P")
	press(m, keyEsc)
	if m.confirm != nil {
		t.Error("esc left the confirm page open")
	}
	press(m, "P")
	press(m, "x")
	if m.confirm == nil {
		t.Error("another key closed the confirm page")
	}
	if _, cmd := m.Update(ctrlC()); !isQuit(cmd) {
		t.Error("ctrl+c did not quit from the confirm page")
	}
}

func TestQuitQuestion_Keys(t *testing.T) {
	m, _, fr := newActionModel(t)
	selectRepo(t, m, "clean")
	press(m, "f")
	fr.next(t)
	press(m, "q")
	press(m, "z") // not an answer
	if !m.askQuit {
		t.Error("another key answered the question")
	}
	press(m, keyEsc)
	if m.askQuit {
		t.Error("esc did not answer the question")
	}
	press(m, "q")
	if _, cmd := m.Update(ctrlC()); !isQuit(cmd) {
		t.Error("ctrl+c did not quit from the question")
	}
}

func TestMass_Keys(t *testing.T) {
	m, _, _ := newMassModel(t)
	press(m, "m")
	press(m, "z") // not an action
	if m.mass == nil || m.mass.page != massMenu {
		t.Fatal("another key left the menu")
	}
	press(m, "f")
	press(m, "z") // neither y nor esc
	if m.mass.page != massWarning {
		t.Fatal("another key left the warning page")
	}
	press(m, keyEsc)
	press(m, keyEsc)
	if m.mass != nil {
		t.Fatal("esc on the menu did not close the dialog")
	}

	press(m, "m")
	if _, cmd := m.Update(ctrlC()); !isQuit(cmd) {
		t.Error("ctrl+c did not quit from the dialog")
	}
}

func TestMass_GridKeys(t *testing.T) {
	m, f, fr := newMassModel(t)
	cmd := startMassRun(m, "f")
	press(m, "z") // nothing to do while it runs
	if m.mass.stopping {
		t.Error("another key stopped the run")
	}
	c := fr.next(t)
	press(m, keyEsc)
	if v := m.View().Content; !strings.Contains(v, "stopping after") {
		t.Errorf("no stopping line:\n%s", v)
	}
	c.answer <- action.Result{}
	m.Update(actionResult(t, cmd))
	if !m.mass.ended {
		t.Fatal("the run did not end")
	}
	press(m, "z") // not a cursor key
	if m.mass == nil {
		t.Fatal("another key left the finished grid")
	}
	scans := len(f.ctxs)
	press(m, keyEnter)
	if m.mass != nil || len(f.ctxs) != scans+1 {
		t.Error("enter did not close the grid and rescan")
	}
}

func TestMass_WarningWithoutBareOrSkipped(t *testing.T) {
	m, _, _ := newRunModel(t, []scan.Item{repo("only", results()[0].Result)})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	press(m, "m")
	press(m, "f")
	wantView(t, m, []string{"on 1 repository"}, []string{"Bare repositories", "Skipped:"})
}

func TestSettings_EditKeys(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	press(m, "s")
	for i, f := range settingFields {
		if f.key == "depth" {
			m.settings.cursor = i
		}
	}
	press(m, keyEnter)
	old := string(m.settings.buf)
	m.settings.buf = nil
	backspace(m) // an empty buffer stays empty
	press(m, "7")
	if string(m.settings.buf) != "7" {
		t.Fatalf("buf = %q after typing", string(m.settings.buf))
	}
	if !strings.Contains(m.View().Content, "7█") {
		t.Errorf("the row being edited shows no cursor:\n%s", m.View().Content)
	}
	if !strings.Contains(m.View().Content, "enter keep") {
		t.Errorf("the keys line is not the editor's:\n%s", m.View().Content)
	}
	m.Update(tea.PasteMsg{Content: "a\nb"})
	if string(m.settings.buf) != "7ab" {
		t.Errorf("buf = %q, want the paste without its newline", string(m.settings.buf))
	}
	press(m, keyEsc)
	if m.settings.editing || m.settings.values[m.settings.cursor] != old {
		t.Error("esc did not drop the edit")
	}
	press(m, keyEnter)
	if _, cmd := m.Update(ctrlC()); !isQuit(cmd) {
		t.Error("ctrl+c did not quit while editing")
	}
}

func TestSettings_PageKeys(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	press(m, "s")
	m.settings.cursor = 1
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.settings.cursor != 0 {
		t.Errorf("up left the cursor on %d", m.settings.cursor)
	}
	press(m, "z") // nothing
	if m.settings == nil {
		t.Fatal("another key left the page")
	}
	if !isQuit(press(m, "q")) {
		t.Error("q did not quit from the page")
	}
}

func TestSettings_ToggleBothWays(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	press(m, "s")
	for i, f := range settingFields {
		if f.key == "verbose" {
			m.settings.cursor = i
		}
	}
	before := m.settings.values[m.settings.cursor]
	press(m, keyEnter)
	press(m, keyEnter)
	if m.settings.values[m.settings.cursor] != before {
		t.Errorf("two toggles: %q, want %q", m.settings.values[m.settings.cursor], before)
	}
}

// The first of several invalid rows is the one reported.
func TestSettings_FirstErrorWins(t *testing.T) {
	m, _, fs := newSettingsModel(t)
	press(m, "s")
	editRow(t, m, "depth", "x")
	editRow(t, m, "maxDepth", "y")
	pressSave(m)
	if len(fs.saved) != 0 {
		t.Fatal("an invalid save was written")
	}
	wantView(t, m, []string{`depth: not a number: "x"`}, []string{`maxDepth: not a number`})
}

func TestSettings_BadBoolValue(t *testing.T) {
	for _, f := range settingFields {
		if !f.flag {
			continue
		}
		c := config.Defaults()
		if err := f.set(&c, "maybe"); err == nil || !strings.Contains(err.Error(), "not true or false") {
			t.Errorf("%s: err = %v", f.key, err)
		}

		return
	}
	t.Fatal("no boolean row")
}

func TestSettings_OverlayFailureShowsTheEdits(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	m.cfg.Overlay = func(c config.Config) (config.Config, error) { return c, errors.New("bad variable") }
	press(m, "s")
	if strings.Contains(m.View().Content, "overridden:") {
		t.Errorf("a failed overlay still overrides:\n%s", m.View().Content)
	}
}

func TestSettings_ErrorIsStyledWithColor(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	m.cfg.Color = true
	m.applyColor()
	press(m, "s")
	editRow(t, m, "depth", "x")
	pressSave(m)
	v := m.View().Content
	if !strings.Contains(v, "not a number") || !strings.Contains(v, "\x1b[") {
		t.Errorf("the error is not styled:\n%q", v)
	}
}
