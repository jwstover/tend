package task

import "time"

// Session is a Claude Code session launched or resumed against a task.
// ExternalID is the claude --session-id UUID; Label is the task title
// snapshotted at launch time, so sessions still read correctly after a
// rename or delete — the same convention Event.TaskTitle and
// LogEntry.TaskTitle use.
type Session struct {
	ID           int64
	TaskID       int64
	ExternalID   string
	Cwd          string
	Label        string
	StartedAt    time.Time
	LastActiveAt time.Time
}
