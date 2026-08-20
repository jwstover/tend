package tui

import (
	"context"
	"errors"
	"os"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestMain disables runRecap by default so no test accidentally shells
// out to a real `claude` binary — sessionFinishedMsg/sessionResumedMsg
// are already driven through drive() (see sessions_test.go), which
// executes every tea.Cmd a handler returns, including the batched-in
// recap command.
func TestMain(m *testing.M) {
	runRecap = func(ctx context.Context, cwd, externalID, excerpt string) (string, error) {
		return "", errors.New("recap disabled in tests")
	}
	os.Exit(m.Run())
}

func stubRecap(t *testing.T, body string, err error) {
	t.Helper()
	prev := runRecap
	runRecap = func(ctx context.Context, cwd, externalID, excerpt string) (string, error) {
		return body, err
	}
	t.Cleanup(func() { runRecap = prev })
}

func TestRecapLogsEntryOnSessionFinished(t *testing.T) {
	stubRecap(t, "did the thing", nil)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, sessionFinishedMsg{taskID: parent.ID, externalID: "ext-1", cwd: "/tmp/work", label: parent.Title})
	_ = m

	waitFor(t, "recap logged", func() bool {
		entries, err := s.ListTaskLog(ctx, parent.ID)
		return err == nil && len(entries) == 1 && entries[0].Body == recapNotePrefix+"did the thing"
	})
}

func TestRecapSwallowsFailureQuietly(t *testing.T) {
	stubRecap(t, "", errors.New("boom"))
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, sessionFinishedMsg{taskID: parent.ID, externalID: "ext-1", cwd: "/tmp/work", label: parent.Title})

	waitFor(t, "session recorded", func() bool {
		sessions, err := s.ListSessionsForTask(ctx, parent.ID)
		return err == nil && len(sessions) == 1
	})
	if got := m.(app).status.text; got != "session recorded" {
		t.Errorf("status = %q, want unchanged %q", got, "session recorded")
	}
	entries, err := s.ListTaskLog(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListTaskLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no log entry after a failed recap, got %+v", entries)
	}
}

func TestRecapEmptyOutputSkipsLogEntry(t *testing.T) {
	stubRecap(t, "", nil)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	_ = drive(t, m, sessionFinishedMsg{taskID: parent.ID, externalID: "ext-1", cwd: "/tmp/work", label: parent.Title})

	waitFor(t, "session recorded", func() bool {
		sessions, err := s.ListSessionsForTask(ctx, parent.ID)
		return err == nil && len(sessions) == 1
	})
	entries, err := s.ListTaskLog(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListTaskLog: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no log entry for empty recap output, got %+v", entries)
	}
}

func TestRecapAutoNamesSessionOnSessionFinished(t *testing.T) {
	stubRecap(t, "LABEL: fixed the flaky test\nRECAP: did the thing", nil)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	_ = drive(t, m, sessionFinishedMsg{taskID: parent.ID, externalID: "ext-1", cwd: "/tmp/work", label: parent.Title})

	waitFor(t, "recap logged", func() bool {
		entries, err := s.ListTaskLog(ctx, parent.ID)
		return err == nil && len(entries) == 1 && entries[0].Body == recapNotePrefix+"did the thing"
	})
	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Label != "fixed the flaky test" {
		t.Errorf("sessions = %+v, want a single session auto-named %q", sessions, "fixed the flaky test")
	}
}

func TestRecapWithoutLabelMarkerLeavesSessionLabelUnchanged(t *testing.T) {
	stubRecap(t, "did the thing", nil)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	_ = drive(t, m, sessionFinishedMsg{taskID: parent.ID, externalID: "ext-1", cwd: "/tmp/work", label: parent.Title})

	waitFor(t, "recap logged", func() bool {
		entries, err := s.ListTaskLog(ctx, parent.ID)
		return err == nil && len(entries) == 1
	})
	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Label != parent.Title {
		t.Errorf("sessions = %+v, want label unchanged at %q", sessions, parent.Title)
	}
}

func TestQuitAsksConfirmationWhileRecapPending(t *testing.T) {
	stubRecap(t, "did the thing", nil)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// Deliberately don't drain the returned Cmd — pendingRecaps is
	// incremented synchronously in the Update case, before the recap
	// itself ever runs, so this leaves it "in flight" from the test's
	// point of view without needing a goroutine/channel dance.
	m, _ = m.Update(sessionFinishedMsg{taskID: parent.ID, externalID: "ext-1", cwd: "/tmp/work", label: parent.Title})
	if got := m.(app).pendingRecaps; got != 1 {
		t.Fatalf("pendingRecaps = %d, want 1", got)
	}

	quitMsg := func(cmd tea.Cmd) bool {
		for _, msg := range collect(cmd) {
			if _, ok := msg.(tea.QuitMsg); ok {
				return true
			}
		}
		return false
	}

	// q on the bare list warns instead of quitting outright.
	var cmd tea.Cmd
	m, cmd = m.Update(keyPress('q'))
	if quitMsg(cmd) {
		t.Errorf("q quit immediately with a recap pending")
	}
	if !m.(app).quitPending {
		t.Errorf("quitPending = false, want true")
	}

	// Any other key cancels the confirmation without quitting.
	cancelled, cmd := m.Update(keyPress('x'))
	if quitMsg(cmd) {
		t.Errorf("cancelling key quit")
	}
	if cancelled.(app).quitPending {
		t.Errorf("quitPending still true after a cancelling key")
	}

	// A second q confirms and quits.
	_, cmd = m.Update(keyPress('q'))
	if !quitMsg(cmd) {
		t.Errorf("second q did not quit")
	}
}

func TestRecapDoneDecrementsPendingRecaps(t *testing.T) {
	stubRecap(t, "did the thing", nil)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m, _ = m.Update(sessionFinishedMsg{taskID: parent.ID, externalID: "ext-1", cwd: "/tmp/work", label: parent.Title})
	if got := m.(app).pendingRecaps; got != 1 {
		t.Fatalf("pendingRecaps = %d, want 1", got)
	}

	m = drive(t, m, recapDoneMsg{})
	if got := m.(app).pendingRecaps; got != 0 {
		t.Errorf("pendingRecaps = %d, want 0 after recapDoneMsg", got)
	}

	// With nothing pending, q quits straight away again.
	_, cmd := m.Update(keyPress('q'))
	quit := false
	for _, msg := range collect(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
		}
	}
	if !quit {
		t.Errorf("q did not quit once pendingRecaps returned to 0")
	}
}

func TestRecapFiresOnSessionResumed(t *testing.T) {
	stubRecap(t, "resumed and finished it", nil)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	_ = drive(t, m, sessionResumedMsg{
		sessionRowID: sess.ID,
		taskID:       parent.ID,
		cwd:          sess.Cwd,
		externalID:   sess.ExternalID,
	})

	waitFor(t, "recap logged", func() bool {
		entries, err := s.ListTaskLog(ctx, parent.ID)
		return err == nil && len(entries) == 1 && entries[0].Body == recapNotePrefix+"resumed and finished it"
	})
}
