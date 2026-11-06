package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/settings"
)

// The Settings view (`,`): one row per entry of the internal/settings
// registry, with its current value (or the default, dimmed, when unset).
// ⏎ edits the selected row by its kind -- an inline option list for a
// choice, the prompt modal for text or a custom template, a flip for a
// bool -- and `x` resets it. Rows come from settings.Entries(), so a new
// registry entry needs no change here. Like the Schedules view it reloads
// on dbChangedMsg.

// settingRow is a registry entry with its stored value, resolved at load.
type settingRow struct {
	entry settings.Entry
	value string
	set   bool
}

// settingsView is the state of modeSettings.
type settingsView struct {
	rows   []settingRow
	cursor int
	// choosing is true while the selected choice row shows its option list;
	// choice is the highlighted option within it.
	choosing bool
	choice   int
}

type settingsLoadedMsg struct{ rows []settingRow }

func (a *app) startSettings() {
	a.mode = modeSettings
	a.deletePending = false
	a.stv.choosing = false
	a.resize()
}

func (a *app) leaveSettings() tea.Cmd {
	a.mode = modeList
	a.deletePending = false
	a.stv.choosing = false
	a.resize()
	return a.loadTasks(modeList)
}

func (a app) selectedSetting() (settingRow, bool) {
	if a.stv.cursor < 0 || a.stv.cursor >= len(a.stv.rows) {
		return settingRow{}, false
	}
	return a.stv.rows[a.stv.cursor], true
}

// loadSettings reads the stored value of every registry entry.
func (a app) loadSettings() tea.Cmd {
	return func() tea.Msg {
		entries := settings.Entries()
		rows := make([]settingRow, 0, len(entries))
		for _, e := range entries {
			v, ok, err := a.store.Setting(a.ctx, e.Key)
			if err != nil {
				return errMsg{err}
			}
			rows = append(rows, settingRow{entry: e, value: v, set: ok})
		}
		return settingsLoadedMsg{rows: rows}
	}
}

func (a *app) applySettings(msg settingsLoadedMsg) {
	a.stv.rows = msg.rows
	a.stv.cursor = max(min(a.stv.cursor, len(a.stv.rows)-1), 0)
	if !a.stv.choosing {
		return
	}
	if row, ok := a.selectedSetting(); !ok || row.entry.Kind != settings.KindChoice {
		a.stv.choosing = false
	}
}

// choiceOptions is the inline list of a choice row: unset, the fixed
// choices, then (when allowed) a custom value.
func choiceOptions(e settings.Entry) []string {
	opts := []string{"auto (default)"}
	opts = append(opts, e.Choices...)
	if e.AllowCustom {
		opts = append(opts, "custom…")
	}
	return opts
}

func (a app) handleSettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, a.keys.Quit) && msg.String() != "q":
		return a, tea.Quit
	case key.Matches(msg, a.keys.Help):
		a.helpOpen = true
		return a, nil
	case key.Matches(msg, a.keys.Palette):
		a.openPalette()
		return a, nil
	}
	if a.stv.choosing {
		return a.handleSettingsChoiceKey(msg)
	}
	switch {
	case key.Matches(msg, a.keys.Quit), key.Matches(msg, a.keys.Back), key.Matches(msg, a.keys.Settings):
		return a, a.leaveSettings()
	case key.Matches(msg, a.keys.ScrollDown):
		a.stv.cursor = max(min(a.stv.cursor+1, len(a.stv.rows)-1), 0)
		return a, nil
	case key.Matches(msg, a.keys.ScrollUp):
		a.stv.cursor = max(a.stv.cursor-1, 0)
		return a, nil
	}
	row, ok := a.selectedSetting()
	if !ok {
		return a, nil
	}
	switch msg.String() {
	case "x":
		return a, a.clearSetting(row.entry)
	case "enter":
		return a.editSetting(row)
	}
	return a, nil
}

// editSetting starts editing a row according to its kind.
func (a app) editSetting(row settingRow) (tea.Model, tea.Cmd) {
	switch row.entry.Kind {
	case settings.KindChoice:
		a.stv.choosing = true
		a.stv.choice = 0
		for i, o := range choiceOptions(row.entry) {
			if row.set && o == row.value {
				a.stv.choice = i
			}
		}
		return a, nil
	case settings.KindBool:
		cur := row.entry.Default
		if row.set {
			cur = row.value
		}
		next := "true"
		if cur == "true" {
			next = "false"
		}
		return a, a.writeSetting(row.entry, next)
	default:
		return a, a.openSettingModal(row)
	}
}

// openSettingModal opens the single-line prompt for a key, prefilled with
// its current value.
func (a *app) openSettingModal(row settingRow) tea.Cmd {
	cmd := a.modal.Open(modalSettingValue, false, row.entry.Label, 0, row.entry.Key)
	if row.set {
		a.modal.area.SetValue(row.value)
	}
	return cmd
}

