package tui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/task"
)

// stubCapturePane pins pollSessions' pane read to fn, without a real tmux
// server — the same seam stubSessionAlive gives the drain its liveness
// answer.
func stubCapturePane(t *testing.T, fn func(task.Session) (string, error)) {
	t.Helper()
	prev := capturePane
	capturePane = fn
	t.Cleanup(func() { capturePane = prev })
}

const workingChromeFixture = "  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents"
const idleChromeFixture = "  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
const blockedChromeFixture = "Enter to select · Tab/Arrow keys to navigate · Esc to cancel"

// The core case: a live session whose pane shows active-tool chrome gets
// promoted to working — the one status no Claude Code hook can report.
func TestPollSessionsWritesWorkingForLiveMatchingSession(t *testing.T) {
	stubSessionAlive(t, true)
	stubCapturePane(t, func(task.Session) (string, error) {
		return workingChromeFixture, nil
	})
	ctx := context.Background()
	_, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	pollSessions(ctx, s, pollInterval)

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionWorking {
		t.Errorf("Status = %q, want working", sessions[0].Status)
	}
}

// A dead session (tmux gone) must never be captured — has-session is the
// liveness authority, same as the recap drain's sessionAlive — and must
// be written 'ended' itself, since a host that died took its chance to
// fire SessionEnd with it. Without this the row's last hook-reported
// status (here 'unknown', a session no hook has touched yet) would read
// as live forever.
func TestPollSessionsMarksDeadSessionEnded(t *testing.T) {
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
	_, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if changed := pollSessions(ctx, s, pollInterval); !changed {
		t.Error("pollSessions reported no change for a dead session")
	}

	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Error("capturePane was called for a session with no live tmux session")
	}
	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionEnded {
		t.Errorf("Status = %q, want ended", sessions[0].Status)
	}
}

// The CAS guard applies to the ended write too: a hook that reports in
// (however unlikely once tmux is already gone) between SessionsWithTmux's
// read and the poller's write must win, not the poller's ended guess.
func TestPollSessionsEndedWriteLosesRaceToConcurrentHook(t *testing.T) {
	ctx := context.Background()
	_, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Race simulated the same way the working/idle CAS tests do it: the
	// hook's write happens between SessionsWithTmux's read (inside
	// pollSessions) and sessionAlive's answer, via the sessionAlive stub
	// itself rather than capturePane, since a dead session never reaches
	// capturePane at all.
	prev := sessionAlive
	sessionAlive = func(task.Session) bool {
		if err := s.SetSessionStatus(ctx, sess.ExternalID, task.SessionBlocked); err != nil {
			t.Fatalf("SetSessionStatus: %v", err)
		}
		return false
	}
	t.Cleanup(func() { sessionAlive = prev })

	pollSessions(ctx, s, pollInterval)

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionBlocked {
		t.Errorf("Status = %q, want the hook's status left standing", sessions[0].Status)
	}
}

// Idle chrome observed on a session a hook just marked blocked must leave
// it alone within the same tick — the settle path only fires once
// settleAfter has actually elapsed, not on first observation.
func TestPollSessionsLeavesFreshBlockedAlone(t *testing.T) {
	stubSessionAlive(t, true)
	stubCapturePane(t, func(task.Session) (string, error) {
		return idleChromeFixture, nil
	})
	ctx := context.Background()
	_, s := newTestApp(t)
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

	pollSessions(ctx, s, pollInterval)

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionBlocked {
		t.Errorf("Status = %q, want blocked left standing — too soon to settle", sessions[0].Status)
	}
}

// The bug this exists to fix: a session a hook marked blocked (e.g. a
// Notification whose matching Stop never landed) and which has since sat
// unchanged past settleAfter, with a pane showing neither working nor
// blocked chrome, must settle back to idle — otherwise nothing ever
// moves it off blocked again.
func TestPollSessionsSettlesStaleBlockedToIdle(t *testing.T) {
	stubSessionAlive(t, true)
	stubCapturePane(t, func(task.Session) (string, error) {
		return idleChromeFixture, nil
	})
	ctx := context.Background()
	_, s := newTestApp(t)
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

	// settleAfter: 0 stands in for "well past the floor" without a real
	// sleep — pollSessions takes it as a parameter for exactly this.
	pollSessions(ctx, s, 0)

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionIdle {
		t.Errorf("Status = %q, want settled to idle", sessions[0].Status)
	}
}

