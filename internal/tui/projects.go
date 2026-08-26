package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/task"
)

// allProjectsRow is the cursor position of the synthetic "All" row that
// heads the projects column. It is a view, not a place to put things: the
// task list shows every project, and the capture target is left alone (see
// setProjectCursor).
const allProjectsRow = 0

// visibleProjects is the projects the column actually lists: everything
// but the archived ones, which stay in the database and keep their tasks.
func (a app) visibleProjects() []task.Project {
	out := make([]task.Project, 0, len(a.projects))
	for _, p := range a.projects {
		if !p.Archived() {
			out = append(out, p)
		}
	}
	return out
}

// projectRows is the number of selectable rows: All plus every visible
// project.
func (a app) projectRows() int { return len(a.visibleProjects()) + 1 }

// selectedProject returns the project under the projects cursor. ok is
// false on the All row, which is every caller's "no specific project".
func (a app) selectedProject() (task.Project, bool) {
	visible := a.visibleProjects()
	idx := a.projectCursor - 1
	if idx < 0 || idx >= len(visible) {
		return task.Project{}, false
	}
	return visible[idx], true
}

// setProjectCursor moves the projects cursor and returns the commands that
// follow from it: the task list reloads scoped to the new selection, and a
// real project becomes the capture target.
//
// The All row deliberately does not touch the capture target. It is a way
// of looking at everything, not a place to put a new task, so capture
// keeps aiming at whichever real project was last selected.
func (a *app) setProjectCursor(row int) tea.Cmd {
	if row < 0 || row >= a.projectRows() {
		return nil
	}
	a.projectCursor = row

	if p, ok := a.selectedProject(); ok {
		a.projectFilter = &p.ID
		if p.ID != a.activeProjectID {
			a.activeProjectID = p.ID
			return tea.Batch(a.loadTasks(a.mode), a.persistActiveProject(p.ID))
		}
	} else {
		a.projectFilter = nil
	}
	return a.loadTasks(a.mode)
}

// persistActiveProject records the capture target so `tend add` in a bare
// shell aims at the same project the TUI is pointing at
// (docs/projects-plan.md §3). A failure here is not worth interrupting
// navigation over: it degrades to capture landing in the previous target.
func (a app) persistActiveProject(id int64) tea.Cmd {
	return func() tea.Msg {
		_ = a.store.SetActiveProject(a.ctx, id)
		return nil
	}
}

// loadProjects fetches the column's contents plus the stored capture
// target.
func (a app) loadProjects() tea.Cmd {
	return func() tea.Msg {
		projects, err := a.store.ListProjects(a.ctx)
		if err != nil {
			return errMsg{err}
		}
		active, err := a.store.ActiveProjectID(a.ctx)
		if err != nil {
			return errMsg{err}
		}
		return projectsLoadedMsg{projects: projects, active: active}
	}
}

// syncProjectCursor re-points the cursor after a projects reload. It
// follows the selected project by id rather than by row, so a project
// appearing, being renamed or being deleted doesn't silently move the
// selection onto a different one.
func (a *app) syncProjectCursor(wantID int64, hadSelection bool) {
	if !hadSelection {
		a.projectCursor = allProjectsRow
		a.projectFilter = nil
		return
	}
	for i, p := range a.visibleProjects() {
		if p.ID == wantID {
			a.projectCursor = i + 1
			id := p.ID
			a.projectFilter = &id
			return
		}
	}
	// The selected project is gone (deleted or archived): fall back to
	// All rather than to whatever row happens to sit at that index.
	a.projectCursor = allProjectsRow
	a.projectFilter = nil
}

// toggleProjects shows or hides the projects column.
func (a app) toggleProjects() (tea.Model, tea.Cmd) {
	a.showProjects = !a.showProjects
	if !a.showProjects && a.focus == paneProjects {
		a.focus = paneTasks
	}
	a.resize()
	if a.showProjects && !a.projectsVisible() {
		a.status = flash{text: "terminal too narrow for the projects column"}
	}
	return a, nil
}

// focusProjects moves the keyboard to the projects column, opening it
// first if it was hidden. Reports false when the terminal is too narrow
// for the column to exist at all, so callers can leave focus alone.
func (a *app) focusProjects() bool {
	probe := *a
	probe.showProjects = true
	if !probe.projectsVisible() {
		return false
	}
	a.showProjects = true
	a.focus = paneProjects
	a.resize()
	return true
}

