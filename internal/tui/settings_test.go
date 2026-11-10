package tui

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/ivuorinen/git-dirty/internal/config"
	"github.com/ivuorinen/git-dirty/internal/scan"
)

const testConfigPath = "/home/u/.config/git-dirty/config.json"

// fakeSettings stands in for main's Reconfigure and config.Save.
type fakeSettings struct {
	err   error           // what Reconfigure returns
	saved []config.Config // each file layer Save was given
}

func (f *fakeSettings) reconfigure(file config.Config) (Config, error) {
	if f.err != nil {
		return Config{}, f.err
	}

	return Config{Root: root, Scan: scan.Options{Root: root, MaxDepth: file.MaxDepth}}, nil
}

func (f *fakeSettings) save(_ string, file config.Config) error {
	f.saved = append(f.saved, file)

	return nil
}

// newSettingsModel returns a model with a finished scan and the settings
// page's inputs: the defaults as the file layer, with checkStash turned on
// by the environment or a flag.
func newSettingsModel(t *testing.T) (*Model, *feeder, *fakeSettings) {
	t.Helper()
	m, f, _ := newActionModel(t)
	fs := &fakeSettings{}
	m.cfg.ConfigPath = testConfigPath
	m.cfg.File = config.Defaults()
	m.cfg.Overlay = func(c config.Config) (config.Config, error) {
		c.CheckStash = true

		return c, nil
	}
	m.cfg.Reconfigure = fs.reconfigure
	m.cfg.Save = fs.save

	return m, f, fs
}

// editRow selects the row for key and replaces its value with value.
func editRow(t *testing.T, m *Model, key, value string) {
	t.Helper()
	for i, f := range settingFields {
		if f.key == key {
			m.settings.cursor = i
			press(m, keyEnter)
			if !m.settings.editing {
				t.Fatalf("enter did not start editing %s", key)
			}
			for range m.settings.buf {
				m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
			}
			m.Update(tea.PasteMsg{Content: value})
			press(m, keyEnter)

			return
		}
	}
	t.Fatalf("no %s row", key)
}

func pressSave(m *Model) tea.Cmd {
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	return cmd
}

func TestSettings_InvalidNumberSavesNothing(t *testing.T) {
	m, f, fs := newSettingsModel(t)
	press(m, "s")
	editRow(t, m, "depth", "x")
	pressSave(m)
	if len(fs.saved) != 0 {
		t.Fatal("Save was called with depth x")
	}
	if !strings.Contains(m.View().Content, `depth: not a number: "x"`) {
		t.Errorf("settings page lacks the error:\n%s", m.View().Content)
	}
	if len(f.ctxs) != 1 {
		t.Error("an invalid save rescanned")
	}
}

func TestSettings_SaveWritesTheFileLayerAndRescans(t *testing.T) {
	m, f, fs := newSettingsModel(t)
	press(m, "s")
	editRow(t, m, "maxDepth", "9")
	pressSave(m)
	if len(fs.saved) != 1 {
		t.Fatalf("Save called %d times, want 1", len(fs.saved))
	}
	want := config.Defaults()
	want.MaxDepth = 9
	if !reflect.DeepEqual(fs.saved[0], want) {
		t.Errorf("saved %+v\nwant the file layer with only maxDepth changed %+v", fs.saved[0], want)
	}
	if m.settings != nil {
		t.Error("the settings page stayed open after a save")
	}
	if len(f.opts) != 2 || f.opts[1].MaxDepth != 9 {
		t.Errorf("scans started with %+v, want a second one with maxDepth 9", f.opts)
	}
	if !strings.Contains(m.status(), "settings saved to "+testConfigPath) {
		t.Errorf("status = %q", m.status())
	}
}

// TestSettings_SavesWithConfigSave: with no Save given, the page writes the
// file with config.Save.
func TestSettings_SavesWithConfigSave(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	path := filepath.Join(t.TempDir(), "config.json")
	m.cfg.ConfigPath, m.cfg.Save = path, nil
	press(m, "s")
	editRow(t, m, "maxDepth", "9")
	pressSave(m)
	got, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxDepth != 9 {
		t.Errorf("the saved file has maxDepth %d, want 9", got.MaxDepth)
	}
}

func TestSettings_KeepsWhitespaceInDir(t *testing.T) {
	m, _, fs := newSettingsModel(t)
	press(m, "s")
	editRow(t, m, "dir", "/src/old ")
	pressSave(m)
	if len(fs.saved) != 1 || fs.saved[0].Dir != "/src/old " {
		t.Errorf("saved %+v, want dir %q exactly", fs.saved, "/src/old ")
	}
}