func (a app) handleSettingsChoiceKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	row, ok := a.selectedSetting()
	if !ok {
		a.stv.choosing = false
		return a, nil
	}
	opts := choiceOptions(row.entry)
	switch {
	case key.Matches(msg, a.keys.Back), key.Matches(msg, a.keys.Quit):
		a.stv.choosing = false
	case key.Matches(msg, a.keys.ScrollDown):
		a.stv.choice = min(a.stv.choice+1, len(opts)-1)
	case key.Matches(msg, a.keys.ScrollUp):
		a.stv.choice = max(a.stv.choice-1, 0)
	case msg.String() == "enter":
		a.stv.choosing = false
		switch {
		case a.stv.choice == 0:
			return a, a.clearSetting(row.entry)
		case row.entry.AllowCustom && a.stv.choice == len(opts)-1:
			return a, a.openSettingModal(row)
		default:
			return a, a.writeSetting(row.entry, opts[a.stv.choice])
		}
	}
	return a, nil
}

// submitSettingValue handles the modal: an invalid value is refused with
// Validate's message and nothing is written.
func (a app) submitSettingValue(key, value string) (tea.Model, tea.Cmd) {
	e, ok := settings.Lookup(key)
	if !ok {
		return a, nil
	}
	return a, a.writeSetting(e, value)
}

// writeSetting validates and stores a value. Validation is pure; the write
// is a Cmd.
func (a app) writeSetting(e settings.Entry, value string) tea.Cmd {
	if err := settings.Validate(e.Key, value); err != nil {
		return statusCmd(flash{text: err.Error(), isErr: true})
	}
	return func() tea.Msg {
		if err := a.store.SetSetting(a.ctx, e.Key, value); err != nil {
			return statusMsg{isErr: true, text: err.Error()}
		}
		return refreshMsg{status: flash{kind: flashDone, text: e.Label + " set"}}
	}
}

func (a app) clearSetting(e settings.Entry) tea.Cmd {
	return func() tea.Msg {
		if err := a.store.ClearSetting(a.ctx, e.Key); err != nil {
			return statusMsg{isErr: true, text: err.Error()}
		}
		return refreshMsg{status: flash{kind: flashDone, text: e.Label + " reset to default"}}
	}
}

// settingDefault is what an unset row shows after "default: ".
func settingDefault(e settings.Entry) string {
	switch {
	case e.Default != "" && e.DefaultNote != "":
		return e.Default + " (" + e.DefaultNote + ")"
	case e.Default != "":
		return e.Default
	case e.DefaultNote != "":
		return e.DefaultNote
	default:
		return "none"
	}
}

func (a app) settingsBody() string {
	s, g := a.styles, a.styles.Glyphs
	h := max(a.bodyHeight, 1)
	lines := []string{"", "  " + s.SubHeader.Render("SETTINGS")}
	cursorLine := 0
	for i, r := range a.stv.rows {
		selected := i == a.stv.cursor
		gutter, gutterStyle := "  ", s.Normal
		if selected {
			gutter, gutterStyle = g.SelBar+" ", s.SelBar
			cursorLine = len(lines)
		}
		val := s.Normal.Render(r.value)
		if !r.set {
			val = s.Dimmed.Render("default: " + settingDefault(r.entry))
		}
		line := gutterStyle.Render(gutter) + s.Title.Render(padRight(truncTail(r.entry.Label, 28, g.Ellipsis), 28)) + " " + val
		lines = append(lines, lipgloss.NewStyle().MaxWidth(a.width).Render(line))
		if !selected {
			continue
		}
		if r.entry.Description != "" {
			lines = append(lines, lipgloss.NewStyle().MaxWidth(a.width).Render("    "+s.Muted.Render(r.entry.Description)))
		}
		if a.stv.choosing {
			for j, o := range choiceOptions(r.entry) {
				mark := "  "
				style := s.Normal
				if j == a.stv.choice {
					mark, style = g.SelBar+" ", s.SelBar
				}
				cur := ""
				if (j == 0 && !r.set) || (r.set && o == r.value) {
					cur = s.Faint.Render("  (current)")
				}
				lines = append(lines, "      "+style.Render(mark)+s.Normal.Render(o)+cur)
			}
		}
	}
	if len(a.stv.rows) == 0 {
		lines = append(lines, "", "  "+s.Muted.Render("no settings"))
	}
	off := 0
	if cursorLine >= h {
		off = cursorLine - h + 1
	}
	return strings.Join(fitPane(lines, a.width, h, off), "\n")
}

func (a app) settingsHints() [][2]string {
	if a.stv.choosing {
		return [][2]string{{"j/k", "move"}, {"⏎", "choose"}, {"esc", "cancel"}}
	}
	return [][2]string{{"j/k", "move"}, {"⏎", "edit"}, {"x", "reset"}, {"esc/,", "back"}, {"?", "help"}}
}
