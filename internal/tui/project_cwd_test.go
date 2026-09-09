package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A task with no sessions yet inherits its project's default cwd in the
// launch prompt — the whole point of the setting (tend task #190).
func TestNewSessionPromptDefaultsToTheProjectCwd(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "app")
	if err := s.SetProjectCwd(ctx, made[0].ID, "/tmp/app"); err != nil {
		t.Fatalf("SetProjectCwd: %v", err)
	}
	if _, err := s.AddTaskIn(ctx, made[0].ID, "first task in app"); err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = stepR(t, m)
	a := m.(app)
	if a.promptKind != promptSessionCwd {
		t.Fatalf("promptKind = %v, want promptSessionCwd", a.promptKind)
	}
	if a.prompt.Value() != "/tmp/app" {
		t.Errorf("prompt prefilled with %q, want the project's default /tmp/app", a.prompt.Value())
	}
}

// A task that has already been worked on somewhere keeps offering that
// directory: the task-level evidence is more specific than the project
// default, and a deliberate one-off choice shouldn't be silently undone.
func TestLastSessionCwdBeatsTheProjectDefault(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "app")
	if err := s.SetProjectCwd(ctx, made[0].ID, "/tmp/app"); err != nil {
		t.Fatalf("SetProjectCwd: %v", err)
	}
	parent, err := s.AddTaskIn(ctx, made[0].ID, "worked on elsewhere")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/elsewhere", parent.Title, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = stepR(t, m) // picker open, sel = 0 (+ new session)
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	a := m.(app)
	if a.promptKind != promptSessionCwd {
		t.Fatalf("promptKind = %v, want promptSessionCwd", a.promptKind)
	}
	if a.prompt.Value() != "/tmp/elsewhere" {
		t.Errorf("prompt prefilled with %q, want the last session's /tmp/elsewhere", a.prompt.Value())
	}
}

// Without a project default the prompt still falls back to where tend
// was started, exactly as before the setting existed.
func TestNewSessionPromptFallsBackToStartCwd(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "app")
	if _, err := s.AddTaskIn(ctx, made[0].ID, "task"); err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	a := stepR(t, m).(app)
	if a.prompt.Value() != a.startCwd {
		t.Errorf("prompt prefilled with %q, want startCwd %q", a.prompt.Value(), a.startCwd)
	}
}

// `w` on a project in the column opens the default-cwd prompt, seeded
// with tend's own directory when nothing is set yet; what's typed is saved
// and offered back on the next `w`.
func TestSetProjectCwdFromTheColumn(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "app")

	m = focusProjectRow(t, m, "app")
	m = drive(t, m, keyPress('w'))
	a := m.(app)
	if a.promptKind != promptProjectCwd {
		t.Fatalf("promptKind after w = %v, want promptProjectCwd", a.promptKind)
	}
	if a.prompt.Value() != a.startCwd {
		t.Errorf("unset default seeded with %q, want startCwd %q", a.prompt.Value(), a.startCwd)
	}
	for range len(a.startCwd) {
		m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "/tmp/app/" {
		m = drive(t, m, keyPress(r))
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	waitFor(t, "project cwd saved", func() bool {
		got, err := s.GetProject(ctx, made[0].ID)
		return err == nil && got.Cwd == "/tmp/app"
	})

	// The column reloads after the mutation; the next prompt starts on
	// the stored value rather than tend's directory.
	m = drive(t, m, refreshMsg{})
	m = focusProjectRow(t, m, "app")
	m = drive(t, m, keyPress('w'))
	if got := m.(app).prompt.Value(); got != "/tmp/app" {
		t.Errorf("prompt seeded with %q, want the stored /tmp/app", got)
	}
}

// Submitting an empty prompt clears the default, the way an empty tags or
// due prompt does.
func TestEmptyProjectCwdPromptClearsTheDefault(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "app")
	if err := s.SetProjectCwd(ctx, made[0].ID, "/tmp/app"); err != nil {
		t.Fatalf("SetProjectCwd: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = focusProjectRow(t, m, "app")
	m = drive(t, m, keyPress('w'))
	for range len("/tmp/app") {
		m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	_ = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	waitFor(t, "project cwd cleared", func() bool {
		got, err := s.GetProject(ctx, made[0].ID)
		return err == nil && got.Cwd == ""
	})
}

// The All row is a view, not a project, so there is nothing to set a
// default on.
func TestProjectCwdKeyOnAllRowIsANoop(t *testing.T) {
	m, s := newTestApp(t)
	m, _ = seedProjects(t, m, s, "app")

	m = drive(t, m, keyPress('h'))
	m = drive(t, m, keyPress('g')) // All
	m = drive(t, m, keyPress('w'))
	if got := m.(app).promptKind; got != promptNone {
		t.Errorf("promptKind after w on All = %v, want no prompt", got)
	}
}