// handleProjectsKey owns the keyboard while the projects column is
// focused. It claims only its own keys and reports false for everything
// else, so the global bindings (`q`, `:`, `?`, `S`, `i`) keep working from
// here.
func (a *app) handleProjectsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, a.keys.ExpandOpen), key.Matches(msg, a.keys.ExpandToggle):
		a.focus = paneTasks
		return *a, nil, true

	case key.Matches(msg, a.keys.ScrollDown):
		return *a, a.setProjectCursor(a.projectCursor + 1), true

	case key.Matches(msg, a.keys.ScrollUp):
		return *a, a.setProjectCursor(a.projectCursor - 1), true

	case key.Matches(msg, a.keys.QuickAdd):
		return *a, a.openPrompt(promptNewProject, "new project: ", 0), true

	case key.Matches(msg, a.keys.Rename):
		if p, ok := a.selectedProject(); ok {
			return *a, a.openPromptWith(promptRenameProject,
				fmt.Sprintf("rename %s: ", p.Name), p.Name, p.ID), true
		}
		a.status = flash{text: "All is not a project"}
		return *a, nil, true

	case key.Matches(msg, a.keys.Delete):
		if _, ok := a.selectedProject(); ok {
			a.deletePending = true
			a.resize()
		}
		return *a, nil, true

	case key.Matches(msg, a.keys.Archive):
		if p, ok := a.selectedProject(); ok {
			return *a, a.setProjectArchived(p, !p.Archived()), true
		}
		return *a, nil, true
	}

	// `G` / `gg` are the list component's own bindings elsewhere; here the
	// column is small enough that only the two extremes are worth having.
	switch msg.String() {
	case "G":
		return *a, a.setProjectCursor(a.projectRows() - 1), true
	case "g":
		return *a, a.setProjectCursor(allProjectsRow), true
	}
	return *a, nil, false
}

func (a app) setProjectArchived(p task.Project, archived bool) tea.Cmd {
	verb := "archived"
	if !archived {
		verb = "restored"
	}
	return a.mutate(flash{kind: flashEdit, text: p.Name + " " + verb}, func() error {
		return a.store.SetProjectArchived(a.ctx, p.ID, archived)
	})
}

// deleteSelectedProject removes the project under the cursor. Its tasks
// are reassigned to the default project by the store, not deleted -- a
// project is a grouping, and dropping one must never drop work.
func (a app) deleteSelectedProject() tea.Cmd {
	p, ok := a.selectedProject()
	if !ok {
		return nil
	}
	return a.mutate(flash{kind: flashDone,
		text: fmt.Sprintf("deleted %s; its tasks moved to Unsorted", p.Name)}, func() error {
		return a.store.DeleteProject(a.ctx, p.ID)
	})
}

// projectsView renders the column: the All row, then every unarchived
// project with its live task count.
func (a app) projectsView() string {
	w := projectsPaneWidth
	focused := a.focus == paneProjects

	rows := make([]string, 0, a.bodyHeight)
	visible := a.visibleProjects()

	total := int64(0)
	for _, p := range visible {
		total += p.LiveCount
	}
	rows = append(rows, a.projectRow("All", total, a.projectCursor == allProjectsRow, focused, false))
	for i, p := range visible {
		rows = append(rows, a.projectRow(p.Name, p.LiveCount, a.projectCursor == i+1,
			focused, p.ID == a.activeProjectID))
	}

	// Pad to the body height so the divider beside it runs full length.
	for len(rows) < a.bodyHeight {
		rows = append(rows, strings.Repeat(" ", w))
	}
	if len(rows) > a.bodyHeight {
		rows = rows[:max(a.bodyHeight, 0)]
	}
	return strings.Join(rows, "\n")
}

// projectRow renders one line of the column: selection bar, name, and a
// right-aligned live count. active marks the capture target -- the project
// a bare `tend add` will land in.
func (a app) projectRow(name string, count int64, selected, focused, active bool) string {
	s, g := a.styles, a.styles.Glyphs
	w := projectsPaneWidth

	gutter := "  "
	gutterStyle := s.Normal
	if selected {
		gutter = g.SelBar + " "
		gutterStyle = s.SelBar
	}

	// The capture-target marker only earns its column when the selection
	// isn't already sitting on it.
	marker := " "
	if active && !selected {
		marker = g.CaretClosed
	}

	countText := ""
	if count > 0 {
		countText = fmt.Sprintf("%d", count)
	}

	nameW := max(w-runeWidth(gutter)-runeWidth(marker)-runeWidth(countText)-1, 1)
	label := truncTail(name, nameW, g.Ellipsis)

	nameStyle := s.Dimmed
	switch {
	case selected && focused:
		nameStyle = s.Title.Bold(true)
	case selected:
		nameStyle = s.Title
	}

	gap := max(w-runeWidth(gutter)-runeWidth(marker)-runeWidth(label)-runeWidth(countText), 0)
	line := gutterStyle.Render(gutter) + s.Accent.Render(marker) + nameStyle.Render(label) +
		strings.Repeat(" ", gap) + s.CountLabel.Render(countText)
	return line
}
