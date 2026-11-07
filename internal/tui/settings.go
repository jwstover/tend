package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/settings"
)

// The Settings view (`,`): a form over the internal/settings registry.
// Entries are grouped under their Section's heading, each one a labeled
// field with its description beneath. An unset field shows its default as
// a dimmed placeholder. ⏎ edits the selected field by its kind -- a
// dropdown for a choice, an inline text input for text (and a choice's
// "custom…"), a flip for a bool -- and `x` resets it. Rows come from
// settings.Entries(), so a new registry entry (or section) needs no change
// here. Like the Schedules view it reloads on dbChangedMsg.

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
	// choosing is true while the selected choice row shows its dropdown;
	// choice is the highlighted option within it.
	choosing bool
	choice   int
	// editing is true while the selected row's text input has focus; it
	// holds live reloads back (inputBusy). editErr is the last value
	// Validate refused, shown under the field until the next keypress.
	editing bool
	editErr string
	input   textinput.Model
}

func newSettingsInput() textinput.Model {
	in := textinput.New()
	in.Prompt = ""
	return in
}

type settingsLoadedMsg struct{ rows []settingRow }

func (a *app) startSettings() {
	a.mode = modeSettings
	a.deletePending = false
	a.stopSettingEdit()
	a.resize()
}

func (a *app) leaveSettings() tea.Cmd {
	a.mode = modeList
	a.deletePending = false
	a.stopSettingEdit()
	a.resize()
	return a.loadTasks(modeList)
}

// stopSettingEdit closes any dropdown or input.
func (a *app) stopSettingEdit() {
	a.stv.choosing = false
	a.stv.editing = false
	a.stv.editErr = ""
	a.stv.input.Blur()
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

// applySettings swaps in fresh rows. An open dropdown or input stays open
// (with its text) as long as the selected row is still the same entry.
func (a *app) applySettings(msg settingsLoadedMsg) {
	prev, hadPrev := a.selectedSetting()
	a.stv.rows = msg.rows
	a.stv.cursor = max(min(a.stv.cursor, len(a.stv.rows)-1), 0)
	if !a.stv.choosing && !a.stv.editing {
		return
	}
	if row, ok := a.selectedSetting(); !ok || !hadPrev || row.entry.Key != prev.entry.Key {
		a.stopSettingEdit()
	}
}

// choiceOptions is the dropdown of a choice row: unset, the fixed choices,
// then (when allowed) a custom value.
func choiceOptions(e settings.Entry) []string {
	opts := []string{"auto (default)"}
	opts = append(opts, e.Choices...)
	if e.AllowCustom {
		opts = append(opts, "custom…")
	}
	return opts
}

func (a app) handleSettingsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// A focused input takes every key but ⏎/esc/ctrl+c, so `q`, `x`, `,`,
	// `?` and the rest type.
	if a.stv.editing {
		return a.handleSettingsInputKey(msg)
	}
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
	case key.Matches(msg, a.keys.ScrollDown), msg.String() == "tab", msg.String() == "down":
		a.stv.cursor = max(min(a.stv.cursor+1, len(a.stv.rows)-1), 0)
		return a, nil
	case key.Matches(msg, a.keys.ScrollUp), msg.String() == "shift+tab", msg.String() == "up":
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
	case "enter", "space":
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
		opts := choiceOptions(row.entry)
		for i, o := range opts {
			if row.set && o == row.value {
				a.stv.choice = i
			}
		}
		// A stored value outside the choices is a custom one.
		if row.set && a.stv.choice == 0 && row.entry.AllowCustom {
			a.stv.choice = len(opts) - 1
		}
		return a, nil
	case settings.KindBool:
		next := "true"
		if settingBool(row) {
			next = "false"
		}
		return a, a.writeSetting(row.entry, next)
	default:
		return a, a.startSettingInput(row)
	}
}

// startSettingInput focuses the inline input on a row, prefilled with its
// stored value; an unset row shows its default as the placeholder.
func (a *app) startSettingInput(row settingRow) tea.Cmd {
	a.stv.choosing = false
	a.stv.editing = true
	a.stv.editErr = ""
	a.stv.input.Reset()
	a.stv.input.Placeholder = settingPlaceholder(row.entry)
	a.stv.input.SetWidth(a.settingsFieldWidth() - 1) // its cursor takes a cell
	if row.set {
		a.stv.input.SetValue(row.value)
		a.stv.input.CursorEnd()
	}
	return a.stv.input.Focus()
}

