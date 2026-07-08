package task

import (
	"testing"
	"time"
)

func stateEvent(taskID int64, title, old, new string) Event {
	return Event{TaskID: taskID, TaskTitle: title, Kind: EventState, Old: &old, New: &new}
}

func TestSummarizePrecedence(t *testing.T) {
	sum := Summarize([]Event{
		stateEvent(1, "ship it", "todo", "doing"),
		stateEvent(1, "ship it", "doing", "done"),
		stateEvent(2, "stuck", "todo", "doing"),
		stateEvent(2, "stuck", "doing", "blocked"),
		stateEvent(3, "underway", "todo", "doing"),
	})
	if len(sum.Completed) != 1 || sum.Completed[0].TaskID != 1 {
		t.Errorf("Completed = %v, want task 1 only", sum.Completed)
	}
	if len(sum.Blocked) != 1 || sum.Blocked[0].TaskID != 2 {
		t.Errorf("Blocked = %v, want task 2 only (blocked wins over started)", sum.Blocked)
	}
	if len(sum.Started) != 1 || sum.Started[0].TaskID != 3 {
		t.Errorf("Started = %v, want task 3 only", sum.Started)
	}
}

func TestSummarizeReplaysBounces(t *testing.T) {
	sum := Summarize([]Event{
		// done then reopened: not completed, but doing was touched.
		stateEvent(1, "reopened", "doing", "done"),
		stateEvent(1, "reopened", "done", "doing"),
		// blocked then unblocked back to todo: nothing to report.
		stateEvent(2, "unblocked", "todo", "blocked"),
		stateEvent(2, "unblocked", "blocked", "todo"),
	})
	if len(sum.Completed) != 0 {
		t.Errorf("Completed = %v, want empty after reopen", sum.Completed)
	}
	if len(sum.Blocked) != 0 {
		t.Errorf("Blocked = %v, want empty after unblock", sum.Blocked)
	}
	if len(sum.Started) != 1 || sum.Started[0].TaskID != 1 {
		t.Errorf("Started = %v, want task 1 (started is sticky)", sum.Started)
	}
}

func TestSummarizeTriage(t *testing.T) {
	sum := Summarize([]Event{
		stateEvent(1, "a", "inbox", "todo"),
		stateEvent(2, "b", "inbox", "someday"),
		stateEvent(3, "c", "inbox", "doing"),
		stateEvent(1, "a", "todo", "doing"), // second transition, still one triage
	})
	if sum.Triaged != 3 {
		t.Errorf("Triaged = %d, want 3", sum.Triaged)
	}
	if len(sum.Started) != 2 {
		t.Errorf("Started = %v, want tasks 1 and 3", sum.Started)
	}
}

func TestSummarizeIgnoresNonStateEvents(t *testing.T) {
	st := "inbox"
	sum := Summarize([]Event{
		{TaskID: 1, TaskTitle: "captured", Kind: EventCreated, New: &st},
		{TaskID: 2, TaskTitle: "removed", Kind: EventDeleted, Old: &st},
	})
	if len(sum.Completed)+len(sum.Blocked)+len(sum.Started)+sum.Triaged != 0 {
		t.Errorf("Summarize = %+v, want empty summary", sum)
	}
}

func TestLastWorkdayStart(t *testing.T) {
	loc := time.FixedZone("test", -7*3600)
	cases := []struct {
		now  time.Time
		want time.Time
	}{
		// Monday morning reports Friday.
		{time.Date(2026, 7, 6, 9, 0, 0, 0, loc), time.Date(2026, 7, 3, 0, 0, 0, 0, loc)},
		// Tuesday reports Monday.
		{time.Date(2026, 7, 7, 9, 0, 0, 0, loc), time.Date(2026, 7, 6, 0, 0, 0, 0, loc)},
		// Sunday reports Friday too.
		{time.Date(2026, 7, 5, 9, 0, 0, 0, loc), time.Date(2026, 7, 3, 0, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		if got := LastWorkdayStart(c.now); !got.Equal(c.want) {
			t.Errorf("LastWorkdayStart(%v) = %v, want %v", c.now, got, c.want)
		}
	}
}
