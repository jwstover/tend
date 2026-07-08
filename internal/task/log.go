package task

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrEmptyNote is returned when a captured log entry is blank.
var ErrEmptyNote = errors.New("log entry is empty")

// LogEntry is a manual standup note: a quick free-form line captured via
// the TUI or `tend log`. TaskID is optional context; the note stands on
// its own if the task is later deleted. TaskTitle is joined in for
// display where the store query provides it — empty for freestanding
// notes, deleted tasks, or queries that don't need it.
type LogEntry struct {
	ID        int64
	TaskID    *int64
	TaskTitle string
	Body      string
	CreatedAt time.Time
}

// Ref renders the note's task reference for display: the title when
// known, "#42" when only the id survives, "" when freestanding.
func (n LogEntry) Ref() string {
	switch {
	case n.TaskID == nil:
		return ""
	case n.TaskTitle == "":
		return fmt.Sprintf("#%d", *n.TaskID)
	default:
		return n.TaskTitle
	}
}

// NoteGroup is one task's notes — or the freestanding notes when TaskID
// is nil — for the standup view's grouped rendering.
type NoteGroup struct {
	TaskID *int64
	Title  string // task title, "#42" fallback, or "" for freestanding
	Notes  []LogEntry
}

// DayNotes is one local calendar day's slice of a notes window.
type DayNotes struct {
	Day   time.Time // local midnight of the day
	Notes []LogEntry
}

// SplitNotesByDay buckets a chronological notes window into local
// calendar days, preserving order — the standup view's top-level
// sections, so yesterday's notes never mix with today's.
func SplitNotesByDay(notes []LogEntry) []DayNotes {
	var days []DayNotes
	for _, n := range notes {
		local := n.CreatedAt.Local()
		day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
		if len(days) == 0 || !days[len(days)-1].Day.Equal(day) {
			days = append(days, DayNotes{Day: day})
		}
		days[len(days)-1].Notes = append(days[len(days)-1].Notes, n)
	}
	return days
}

// DayLabel names a local calendar day relative to now: "Today",
// "Yesterday", or the date itself.
func DayLabel(day, now time.Time) string {
	sameDay := func(a, b time.Time) bool {
		return a.Year() == b.Year() && a.YearDay() == b.YearDay()
	}
	switch {
	case sameDay(day, now):
		return "Today"
	case sameDay(day, now.AddDate(0, 0, -1)):
		return "Yesterday"
	default:
		return day.Format("Mon Jan 2")
	}
}

// GroupNotes buckets a window of notes (oldest first) by task. Groups
// keep the order each task first appeared in the window, and notes stay
// chronological within their group, so every group reads as that
// workstream's narrative.
func GroupNotes(notes []LogEntry) []NoteGroup {
	byKey := make(map[int64]int) // task id (0 = freestanding) → groups index
	var groups []NoteGroup
	for _, n := range notes {
		key := int64(0)
		if n.TaskID != nil {
			key = *n.TaskID
		}
		i, ok := byKey[key]
		if !ok {
			i = len(groups)
			byKey[key] = i
			groups = append(groups, NoteGroup{TaskID: n.TaskID, Title: n.Ref()})
		}
		groups[i].Notes = append(groups[i].Notes, n)
	}
	return groups
}

// NormalizeNote trims surrounding whitespace and rejects blank notes —
// the entire validation surface, mirroring capture.
func NormalizeNote(s string) (string, error) {
	n := strings.TrimSpace(s)
	if n == "" {
		return "", ErrEmptyNote
	}
	return n, nil
}

// StandupMarkdown renders the standup export shared by `tend standup`
// and the TUI's yank: manual notes first (the user's own words), then
// the report generated from the event log. label names the reporting
// window ("Yesterday", "Since Friday"). Note timestamps render in local
// time; CreatedAt is stored UTC.
func StandupMarkdown(label string, notes []LogEntry, sum Summary, live []Task) string {
	var b strings.Builder

	for _, day := range SplitNotesByDay(notes) {
		fmt.Fprintf(&b, "**Notes — %s**\n", day.Day.Format("Mon Jan 2"))
		for _, grp := range GroupNotes(day.Notes) {
			switch {
			case grp.TaskID == nil:
				b.WriteString("- general\n")
			case grp.Title != fmt.Sprintf("#%d", *grp.TaskID):
				fmt.Fprintf(&b, "- %s (#%d)\n", grp.Title, *grp.TaskID)
			default:
				fmt.Fprintf(&b, "- %s\n", grp.Title)
			}
			for _, n := range grp.Notes {
				lines := strings.Split(n.Body, "\n")
				fmt.Fprintf(&b, "  - %s — %s\n",
					n.CreatedAt.Local().Format("15:04"), lines[0])
				for _, l := range lines[1:] {
					fmt.Fprintf(&b, "    %s\n", l)
				}
			}
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "**%s**\n", label)
	for _, it := range sum.Completed {
		fmt.Fprintf(&b, "- Completed: %s (#%d)\n", it.Title, it.TaskID)
	}
	for _, it := range sum.Blocked {
		fmt.Fprintf(&b, "- Blocked: %s (#%d)\n", it.Title, it.TaskID)
	}
	for _, it := range sum.Started {
		fmt.Fprintf(&b, "- Started: %s (#%d)\n", it.Title, it.TaskID)
	}
	if sum.Triaged > 0 {
		fmt.Fprintf(&b, "- Triaged %d inbox item(s)\n", sum.Triaged)
	}
	if len(sum.Completed)+len(sum.Blocked)+len(sum.Started) == 0 && sum.Triaged == 0 {
		b.WriteString("- nothing logged\n")
	}

	b.WriteString("\n**Today**\n")
	today := 0
	for _, t := range live {
		if t.State == StateDoing {
			fmt.Fprintf(&b, "- %s (#%d)\n", t.Title, t.ID)
			today++
		}
	}
	if today == 0 {
		b.WriteString("- nothing in progress\n")
	}

	b.WriteString("\n**Blockers**\n")
	blockers := 0
	for _, t := range live {
		if t.State == StateBlocked {
			fmt.Fprintf(&b, "- %s (#%d)\n", t.Title, t.ID)
			blockers++
		}
	}
	if blockers == 0 {
		b.WriteString("- none\n")
	}
	return b.String()
}
