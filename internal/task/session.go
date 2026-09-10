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
	// SessionUnknown is the honest default for a session tend has never
	// heard from: a row from before launch-time rows existed, or a
	// status value tend doesn't recognize. A session tend launched
	// itself never starts here — it starts at SessionStarting.
	SessionUnknown SessionStatus = "unknown"
	// SessionStarting means claude is being started, or is up, but
	// nothing has been observed about what it's doing yet. Written at
	// launch when the row is created (see Session.Status) and again by
	// the SessionStart hook.
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
// Status is what tend last observed about the session and StatusUpdatedAt
// is when that changed; zero only for a row from before rows were written
// at launch. Both are a convenience indicator, deliberately not a source
// of truth (`tmux has-session` is; see SessionEnded). The row is written
// at launch, ahead of the terminal handoff, with Status SessionStarting —
// so the session's own hooks find it from the very first turn, and a
// fresh session reads starting, then idle, without ever being resumed.
//
// StepRunID is set when the session ran a workflow step (it points at
// that step run, see internal/workflow) so the SESSIONS section can say
// which step a session belonged to; nil for an ordinary session.
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
	StepRunID       *int64
}

// TaskSession is a Session together with the title and state of the task
// it belongs to, for lists that span tasks — the agents view shows every
// session in a project, and a row there has to say whose it is. Sessions
// listed under one task (ListSessionsForTask) already know, so they stay
// plain Sessions.
type TaskSession struct {
	Session
	TaskTitle string
	TaskState State
}

// Headless reports whether the session ran a workflow step under the
// runner rather than in a terminal of its own: it has a step run and no
// tmux session (the pane is the runner's, not claude's). Such a session
// has no pane to attach to, and its transcript is the runner's to drive
// while its run is live.
func (s Session) Headless() bool {
	return s.StepRunID != nil && s.TmuxSession == ""
}
