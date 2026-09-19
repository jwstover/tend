package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/task"
)

// The project picker is the `P` key: choose which project a task belongs
// to. It clones the palette/url-picker overlay idiom rather than inventing
// a new one -- same bottom-anchored bordered box, same arrow/ctrl-n/ctrl-p
// navigation, same Enter-to-act and Esc-to-dismiss.

// openProjectPicker arms the picker for a task, starting on the project
// the task is already in so Enter is a no-op rather than a surprise.
func (a *app) openProjectPicker(t task.Task) {
	a.projectPickerTaskID = t.ID
	a.projectPickerLabel = t.Title
	a.projectPicker = picker[task.Project]{
		open: true, items: a.activeProjects(), numbered: true,
		label: func(p task.Project) string { return p.Name },
	}
	for i, p := range a.activeProjects() {
		if p.ID == t.ProjectID {
			a.projectPicker.sel = i
			break
		}
	}
	a.projectPicker.scroll(a.height)
}

func (a *app) closeProjectPicker() {
	a.projectPickerTaskID = 0
	a.projectPickerLabel = ""
	a.projectPicker = picker[task.Project]{}
}

// handleProjectPickerKey owns the keyboard while the picker is open.
func (a app) handleProjectPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	id := a.projectPickerTaskID
	action, idx := a.projectPicker.key(msg, a.height)
	switch action {
	case pickerCancel:
		a.closeProjectPicker()
		return a, nil
	case pickerPick:
		p, ok := a.projectPicker.at(idx)
		a.closeProjectPicker()
		if !ok {
			return a, nil
		}
		return a, a.moveTaskToProject(id, p)
	}
	return a, nil
}

// moveTaskToProject moves a task, and its whole sub-tree, into a project.
func (a app) moveTaskToProject(taskID int64, p task.Project) tea.Cmd {
	return a.mutate(flash{kind: flashEdit,
		text: fmt.Sprintf("#%d moved to %s", taskID, p.Name)}, func() error {
		return a.store.SetProject(a.ctx, taskID, p.ID)
	})
}

// projectPickerView renders the chooser box: a title row naming the task,
// a divider, the filter prompt, then the numbered projects with the
// current one marked.
func (a app) projectPickerView() string {
	s, g := a.styles, a.styles.Glyphs
	title := truncTail(a.projectPickerLabel, max(a.width-30, 10), g.Ellipsis)
	return a.projectPicker.render(s, a.width, a.height, pickerView[task.Project]{
		icon:  s.Accent.Bold(true).Render(g.CaretClosed + " "),
		title: s.Title.Render("move ") + s.Dimmed.Render(title),
		hint:  s.Muted.Render("  to project"),
		row: func(p task.Project, selected bool, w int) string {
			count := s.CountLabel.Render(fmt.Sprintf("  %d", p.LiveCount))
			if selected {
				return s.Title.Render(p.Name) + count
			}
			return s.Dimmed.Render(p.Name) + count
		},
		empty:   "no projects yet - press [ then n to make one",
		noMatch: "no matching projects",
	})
}
