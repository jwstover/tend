package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/task"
)

// startStandup switches into the standup view and resets the session:
// the window opens at the last workday and the data reloads.
func (a *app) startStandup() {
	a.mode = modeStandup
	a.standupSince = task.LastWorkdayStart(time.Now())
	a.standupNotes, a.standupEvents, a.standupLive = nil, nil, nil
}

// handleStandupKey processes the standup view's keys. The view has no
// selection and no mutations beyond note capture, so anything unhandled
// is swallowed rather than passed to the list underneath.
func (a app) handleStandupKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	// ctrl+c always quits; `q` closes the view like esc.
	case key.Matches(msg, a.keys.Quit) && msg.String() != "q":
		return a, tea.Quit

	case key.Matches(msg, a.keys.Back), key.Matches(msg, a.keys.Standup),
		key.Matches(msg, a.keys.Quit):
		a.mode = modeList
		return a, a.loadTasks(modeList)

	case key.Matches(msg, a.keys.Help):
		a.helpOpen = true
		return a, nil

	case key.Matches(msg, a.keys.Palette):
		a.openPalette()
		return a, nil

	// Both the global note key and the list view's add key capture a
	// note here — in this view "add" can only mean one thing.
	case key.Matches(msg, a.keys.Note), key.Matches(msg, a.keys.QuickAdd):
		return a, a.modal.Open(modalLog, true, "note", 0, "")

	case key.Matches(msg, a.keys.ExpandClose): // h — window back a day
		a.standupSince = a.standupSince.AddDate(0, 0, -1)
		return a, a.loadStandup()

	case key.Matches(msg, a.keys.ExpandOpen): // l — window forward a day
		now := time.Now()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		if a.standupSince.Before(today) {
			a.standupSince = a.standupSince.AddDate(0, 0, 1)
			return a, a.loadStandup()
		}
		return a, nil

	case key.Matches(msg, a.keys.SortToggle):
		a.standupChrono = !a.standupChrono
		text := "notes grouped by task"
		if a.standupChrono {
			text = "notes in chronological order"
		}
		a.status = flash{text: text}
		return a, nil

	case key.Matches(msg, a.keys.Yank):
		md := task.StandupMarkdown(
			task.WindowLabel(a.standupSince, time.Now()),
			a.standupNotes, task.Summarize(a.standupEvents), a.standupLive)
		a.status = flash{kind: flashDone, text: "standup copied to clipboard"}
		return a, tea.SetClipboard(md)
	}
	return a, nil
}

// loadStandup fetches the window's notes and events plus the live tasks
// (for the Today and Blockers sections).
func (a app) loadStandup() tea.Cmd {
	since := a.standupSince
	return func() tea.Msg {
		now := time.Now()
		notes, err := a.store.ListLogEntries(a.ctx, since, now)
		if err != nil {
			return errMsg{err}
		}
		events, err := a.store.ListEvents(a.ctx, since, now)
		if err != nil {
			return errMsg{err}
		}
		live, err := a.store.ListLive(a.ctx)
		if err != nil {
			return errMsg{err}
		}
		return standupLoadedMsg{notes: notes, events: events, live: live}
	}
}

// standupWidths splits the body for the two panes: notes left, report
// right, one divider column between.
func (a app) standupWidths() (leftW, rightW int) {
	w := max(a.width, 20)
	leftW = w / 2
	return leftW, w - leftW - 1
}

// standupView renders the two-pane body: manual notes on the left, the
// generated report on the right, both fitted to exactly bodyHeight rows.
func (a app) standupView() string {
	h := max(a.bodyHeight, 1)
	leftW, rightW := a.standupWidths()

	left := fitPane(a.notesPaneLines(leftW), leftW, h)
	right := fitPane(a.reportPaneLines(rightW), rightW, h)
	divider := strings.TrimSuffix(
		strings.Repeat(a.styles.Rule.Render(a.styles.Glyphs.RuleV)+"\n", h), "\n")

	return lipgloss.JoinHorizontal(lipgloss.Top,
		strings.Join(left, "\n"), divider, strings.Join(right, "\n"))
}

// fitPane pads every line to the pane width and the pane to exactly
// height rows; when the content overflows, the oldest (topmost) rows
// scroll away so the newest stay visible.
func fitPane(lines []string, width, height int) []string {
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	}
	out := make([]string, 0, height)
	for _, l := range lines {
		if gap := width - lipgloss.Width(l); gap > 0 {
			l += strings.Repeat(" ", gap)
		}
		out = append(out, l)
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", max(width, 0)))
	}
	return out
}

// notesPaneLines lays out the manual entries. The default groups them
// by task (each group a workstream's narrative, with the task title as
// its header); `s` flips to a flat chronology under local-day headers.
func (a app) notesPaneLines(width int) []string {
	s := a.styles
	lines := []string{"", "  " + s.SubHeader.Render("NOTES")}

	if len(a.standupNotes) == 0 {
		lines = append(lines, "",
			"  "+s.Muted.Render("no notes yet — press ")+
				s.FooterKey.Render("n")+s.Muted.Render(" to add one"))
		return lines
	}
	if a.standupChrono {
		return append(lines, a.chronoNoteLines(width)...)
	}
	return append(lines, a.groupedNoteLines(width)...)
}

