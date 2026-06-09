// Package task holds the domain types and rules. It has zero I/O and
// depends on nothing else in the module.
package task

import (
	"errors"
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

// ErrEmptyTitle is returned when a captured title is blank.
var ErrEmptyTitle = errors.New("task title is empty")

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
