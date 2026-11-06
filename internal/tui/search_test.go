package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

func TestBodySnippet(t *testing.T) {
	body := "line one\n\nthe quick brown fox jumps over the lazy dog and keeps running far away"
	got := bodySnippet(body, []string{"LAZY"}, 30)
	if !strings.Contains(got, "lazy") || strings.Contains(got, "\n") {
		t.Errorf("snippet %q should be one line around the match", got)
	}
	if got := bodySnippet(body, []string{"absent"}, 30); got != "" {
		t.Errorf("no match should give no snippet, got %q", got)
	}
}

// gf opens the overlay; typing finds a done task in another project by
// its body; enter jumps there like gt.
func TestSearchJumpsToDoneTaskInOtherProject(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)

	home, err := s.CreateProject(ctx, "home")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	target, err := s.AddTaskWithBodyIn(ctx, home.ID, "finished chore", "remember the zebra crossing")
	if err != nil {
		t.Fatalf("AddTaskWithBodyIn: %v", err)
	}
	if err := s.SetState(ctx, target.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('g'))
	m = drive(t, m, keyPress('f'))
	if !m.(app).searchOpen {
		t.Fatal("gf should open the search overlay")
	}
	m = typeText(t, m, "zebra")
	a := m.(app)
	if len(a.searchResults) != 1 || a.searchResults[0].ID != target.ID {
		t.Fatalf("results = %v, want the done task", a.searchResults)
	}
	if !strings.Contains(a.searchView(), "zebra") {
		t.Error("view should show a body snippet")
	}

	m = drive(t, m, enter())
	a = m.(app)
	if a.searchOpen {
		t.Error("enter should close the overlay")
	}
	sel, ok := a.selected()
	if !ok || sel.ID != target.ID {
		t.Fatalf("selected = %v, %v; want task %d", sel, ok, target.ID)
	}
	if !a.showCompleted || !a.showDetail {
		t.Errorf("showCompleted=%v showDetail=%v, want both", a.showCompleted, a.showDetail)
	}
}

func TestSearchEscCloses(t *testing.T) {
	m, _ := newTestApp(t)
	m = drive(t, m, keyPress('g'))
	m = drive(t, m, keyPress('f'))
	m = typeText(t, m, "x")
	m = drive(t, m, esc())
	if m.(app).searchOpen {
		t.Error("esc should close the overlay")
	}
}