// TestSettings_UntouchedListKeepsItsEntries: the form shows a list comma
// separated, so re-parsing an unedited row would split an entry with a comma.
func TestSettings_UntouchedListKeepsItsEntries(t *testing.T) {
	m, _, fs := newSettingsModel(t)
	want := []string{"archive,old", "tmp"}
	m.cfg.File.Exclude = slices.Clone(want)
	press(m, "s")
	editRow(t, m, "maxDepth", "9")
	pressSave(m)
	if len(fs.saved) != 1 || !slices.Equal(fs.saved[0].Exclude, want) {
		t.Errorf("saved %+v, want exclude %q untouched", fs.saved, want)
	}
}

func TestSettings_ReconfigureErrorWritesNothing(t *testing.T) {
	m, f, fs := newSettingsModel(t)
	fs.err = errors.New("/missing: no such file or directory")
	press(m, "s")
	editRow(t, m, "dir", "/missing")
	pressSave(m)
	if len(fs.saved) != 0 || len(f.ctxs) != 1 {
		t.Fatalf("a failed reconfigure saved %d times and started %d scans", len(fs.saved), len(f.ctxs))
	}
	if !strings.Contains(m.View().Content, "/missing: no such file or directory") {
		t.Errorf("settings page lacks the error:\n%s", m.View().Content)
	}
}

func TestSettings_Rows(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	press(m, "s")
	v := m.View().Content
	var stash string
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "interactive") {
			t.Errorf("settings page has an interactive row: %q", line)
		}
		if strings.Contains(line, "checkStash") {
			stash = line
		}
	}
	if !strings.Contains(stash, "overridden: true") {
		t.Errorf("checkStash row = %q, want it to show the override", stash)
	}
	if strings.Count(v, "overridden:") != 1 {
		t.Errorf("rows the environment does not change show overridden:\n%s", v)
	}
}

// TestSettings_OverrideFollowsTheEditedValue: a variable equal to the file's
// value shows nothing until the row is edited away from it.
func TestSettings_OverrideFollowsTheEditedValue(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	m.cfg.File.MaxDepth = 5
	m.cfg.Overlay = func(c config.Config) (config.Config, error) {
		c.MaxDepth = 5

		return c, nil
	}
	press(m, "s")
	if v := m.View().Content; strings.Contains(v, "overridden:") {
		t.Errorf("an override equal to the file's value is shown:\n%s", v)
	}
	editRow(t, m, "maxDepth", "9")
	if v := m.View().Content; !strings.Contains(v, "overridden: 5") {
		t.Errorf("the edited row does not show the override:\n%s", v)
	}
}

// TestSettings_FitsAShortWindow: 12 rows hold four settings; the selected
// row, a failed save's reason and the keys stay on screen.
func TestSettings_FitsAShortWindow(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	press(m, "s")
	fits := func() {
		t.Helper()
		if rows := strings.Count(m.View().Content, "\n") + 1; rows > 12 {
			t.Errorf("the page is %d rows in a 12-row window", rows)
		}
	}
	fits()
	for range settingFields {
		press(m, "j")
	}
	fits()
	wantView(t, m, []string{"> verbose", "ctrl+s save"}, []string{"> dir"})
	editRow(t, m, "depth", "x")
	pressSave(m)
	fits()
	wantView(t, m, []string{"> depth", `depth: not a number: "x"`, "ctrl+s save"}, nil)
}

func TestSettings_TogglesAndDiscards(t *testing.T) {
	m, _, fs := newSettingsModel(t)
	press(m, "s")
	for i, f := range settingFields {
		if f.key == "verbose" {
			m.settings.cursor = i
		}
	}
	press(m, keyEnter)
	if v := m.View().Content; !strings.Contains(v, "verbose         true") {
		t.Errorf("enter did not toggle verbose:\n%s", v)
	}
	press(m, keyEsc)
	if m.settings != nil || len(fs.saved) != 0 {
		t.Error("esc did not leave the page without saving")
	}
}

func TestSettings_RefusedWhileAnActionRuns(t *testing.T) {
	m, _, _ := newSettingsModel(t)
	fr := &fakeRun{calls: make(chan call, 1)}
	m.cfg.Run = fr.run
	selectRepo(t, m, "clean")
	press(m, "f")
	fr.next(t)
	press(m, "s")
	if m.settings != nil {
		t.Fatal("s opened the settings page while an action runs")
	}
	if !strings.Contains(m.status(), "settings: an action is running") {
		t.Errorf("status = %q, want the busy refusal", m.status())
	}
}