// A pane that still shows recognized blocked chrome must never be
// settled to idle, no matter how long the status has sat unchanged — a
// session genuinely still waiting on the user must never lose that
// signal just because time passed.
func TestPollSessionsNeverSettlesWhileBlockedChromeShows(t *testing.T) {
	stubSessionAlive(t, true)
	stubCapturePane(t, func(task.Session) (string, error) {
		return blockedChromeFixture, nil
	})
	ctx := context.Background()
	_, s := newTestApp(t)
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

	pollSessions(ctx, s, 0)

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionBlocked {
		t.Errorf("Status = %q, want blocked left standing — chrome still shows it's open", sessions[0].Status)
	}
}

// A freshly-started session (SessionStart fired, nothing else yet) that
// turns out to already be sitting idle settles the same way a stale
// blocked does — 'starting' is just as eligible.
func TestPollSessionsSettlesStaleStartingToIdle(t *testing.T) {
	stubSessionAlive(t, true)
	stubCapturePane(t, func(task.Session) (string, error) {
		return idleChromeFixture, nil
	})
	ctx := context.Background()
	_, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.SetSessionStatus(ctx, "ext-1", task.SessionStarting); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}

	pollSessions(ctx, s, 0)

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionIdle {
		t.Errorf("Status = %q, want settled to idle", sessions[0].Status)
	}
}

// The race the CAS exists for: a hook lands in the window between
// SessionsWithTmux's read and the poller's write (simulated here inside
// the capturePane stub, which runs after that read and before the
// write). The hook's status must win.
func TestPollSessionsLosesRaceToConcurrentHook(t *testing.T) {
	stubSessionAlive(t, true)
	ctx := context.Background()
	_, s := newTestApp(t)
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

	pollSessions(ctx, s, pollInterval)

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionBlocked {
		t.Errorf("Status = %q, want the hook's status left standing", sessions[0].Status)
	}
}

// The self-correction case: a session already marked working (whether
// from a genuine prior tick or a stale/racy one) whose pane no longer
// shows working chrome must be taken back down to idle. Without this, a
// wrongly-set working status would stay pinned until the next hook fires
// — which might never happen if the user simply stops interacting.
func TestPollSessionsDemotesStaleWorkingToIdle(t *testing.T) {
	stubSessionAlive(t, true)
	stubCapturePane(t, func(task.Session) (string, error) {
		return idleChromeFixture, nil
	})
	ctx := context.Background()
	_, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.SetSessionWorkingIfUnchanged(ctx, sess.ExternalID, sess.StatusUpdatedAt); err != nil {
		t.Fatalf("SetSessionWorkingIfUnchanged: %v", err)
	}

	pollSessions(ctx, s, pollInterval)

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionIdle {
		t.Errorf("Status = %q, want demoted to idle", sessions[0].Status)
	}
}

// The demotion path is guarded by the same CAS as the promotion path: a
// hook landing in the window between SessionsWithTmux's read and the
// poller's demotion write must win, not the poller's idle guess.
func TestPollSessionsDemotionLosesRaceToConcurrentHook(t *testing.T) {
	stubSessionAlive(t, true)
	ctx := context.Background()
	_, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := s.SetSessionWorkingIfUnchanged(ctx, sess.ExternalID, sess.StatusUpdatedAt); err != nil {
		t.Fatalf("SetSessionWorkingIfUnchanged: %v", err)
	}
	stubCapturePane(t, func(sess task.Session) (string, error) {
		// A real race always has at least this much separation — a hook and
		// a poll tick are always distinct OS processes, each with several
		// milliseconds of unavoidable spawn/exec overhead — so this sleep
		// reflects reality rather than manufacturing an unrealistically
		// tight race no real hook/poller pair could ever produce.
		time.Sleep(2 * time.Millisecond)
		if err := s.SetSessionStatus(ctx, sess.ExternalID, task.SessionBlocked); err != nil {
			t.Fatalf("SetSessionStatus: %v", err)
		}
		return idleChromeFixture, nil
	})

	pollSessions(ctx, s, pollInterval)

	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if sessions[0].Status != task.SessionBlocked {
		t.Errorf("Status = %q, want the hook's status left standing", sessions[0].Status)
	}
}
