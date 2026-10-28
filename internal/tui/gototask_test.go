package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/task"
)

// gotoTask drives the whole chord: `g`, `t`, the id, enter.
func gotoTask(t *testing.T, m tea.Model, id string) tea.Model {
	t.Helper()
	m = drive(t, m, keyPress('g'))
	m = drive(t, m, keyPress('t'))
	if m.(app).modal.kind != modalGotoTask {
		t.Fatal("gt should open the go-to-task modal")
	}
	m = typeText(t, m, id)
	return drive(t, m, enter())
}

// A done grandchild in another project than the one selected: the column
// moves to its project, completed tasks come into view because its
// top-level ancestor is done, both ancestors open, and the cursor and the
// detail pane land on it.
func TestGotoTaskRevealsHiddenTask(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)

	work, err := s.CreateProject(ctx, "work")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	home, err := s.CreateProject(ctx, "home")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := s.AddTaskIn(ctx, work.ID, "decoy"); err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	root, err := s.AddTaskIn(ctx, home.ID, "root")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	mid, err := s.AddChild(ctx, root.ID, "mid")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	leaf, err := s.AddChild(ctx, mid.ID, "leaf")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	for _, id := range []int64{leaf.ID, mid.ID, root.ID} {
		if err := s.SetState(ctx, id, task.StateDone); err != nil {
			t.Fatalf("SetState: %v", err)
		}
	}

	m = drive(t, m, refreshMsg{})
	a := m.(app)
	row := -1
	for i, p := range a.visibleProjects() {
		if p.ID == work.ID {
			row = i + 1
		}
	}
	m = drive(t, a, a.setProjectCursor(row)())

	m = gotoTask(t, m, "#"+itoa(leaf.ID))
	a = m.(app)
	if a.projectFilter == nil || *a.projectFilter != home.ID {
		t.Errorf("project filter = %v, want the leaf's project #%d", a.projectFilter, home.ID)
	}
	if !a.showCompleted {
		t.Error("completed tasks should be shown: the leaf's top-level ancestor is done")
	}
	if !a.expanded[root.ID] || !a.expanded[mid.ID] {
		t.Errorf("ancestors should be expanded: %v", a.expanded)
	}
	if sel, ok := a.selected(); !ok || sel.ID != leaf.ID {
		t.Fatalf("selection = %+v, want #%d", sel, leaf.ID)
	}
	if !a.showDetail || a.detailID != leaf.ID {
		t.Errorf("detail pane open=%v on #%d, want it open on #%d", a.showDetail, a.detailID, leaf.ID)
	}
	if a.pendingSelectID != 0 {
		t.Errorf("pendingSelectID = %d, want it cleared once the row was found", a.pendingSelectID)
	}
}

// A visible top-level task needs nothing changed: no project move, no
// completed toggle, just the cursor and the pane.
func TestGotoTaskVisibleTask(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	first, err := s.AddTask(ctx, "first")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddTask(ctx, "second"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = gotoTask(t, m, itoa(first.ID))
	a := m.(app)
	if sel, ok := a.selected(); !ok || sel.ID != first.ID {
		t.Fatalf("selection = %+v, want #%d", sel, first.ID)
	}
	if a.showCompleted || a.projectFilter != nil {
		t.Errorf("nothing should have changed: showCompleted=%v filter=%v", a.showCompleted, a.projectFilter)
	}
	if !a.showDetail || a.detailID != first.ID {
		t.Errorf("detail pane open=%v on #%d, want it open on #%d", a.showDetail, a.detailID, first.ID)
	}
}

func TestGotoTaskRejectsBadInput(t *testing.T) {
	m, _ := newTestApp(t)
	for _, tc := range []struct{ in, want string }{
		{"abc", "not a task id"},
		{"999", "no task #999"},
	} {
		got := gotoTask(t, m, tc.in).(app)
		if !strings.Contains(got.status.text, tc.want) || !got.status.isErr {
			t.Errorf("%q: flash = %+v, want an error containing %q", tc.in, got.status, tc.want)
		}
		if got.pendingSelectID != 0 || got.showDetail {
			t.Errorf("%q: a failed go-to should leave the view alone", tc.in)
		}
	}
}