// dayHeadingLine renders a day section header: the relative label in
// green bold so the sections anchor the pane, the date suffix dimmed.
func (a app) dayHeadingLine(day, now time.Time) string {
	s := a.styles
	label := task.DayLabel(day, now)
	heading := "  " + s.DayHeading.Render(label)
	if date := day.Format("Mon Jan 2"); label != date {
		heading += s.Dimmed.Render(" · " + date)
	}
	return heading
}

// groupedNoteLines renders a section per local day, task groups within
// it (freestanding notes under "general"), entries chronological within
// their group and indented beneath its header.
func (a app) groupedNoteLines(width int) []string {
	s, g := a.styles, a.styles.Glyphs
	now := time.Now()
	var lines []string
	const indent = 13 // "      15:04  "
	for _, day := range task.SplitNotesByDay(a.standupNotes) {
		lines = append(lines, "", a.dayHeadingLine(day.Day, now))
		for _, grp := range task.GroupNotes(day.Notes) {
			header := "    " + s.Muted.Render("general")
			if grp.TaskID != nil {
				header = "    " + s.Title.Bold(true).Render(truncTail(grp.Title, max(width-12, 10), g.Ellipsis)) +
					s.DetailID.Render(fmt.Sprintf("  #%d", *grp.TaskID))
			}
			lines = append(lines, header)
			for _, n := range grp.Notes {
				wrapped := strings.Split(ansi.Wrap(n.Body, max(width-indent-2, 10), ""), "\n")
				lines = append(lines,
					"      "+s.Muted.Render(n.CreatedAt.Local().Format("15:04"))+"  "+wrapped[0])
				for _, l := range wrapped[1:] {
					lines = append(lines, strings.Repeat(" ", indent)+l)
				}
			}
			lines = append(lines, "")
		}
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1] // day spacing comes from the next header
		}
	}
	return lines
}

// chronoNoteLines renders the flat timeline: local-day headers, then
// hanging-indented entries prefixed with their task title.
func (a app) chronoNoteLines(width int) []string {
	s, g := a.styles, a.styles.Glyphs
	now := time.Now()
	var lines []string
	const indent = 11 // "    15:04  "
	for _, day := range task.SplitNotesByDay(a.standupNotes) {
		lines = append(lines, "", a.dayHeadingLine(day.Day, now))
		for _, n := range day.Notes {
			prefix := ""
			if ref := n.Ref(); ref != "" {
				prefix = s.Project.Render(truncTail(ref, 24, g.Ellipsis) + ": ")
			}
			wrapped := strings.Split(
				ansi.Wrap(n.Body, max(width-indent-2-lipgloss.Width(prefix), 10), ""), "\n")
			lines = append(lines,
				"    "+s.Muted.Render(n.CreatedAt.Local().Format("15:04"))+"  "+prefix+wrapped[0])
			for _, l := range wrapped[1:] {
				lines = append(lines, strings.Repeat(" ", indent)+l)
			}
		}
	}
	return lines
}

// reportPaneLines renders the generated standup report: the window
// summary derived from task events, then the live Today and Blockers
// sections — the TUI twin of `tend standup`.
func (a app) reportPaneLines(width int) []string {
	s, g := a.styles, a.styles.Glyphs
	sum := task.Summarize(a.standupEvents)
	lines := []string{"", "  " + s.SubHeader.Render("REPORT")}

	item := func(glyph string, style lipgloss.Style, title string, id int64) string {
		idStr := fmt.Sprintf("  #%d", id)
		t := truncTail(title, max(width-6-len(idStr), 8), g.Ellipsis)
		return "   " + style.Render(glyph) + "  " + s.Title.Render(t) + s.DetailID.Render(idStr)
	}
	section := func(title string) {
		lines = append(lines, "", "  "+s.Dimmed.Bold(true).Render(title))
	}
	empty := func(text string) {
		lines = append(lines, "   "+s.Faint.Render(text))
	}

	section(task.WindowLabel(a.standupSince, time.Now()))
	for _, it := range sum.Completed {
		lines = append(lines, item(g.State[task.StateDone], s.CheckDone, it.Title, it.TaskID))
	}
	for _, it := range sum.Blocked {
		lines = append(lines, item(g.State[task.StateBlocked], s.State[task.StateBlocked], it.Title, it.TaskID))
	}
	for _, it := range sum.Started {
		lines = append(lines, item(g.State[task.StateDoing], s.State[task.StateDoing], it.Title, it.TaskID))
	}
	if sum.Triaged > 0 {
		lines = append(lines, "   "+s.Muted.Render(fmt.Sprintf("· triaged %d inbox item(s)", sum.Triaged)))
	}
	if len(sum.Completed)+len(sum.Blocked)+len(sum.Started) == 0 && sum.Triaged == 0 {
		empty("nothing logged")
	}

	section("Today")
	today := 0
	for _, t := range a.standupLive {
		if t.State == task.StateDoing {
			lines = append(lines, item(g.State[task.StateDoing], s.State[task.StateDoing], t.Title, t.ID))
			today++
		}
	}
	if today == 0 {
		empty("nothing in progress")
	}

	section("Blockers")
	blockers := 0
	for _, t := range a.standupLive {
		if t.State == task.StateBlocked {
			lines = append(lines, item(g.State[task.StateBlocked], s.State[task.StateBlocked], t.Title, t.ID))
			blockers++
		}
	}
	if blockers == 0 {
		empty("none")
	}
	return lines
}
