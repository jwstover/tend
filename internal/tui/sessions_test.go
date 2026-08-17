package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// stepR presses `r` on the selected task and runs the resulting load
// command by hand, stopping short of anything that would shell out to
// claude — mirrors the o/url-picker tests' "step once" idiom.
func stepR(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	m2, cmd := m.Update(keyPress('r'))
	if cmd == nil {
		t.Fatal("r did not produce a load command")
	}
	m3, _ := m2.Update(cmd())
	return m3
}

func TestSessionsKeyNoSessionsOpensCwdPrompt(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "do the thing"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = stepR(t, m)
	a := m.(app)
	if a.promptKind != promptSessionCwd {
		t.Fatalf("promptKind = %v, want promptSessionCwd", a.promptKind)
	}
	if a.sessionPickerOpen {
		t.Error("picker should be skipped with no existing sessions")
	}
	if a.prompt.Value() != a.startCwd {
		t.Errorf("prompt prefilled with %q, want startCwd %q", a.prompt.Value(), a.startCwd)
	}
}

func TestSessionsKeyWithSessionsOpensPicker(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = stepR(t, m)
	a := m.(app)
	if !a.sessionPickerOpen {
		t.Fatal("picker not open with an existing session")
	}
	if len(a.sessionPickerSessions) != 1 || a.sessionPickerSessions[0].ExternalID != "ext-1" {
		t.Errorf("sessionPickerSessions = %+v, want one session ext-1", a.sessionPickerSessions)
	}
	if a.sessionPickerSel != 0 {
		t.Errorf("sessionPickerSel = %d, want 0 (+ new session)", a.sessionPickerSel)
	}

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"claude sessions", "+ new session", "work"} {
		if !strings.Contains(content, want) {
			t.Errorf("picker missing %q:\n%s", want, content)
		}
	}
}

func TestSessionPickerEnterOnNewRowOpensPromptWithLastCwd(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = stepR(t, m) // picker open, sel = 0 (+ new session)

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	a := m.(app)
	if a.sessionPickerOpen {
		t.Error("picker still open after choosing + new session")
	}
	if a.promptKind != promptSessionCwd {
		t.Fatalf("promptKind = %v, want promptSessionCwd", a.promptKind)
	}
	if a.prompt.Value() != "/tmp/work" {
		t.Errorf("prompt prefilled with %q, want the last session's cwd /tmp/work", a.prompt.Value())
	}
}

func TestSessionPickerDigitResumesAndClosesPicker(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = stepR(t, m)

	// Step once so the resulting resume command (which shells out) isn't run.
	m2, cmd := m.Update(keyPress('1'))
	if m2.(app).sessionPickerOpen {
		t.Error("picker still open after choosing a session by digit")
	}
	if cmd == nil {
		t.Error("choosing a session did not produce a resume command")
	}
}

func TestSessionPickerEscDismisses(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = stepR(t, m)

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.(app).sessionPickerOpen {
		t.Error("picker still open after esc")
	}
}

func TestSessionFinishedMsgRecordsSession(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, sessionFinishedMsg{
		taskID: parent.ID, externalID: "ext-new", cwd: "/tmp/new-work", label: parent.Title,
	})

	waitFor(t, "session recorded", func() bool {
		sessions, err := s.ListSessionsForTask(ctx, parent.ID)
		return err == nil && len(sessions) == 1 && sessions[0].ExternalID == "ext-new"
	})
	_ = m
}

func TestSessionFinishedMsgErrDoesNotRecord(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, sessionFinishedMsg{
		taskID: parent.ID, externalID: "ext-fail", cwd: "/tmp/x", label: parent.Title,
		err: context.DeadlineExceeded,
	})
	if !m.(app).status.isErr {
		t.Error("expected an error flash after a failed session")
	}
	sessions, err := s.ListSessionsForTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("session recorded despite a non-nil exec error: %+v", sessions)
	}
}

func TestSessionResumedMsgTouchesSession(t *testing.T) {
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

	waitFor(t, "session touched", func() bool {
		sessions, err := s.ListSessionsForTask(ctx, parent.ID)
		return err == nil && len(sessions) == 1
	})
	if !strings.Contains(m.(app).status.text, "resumed") {
		t.Errorf("status = %q, want it to mention resume", m.(app).status.text)
	}
}

func TestDetailPaneShowsSessions(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "ongoing work")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/home/me/code/my-project", "fixed the flaky test"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress(']')) // open detail

	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"SESSIONS  1", "fixed the flaky test"} {
		if !strings.Contains(content, want) {
			t.Errorf("detail pane missing %q:\n%s", want, content)
		}
	}
}
