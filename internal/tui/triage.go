package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/td/internal/task"
)

// handleTriageKey processes the fast single-key mutations available while
// triaging the inbox. Every action targets the current card — the head of
// the session queue. The third return reports whether the key was
// consumed.
func (a *app) handleTriageKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if len(a.triageQueue) == 0 {
		return *a, nil, false
	}
	t := a.triageQueue[0]

	if st, ok := a.stateForKey(msg); ok {
		return *a, a.setState(t, st), true
	}

	switch {
	case key.Matches(msg, a.keys.SetDue):
		cmd := a.openPrompt(promptDue, fmt.Sprintf("due for #%d, YYYY-MM-DD (empty clears): ", t.ID), t.ID)
		return *a, cmd, true
	}
	return *a, nil, false
}

// skipCurrent moves the current card to the back of the session queue —
// pure TUI state, no store mutation.
func (a *app) skipCurrent() {
	if len(a.triageQueue) > 1 {
		head := a.triageQueue[0]
		a.triageQueue = append(a.triageQueue[1:], head)
	}
}

// mergeTriageQueue reconciles the session queue with a fresh inbox load:
// surviving cards keep their (possibly skipped) order, processed cards
// drop out, and newly captured tasks join at the back.
func mergeTriageQueue(old, fresh []task.Task) []task.Task {
	byID := make(map[int64]task.Task, len(fresh))
	for _, t := range fresh {
		byID[t.ID] = t
	}
	out := make([]task.Task, 0, len(fresh))
	for _, t := range old {
		if f, ok := byID[t.ID]; ok {
			out = append(out, f)
			delete(byID, t.ID)
		}
	}
	for _, t := range fresh {
		if _, ok := byID[t.ID]; ok {
			out = append(out, t)
		}
	}
	return out
}

// hasTaskID reports whether any task in the slice has the given ID.
func hasTaskID(tasks []task.Task, id int64) bool {
	for _, t := range tasks {
		if t.ID == id {
			return true
		}
	}
	return false
}

// triageView renders the triage body: one card at a time over a session
// progress bar, or the inbox-zero reward once nothing is left. The result
// is padded to exactly bodyHeight lines so the bottom chrome stays put.
func (a app) triageView() string {
	w, h := max(a.width, 1), max(a.bodyHeight, 1)
	var lines []string
	switch {
	case len(a.triageQueue) > 0:
		lines = a.triageCardLines(w)
	case a.inboxCount > 0:
		// Entered triage but the inbox load hasn't landed yet.
		lines = []string{"", "  " + a.styles.Muted.Render("loading inbox…")}
	default:
		lines = a.inboxZeroLines(w, h)
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:h], "\n")
}

// triageCardLines lays out the progress bar, the current capture in its
// bordered card, and the quiet two-column action grid.
func (a app) triageCardLines(width int) []string {
	s, g := a.styles, a.styles.Glyphs
	cur := a.triageQueue[0]
	done, left := a.triageProcessed, len(a.triageQueue)
	total := done + left

	lines := []string{""}

	// Progress bar: filled segments advance as the session burns down.
	barW := max(min(width-16, 40), 1)
	filled := min(int(float64(done)/float64(total)*float64(barW)+0.5), barW)
	lines = append(lines, "  "+
		s.ProgressDone.Render(strings.Repeat(g.ProgressOn, filled))+
		s.ProgressRest.Render(strings.Repeat(g.ProgressOff, barW-filled))+
		s.Muted.Render(fmt.Sprintf("  %d done · %d left", done, left)))
	lines = append(lines, "", "")

	// The captured card.
	iw := max(width-8, 12)
	row := func(inner string) string {
		gap := max(iw-lipgloss.Width(inner), 0)
		return "   " + s.CardBorder.Render(g.RuleV) + inner +
			strings.Repeat(" ", gap) + s.CardBorder.Render(g.RuleV)
	}
	lines = append(lines, "   "+s.CardBorder.Render(g.BoxTL+strings.Repeat(g.RuleH, iw)+g.BoxTR))
	title := truncTail(cur.Title, max(iw-5, 1), g.Ellipsis)
	lines = append(lines, row(" "+s.State[task.StateInbox].Render(g.State[task.StateInbox])+
		"  "+s.Title.Bold(true).Render(title)))
	sub := "captured from shell · no body yet"
	if strings.TrimSpace(cur.BodyMD) != "" {
		sub = "captured " + relTime(cur.CreatedAt, time.Now().UTC()) + " · body present"
	}
	lines = append(lines, row("    "+s.Muted.Render(truncTail(sub, max(iw-4, 1), g.Ellipsis))))
	lines = append(lines, "   "+s.CardBorder.Render(g.BoxBL+strings.Repeat(g.RuleH, iw)+g.BoxBR))
	lines = append(lines, "")

	// Action grid: key accent-bold, label in the target state's color.
	type act struct {
		key, label string
		style      lipgloss.Style
	}
	acts := []act{
		{"t", "→ todo", s.State[task.StateTodo]}, {"s", "→ someday", s.State[task.StateSomeday]},
		{"d", "→ doing", s.State[task.StateDoing]}, {"e", "edit body in $EDITOR", s.Accent},
		{"b", "→ blocked", s.State[task.StateBlocked]}, {"P", "set project", s.Accent},
		{"x", "→ done", s.State[task.StateDone]}, {"u", "set due", s.Accent},
		{"p", "set priority", s.Accent}, {"⏎", "skip for now", s.Muted},
	}
	colW := width / 2
	for i := 0; i < len(acts); i += 2 {
		l := "    " + s.FooterKey.Render(acts[i].key) + "  " + acts[i].style.Render(acts[i].label)
		r := ""
		if i+1 < len(acts) {
			r = s.FooterKey.Render(acts[i+1].key) + "  " + acts[i+1].style.Render(acts[i+1].label)
		}
		lines = append(lines, l+strings.Repeat(" ", max(colW-lipgloss.Width(l), 1))+r)
	}
	return lines
}

// inboxZeroLines is the reward screen — the only celebratory moment in td.
func (a app) inboxZeroLines(width, height int) []string {
	s, g := a.styles, a.styles.Glyphs
	content := []string{
		centerLine(s.InboxZero.Render(g.ZeroMark), width),
		"",
		centerLine(s.InboxZero.Render("inbox zero"), width),
		"",
		centerLine(s.Dimmed.Render("Nothing left to process. The dump is clean."), width),
		centerLine(s.Muted.Render("Capture more from the shell with ")+s.Accent.Render(`td add "…"`), width),
		"",
		centerLine(s.FooterKey.Render("esc")+s.Muted.Render(" back to list"), width),
	}
	top := max((height-len(content))/2, 0)
	lines := make([]string, 0, top+len(content))
	for range top {
		lines = append(lines, "")
	}
	return append(lines, content...)
}

// centerLine left-pads a styled line so it sits centered in width.
func centerLine(line string, width int) string {
	pad := (width - lipgloss.Width(line)) / 2
	if pad < 1 {
		return line
	}
	return strings.Repeat(" ", pad) + line
}
