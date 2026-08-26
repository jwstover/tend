package task

import "time"

// EventKind classifies a row of the task_events activity log.
type EventKind string

const (
	EventCreated EventKind = "created"
	EventState   EventKind = "state"
	EventDeleted EventKind = "deleted"
	// EventProject records a task moving between projects. Old and New
	// hold project *names*, snapshotted like TaskTitle, so the log stays
	// readable after a project is renamed or deleted.
	EventProject EventKind = "project"
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

// MovedItem is one task's project move. To is where it ended up, so a
// task moved twice in the window reports its destination, not its route.
type MovedItem struct {
	TaskID int64
	Title  string
	To     string
}

// Summary aggregates a window of events for standup rendering. Each
// task appears in at most one of Completed/Blocked/Started, chosen by
// that precedence. Triaged counts tasks that left the inbox.
type Summary struct {
	Completed []SummaryItem
	Blocked   []SummaryItem
	Started   []SummaryItem
	// Moved is independent of the three above: a task can be completed
	// and have changed project in the same window, and both are worth
	// reporting.
	Moved   []MovedItem
	Triaged int
}

// Empty reports whether a summary has nothing worth printing.
func (s Summary) Empty() bool {
	return len(s.Completed)+len(s.Blocked)+len(s.Started)+len(s.Moved) == 0 && s.Triaged == 0
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
		moved                                bool
		movedTo                              string
	}
	accs := make(map[int64]*acc)
	var order []int64

	touch := func(ev Event) *acc {
		a, ok := accs[ev.TaskID]
		if !ok {
			a = &acc{}
			accs[ev.TaskID] = a
			order = append(order, ev.TaskID)
		}
		a.title = ev.TaskTitle
		return a
	}

	for _, ev := range events {
		if ev.Kind == EventProject {
			if ev.New == nil {
				continue
			}
			a := touch(ev)
			// Last move wins: the window reports where a task ended up.
			a.moved, a.movedTo = true, *ev.New
			continue
		}
		if ev.Kind != EventState || ev.Old == nil || ev.New == nil {
			continue
		}
		a := touch(ev)

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
		if a.moved {
			sum.Moved = append(sum.Moved, MovedItem{TaskID: id, Title: a.title, To: a.movedTo})
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