func (a app) handleSettingsInputKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	row, ok := a.selectedSetting()
	if !ok {
		a.stopSettingEdit()
		return a, nil
	}
	switch msg.String() {
	case "ctrl+c":
		return a, tea.Quit
	case "esc":
		a.stopSettingEdit()
		return a, nil
	case "enter":
		value := strings.TrimSpace(a.stv.input.Value())
		// An invalid value is refused and the input stays open on it, so
		// it can be fixed; nothing is written.
		if err := settings.Validate(row.entry.Key, value); err != nil {
			a.stv.editErr = err.Error()
			a.status = flash{text: err.Error(), isErr: true}
			return a, nil
		}
		a.stopSettingEdit()
		return a, a.storeSetting(row.entry, value)
	}
	a.stv.editErr = ""
	var cmd tea.Cmd
	a.stv.input, cmd = a.stv.input.Update(msg)
	return a, cmd
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
	case key.Matches(msg, a.keys.ScrollDown), msg.String() == "down", msg.String() == "tab":
		a.stv.choice = min(a.stv.choice+1, len(opts)-1)
	case key.Matches(msg, a.keys.ScrollUp), msg.String() == "up", msg.String() == "shift+tab":
		a.stv.choice = max(a.stv.choice-1, 0)
	case msg.String() == "enter", msg.String() == "space":
		a.stv.choosing = false
		switch {
		case a.stv.choice == 0:
			return a, a.clearSetting(row.entry)
		case row.entry.AllowCustom && a.stv.choice == len(opts)-1:
			return a, a.startSettingInput(row)
		default:
			return a, a.writeSetting(row.entry, opts[a.stv.choice])
		}
	}
	return a, nil
}

// writeSetting validates and stores a value, refusing an invalid one with
// Validate's message in the status line.
func (a app) writeSetting(e settings.Entry, value string) tea.Cmd {
	if err := settings.Validate(e.Key, value); err != nil {
		return statusCmd(flash{text: err.Error(), isErr: true})
	}
	return a.storeSetting(e, value)
}

