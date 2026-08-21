package tui

import (
	"context"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// Backgrounding a session's whole point is a negative property: the
// recap must NOT fire. Firing `claude -p --resume`
// against a still-running session would put two processes on one session
// id and one transcript file. stubRecap would log an entry if the recap
// ran, so an empty log is the assertion.
func TestBackgroundedSessionSkipsRecap(t *testing.T) {
	stubRecap(t, "did the thing", nil)
	stubSessionAlive(t, true)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, sessionFinishedMsg{
		taskID:       parent.ID,
		externalID:   "ext-1",
		cwd:          "/tmp/work",
		label:        parent.Title,
		tmuxSession:  "tend-ext-1",
		backgrounded: true,
	})
	_ = m

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want the backgrounded session still recorded", len(sessions))
	}
	if sessions[0].TmuxSession != "tend-ext-1" {
		t.Errorf("TmuxSession = %q, want tend-ext-1 so it can be attached later", sessions[0].TmuxSession)
	}
	if !sessions[0].NeedsRecap {
		t.Error("NeedsRecap = false, want the skipped recap recorded as owed")
	}

	entries, err := s.ListTaskLog(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListTaskLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("log entries = %+v, want none — a live session must not be recapped", entries)
	}
}

// The contrast case: a session that really ended still recaps exactly as
// it would with no tmux wrapping at all, so backgrounding support
// changed nothing about the normal path.
func TestExitedSessionStillRecaps(t *testing.T) {
	stubRecap(t, "did the thing", nil)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, sessionFinishedMsg{
		taskID:      parent.ID,
		externalID:  "ext-1",
		cwd:         "/tmp/work",
		label:       parent.Title,
		tmuxSession: "tend-ext-1",
	})
	_ = m

	waitFor(t, "recap logged for an exited session", func() bool {
		entries, err := s.ListTaskLog(ctx, parent.ID)
		return err == nil && len(entries) == 1
	})

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 1 || sessions[0].NeedsRecap {
		t.Errorf("sessions = %+v, want NeedsRecap false — the recap was not skipped", sessions)
	}
}

// Resuming and then backgrounding bumps last_active_at but likewise owes
// a recap rather than firing one.
func TestBackgroundedResumeSkipsRecap(t *testing.T) {
	stubRecap(t, "did more", nil)
	stubSessionAlive(t, true)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, sessionResumedMsg{
		sessionRowID: sess.ID,
		taskID:       parent.ID,
		cwd:          "/tmp/work",
		externalID:   "ext-1",
		backgrounded: true,
	})
	_ = m

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 1 || !sessions[0].NeedsRecap {
		t.Fatalf("sessions = %+v, want NeedsRecap true", sessions)
	}

	entries, err := s.ListTaskLog(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListTaskLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("log entries = %+v, want none", entries)
	}
}

// stubSessionAlive pins the drain's liveness answer so a test can decide
// whether a backgrounded session counts as still running, without a real
// tmux server — the same seam stubRecap provides for the recap call.
func stubSessionAlive(t *testing.T, alive bool) {
	t.Helper()
	prev := sessionAlive
	sessionAlive = func(task.Session) bool { return alive }
	t.Cleanup(func() { sessionAlive = prev })
}
