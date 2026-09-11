package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/task"
)

// selectTask lands the cursor on a task by id, so a test's starting row
// doesn't depend on the list's sort order.
func selectTask(t *testing.T, m tea.Model, id int64) tea.Model {
	t.Helper()
	ap := m.(app)
	if !ap.selectByID(id) {
		t.Fatalf("task #%d is not a visible row", id)
	}
	if sel, ok := ap.selected(); !ok || sel.ID != id {
		t.Fatalf("selection = %+v, want #%d", sel, id)
	}
	return ap
}

// parentRowLabels is the picker's visible rows, top to bottom.
func parentRowLabels(m tea.Model) []string {
	rows := m.(app).parentPickerMatches()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.label)
	}
	return out
}

// m on a sub-task offers the top level first; picking it lifts the task
// out of its branch and the cursor follows it there.
func TestParentPickerLiftsChildToTopLevel(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	child, err := s.AddChild(ctx, parent.ID, "child")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if _, err := s.AddTask(ctx, "other"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = selectTask(t, m, parent.ID)
	m = drive(t, m, keyPress('l')) // expand so the child is a row
	m = selectTask(t, m, child.ID)

	m = drive(t, m, keyPress('m'))
	if !m.(app).parentPickerOpen {
		t.Fatal("m should open the parent picker")
	}
	got := parentRowLabels(m)
	want := []string{task.TopLevelLabel, "other"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows = %q, want %q (top level first, current parent left out)", got, want)
	}

	m = drive(t, m, keyPress('1'))
	if m.(app).parentPickerOpen {
		t.Error("picking a row should close the picker")
	}
	waitForTask(t, s, child.ID, "child lifted to top level", func(got task.Task) bool {
		return got.ParentID == nil
	})

	m = drive(t, m, refreshMsg{})
	if sel, ok := m.(app).selected(); !ok || sel.ID != child.ID {
		t.Errorf("selection after the move = %+v, want the moved child #%d", sel, child.ID)
	}
	if strings.Contains(m.(app).status.text, "moved") && !strings.Contains(m.(app).status.text, "top level") {
		t.Errorf("flash = %q, want it to say top level", m.(app).status.text)
	}
}

// m on a top-level task omits the pointless top-level row; picking a
// sibling nests the task under it, opens that branch, and keeps the
// cursor on the task at its new depth.
func TestParentPickerNestsUnderAnotherTask(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	alpha, err := s.AddTask(ctx, "alpha")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	beta, err := s.AddTask(ctx, "beta")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = selectTask(t, m, alpha.ID)

	m = drive(t, m, keyPress('m'))
	got := parentRowLabels(m)
	if len(got) != 1 || got[0] != "beta" {
		t.Fatalf("rows = %q, want just beta: a top-level task gets no top-level row", got)
	}

	m = drive(t, m, keyPress('1'))
	waitForTask(t, s, alpha.ID, "alpha nested under beta", func(got task.Task) bool {
		return got.ParentID != nil && *got.ParentID == beta.ID
	})

	m = drive(t, m, refreshMsg{})
	a := m.(app)
	if !a.expanded[beta.ID] {
		t.Error("the new parent should be expanded so the moved task stays in view")
	}
	if sel, ok := a.selected(); !ok || sel.ID != alpha.ID {
		t.Errorf("selection after the move = %+v, want the moved task #%d", sel, alpha.ID)
	}
	if a.pendingMove != nil || a.pendingSelectID != 0 {
		t.Errorf("pending move state should be cleared once the row is found: %+v / %d", a.pendingMove, a.pendingSelectID)
	}
}

// The candidates leave out what the store would refuse or ignore: the
// task itself, everything beneath it, and the parent it already has. The
// rest read as breadcrumb paths so same-titled tasks can be told apart.
func TestParentPickerCandidatesExcludeSubtreeAndCurrentParent(t *testing.T) {
	ctx := context.Background()
	_, s := newTestApp(t)
	root, err := s.AddTask(ctx, "root")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	mid, err := s.AddChild(ctx, root.ID, "mid")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if _, err := s.AddChild(ctx, mid.ID, "leaf"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if _, err := s.AddChild(ctx, root.ID, "sibling"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	if _, err := s.AddTask(ctx, "other"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	all, err := s.ListProjectTasks(ctx, mid.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectTasks: %v", err)
	}
	midTask, err := s.GetTask(ctx, mid.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	rows := parentCandidates(midTask, all)
	labels := make([]string, 0, len(rows))
	for _, r := range rows {
		labels = append(labels, r.label)
	}
	want := []string{task.TopLevelLabel, "other", "root / sibling"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("candidates = %q, want %q", labels, want)
	}
	if rows[0].id != nil {
		t.Error("the top-level row should carry a nil parent id")
	}
}

// Typing narrows the rows case-insensitively over the breadcrumb, resets
// the cursor, and the digit shortcuts follow the visible order; backspace
// widens again.
func TestParentPickerTypeToFilter(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	mover, err := s.AddTask(ctx, "mover")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	for _, title := range []string{"Apple pie", "banana", "cherry"} {
		if _, err := s.AddTask(ctx, title); err != nil {
			t.Fatalf("AddTask(%q): %v", title, err)
		}
	}
	m = drive(t, m, refreshMsg{})
	m = selectTask(t, m, mover.ID)

	m = drive(t, m, keyPress('m'))
	if got := parentRowLabels(m); len(got) != 3 {
		t.Fatalf("rows = %q, want the three other tasks", got)
	}
	// Move the cursor first so the reset on typing is observable. j is
	// filter text in here, not navigation, so use the arrow.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.(app).parentPickerSel != 1 {
		t.Fatalf("sel after down = %d, want 1", m.(app).parentPickerSel)
	}

	for _, r := range "PIE" {
		m = drive(t, m, keyPress(r))
	}
	a := m.(app)
	if got := parentRowLabels(m); len(got) != 1 || got[0] != "Apple pie" {
		t.Fatalf("rows after typing PIE = %q, want just Apple pie", got)
	}
	if a.parentPickerSel != 0 {
		t.Errorf("sel after typing = %d, want reset to 0", a.parentPickerSel)
	}

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := m.(app).parentPickerQuery; got != "PI" {
		t.Fatalf("query after backspace = %q, want PI", got)
	}
	if got := parentRowLabels(m); len(got) != 1 {
		t.Fatalf("rows after backspace = %q, want Apple pie still", got)
	}

	// The digit picks by visible index, so 1 is the filtered hit.
	m = drive(t, m, keyPress('1'))
	if m.(app).parentPickerOpen {
		t.Fatal("digit should pick and close")
	}
	waitForTask(t, s, mover.ID, "mover nested under Apple pie", func(got task.Task) bool {
		if got.ParentID == nil {
			return false
		}
		p, err := s.GetTask(ctx, *got.ParentID)
		return err == nil && p.Title == "Apple pie"
	})
}
