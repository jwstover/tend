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
	a.standupScroll, a.standupCursor = 0, 0
	a.standupCollapsed = map[string]bool{}
	a.standupJumpToLatest = true
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
		a.standupJumpToLatest = true
		return a, a.loadStandup()

	case key.Matches(msg, a.keys.ExpandOpen): // l — window forward a day
		now := time.Now()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		if a.standupSince.Before(today) {
			a.standupSince = a.standupSince.AddDate(0, 0, 1)
			a.standupJumpToLatest = true
			return a, a.loadStandup()
		}
		return a, nil

	case key.Matches(msg, a.keys.SortToggle):
		a.standupChrono = !a.standupChrono
		a.standupScroll = a.standupMaxScroll()
		text := "notes grouped by task"
		if a.standupChrono {
			text = "notes in chronological order"
		}
		a.status = flash{text: text}
		return a, nil

	case key.Matches(msg, a.keys.Yank):
		md := task.StandupMarkdown(
			task.WindowLabel(a.standupSince, time.Now()),
			a.visibleStandupNotes(), task.Summarize(a.standupEvents), a.standupLive)
		a.status = flash{kind: flashDone, text: "standup copied to clipboard"}
		return a, tea.SetClipboard(md)

	// C hides/shows Claude session recap notes in both panes and the yank.
	case key.Matches(msg, a.keys.ToggleRecaps):
		a.standupHideRecaps = !a.standupHideRecaps
		text := "recap notes shown"
		if a.standupHideRecaps {
			text = "recap notes hidden"
		}
		a.status = flash{text: text}
		a.standupCursor = min(a.standupCursor, max(len(a.standupGroups())-1, 0))
		a.standupScroll = min(a.standupScroll, a.standupMaxScroll())
		return a, nil

	// tab flips the focused note-group's log entries open or closed.
	case key.Matches(msg, a.keys.ExpandToggle):
		if groups := a.standupGroups(); a.standupCursor >= 0 && a.standupCursor < len(groups) {
			gkey := groups[a.standupCursor].key
			if a.standupCollapsed[gkey] {
				delete(a.standupCollapsed, gkey)
			} else {
				a.standupCollapsed[gkey] = true
			}
		}
		return a, nil

	case key.Matches(msg, a.keys.ScrollDown):
		if a.standupChrono {
			a.standupScroll = min(a.standupScroll+1, a.standupMaxScroll())
			return a, nil
		}
		if groups := a.standupGroups(); a.standupCursor < len(groups)-1 {
			a.standupCursor++
		}
		a.followStandupCursor()
		return a, nil

	case key.Matches(msg, a.keys.ScrollUp):
		if a.standupChrono {
			a.standupScroll = max(a.standupScroll-1, 0)
			return a, nil
		}
		a.standupCursor = max(a.standupCursor-1, 0)
		a.followStandupCursor()
		return a, nil

	case key.Matches(msg, a.keys.PageDown):
		a.standupScroll = min(a.standupScroll+max(a.bodyHeight-1, 1), a.standupMaxScroll())
		return a, nil

	case key.Matches(msg, a.keys.PageUp):
		a.standupScroll = max(a.standupScroll-max(a.bodyHeight-1, 1), 0)
		return a, nil
	}
	return a, nil
}

// standupGroupRef pairs a note group with the day it falls under, giving
// each group on the standup notes pane a stable identity across renders
// for cursor movement and collapse state.
type standupGroupRef struct {
	key string
	day time.Time
	grp task.NoteGroup
}

// visibleStandupNotes is the current notes window, minus Claude session
// recap entries when standupHideRecaps is on (the `C` toggle) — the
// source every rendering path and the yank markdown read from instead of
// standupNotes directly.
func (a app) visibleStandupNotes() []task.LogEntry {
	if !a.standupHideRecaps {
		return a.standupNotes
	}
	out := make([]task.LogEntry, 0, len(a.standupNotes))
	for _, n := range a.standupNotes {
		if !strings.HasPrefix(n.Body, recapNotePrefix) {
			out = append(out, n)
		}
	}
	return out
}

// standupGroups flattens the current notes window into the same
// day-then-task order groupedNoteLines renders, so cursor index i always
// refers to the same group as line offset groupStart[i].
func (a app) standupGroups() []standupGroupRef {
	var out []standupGroupRef
	for _, day := range task.SplitNotesByDay(a.visibleStandupNotes()) {
		for _, grp := range task.GroupNotes(day.Notes) {
			out = append(out, standupGroupRef{key: standupGroupKey(day.Day, grp), day: day.Day, grp: grp})
		}
	}
	return out
}

// standupGroupKey identifies a note group across reloads of the same
// window (day + task, or day + "general" for freestanding notes) so
// collapse state and cursor position survive a background refresh.
func standupGroupKey(day time.Time, grp task.NoteGroup) string {
	id := "general"
	if grp.TaskID != nil {
		id = fmt.Sprintf("%d", *grp.TaskID)
	}
	return day.Format("2006-01-02") + "|" + id
}

// standupMaxScroll returns the highest valid scroll offset for the
// current content and body height, shared by both panes.
func (a app) standupMaxScroll() int {
	h := max(a.bodyHeight, 1)
	leftW, rightW := a.standupWidths()
	left, _ := a.notesPaneLines(leftW)
	right := a.reportPaneLines(rightW)
	return max(max(len(left), len(right))-h, 0)
}

