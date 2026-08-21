package task

import "time"

// SessionStatus is what tend last observed about a Claude Code session's
// own state. It is a cache of something watched from outside the
// process, not a workflow the user moves a row through — which is why
// it's a plain string column with no states-table foreign key the way
// Task.State has. A value tend doesn't recognize reads as SessionUnknown
// rather than failing.
type SessionStatus string

const (
	// SessionUnknown is the honest default: a session tend has never
	// heard from. Also where a brand-new session sits until its first
	// hook event lands against a row that exists (see the ordering note
	// on Session.Status).
	SessionUnknown SessionStatus = "unknown"
	// SessionStarting is set by the SessionStart hook — claude is up,
	// but nothing has been observed about what it's doing yet.
	SessionStarting SessionStatus = "starting"
	// SessionWorking means claude is mid-turn. No hook reports this;
	// Claude Code fires nothing during a tool call, so only the
	// capture-pane poller ever writes it.
	SessionWorking SessionStatus = "working"
	// SessionIdle is set by the Stop hook — claude finished a turn and
	// is sitting at the input prompt.
	SessionIdle SessionStatus = "idle"
	// SessionBlocked is set by the Notification hook — claude is waiting
	// on the user, typically a permission prompt.
	SessionBlocked SessionStatus = "blocked"
	// SessionEnded is set by the SessionEnd hook. Note this fires on
	// /clear too, where the process keeps running, so it is never the
	// authority on liveness — `tmux has-session` is.
	SessionEnded SessionStatus = "ended"
)

// Session is a Claude Code session launched or resumed against a task.
// ExternalID is the claude --session-id UUID; Label is the task title
// snapshotted at launch time, so sessions still read correctly after a
// rename or delete — the same convention Event.TaskTitle and
// LogEntry.TaskTitle use.
//
// TmuxSession is the name of the tmux session wrapping this one, empty
// when it wasn't launched under tmux (no tmux on $PATH, or a row written
// before tmux-backed launch/attach existed). A non-empty name is a
// candidate for attaching, not a promise the session is still alive —
// only `tmux has-session` answers that.
//
// NeedsRecap marks a session that was backgrounded rather than exited,
// so its recap was deliberately skipped and is still owed. Any tend
// instance drains it once the session is really gone.
//
// Status is hook-reported and StatusUpdatedAt is when it last changed,
// zero if never. Both are a convenience indicator, deliberately
// not a source of truth: a session's row isn't written until its first
// terminal handoff *returns*, so hook events fired during a brand-new
// session's first run land on no row at all and are dropped.
type Session struct {
	ID              int64
	TaskID          int64
	ExternalID      string
	Cwd             string
	Label           string
	TmuxSession     string
	NeedsRecap      bool
	Status          SessionStatus
	StatusUpdatedAt time.Time
	StartedAt       time.Time
	LastActiveAt    time.Time
}
