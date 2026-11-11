package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/ivuorinen/git-dirty/internal/config"
	"github.com/ivuorinen/git-dirty/internal/render"
)

// settingField is one row of the settings page: a config file key, shown
// and edited as text.
type settingField struct {
	key  string
	flag bool // a boolean, which enter toggles
	get  func(config.Config) string
	set  func(*config.Config, string) error
}

// settingFields are the rows, in the config file's order. interactive is
// not one: only --plain turns it off and nothing turns it on, so saving it
// off would leave editing the file by hand as the only way back.
var settingFields = []settingField{
	textSetting("dir", func(c *config.Config) *string { return &c.Dir }),
	listSetting("exclude", func(c *config.Config) *[]string { return &c.Exclude }),
	listSetting("mainBranches", func(c *config.Config) *[]string { return &c.MainBranches }),
	numberSetting("depth", func(c *config.Config) *int { return &c.Depth }),
	numberSetting("maxDepth", func(c *config.Config) *int { return &c.MaxDepth }),
	flagSetting("checkStash", func(c *config.Config) *bool { return &c.CheckStash }),
	flagSetting("checkUntracked", func(c *config.Config) *bool { return &c.CheckUntracked }),
	flagSetting("color", func(c *config.Config) *bool { return &c.Color }),
	flagSetting("truncate", func(c *config.Config) *bool { return &c.Truncate }),
	flagSetting("showBranch", func(c *config.Config) *bool { return &c.ShowBranch }),
	flagSetting("verbose", func(c *config.Config) *bool { return &c.Verbose }),
}

func textSetting(k string, p func(*config.Config) *string) settingField {
	return settingField{
		key: k,
		get: func(c config.Config) string { return *p(&c) },
		set: func(c *config.Config, s string) error {
			*p(c) = s // exactly as given: a path may end in a space

			return nil
		},
	}
}

// listSetting is a comma-separated list, as -e takes it.
func listSetting(k string, p func(*config.Config) *[]string) settingField {
	return settingField{
		key: k,
		get: func(c config.Config) string { return strings.Join(*p(&c), ", ") },
		set: func(c *config.Config, s string) error {
			*p(c) = config.SplitList(s)

			return nil
		},
	}
}

func numberSetting(k string, p func(*config.Config) *int) settingField {
	return settingField{
		key: k,
		get: func(c config.Config) string { return strconv.Itoa(*p(&c)) },
		set: func(c *config.Config, s string) error {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				return fmt.Errorf("%s: not a number: %q", k, s)
			}
			*p(c) = n

			return nil
		},
	}
}

func flagSetting(k string, p func(*config.Config) *bool) settingField {
	return settingField{
		key:  k,
		flag: true,
		get:  func(c config.Config) string { return strconv.FormatBool(*p(&c)) },
		set: func(c *config.Config, s string) error {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return fmt.Errorf("%s: not true or false: %q", k, s)
			}
			*p(c) = b

			return nil
		},
	}
}

// settingsPage is the settings page: the config file's layer as text, one
// value per row, edited until it is saved or left.
//
// A row is edited as a plain buffer: typing and a terminal paste append,
// backspace deletes. bubbles/textinput was not used: it imports
// atotto/clipboard, which runs xclip, xsel, wl-paste or pbpaste on ctrl+v,
// and only internal/action may run a program (CLAUDE.md).
type settingsPage struct {
	values  []string
	cursor  int
	editing bool
	buf     []rune // the row being edited
	err     string
}

// insert appends s to the buffer without its control characters: a value
// is one line, and a paste may hold newlines or escapes.
func (p *settingsPage) insert(s string) {
	for _, r := range s {
		if !unicode.IsControl(r) {
			p.buf = append(p.buf, r)
		}
	}
}

// requestSettings opens the settings page over the config file's layer.
// A save rescans, and a scan and an action never overlap.
func (m *Model) requestSettings() {
	if m.busy {
		m.message = "settings: an action is running"

		return
	}
	p := &settingsPage{}
	for _, f := range settingFields {
		p.values = append(p.values, f.get(m.cfg.File))
	}
	m.settings = p
}

// settingsKey handles the settings page.
func (m *Model) settingsKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.settings
	if p.editing {
		return m.settingsEditKey(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Up):
		p.cursor = max(p.cursor-1, 0)
	case key.Matches(msg, m.keys.Down):
		p.cursor = min(p.cursor+1, len(settingFields)-1)
	case msg.String() == keyEnter:
		return m.settingsEnter()
	case msg.String() == "ctrl+s":
		return m.saveSettings()
	case msg.String() == keyEsc:
		m.settings = nil // unsaved edits are discarded
	case key.Matches(msg, m.keys.Quit):
		return m.quit()
	}

	return nil
}

