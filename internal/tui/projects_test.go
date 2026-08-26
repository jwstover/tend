package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
)

// seedProjects creates projects and reloads the app so the column has
// contents. Returns the app positioned exactly as a fresh start would be.
func seedProjects(t *testing.T, m tea.Model, s *store.Store, names ...string) (tea.Model, []task.Project) {
	t.Helper()
	ctx := context.Background()
	made := make([]task.Project, 0, len(names))
	for _, n := range names {
		p, err := s.CreateProject(ctx, n)
		if err != nil {
			t.Fatalf("CreateProject(%q): %v", n, err)
		}
		made = append(made, p)
	}
	return drive(t, m, refreshMsg{}), made
}

// focusProjectRow moves the keyboard to the projects column and lands the
// cursor on a named project. Row order follows ListProjects (the default
// project pinned first, then by name), so tests navigate by name rather
// than by an assumed index.
func focusProjectRow(t *testing.T, m tea.Model, name string) tea.Model {
	t.Helper()
	m = drive(t, m, keyPress('h'))
	if got := m.(app).focus; got != paneProjects {
		t.Fatalf("focus after h = %v, want paneProjects", got)
	}
	want := -1
	for i, p := range m.(app).visibleProjects() {
		if p.Name == name {
			want = i + 1 // the All row sits at 0
			break
		}
	}
	if want < 0 {
		t.Fatalf("project %q is not in the column", name)
	}
	for m.(app).projectCursor < want {
		m = drive(t, m, keyPress('j'))
	}
	for m.(app).projectCursor > want {
		m = drive(t, m, keyPress('k'))
	}
	if p, ok := m.(app).selectedProject(); !ok || p.Name != name {
		t.Fatalf("landed on %q, want %q", p.Name, name)
	}
	return m
}

// projectPickerDigit is the 1-based key that picks a named project in the
// P overlay, which lists projects in the same order as the column.
func projectPickerDigit(t *testing.T, m tea.Model, name string) rune {
	t.Helper()
	for i, p := range m.(app).visibleProjects() {
		if p.Name == name {
			if i >= 9 {
				t.Fatalf("project %q is past the digit shortcuts", name)
			}
			return rune('1' + i)
		}
	}
	t.Fatalf("project %q is not in the picker", name)
	return 0
}

func TestProjectsColumnRendersAllAndProjects(t *testing.T) {
	m, s := newTestApp(t)
	m, _ = seedProjects(t, m, s, "alpha", "beta")

	a := m.(app)
	if !a.projectsVisible() {
		t.Fatal("projects column should be visible at 100 cols")
	}
	view := a.projectsView()
	for _, want := range []string{"All", "Unsorted", "alpha", "beta"} {
		if !strings.Contains(view, want) {
			t.Errorf("projects column missing %q:\n%s", want, view)
		}
	}
	// All plus the seeded default plus the two new ones.
	if got := a.projectRows(); got != 4 {
		t.Errorf("projectRows = %d, want 4", got)
	}
}

// The column is the third pane but the first in order, so h from the task
// list reaches it and l comes back.
func TestHFromTaskListFocusesProjectsAndLReturns(t *testing.T) {
	m, s := newTestApp(t)
	m, _ = seedProjects(t, m, s, "alpha")

	if got := m.(app).focus; got != paneTasks {
		t.Fatalf("focus starts at %v, want paneTasks", got)
	}
	m = drive(t, m, keyPress('h'))
	if got := m.(app).focus; got != paneProjects {
		t.Fatalf("focus after h = %v, want paneProjects", got)
	}
	m = drive(t, m, keyPress('l'))
	if got := m.(app).focus; got != paneTasks {
		t.Errorf("focus after l = %v, want paneTasks", got)
	}
}

// h must still collapse an open branch before it leaves the list -- the
// fallthrough is "nothing left to collapse", not "always move".
func TestHCollapsesBranchBeforeLeavingTheList(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	parent, err := s.AddTask(ctx, "parent")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.AddChild(ctx, parent.ID, "child"); err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('l')) // expand the branch
	if !m.(app).expanded[parent.ID] {
		t.Fatal("l should expand the branch")
	}
	m = drive(t, m, keyPress('h')) // collapses, does not leave
	a := m.(app)
	if a.expanded[parent.ID] {
		t.Error("h should collapse the branch")
	}
	if a.focus != paneTasks {
		t.Errorf("focus = %v, want paneTasks: h collapses before it leaves", a.focus)
	}
	m = drive(t, m, keyPress('h')) // now there is nothing to collapse
	if got := m.(app).focus; got != paneProjects {
		t.Errorf("second h focus = %v, want paneProjects", got)
	}
}

