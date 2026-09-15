package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
)

// seedSelectedTask adds a todo task and parks the list cursor on it.
func seedSelectedTask(t *testing.T, m tea.Model, s *store.Store, title string) (tea.Model, task.Task) {
	t.Helper()
	ctx := context.Background()
	tk, err := s.AddTask(ctx, title)
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetState(ctx, tk.ID, task.StateTodo); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	for i := 0; i < 10; i++ {
		if sel, ok := m.(app).selected(); ok && sel.ID == tk.ID {
			return m, tk
		}
		m = drive(t, m, keyPress('j'))
	}
	t.Fatalf("could not park the cursor on %q", title)
	return m, tk
}

// dd on a task no longer deletes outright: a panel names the task and the
// sub-tasks that would cascade with it, and only y deletes.
func TestDeleteTaskAsksFirst(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, tk := seedSelectedTask(t, m, s, "doomed")
	if _, err := s.AddChild(ctx, tk.ID, "along for the ride"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('d'))
	m = drive(t, m, keyPress('d'))
	if m.(app).confirm == nil {
		t.Fatal("dd on a task should ask before deleting")
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "doomed?") {
		t.Errorf("the confirmation should name the task:\n%s", view)
	}
	if !strings.Contains(view, "with its 1 sub-task") {
		t.Errorf("the confirmation should say sub-tasks go too:\n%s", view)
	}
	if _, err := s.GetTask(ctx, tk.ID); err != nil {
		t.Fatalf("the task should still exist until y: %v", err)
	}

	_ = drive(t, m, keyPress('y'))
	waitFor(t, "task deleted", func() bool {
		_, err := s.GetTask(ctx, tk.ID)
		return err != nil
	})
}

// Anything but y cancels a pending task delete and leaves the task alone.
func TestDeleteTaskConfirmationCancels(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, tk := seedSelectedTask(t, m, s, "keeper")

	m = drive(t, m, keyPress('d'))
	m = drive(t, m, keyPress('d'))
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	a := m.(app)
	if a.confirm != nil {
		t.Fatal("esc should clear the confirmation")
	}
	if strings.Contains(ansi.Strip(m.View().Content), "keeper?") {
		t.Error("the confirmation panel should be gone after esc")
	}
	if _, err := s.GetTask(ctx, tk.ID); err != nil {
		t.Fatalf("a cancelled delete should leave the task: %v", err)
	}
	if sel, ok := a.selected(); !ok || sel.ID != tk.ID {
		t.Errorf("cursor after cancel = %+v, want to stay on keeper", sel)
	}
}

// The palette's delete goes through the same confirmation as dd.
func TestPaletteDeleteTaskAsksFirst(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, tk := seedSelectedTask(t, m, s, "doomed")

	for _, c := range m.(app).paletteCommands() {
		if c.label != "Delete selected task" {
			continue
		}
		m, _ = c.act(m.(app))
	}
	if m.(app).confirm == nil {
		t.Fatal("the palette delete should ask before deleting")
	}
	if _, err := s.GetTask(ctx, tk.ID); err != nil {
		t.Fatalf("the task should still exist until y: %v", err)
	}
}