// settingsEnter toggles a boolean row, or starts editing a text row.
func (m *Model) settingsEnter() tea.Cmd {
	p := m.settings
	if settingFields[p.cursor].flag {
		on, _ := strconv.ParseBool(p.values[p.cursor])
		p.values[p.cursor] = strconv.FormatBool(!on)

		return nil
	}
	p.editing = true
	p.buf = []rune(p.values[p.cursor])

	return nil
}

// settingsEditKey: enter keeps the edit, esc drops it, backspace deletes
// and a typed character is appended.
func (m *Model) settingsEditKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.settings
	switch msg.String() {
	case keyEnter:
		p.values[p.cursor] = string(p.buf)
		p.editing = false
	case keyEsc:
		p.editing = false
	case keyForceQuit:
		return m.quit()
	case "backspace":
		if len(p.buf) > 0 {
			p.buf = p.buf[:len(p.buf)-1]
		}
	default:
		p.insert(msg.Text)
	}

	return nil
}

// saveSettings applies the rows to the file layer, rebuilds the settings
// as main does at startup, writes the file and rescans with the result. A
// failing step shows its error on the page, and nothing after it happens:
// a settings error writes nothing, and a failed write changes nothing.
func (m *Model) saveSettings() tea.Cmd {
	p := m.settings
	file, err := m.editedFile()
	if err != nil {
		p.err = err.Error()

		return nil
	}
	next, err := m.cfg.Reconfigure(file)
	if err != nil {
		p.err = err.Error()

		return nil
	}
	save := m.cfg.Save
	if save == nil {
		save = config.Save
	}
	if err := save(m.cfg.ConfigPath, file); err != nil {
		p.err = "save: " + err.Error()

		return nil
	}
	next.ConfigPath, next.File = m.cfg.ConfigPath, file
	next.Start, next.Run, next.Now = m.cfg.Start, m.cfg.Run, m.cfg.Now
	next.Overlay, next.Reconfigure, next.Save = m.cfg.Overlay, m.cfg.Reconfigure, m.cfg.Save
	m.cfg = next
	m.applyColor()
	m.settings = nil
	m.message = "settings saved to " + render.Sanitize(m.cfg.ConfigPath)

	return tea.Batch(m.startScan(), m.spinner.Tick)
}

// editedFile returns the config file's layer with the rows as edited, and
// the first error from a row that does not parse; such a row keeps the
// file's value. An untouched row is not parsed: the list text cannot hold
// an entry with a comma.
func (m *Model) editedFile() (config.Config, error) {
	p, file := m.settings, m.cfg.File
	var first error
	for i, f := range settingFields {
		if p.values[i] == f.get(file) {
			continue
		}
		if err := f.set(&file, p.values[i]); err != nil && first == nil {
			first = err
		}
	}

	return file, first
}

// overlaid returns edited with the environment and flags applied, or
// edited itself if they cannot be applied. They are re-applied to the rows
// as edited, not read from a snapshot, because a variable that equals the
// file's value still overrides the value the row is edited to.
func (m *Model) overlaid(edited config.Config) config.Config {
	eff, err := m.cfg.Overlay(edited)
	if err != nil {
		return edited
	}

	return eff
}

// settingsView is the settings page. A row that the environment or a flag
// overrides shows the value in effect.
func (m *Model) settingsView() string {
	p := m.settings
	edited, _ := m.editedFile()
	eff := m.overlaid(edited)
	head := []string{"", m.bold("Settings: " + render.Sanitize(m.cfg.ConfigPath)), ""}
	rows := make([]string, 0, len(settingFields))
	for i, f := range settingFields {
		gutter := "  "
		if i == p.cursor {
			gutter = "> "
		}
		value := render.Sanitize(p.values[i])
		if p.editing && i == p.cursor {
			value = render.Sanitize(string(p.buf)) + "█"
		}
		line := fmt.Sprintf("%s%-15s %s", gutter, f.key, value)
		if v := f.get(eff); v != f.get(edited) {
			line += "   overridden: " + render.Sanitize(v)
		}
		rows = append(rows, line)
	}
	var tail []string
	if p.err != "" {
		why := render.Sanitize(p.err)
		if st, ok := m.styles[render.Error]; ok {
			why = st.Render(why)
		}
		tail = append(tail, "", why)
	}
	keys := "↑/↓ choose • enter edit or toggle • ctrl+s save • esc back"
	if p.editing {
		keys = "enter keep • esc cancel"
	}
	tail = append(tail, "", keys)
	// The rows give way in a short window, so the selected row, the reason a
	// save failed and the keys stay on screen. Three rows are outside the
	// page: the title, the status and the empty help row.
	room := max(1, m.height-3-len(head)-len(tail))

	return strings.Join(slices.Concat(head, window(rows, room, p.cursor), tail), "\n")
}
