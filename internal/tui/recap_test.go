package tui

import (
	"context"
	"errors"
	"os"
	"testing"
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

	m = drive(t, m, sessionFinishedMsg{taskID: parent.ID, externalID: "ext-1", cwd: "/tmp/work", label: parent.Title})

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

func TestRecapFiresOnSessionResumed(t *testing.T) {
	stubRecap(t, "resumed and finished it", nil)
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sess, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, sessionResumedMsg{
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