func TestSelectingAProjectScopesTheTaskList(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "alpha")
	alpha := made[0]

	if _, err := s.AddTaskIn(ctx, task.DefaultProjectID, "in unsorted"); err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	if _, err := s.AddTaskIn(ctx, alpha.ID, "in alpha"); err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	// All shows both.
	if got := len(m.(app).tasks); got != 2 {
		t.Fatalf("All shows %d tasks, want 2", got)
	}

	m = focusProjectRow(t, m, "alpha")

	a := m.(app)
	if len(a.tasks) != 1 || a.tasks[0].Title != "in alpha" {
		t.Errorf("scoped list = %+v, want only the alpha task", a.tasks)
	}

	// ...and All brings everything back.
	for m.(app).projectCursor > allProjectsRow {
		m = drive(t, m, keyPress('k'))
	}
	if got := len(m.(app).tasks); got != 2 {
		t.Errorf("back on All shows %d tasks, want 2", got)
	}
}

// Selecting a project makes it the capture target, so `tend add` from a
// bare shell lands where the TUI is pointing (docs/projects-plan.md §3).
func TestSelectingAProjectPersistsTheCaptureTarget(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "alpha")

	focusProjectRow(t, m, "alpha")

	waitFor(t, "active project persisted", func() bool {
		got, err := s.ActiveProjectID(ctx)
		return err == nil && got == made[0].ID
	})

	// A task captured now lands there without being told.
	captured, err := s.AddTask(ctx, "lands in alpha")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if captured.ProjectID != made[0].ID {
		t.Errorf("captured into %d, want alpha %d", captured.ProjectID, made[0].ID)
	}
}

// The All row is a view, not a destination: moving onto it must not
// redirect capture.
func TestAllRowLeavesTheCaptureTargetAlone(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "alpha")

	m = focusProjectRow(t, m, "alpha")
	waitFor(t, "active project persisted", func() bool {
		got, err := s.ActiveProjectID(ctx)
		return err == nil && got == made[0].ID
	})

	// `g` jumps straight to All. Walking up with k would pass through
	// other projects on the way, and landing on each of those is
	// *supposed* to retarget capture -- this test is about All alone.
	m = drive(t, m, keyPress('g'))
	if _, ok := m.(app).selectedProject(); ok {
		t.Fatal("expected to be on the All row")
	}
	if m.(app).projectFilter != nil {
		t.Error("All should clear the project filter")
	}

	got, err := s.ActiveProjectID(ctx)
	if err != nil || got != made[0].ID {
		t.Errorf("active project = %d (err %v), want alpha %d unchanged", got, err, made[0].ID)
	}
}

func TestNewProjectFromTheColumn(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)

	m = drive(t, m, keyPress('h'))
	m = drive(t, m, keyPress('n'))
	if m.(app).promptKind != promptNewProject {
		t.Fatalf("promptKind after n = %v, want promptNewProject", m.(app).promptKind)
	}
	for _, r := range "gardening" {
		m = drive(t, m, keyPress(r))
	}
	_ = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	waitFor(t, "project created", func() bool {
		_, err := s.ProjectByName(ctx, "gardening")
		return err == nil
	})
}

func TestRenameProjectFromTheColumn(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "alpha")

	m = focusProjectRow(t, m, "alpha")
	m = drive(t, m, keyPress('R'))
	if m.(app).promptKind != promptRenameProject {
		t.Fatalf("promptKind after R = %v, want promptRenameProject", m.(app).promptKind)
	}
	// The prompt is seeded with the current name, so clear it first.
	for range len("alpha") {
		m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	for _, r := range "omega" {
		m = drive(t, m, keyPress(r))
	}
	_ = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	waitFor(t, "project renamed", func() bool {
		got, err := s.GetProject(ctx, made[0].ID)
		return err == nil && got.Name == "omega"
	})
}

// Deleting a project must never delete work: its tasks come back to
// Unsorted.
func TestDeleteProjectFromTheColumnKeepsItsTasks(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "doomed")
	stranded, err := s.AddTaskIn(ctx, made[0].ID, "keep me")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = focusProjectRow(t, m, "doomed")
	m = drive(t, m, keyPress('d'))
	if !m.(app).deletePending {
		t.Fatal("first d should arm the delete chord")
	}
	_ = drive(t, m, keyPress('d'))

	waitFor(t, "project deleted and its task reassigned", func() bool {
		if _, err := s.GetProject(ctx, made[0].ID); err == nil {
			return false
		}
		got, err := s.GetTask(ctx, stranded.ID)
		return err == nil && got.ProjectID == task.DefaultProjectID
	})
}