// followStandupCursor adjusts the scroll offset just enough to bring the
// focused note-group's header back into view.
func (a *app) followStandupCursor() {
	h := max(a.bodyHeight, 1)
	leftW, _ := a.standupWidths()
	_, groupStart := a.notesPaneLines(leftW)
	if a.standupCursor < 0 || a.standupCursor >= len(groupStart) {
		return
	}
	line := groupStart[a.standupCursor]
	switch {
	case line < a.standupScroll:
		a.standupScroll = line
	case line >= a.standupScroll+h:
		a.standupScroll = line - h + 1
	}
	a.standupScroll = max(min(a.standupScroll, a.standupMaxScroll()), 0)
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
// generated report on the right, both fitted to exactly bodyHeight rows
// and scrolled together by a.standupScroll.
func (a app) standupView() string {
	h := max(a.bodyHeight, 1)
	leftW, rightW := a.standupWidths()

	leftLines, _ := a.notesPaneLines(leftW)
	rightLines := a.reportPaneLines(rightW)
	scroll := max(min(a.standupScroll, max(max(len(leftLines), len(rightLines))-h, 0)), 0)

	left := fitPane(leftLines, leftW, h, scroll)
	right := fitPane(rightLines, rightW, h, scroll)
	divider := strings.TrimSuffix(
		strings.Repeat(a.styles.Rule.Render(a.styles.Glyphs.RuleV)+"\n", h), "\n")

	return lipgloss.JoinHorizontal(lipgloss.Top,
		strings.Join(left, "\n"), divider, strings.Join(right, "\n"))
}

// fitPane pads every line to the pane width and the pane to exactly
// height rows, starting at the given scroll offset.
func fitPane(lines []string, width, height, offset int) []string {
	if offset > 0 {
		if offset > len(lines) {
			offset = len(lines)
		}
		lines = lines[offset:]
	}
	if len(lines) > height {
		lines = lines[:height]
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
// groupStart holds each group's header line offset within the returned
// lines, indexed the same as standupGroups — nil in chronological mode,
// where there are no groups to focus or collapse.
func (a app) notesPaneLines(width int) (lines []string, groupStart []int) {
	s := a.styles
	lines = []string{"", "  " + s.SubHeader.Render("NOTES")}

	if len(a.standupNotes) == 0 {
		lines = append(lines, "",
			"  "+s.Muted.Render("no notes yet — press ")+
				s.FooterKey.Render("n")+s.Muted.Render(" to add one"))
		return lines, nil
	}
	if len(a.visibleStandupNotes()) == 0 {
		lines = append(lines, "",
			"  "+s.Muted.Render("all notes are recaps — press ")+
				s.FooterKey.Render("C")+s.Muted.Render(" to show them"))
		return lines, nil
	}
	if a.standupChrono {
		return append(lines, a.chronoNoteLines(width)...), nil
	}
	base := len(lines)
	body, offsets := a.groupedNoteLines(width)
	lines = append(lines, body...)
	for i, o := range offsets {
		offsets[i] = o + base
	}
	return lines, offsets
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
// their group and indented beneath its header. The group under the
// cursor is highlighted; a collapsed group (see a.standupCollapsed)
// shows only its header and an entry count. groupStart holds each
// group's header line offset, indexed the same as standupGroups.
func (a app) groupedNoteLines(width int) (lines []string, groupStart []int) {
	s, g := a.styles, a.styles.Glyphs
	now := time.Now()
	const indent = 13 // "      15:04  "

	groups := a.standupGroups()
	var lastDay time.Time
	for i, gr := range groups {
		if i == 0 || !gr.day.Equal(lastDay) {
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1] // previous day's spacing comes from this header
			}
			lines = append(lines, "", a.dayHeadingLine(gr.day, now))
			lastDay = gr.day
		}

		focused := i == a.standupCursor
		collapsed := a.standupCollapsed[gr.key]
		marker := "  "
		if focused {
			marker = s.SelBar.Render(g.SelBar) + " "
		}
		caret := caretStyle(s, focused).Render(caretGlyph(g, !collapsed))

		header := marker + caret + " " + s.Muted.Render("general")
		if gr.grp.TaskID != nil {
			header = marker + caret + " " +
				s.Title.Bold(true).Render(truncTail(gr.grp.Title, max(width-12, 10), g.Ellipsis)) +
				s.DetailID.Render(fmt.Sprintf("  #%d", *gr.grp.TaskID))
		}
		groupStart = append(groupStart, len(lines))
		lines = append(lines, header)

		if collapsed {
			noun := "entry"
			if n := len(gr.grp.Notes); n != 1 {
				noun = "entries"
			}
			lines = append(lines,
				"      "+s.Faint.Render(fmt.Sprintf("%d %s hidden — tab to expand", len(gr.grp.Notes), noun)),
				"")
			continue
		}

		for _, n := range gr.grp.Notes {
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
	return lines, groupStart
}

// chronoNoteLines renders the flat timeline: local-day headers, then
// hanging-indented entries prefixed with their task title.
func (a app) chronoNoteLines(width int) []string {
	s, g := a.styles, a.styles.Glyphs
	now := time.Now()
	var lines []string
	const indent = 11 // "    15:04  "
	for _, day := range task.SplitNotesByDay(a.visibleStandupNotes()) {
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