// storeSetting is the write of an already validated value, as a Cmd.
func (a app) storeSetting(e settings.Entry, value string) tea.Cmd {
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

func settingBool(r settingRow) bool {
	if r.set {
		return r.value == "true"
	}
	return r.entry.Default == "true"
}

// settingPlaceholder is what an unset field shows, dimmed, in place of a
// value: the default itself, or "auto" when unset means "work it out".
func settingPlaceholder(e settings.Entry) string {
	switch {
	case e.Default != "":
		return e.Default
	case e.Kind == settings.KindChoice:
		return "auto"
	default:
		return "none"
	}
}

// settingDefault describes the default in full, for the line under an
// unset field and the dropdown's "auto" option.
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

// Form geometry. Wide terminals put the label left of its field; narrow
// ones stack the label above it.
const (
	settingsLabelW   = 26
	settingsIndent   = 4  // gutter (2) + section inset (2)
	settingsMaxField = 48 // the input's text width, brackets excluded
)

func (a app) settingsStacked() bool {
	return a.width < settingsIndent+settingsLabelW+24
}

// settingsFieldX is the column the field's opening bracket sits in.
func (a app) settingsFieldX() int {
	if a.settingsStacked() {
		return settingsIndent
	}
	return settingsIndent + settingsLabelW
}

// settingsFieldWidth is the text width inside a field's brackets.
func (a app) settingsFieldWidth() int {
	// "[ " + text + " ▾ ]" plus a cell of right margin.
	return max(min(a.width-a.settingsFieldX()-7, settingsMaxField), 8)
}

func (a app) settingsBody() string {
	s, g := a.styles, a.styles.Glyphs
	h := max(a.bodyHeight, 1)
	w := max(a.width, 20)
	clip := lipgloss.NewStyle().MaxWidth(w)
	add := func(lines *[]string, l string) { *lines = append(*lines, clip.Render(l)) }

	lines := []string{""}
	if len(a.stv.rows) == 0 {
		add(&lines, "  "+s.Muted.Render("no settings"))
	}
	cursorTop, cursorBottom := 0, 0
	section := ""
	for i, r := range a.stv.rows {
		if i == 0 || r.entry.Section != section {
			section = r.entry.Section
			add(&lines, a.settingsHeading(section, w))
			lines = append(lines, "")
		}
		selected := i == a.stv.cursor
		if selected {
			cursorTop = len(lines)
		}
		gutter := "  "
		if selected {
			gutter = s.SelBar.Render(g.SelBar + " ")
		}
		labelStyle := s.Title
		if selected {
			labelStyle = s.SubSelText
		}
		field := a.settingsField(r, selected)
		if a.settingsStacked() {
			add(&lines, gutter+"  "+labelStyle.Render(r.entry.Label))
			add(&lines, gutter+"  "+field)
		} else {
			label := padRight(truncTail(r.entry.Label, settingsLabelW-2, g.Ellipsis), settingsLabelW)
			add(&lines, gutter+"  "+labelStyle.Render(label)+field)
		}
		under := strings.Repeat(" ", a.settingsFieldX()+2)
		if selected && a.stv.choosing {
			for _, l := range a.settingsDropdown(r) {
				add(&lines, strings.Repeat(" ", a.settingsFieldX())+l)
			}
		}
		wrapW := max(w-a.settingsFieldX()-4, 20)
		if selected && a.stv.editErr != "" {
			for _, el := range wrapWords(a.stv.editErr, wrapW) {
				add(&lines, under+s.Error.UnsetPadding().Render(el))
			}
		}
		for _, dl := range wrapWords(r.entry.Description, wrapW) {
			add(&lines, under+s.Muted.Render(dl))
		}
		if !r.set && r.entry.DefaultNote != "" {
			for _, dl := range wrapWords("default: "+settingDefault(r.entry), wrapW) {
				add(&lines, under+s.Faint.Render(dl))
			}
		}
		if selected {
			cursorBottom = len(lines) - 1
		}
		lines = append(lines, "")
	}
	off := 0
	if cursorBottom >= h {
		off = min(cursorBottom-h+1, cursorTop)
	}
	return strings.Join(fitPane(lines, a.width, h, off), "\n")
}

// settingsHeading draws `  Section ──────────`.
func (a app) settingsHeading(name string, width int) string {
	s, g := a.styles, a.styles.Glyphs
	if name == "" {
		name = "General"
	}
	fill := max(width-2-lipgloss.Width(name)-2-2, 0)
	return "  " + s.SubHeader.Render(name) + " " + s.GroupRule.Render(strings.Repeat(g.RuleH, fill))
}

// settingsField renders a row's input: `[ value          ▾ ]` for a choice,
// `[ value            ]` for text, `[x] on` for a bool. The brackets take
// the accent while the row is selected.
func (a app) settingsField(r settingRow, selected bool) string {
	s, g := a.styles, a.styles.Glyphs
	br := s.CardBorder
	if selected {
		br = s.Accent
	}
	if r.entry.Kind == settings.KindBool {
		mark, word := " ", "off"
		if settingBool(r) {
			mark, word = "x", "on"
		}
		txt := s.Normal.Render(word)
		if !r.set {
			txt = s.Dimmed.Render(word) + s.Faint.Render(" (default)")
		}
		return br.Render("[") + s.Normal.Render(mark) + br.Render("]") + " " + txt
	}
	fw := a.settingsFieldWidth()
	var inner string
	switch {
	case selected && a.stv.editing:
		inner = a.stv.input.View()
	case r.set:
		inner = s.Normal.Render(truncTail(r.value, fw, g.Ellipsis))
	default:
		inner = s.Dimmed.Render(truncTail(settingPlaceholder(r.entry), max(fw-10, 1), g.Ellipsis)) +
			s.Faint.Render(" (default)")
	}
	inner += strings.Repeat(" ", max(fw-lipgloss.Width(inner), 0))
	tail := "  "
	if r.entry.Kind == settings.KindChoice {
		tail = " " + br.Render(g.CaretOpen)
	}
	return br.Render("[ ") + inner + tail + br.Render(" ]")
}

// settingsDropdown is the open option list of a choice row, boxed under
// its field: the highlighted option carries the selection bar, the one in
// effect a check.
func (a app) settingsDropdown(r settingRow) []string {
	s, g := a.styles, a.styles.Glyphs
	cb := s.CardBorder
	opts := choiceOptions(r.entry)
	current := -1
	for j, o := range opts {
		if (j == 0 && !r.set) || (j > 0 && r.set && o == r.value) {
			current = j
		}
	}
	if r.set && current < 0 && r.entry.AllowCustom {
		current = len(opts) - 1
	}
	inner := a.settingsFieldWidth() + 2
	lines := []string{cb.Render(g.BoxTL + strings.Repeat(g.RuleH, inner+2) + g.BoxTR)}
	for j, o := range opts {
		mark, style := "  ", s.Normal
		if j == a.stv.choice {
			mark, style = s.SelBar.Render(g.SelBar+" "), s.SubSelText
		}
		check := "  "
		if j == current {
			check = s.CheckDone.Render(g.BoxChecked) + " "
		}
		// "auto" explains itself; a stored custom value shows on "custom…".
		avail := inner - 4
		var body string
		switch {
		case j == 0:
			body = style.Render(o) + s.Faint.Render(truncTail(" "+g.RuleH+" "+settingDefault(r.entry), max(avail-runeWidth(o), 1), g.Ellipsis))
		case j == current && j == len(opts)-1 && r.entry.AllowCustom:
			body = style.Render(truncTail("custom: "+r.value, avail, g.Ellipsis))
		default:
			body = style.Render(truncTail(o, avail, g.Ellipsis))
		}
		content := mark + check + body
		gap := max(inner+2-lipgloss.Width(content), 0)
		lines = append(lines, cb.Render(g.RuleV)+content+strings.Repeat(" ", gap)+cb.Render(g.RuleV))
	}
	lines = append(lines, cb.Render(g.BoxBL+strings.Repeat(g.RuleH, inner+2)+g.BoxBR))
	return lines
}

// wrapWords breaks s into lines of at most w cells at spaces.
func wrapWords(s string, w int) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case runeWidth(line)+1+runeWidth(word) <= w:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

func (a app) settingsHints() [][2]string {
	switch {
	case a.stv.editing:
		return [][2]string{{"⏎", "save"}, {"esc", "cancel"}}
	case a.stv.choosing:
		return [][2]string{{"j/k", "move"}, {"⏎", "choose"}, {"esc", "cancel"}}
	}
	return [][2]string{{"j/k", "move"}, {"⏎", "edit"}, {"x", "reset"}, {"esc/,", "back"}, {"?", "help"}}
}
