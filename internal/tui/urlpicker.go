package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// openURLPicker arms the link chooser with the task's URLs. Used when a task
// has more than one link in its body, so `o` can't guess which to open.
func (a *app) openURLPicker(urls []link) {
	a.urlPickerOpen, a.urlPickerURLs, a.urlPickerSel = true, urls, 0
}

func (a *app) closeURLPicker() {
	a.urlPickerOpen, a.urlPickerURLs, a.urlPickerSel = false, nil, 0
}

// handleURLPickerKey owns the keyboard while the picker is open: ↑/↓ (or
// ctrl+p/ctrl+n) move, ⏎ opens the highlight, a digit 1–9 opens that index
// directly, esc dismisses.
func (a app) handleURLPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.closeURLPicker()
		return a, nil
	case "enter":
		sel := a.urlPickerSel
		if sel >= 0 && sel < len(a.urlPickerURLs) {
			u := a.urlPickerURLs[sel]
			a.closeURLPicker()
			return a, openURLCmd(u.url)
		}
		a.closeURLPicker()
		return a, nil
	case "up", "ctrl+p":
		if a.urlPickerSel > 0 {
			a.urlPickerSel--
		}
		return a, nil
	case "down", "ctrl+n":
		if a.urlPickerSel < len(a.urlPickerURLs)-1 {
			a.urlPickerSel++
		}
		return a, nil
	}
	// A digit 1–9 opens that index immediately. Indices past 9 stay reachable
	// with ↑/↓ + ⏎.
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		if idx := int(msg.Text[0] - '1'); idx < len(a.urlPickerURLs) {
			u := a.urlPickerURLs[idx]
			a.closeURLPicker()
			return a, openURLCmd(u.url)
		}
	}
	return a, nil
}

// urlPickerView renders the chooser box: a title row, a divider, then the
// numbered URLs, the selected one marked with the selection bar.
func (a app) urlPickerView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)

	// Rows sit between `  │ ` and a closing `│` at the last column.
	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.Accent.Bold(true).Render("↗ ")+
		s.Title.Render("open link — ")+s.Muted.Render("⏎ or type a number")))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))

	sel := min(a.urlPickerSel, len(a.urlPickerURLs)-1)
	for i, u := range a.urlPickerURLs {
		num := fmt.Sprintf("%d ", i+1)
		var content string
		if i == sel {
			content = s.SelBar.Render(g.SelBar+" ") +
				s.Accent.Render(num) + s.Link.Render(u.label())
		} else {
			content = "  " + s.Muted.Render(num) + s.Dimmed.Render(u.label())
		}
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}
