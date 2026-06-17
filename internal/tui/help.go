package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/task"
)

// helpEntry is one binding row: the key cap(s) and what they do.
type helpEntry struct{ keys, desc string }

// helpGroup is a titled cluster of bindings.
type helpGroup struct {
	title   string
	entries []helpEntry
}

// helpGroups is the app's real key reference, grouped by intent.
func helpGroups() []helpGroup {
	return []helpGroup{
		{"NAVIGATE", []helpEntry{
			{"j / k", "down / up"},
			{"gg / G", "top / bottom"},
			{"⏎ / tab", "expand / collapse children"},
			{"l / h", "expand / collapse branch"},
			{"]", "toggle detail pane"},
			{"o / O", "open link(s) in body"},
		}},
		{"CAPTURE & FIND", []helpEntry{
			{"n", "quick-add to inbox"},
			{"a", "add sub-task"},
			{"/", "search the list"},
			{": / ctrl+p", "command palette"},
		}},
		{"PROCESS", []helpEntry{
			{"i", "triage the inbox"},
			{"x / space", "mark done"},
			{"c", "change state (chord)"},
			{"p", "set priority (chord)"},
			{"dd", "delete task (chord)"},
			{"P", "set project"},
			{"u", "set due (triage)"},
			{"e", "edit body in $EDITOR"},
			{"U", "add log entry"},
		}},
	}
}

// helpView renders the bordered key reference spliced above the footer:
// group titles in inbox orange, keys accent-bold in a fixed column,
// descriptions in fgDim.
func (a app) helpView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 30)
	cb := s.CardBorder
	const keyCol = 12

	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	lines := []string{"  " + cb.Render(g.BoxTL+g.RuleH+" ") +
		s.Accent.Bold(true).Render("help") +
		cb.Render(" "+strings.Repeat(g.RuleH, max(w-11, 1))+g.BoxTR)}
	lines = append(lines, row(""))
	for _, grp := range helpGroups() {
		lines = append(lines, row(" "+s.State[task.StateInbox].Bold(true).Render(grp.title)))
		for _, e := range grp.entries {
			pad := strings.Repeat(" ", max(keyCol-lipgloss.Width(e.keys), 1))
			lines = append(lines, row("   "+s.FooterKey.Render(e.keys)+pad+s.Dimmed.Render(e.desc)))
		}
		lines = append(lines, row(""))
	}
	lines = append(lines, row(" "+s.FooterKey.Render("esc")+s.Muted.Render(" close")))
	lines = append(lines, "  "+cb.Render(g.BoxBL+strings.Repeat(g.RuleH, w-4)+g.BoxBR))
	return strings.Join(lines, "\n")
}
