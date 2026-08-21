package tui

import (
	"context"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// The gap backgrounding otherwise leaves open: a session backgrounded and
// never re-attached owes a recap that nothing settles. Once it's really
// gone, any tend instance that refreshes must settle it.
func TestDrainRecapsSettlesDeadBackgroundedSession(t *testing.T) {
	stubRecap(t, "LABEL: wrapped up\nRECAP: finished the migration", nil)
	stubSessionAlive(t, false)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.SetSessionNeedsRecap(ctx, "ext-1", true); err != nil {
		t.Fatalf("SetSessionNeedsRecap: %v", err)
	}

	drive(t, m, refreshMsg{})

	waitFor(t, "the owed recap logged", func() bool {
		entries, err := s.ListTaskLog(ctx, parent.ID)
		return err == nil && len(entries) == 1
	})
	waitFor(t, "the debt cleared", func() bool {
		owed, err := s.ListSessionsNeedingRecap(ctx)
		return err == nil && len(owed) == 0
	})
}

// The hazard the whole drain is built around: firing `claude -p --resume`
// against a session that's still running would put two processes on one
// session id and one transcript. Liveness is decided by tmux, never by
// the stored status.
func TestDrainRecapsSkipsLiveSession(t *testing.T) {
	stubRecap(t, "LABEL: nope\nRECAP: should never run", nil)
	stubSessionAlive(t, true)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.SetSessionNeedsRecap(ctx, "ext-1", true); err != nil {
		t.Fatalf("SetSessionNeedsRecap: %v", err)
	}

	drive(t, m, refreshMsg{})

	entries, err := s.ListTaskLog(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListTaskLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("log entries = %+v, want none — a live session must not be recapped", entries)
	}
	owed, err := s.ListSessionsNeedingRecap(ctx)
	if err != nil {
		t.Fatalf("ListSessionsNeedingRecap: %v", err)
	}
	if len(owed) != 1 {
		t.Errorf("owed = %+v, want the debt still outstanding", owed)
	}
}

// A session marked ended by the SessionEnd hook but whose tmux session is
// still alive — what /clear produces — must still be left alone.
func TestDrainRecapsTrustsTmuxOverStoredStatus(t *testing.T) {
	stubRecap(t, "LABEL: nope\nRECAP: should never run", nil)
	stubSessionAlive(t, true)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.SetSessionNeedsRecap(ctx, "ext-1", true); err != nil {
		t.Fatalf("SetSessionNeedsRecap: %v", err)
	}
	if err := s.SetSessionStatus(ctx, "ext-1", task.SessionEnded); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}

	drive(t, m, refreshMsg{})

	entries, err := s.ListTaskLog(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListTaskLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("log entries = %+v, want none — has-session outranks the stored status", entries)
	}
}

// The mirror case: a host that died never fired SessionEnd, so the status
// is still whatever it was. The drain must not require 'ended'.
func TestDrainRecapsSettlesSessionThatNeverReportedEnded(t *testing.T) {
	stubRecap(t, "LABEL: after a reboot\nRECAP: settled late", nil)
	stubSessionAlive(t, false)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.SetSessionNeedsRecap(ctx, "ext-1", true); err != nil {
		t.Fatalf("SetSessionNeedsRecap: %v", err)
	}
	if err := s.SetSessionStatus(ctx, "ext-1", task.SessionIdle); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}

	drive(t, m, refreshMsg{})

	waitFor(t, "the owed recap logged despite a non-ended status", func() bool {
		entries, err := s.ListTaskLog(ctx, parent.ID)
		return err == nil && len(entries) == 1
	})
}

// Claiming happens before the recap runs, so a second instance draining
// the same debt finds nothing left to do.
func TestDrainRecapsClaimsBeforeRunning(t *testing.T) {
	stubRecap(t, "LABEL: once\nRECAP: only once", nil)
	stubSessionAlive(t, false)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.SetSessionNeedsRecap(ctx, "ext-1", true); err != nil {
		t.Fatalf("SetSessionNeedsRecap: %v", err)
	}

	drive(t, m, refreshMsg{})
	waitFor(t, "the first drain to land", func() bool {
		entries, err := s.ListTaskLog(ctx, parent.ID)
		return err == nil && len(entries) == 1
	})

	// A second refresh stands in for another tend instance arriving late.
	drive(t, m, refreshMsg{})

	entries, err := s.ListTaskLog(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListTaskLog: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("log entries = %d, want exactly 1 — the debt must only be claimed once", len(entries))
	}
}
