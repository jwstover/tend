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

// activeProjects is every project that is not archived: the population a
// task can be moved into, and what the column lists by default.
func (a app) activeProjects() []task.Project {
	out := make([]task.Project, 0, len(a.projects))
	for _, p := range a.projects {
		if !p.Archived() {
			out = append(out, p)
		}
	}
	return out
}

// archivedProjects is the projects `A` has hidden. They stay in the
// database and keep their tasks; the column lists them only while
// showArchived is on, so one can be found and restored.
func (a app) archivedProjects() []task.Project {
	out := make([]task.Project, 0, len(a.projects))
	for _, p := range a.projects {
		if p.Archived() {
			out = append(out, p)
		}
	}
	return out
}

// visibleProjects is the projects the column actually lists: the active
// ones, then -- while `C` has them shown -- the archived ones as a block
// underneath, so the everyday list keeps its order and the archive reads
// as a separate shelf rather than being shuffled into it.
func (a app) visibleProjects() []task.Project {
	out := a.activeProjects()
	if a.showArchived {
		out = append(out, a.archivedProjects()...)
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
// follow from it: the scoped list (tasks, or the agents view's sessions)
// reloads for the new selection, and a real project becomes the capture
// target.
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
			return tea.Batch(a.loadScoped(), a.persistActiveProject(p.ID))
		}
	} else {
		a.projectFilter = nil
	}
	return a.loadScoped()
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

	case key.Matches(msg, a.keys.ProjectCwd):
		if p, ok := a.selectedProject(); ok {
			// Seeded with the current default, or tend's own directory when
			// there is none yet, so enter alone accepts the likely answer —
			// the same move openSessionCwdPrompt makes.
			seed := p.Cwd
			if seed == "" {
				seed = a.startCwd
			}
			return *a, a.openPromptWith(promptProjectCwd,
				fmt.Sprintf("default cwd for %s: ", p.Name), seed, p.ID), true
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
			// Restoring is the undo, so it is immediate; archiving asks
			// first, since `A` sits one shift away from `a` and takes the
			// project (and the view of its tasks) off the screen.
			if p.Archived() {
				return *a, a.setProjectArchived(p, false), true
			}
			a.armProjectArchive(p)
		}
		return *a, nil, true

	case key.Matches(msg, a.keys.ToggleArchived):
		return *a, a.toggleArchivedProjects(), true
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

// toggleArchivedProjects shows or hides the archived projects in the
// column. Hiding them while the cursor sits on one falls back to All, the
// way a project archived from under the cursor does, and the scoped list
// reloads only when that fallback actually changed the filter.
func (a *app) toggleArchivedProjects() tea.Cmd {
	if !a.showArchived && len(a.archivedProjects()) == 0 {
		a.status = flash{text: "no archived projects"}
		return nil
	}
	wantID, hadSelection := int64(0), false
	if p, ok := a.selectedProject(); ok {
		wantID, hadSelection = p.ID, true
	}
	before := a.projectFilter

	a.showArchived = !a.showArchived
	a.syncProjectCursor(wantID, hadSelection)

	if a.showArchived {
		n := len(a.archivedProjects())
		noun := "projects"
		if n == 1 {
			noun = "project"
		}
		a.status = flash{text: fmt.Sprintf("%d archived %s shown; A restores one", n, noun)}
	} else {
		a.status = flash{text: "archived projects hidden"}
	}
	if (before == nil) != (a.projectFilter == nil) {
		return a.loadScoped()
	}
	return nil
}

// projectConfirm is a project operation waiting on a `y`: archiving the
// project or deleting it. Both act on a whole project at once -- every
// task in it leaves the column with an archive, or moves to Unsorted with
// a delete -- and both are reached by keys that are easy to hit by
// accident (`A` is shift-`a`, `dd` is muscle memory from the task list),
// so neither runs until a panel has named the project and `y` has agreed.
type projectConfirm struct {
	title string  // panel title, naming the project
	desc  string  // what `y` does, in the panel's key row
	run   tea.Cmd // the mutation `y` fires
}

// armProjectArchive asks before archiving p. `y` in the panel archives;
// anything else leaves the project alone.
func (a *app) armProjectArchive(p task.Project) {
	a.projectConfirm = &projectConfirm{
		title: fmt.Sprintf("archive project %s?", p.Name),
		desc:  "archive; hidden until C shows it, A restores",
		run:   a.setProjectArchived(p, true),
	}
	a.resize()
}