func TestArchivedProjectLeavesTheColumn(t *testing.T) {
	m, s := newTestApp(t)
	m, _ = seedProjects(t, m, s, "seasonal")

	m = focusProjectRow(t, m, "seasonal")
	m = drive(t, m, keyPress('A'))

	waitFor(t, "project archived", func() bool {
		projects, err := s.ListProjects(context.Background())
		if err != nil {
			return false
		}
		for _, p := range projects {
			if p.Name == "seasonal" {
				return p.Archived()
			}
		}
		return false
	})
	m = drive(t, m, refreshMsg{})
	if strings.Contains(m.(app).projectsView(), "seasonal") {
		t.Error("an archived project should leave the column")
	}
}

// P moves a task, and its sub-tree, into another project.
func TestProjectPickerMovesTask(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	m, made := seedProjects(t, m, s, "destination")
	moved, err := s.AddTask(ctx, "move me")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	child, err := s.AddChild(ctx, moved.ID, "child")
	if err != nil {
		t.Fatalf("AddChild: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('P'))
	if !m.(app).projectPickerOpen {
		t.Fatal("P should open the project picker")
	}
	_ = drive(t, m, keyPress(projectPickerDigit(t, m, "destination")))

	waitFor(t, "task and sub-tree moved", func() bool {
		got, err := s.GetTask(ctx, moved.ID)
		if err != nil || got.ProjectID != made[0].ID {
			return false
		}
		kid, err := s.GetTask(ctx, child.ID)
		return err == nil && kid.ProjectID == made[0].ID
	})
}

// The column gives way before the detail split does: the task list is the
// primary surface.
func TestProjectsColumnHidesWhenTooNarrow(t *testing.T) {
	m, s := newTestApp(t)
	m, _ = seedProjects(t, m, s, "alpha")

	m = drive(t, m, tea.WindowSizeMsg{Width: 80, Height: 30})
	if m.(app).projectsVisible() {
		t.Error("projects column should hide below 100 cols")
	}

	// Wide enough on its own, but not once the detail pane opens.
	m = drive(t, m, tea.WindowSizeMsg{Width: 110, Height: 30})
	if !m.(app).projectsVisible() {
		t.Error("projects column should show at 110 cols with no detail pane")
	}
	m = drive(t, m, keyPress(']'))
	if m.(app).projectsVisible() {
		t.Error("projects column should give way to the detail split at 110 cols")
	}

	// With room for all three, both stay.
	m = drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})
	a := m.(app)
	if !a.projectsVisible() || !a.showDetail {
		t.Errorf("at 160 cols both panes should show: projects=%v detail=%v",
			a.projectsVisible(), a.showDetail)
	}
	projW, listW, detailW, full := a.paneWidths()
	if full || projW != projectsPaneWidth || listW <= 0 || detailW <= 0 {
		t.Errorf("paneWidths at 160 = (%d, %d, %d, full=%v)", projW, listW, detailW, full)
	}
}

// Focus must never be stranded on a column that just disappeared.
func TestFocusLeavesProjectsWhenTheColumnHides(t *testing.T) {
	m, s := newTestApp(t)
	m, _ = seedProjects(t, m, s, "alpha")

	m = drive(t, m, keyPress('h'))
	if m.(app).focus != paneProjects {
		t.Fatal("expected focus on the projects column")
	}
	m = drive(t, m, tea.WindowSizeMsg{Width: 70, Height: 30})
	if got := m.(app).focus; got == paneProjects {
		t.Error("focus should leave the projects column when it hides")
	}
}

// `[` toggles the column, and hiding it takes focus with it.
func TestToggleProjectsColumn(t *testing.T) {
	m, s := newTestApp(t)
	m, _ = seedProjects(t, m, s, "alpha")

	m = drive(t, m, keyPress('h'))
	m = drive(t, m, keyPress('['))
	a := m.(app)
	if a.showProjects {
		t.Error("[ should hide the column")
	}
	if a.focus == paneProjects {
		t.Error("hiding the column should move focus off it")
	}
	m = drive(t, m, keyPress('['))
	if !m.(app).showProjects {
		t.Error("[ should show the column again")
	}
}

// Global bindings still work while the projects column has focus -- the
// handler claims only its own keys.
func TestGlobalKeysStillWorkFromTheProjectsColumn(t *testing.T) {
	m, s := newTestApp(t)
	m, _ = seedProjects(t, m, s, "alpha")

	m = drive(t, m, keyPress('h'))
	m = drive(t, m, keyPress('?'))
	if !m.(app).helpOpen {
		t.Error("? should still open help from the projects column")
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	m = drive(t, m, keyPress('S'))
	if got := m.(app).mode; got != modeStandup {
		t.Errorf("S from the projects column gave mode %v, want standup", got)
	}
}
