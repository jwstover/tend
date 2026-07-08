package task

import (
	"strings"
	"testing"
	"time"
)

func note(taskID *int64, title, body string, at time.Time) LogEntry {
	return LogEntry{TaskID: taskID, TaskTitle: title, Body: body, CreatedAt: at}
}

func ptrID(n int64) *int64 { return &n }

func TestGroupNotes(t *testing.T) {
	base := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	groups := GroupNotes([]LogEntry{
		note(ptrID(1), "ship it", "started on the retry logic", base),
		note(nil, "", "paired with sam", base.Add(time.Hour)),
		note(ptrID(2), "review pipeline", "waiting on infra", base.Add(2*time.Hour)),
		note(ptrID(1), "ship it", "fixed for real", base.Add(3*time.Hour)),
	})

	if len(groups) != 3 {
		t.Fatalf("groups = %+v, want 3 (task 1, freestanding, task 2)", groups)
	}
	// Groups keep first-appearance order.
	if groups[0].TaskID == nil || *groups[0].TaskID != 1 || groups[0].Title != "ship it" {
		t.Errorf("group 0 = %+v, want task 1 titled", groups[0])
	}
	if groups[1].TaskID != nil {
		t.Errorf("group 1 = %+v, want freestanding", groups[1])
	}
	if groups[2].TaskID == nil || *groups[2].TaskID != 2 {
		t.Errorf("group 2 = %+v, want task 2", groups[2])
	}
	// Notes stay chronological within their group.
	if len(groups[0].Notes) != 2 || groups[0].Notes[1].Body != "fixed for real" {
		t.Errorf("task 1 notes = %+v, want both, oldest first", groups[0].Notes)
	}
}

func TestLogEntryRef(t *testing.T) {
	if got := (LogEntry{}).Ref(); got != "" {
		t.Errorf("freestanding Ref = %q, want empty", got)
	}
	if got := (LogEntry{TaskID: ptrID(42)}).Ref(); got != "#42" {
		t.Errorf("titleless Ref = %q, want #42 fallback", got)
	}
	if got := (LogEntry{TaskID: ptrID(42), TaskTitle: "ship it"}).Ref(); got != "ship it" {
		t.Errorf("titled Ref = %q, want the title", got)
	}
}

func TestStandupMarkdownGroupsNotes(t *testing.T) {
	base := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	md := StandupMarkdown("Yesterday", []LogEntry{
		note(ptrID(1), "ship it", "started", base),
		note(nil, "", "paired with sam", base.Add(time.Hour)),
		note(ptrID(1), "ship it", "finished", base.Add(2*time.Hour)),
	}, Summary{}, nil)

	if !strings.Contains(md, "- ship it (#1)\n") {
		t.Errorf("markdown missing the titled task group:\n%s", md)
	}
	if !strings.Contains(md, "- general\n") {
		t.Errorf("markdown missing the freestanding group:\n%s", md)
	}
	// Both task notes nest under one group header.
	if strings.Count(md, "- ship it (#1)") != 1 {
		t.Errorf("task group header should appear once:\n%s", md)
	}
	started := strings.Index(md, "started")
	finished := strings.Index(md, "finished")
	paired := strings.Index(md, "paired with sam")
	if started == -1 || finished == -1 || started > finished {
		t.Errorf("group notes out of order:\n%s", md)
	}
	if paired < finished {
		t.Errorf("freestanding note should follow the first group's notes:\n%s", md)
	}
	// The deleted-task fallback keeps the bare id as the group header.
	md = StandupMarkdown("Yesterday", []LogEntry{note(ptrID(9), "", "orphaned", base)}, Summary{}, nil)
	if !strings.Contains(md, "- #9\n") {
		t.Errorf("markdown missing the #id fallback group:\n%s", md)
	}
}

func TestSplitNotesByDay(t *testing.T) {
	loc := time.Local
	mon := time.Date(2026, 7, 6, 9, 0, 0, 0, loc)
	tue := time.Date(2026, 7, 7, 8, 0, 0, 0, loc)
	days := SplitNotesByDay([]LogEntry{
		note(ptrID(1), "ship it", "monday morning", mon),
		note(nil, "", "monday afternoon", mon.Add(6*time.Hour)),
		note(ptrID(1), "ship it", "tuesday", tue),
	})

	if len(days) != 2 {
		t.Fatalf("days = %+v, want 2", days)
	}
	if !days[0].Day.Equal(time.Date(2026, 7, 6, 0, 0, 0, 0, loc)) {
		t.Errorf("day 0 = %v, want Monday local midnight", days[0].Day)
	}
	if len(days[0].Notes) != 2 || len(days[1].Notes) != 1 {
		t.Errorf("bucket sizes = %d/%d, want 2/1", len(days[0].Notes), len(days[1].Notes))
	}
}

func TestDayLabel(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.Local)
	cases := []struct {
		day  time.Time
		want string
	}{
		{time.Date(2026, 7, 7, 0, 0, 0, 0, time.Local), "Today"},
		{time.Date(2026, 7, 6, 0, 0, 0, 0, time.Local), "Yesterday"},
		{time.Date(2026, 7, 3, 0, 0, 0, 0, time.Local), "Fri Jul 3"},
	}
	for _, c := range cases {
		if got := DayLabel(c.day, now); got != c.want {
			t.Errorf("DayLabel(%v) = %q, want %q", c.day, got, c.want)
		}
	}
}

func TestStandupMarkdownSplitsNotesByDay(t *testing.T) {
	mon := time.Date(2026, 7, 6, 9, 0, 0, 0, time.Local)
	tue := mon.AddDate(0, 0, 1)
	md := StandupMarkdown("Yesterday", []LogEntry{
		note(ptrID(1), "ship it", "monday note", mon),
		note(ptrID(1), "ship it", "tuesday note", tue),
	}, Summary{}, nil)

	monHeader := strings.Index(md, "**Notes — Mon Jul 6**")
	tueHeader := strings.Index(md, "**Notes — Tue Jul 7**")
	if monHeader == -1 || tueHeader == -1 || monHeader > tueHeader {
		t.Fatalf("day sections missing or out of order:\n%s", md)
	}
	// The task heads a group in each day it has notes.
	if strings.Count(md, "- ship it (#1)\n") != 2 {
		t.Errorf("task group should appear once per day:\n%s", md)
	}
	monNote := strings.Index(md, "monday note")
	tueNote := strings.Index(md, "tuesday note")
	if monHeader >= monNote || monNote >= tueHeader || tueHeader >= tueNote {
		t.Errorf("notes not inside their day sections:\n%s", md)
	}
}
