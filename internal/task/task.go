// Package task holds the domain types and rules. It has zero I/O and
// depends on nothing else in the module.
package task

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// State is the workflow state of a task. The canonical set lives in the
// states table; these constants mirror the seed rows.
type State string

const (
	StateInbox   State = "inbox"
	StateTodo    State = "todo"
	StateDoing   State = "doing"
	StateBlocked State = "blocked"
	StateDone    State = "done"
	StateSomeday State = "someday"
)

// Valid reports whether s is one of the seeded workflow states.
func (s State) Valid() bool {
	switch s {
	case StateInbox, StateTodo, StateDoing, StateBlocked, StateDone, StateSomeday:
		return true
	}
	return false
}

// ErrEmptyTitle is returned when a captured title is blank.
var ErrEmptyTitle = errors.New("task title is empty")

// Priority bounds: stored values 1 (highest, "A") through 4 ("D"); NULL
// means unprioritized and sorts last.
const (
	PriorityHighest int64 = 1
	PriorityLowest  int64 = 4
)

// PriorityLetter returns "A".."D" for stored values 1..4, "" otherwise.
func PriorityLetter(p *int64) string {
	if p == nil || *p < PriorityHighest || *p > PriorityLowest {
		return ""
	}
	return string(rune('A' + *p - 1))
}

// NormalizeDate parses and canonicalizes an ISO 8601 date (YYYY-MM-DD),
// the only date format the schema stores.
func NormalizeDate(s string) (string, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("invalid date %q (want YYYY-MM-DD)", s)
	}
	return t.Format("2006-01-02"), nil
}

// NormalizeTitle trims surrounding whitespace and rejects blank titles.
// A bare title is the only thing capture requires, so this is the entire
// validation surface for `td add`.
func NormalizeTitle(s string) (string, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", ErrEmptyTitle
	}
	return t, nil
}

// ChildCount summarizes a task's sub-tasks for progress display (the N/M
// indicator in the list and detail pane).
type ChildCount struct {
	Done, Total int64
}

// Task is the domain representation of a row in the tasks table.
// Due and SnoozeUntil stay as ISO 8601 date strings (YYYY-MM-DD); the DB
// compares them lexically and v1 has no date arithmetic to justify parsing.
type Task struct {
	ID          int64
	Title       string
	BodyMD      string
	State       State
	ParentID    *int64
	Project     *string
	Priority    *int64
	Due         *string
	SnoozeUntil *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}