// armProjectDelete asks before deleting the project under the cursor. The
// panel says how much live work moves, so a `dd` that landed on the wrong
// row is caught before the store reassigns anything.
func (a *app) armProjectDelete() {
	p, ok := a.selectedProject()
	if !ok {
		return
	}
	desc := "delete; its tasks move to Unsorted"
	switch p.LiveCount {
	case 0:
	case 1:
		desc = "delete; its 1 live task moves to Unsorted"
	default:
		desc = fmt.Sprintf("delete; its %d live tasks move to Unsorted", p.LiveCount)
	}
	a.projectConfirm = &projectConfirm{
		title: fmt.Sprintf("delete project %s?", p.Name),
		desc:  desc,
		run:   a.deleteProject(p),
	}
	a.resize()
}

// handleProjectConfirmKey consumes the key after a project confirmation
// was armed: `y` runs the archive or delete it names, anything else
// cancels. Reports false when no confirmation is pending.
func (a *app) handleProjectConfirmKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if a.projectConfirm == nil {
		return nil, false
	}
	c := *a.projectConfirm
	a.projectConfirm = nil
	a.resize()
	if msg.String() == "y" {
		return c.run, true
	}
	return nil, true
}

// projectConfirmPanel renders the which-key panel for a pending project
// confirmation: `y` goes ahead, anything else cancels.
func (a app) projectConfirmPanel() string {
	if a.projectConfirm == nil {
		return ""
	}
	entries := []panelEntry{
		{key: "y", desc: a.projectConfirm.desc, keyStyle: a.styles.Error},
		{key: "esc", desc: "cancel", keyStyle: a.styles.Dimmed},
	}
	return renderKeyPanel(a.styles, a.width, a.projectConfirm.title, entries)
}

// deleteProject removes p. Its tasks are reassigned to the default
// project by the store, not deleted -- a project is a grouping, and
// dropping one must never drop work.
func (a app) deleteProject(p task.Project) tea.Cmd {
	return a.mutate(flash{kind: flashDone,
		text: fmt.Sprintf("deleted %s; its tasks moved to Unsorted", p.Name)}, func() error {
		return a.store.DeleteProject(a.ctx, p.ID)
	})
}

// projectsView renders the column: the All row, then every unarchived
// project with its live task count, then -- with `C` -- the archived ones,
// marked and dimmed.
func (a app) projectsView() string {
	w := projectsPaneWidth
	focused := a.focus == paneProjects

	rows := make([]string, 0, a.bodyHeight)
	visible := a.visibleProjects()

	// The All count is the active projects' work whether or not the
	// archive is on show: toggling a view of the column should not make
	// the number beside All jump.
	total := int64(0)
	for _, p := range a.activeProjects() {
		total += p.LiveCount
	}
	rows = append(rows, a.projectRow("All", total, a.projectCursor == allProjectsRow, focused, false, false))
	for i, p := range visible {
		rows = append(rows, a.projectRow(p.Name, p.LiveCount, a.projectCursor == i+1,
			focused, p.ID == a.activeProjectID, p.Archived()))
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
// a bare `tend add` will land in. archived marks a project `A` has hidden:
// the archived glyph takes the marker column and the name is muted, so the
// shelf reads apart from the projects in use even when the cursor is on it.
func (a app) projectRow(name string, count int64, selected, focused, active, archived bool) string {
	s, g := a.styles, a.styles.Glyphs
	w := projectsPaneWidth

	gutter := "  "
	gutterStyle := s.Normal
	if selected {
		gutter = g.SelBar + " "
		gutterStyle = s.SelBar
	}

	// The capture-target marker only earns its column when the selection
	// isn't already sitting on it. An archived project is never the
	// capture target in the column's own terms, so the glyph is free.
	marker, markerStyle := " ", s.Accent
	switch {
	case archived:
		marker, markerStyle = g.Archived, s.Muted
	case active && !selected:
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
	case archived:
		nameStyle = s.Muted
	}

	gap := max(w-runeWidth(gutter)-runeWidth(marker)-runeWidth(label)-runeWidth(countText), 0)
	line := gutterStyle.Render(gutter) + markerStyle.Render(marker) + nameStyle.Render(label) +
		strings.Repeat(" ", gap) + s.CountLabel.Render(countText)
	return line
}
