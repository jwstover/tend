package task

import "time"

// EventKind classifies a row of the task_events activity log.
type EventKind string

const (
	EventCreated EventKind = "created"
	EventState   EventKind = "state"
	EventDeleted EventKind = "deleted"
)

// Event is one row of the append-only activity log. Events record raw
// facts (a state went from Old to New); standup verbs like "started"
// are derived at render time. TaskTitle is a snapshot taken when the
// event was written, so events render even after their task is gone.
type Event struct {
	ID        int64
	TaskID    int64
	TaskTitle string
	Kind      EventKind
	Old       *string
	New       *string
	CreatedAt time.Time
}

// SummaryItem is one task's line in a standup summary.
type SummaryItem struct {
	TaskID int64
	Title  string
}

// Summary aggregates a window of events for standup rendering. Each
// task appears in at most one of Completed/Blocked/Started, chosen by
// that precedence. Triaged counts tasks that left the inbox.
type Summary struct {
	Completed []SummaryItem
	Blocked   []SummaryItem
	Started   []SummaryItem
	Triaged   int
}

// Summarize collapses a window of events (oldest first) into one line
// per task. Transitions are replayed in order so a task that bounced
// around lands where it ended up: done then reopened is not completed,
// blocked then unblocked is not blocked. Started is sticky — touching
// doing at all counts, even if the task moved on.
func Summarize(events []Event) Summary {
	type acc struct {
		title                                string
		completed, blocked, started, triaged bool
	}
	accs := make(map[int64]*acc)
	var order []int64

	for _, ev := range events {
		if ev.Kind != EventState || ev.Old == nil || ev.New == nil {
			continue
		}
		a, ok := accs[ev.TaskID]
		if !ok {
			a = &acc{}
			accs[ev.TaskID] = a
			order = append(order, ev.TaskID)
		}
		a.title = ev.TaskTitle

		switch State(*ev.New) {
		case StateDone:
			a.completed = true
			a.blocked = false
		case StateBlocked:
			a.blocked = true
		case StateDoing:
			a.started = true
		}
		switch State(*ev.Old) {
		case StateDone:
			a.completed = false
		case StateBlocked:
			a.blocked = false
		case StateInbox:
			a.triaged = true
		}
	}

	var sum Summary
	for _, id := range order {
		a := accs[id]
		if a.triaged {
			sum.Triaged++
		}
		item := SummaryItem{TaskID: id, Title: a.title}
		switch {
		case a.completed:
			sum.Completed = append(sum.Completed, item)
		case a.blocked:
			sum.Blocked = append(sum.Blocked, item)
		case a.started:
			sum.Started = append(sum.Started, item)
		}
	}
	return sum
}

// LastWorkdayStart returns local midnight of the most recent weekday
// before t's day, so a Monday standup reports Friday.
func LastWorkdayStart(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	d = d.AddDate(0, 0, -1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// WindowLabel names a reporting window for display: "Yesterday" when it
// starts there, the weekday when it starts within the past week ("Since
// Friday" on a Monday), and the date otherwise.
func WindowLabel(from, now time.Time) string {
	days := int(now.Sub(from).Hours() / 24)
	switch {
	case days <= 1:
		return "Yesterday"
	case days < 7:
		return "Since " + from.Weekday().String()
	default:
		return "Since " + from.Format("2006-01-02")
	}
}
