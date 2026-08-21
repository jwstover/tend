package tui

import (
	"context"
	"sync"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// stubCapturePane pins pollSessionStatusCmd's pane read to fn, without a
// real tmux server — the same seam stubSessionAlive gives the drain its
// liveness answer.
func stubCapturePane(t *testing.T, fn func(task.Session) (string, error)) {
	t.Helper()
	prev := capturePane
	capturePane = fn
	t.Cleanup(func() { capturePane = prev })
}

const workingChromeFixture = "  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents"
const idleChromeFixture = "  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"

// The core case: a live session whose pane shows active-tool chrome gets
// promoted to working — the one status no Claude Code hook can report
// (docs/agent-sessions-plan.md §8.3).
func TestPollSessionStatusWritesWorkingForLiveMatchingSession(t *testing.T) {
	stubSessionAlive(t, true)
	stubCapturePane(t, func(task.Session) (string, error) {
		return workingChromeFixture, nil
	})
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	drive(t, m, pollTickMsg{})

	waitFor(t, "status flips to working", func() bool {
		sessions, err := s.ListSessionsForTask(ctx, parent.ID)
		return err == nil && len(sessions) == 1 && sessions[0].Status == task.SessionWorking
	})
}

// A dead session (tmux gone) must never be captured at all — has-session
// is the liveness authority, same as the recap drain's sessionAlive.
func TestPollSessionStatusSkipsDeadSession(t *testing.T) {
	stubSessionAlive(t, false)
	var mu sync.Mutex
	called := false
	stubCapturePane(t, func(task.Session) (string, error) {
		mu.Lock()
		called = true
		mu.Unlock()
		return workingChromeFixture, nil
	})
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	drive(t, m, pollTickMsg{})

	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Error("capturePane was called for a session with no live tmux session")
	}
	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionUnknown {
		t.Errorf("Status = %q, want unchanged", sessions[0].Status)
	}
}

// Pane chrome that doesn't match must leave whatever status a hook
// already set alone — the poller is a refiner that only ever overrides
// toward working, never a general-purpose status writer.
func TestPollSessionStatusLeavesNonWorkingChromeAlone(t *testing.T) {
	stubSessionAlive(t, true)
	stubCapturePane(t, func(task.Session) (string, error) {
		return idleChromeFixture, nil
	})
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.SetSessionStatus(ctx, "ext-1", task.SessionBlocked); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}

	drive(t, m, pollTickMsg{})

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionBlocked {
		t.Errorf("Status = %q, want blocked left standing — idle chrome must not touch it", sessions[0].Status)
	}
}

// The race the CAS exists for, exercised through the poller rather than
// the store directly: a hook lands in the window between
// SessionsWithTmux's read and the poller's write (simulated here inside
// the capturePane stub, which runs after that read and before the
// write). The hook's status must win.
func TestPollSessionStatusLosesRaceToConcurrentHook(t *testing.T) {
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
	stubCapturePane(t, func(sess task.Session) (string, error) {
		if err := s.SetSessionStatus(ctx, sess.ExternalID, task.SessionBlocked); err != nil {
			t.Fatalf("SetSessionStatus: %v", err)
		}
		return workingChromeFixture, nil
	})

	drive(t, m, pollTickMsg{})

	waitFor(t, "the hook's status left standing", func() bool {
		sessions, err := s.ListSessionsForTask(ctx, parent.ID)
		return err == nil && len(sessions) == 1 && sessions[0].Status == task.SessionBlocked
	})
}
