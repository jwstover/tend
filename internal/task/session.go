package task

import "time"

// Session is a Claude Code session launched or resumed against a task.
// ExternalID is the claude --session-id UUID; Label is the task title
// snapshotted at launch time, so sessions still read correctly after a
// rename or delete — the same convention Event.TaskTitle and
// LogEntry.TaskTitle use.
//
// TmuxSession is the name of the tmux session wrapping this one, empty
// when it wasn't launched under tmux (no tmux on $PATH, or a row written
// before docs/agent-sessions-plan.md §8.1). A non-empty name is a
// candidate for attaching, not a promise the session is still alive —
// only `tmux has-session` answers that.
//
// NeedsRecap marks a session that was backgrounded rather than exited,
// so its recap was deliberately skipped and is still owed. Phase 4.2
// drains it; nothing does yet.
type Session struct {
	ID           int64
	TaskID       int64
	ExternalID   string
	Cwd          string
	Label        string
	TmuxSession  string
	NeedsRecap   bool
	StartedAt    time.Time
	LastActiveAt time.Time
}
