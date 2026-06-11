package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// panelEntry is one cell in a key panel: a key cap, its description, and
// the style for the key cap.
type panelEntry struct {
	key, desc string
	keyStyle  lipgloss.Style
}

// panelEntries adapts bindings to entries with the default key style,
// skipping disabled bindings and ones without help text.
//
//nolint:unused // adapter for callers that take raw bindings
func panelEntries(st Styles, bindings []key.Binding) []panelEntry {
	entries := make([]panelEntry, 0, len(bindings))
	for _, b := range bindings {
		h := b.Help()
		if !b.Enabled() || h.Key == "" {
			continue
		}
		entries = append(entries, panelEntry{key: h.Key, desc: h.Desc, keyStyle: st.PanelKey})
	}
	return entries
}

// renderKeyPanel renders a which-key style panel: a top border, a styled
// title line, and the entries laid out row-major in columns sized to fit
// width. Returns "" for an empty entry list; no trailing newline.
func renderKeyPanel(st Styles, width int, title string, entries []panelEntry) string {
	if len(entries) == 0 {
		return ""
	}

	var keyW, descW int
	for _, e := range entries {
		keyW = max(keyW, lipgloss.Width(e.key))
		descW = max(descW, lipgloss.Width(e.desc))
	}
	const gap = 3
	cellW := keyW + 1 + descW

	// Before the first WindowSizeMsg the width is unknown; render at
	// natural width rather than truncating to nothing.
	if width <= 0 {
		width = st.PanelBorder.GetHorizontalFrameSize() + len(entries)*(cellW+gap)
	}
	avail := max(width-st.PanelBorder.GetHorizontalFrameSize(), 1)
	cols := min(max((avail+gap)/(cellW+gap), 1), len(entries))

	cell := func(e panelEntry) string {
		// Pad the raw key text before styling so alignment isn't
		// counting escape codes.
		pad := strings.Repeat(" ", keyW-lipgloss.Width(e.key)) + e.key
		return e.keyStyle.Render(pad) + " " + st.PanelDesc.Render(e.desc)
	}

	rowStyle := lipgloss.NewStyle().MaxWidth(avail)
	cellStyle := lipgloss.NewStyle().Width(cellW + gap)
	lines := []string{st.PanelTitle.Render(title)}
	for row := 0; row < len(entries); row += cols {
		var b strings.Builder
		for i := row; i < min(row+cols, len(entries)); i++ {
			if i == row+cols-1 || i == len(entries)-1 {
				b.WriteString(cell(entries[i])) // no trailing pad
			} else {
				b.WriteString(cellStyle.Render(cell(entries[i])))
			}
		}
		lines = append(lines, rowStyle.Render(b.String()))
	}
	return st.PanelBorder.Width(max(width, 1)).Render(strings.Join(lines, "\n"))
}
