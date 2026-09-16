package tui

import (
	tea "charm.land/bubbletea/v2"
)

// openURLPicker arms the link chooser with the task's URLs. Used when a task
// has more than one link in its body, so `o` can't guess which to open.
func (a *app) openURLPicker(urls []link) {
	a.urlPicker = picker[link]{
		open: true, items: urls, numbered: true,
		label: func(l link) string { return l.label() },
	}
}

func (a *app) closeURLPicker() {
	a.urlPicker = picker[link]{}
}

// handleURLPickerKey owns the keyboard while the picker is open: type to
// filter, ↑/↓ (or ctrl+p/ctrl+n) move, ⏎ opens the highlight, a digit 1–9
// opens that visible row directly, esc dismisses.
func (a app) handleURLPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	action, idx := a.urlPicker.key(msg, a.height)
	switch action {
	case pickerCancel:
		a.closeURLPicker()
		return a, nil
	case pickerPick:
		u, ok := a.urlPicker.at(idx)
		a.closeURLPicker()
		if !ok {
			return a, nil
		}
		return a, openURLCmd(u.url)
	}
	return a, nil
}

// urlPickerView renders the chooser box: a title row, the filter prompt,
// then the numbered URLs, the selected one marked with the selection bar.
func (a app) urlPickerView() string {
	s := a.styles
	return a.urlPicker.render(s, a.width, a.height, pickerView[link]{
		icon:  s.Accent.Bold(true).Render("↗ "),
		title: s.Title.Render("open link — "),
		hint:  s.Muted.Render("type to filter, ⏎ or a digit picks"),
		row: func(u link, selected bool, w int) string {
			if selected {
				return s.Link.Render(u.label())
			}
			return s.Dimmed.Render(u.label())
		},
		noMatch: "no matching links",
	})
}
