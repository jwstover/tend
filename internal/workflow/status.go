package workflow

import "github.com/jwstover/tend/internal/task"

// SessionStatus maps a live run's state into the agent-session status
// vocabulary the TUI already renders -- the gutter marker, the `g a`
// grouping, the detail pane glyphs -- so a task with a headless run in
// flight reads the same way a task with an interactive session does,
// with no second glyph family or status column (tend task #183).
//
//	pending        -> starting   (a runner has yet to claim it)
//	running        -> working    (a step is executing)
//	waiting_review -> blocked    (a gate is waiting on the user)
//	paused         -> idle       (nothing is happening until resumed)
//
// Terminal states report false: a finished run says nothing about the
// task's agent status, and whatever its sessions last reported stands.
func (s RunState) SessionStatus() (task.SessionStatus, bool) {
	switch s {
	case RunPending:
		return task.SessionStarting, true
	case RunRunning:
		return task.SessionWorking, true
	case RunWaitingReview:
		return task.SessionBlocked, true
	case RunPaused:
		return task.SessionIdle, true
	}
	return "", false
}
