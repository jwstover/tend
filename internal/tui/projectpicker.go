package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/task"
)

// The project picker is the `P` key: choose which project a task belongs
// to. It clones the palette/url-picker overlay idiom rather than inventing
// a new one -- same bottom-anchored bordered box, same arrow/ctrl-n/ctrl-p
// navigation, same Enter-to-act and Esc-to-dismiss.

// openProjectPicker arms the picker for a task, starting on the project
// the task is already in so Enter is a no-op rather than a surprise.
func (a *app) openProjectPicker(t task.Task) {
	a.projectPickerOpen = true
	a.projectPickerTaskID = t.ID
	a.projectPickerLabel = t.Title
	a.projectPickerSel = 0
	for i, p := range a.visibleProjects() {
		if p.ID == t.ProjectID {
			a.projectPickerSel = i
			break
		}
	}
}

func (a *app) closeProjectPicker() {
	a.projectPickerOpen = false
	a.projectPickerTaskID = 0
	a.projectPickerLabel = ""
	a.projectPickerSel = 0
}

// handleProjectPickerKey owns the keyboard while the picker is open.
func (a app) handleProjectPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	projects := a.visibleProjects()

	switch msg.String() {
	case "esc":
		a.closeProjectPicker()
		return a, nil
	case "enter":
		sel, id := a.projectPickerSel, a.projectPickerTaskID
		a.closeProjectPicker()
		if sel >= 0 && sel < len(projects) {
			return a, a.moveTaskToProject(id, projects[sel])
		}
		return a, nil
	case "up", "ctrl+p":
		if a.projectPickerSel > 0 {
			a.projectPickerSel--
		}
		return a, nil
	case "down", "ctrl+n":
		if a.projectPickerSel < len(projects)-1 {
			a.projectPickerSel++
		}
		return a, nil
	}
	// A digit 1-9 picks that project directly, like the URL picker.
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		if idx := int(msg.Text[0] - '1'); idx < len(projects) {
			id := a.projectPickerTaskID
			p := projects[idx]
			a.closeProjectPicker()
			return a, a.moveTaskToProject(id, p)
		}
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
// a divider, then the numbered projects with the current one marked.
func (a app) projectPickerView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)

	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	title := truncTail(a.projectPickerLabel, max(w-30, 10), g.Ellipsis)
	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.Accent.Bold(true).Render(g.CaretClosed+" ")+
		s.Title.Render("move ")+s.Dimmed.Render(title)+
		s.Muted.Render("  to project")))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))

	projects := a.visibleProjects()
	if len(projects) == 0 {
		lines = append(lines, row(s.Muted.Render("no projects yet - press [ then n to make one")))
	}
	sel := min(a.projectPickerSel, len(projects)-1)
	for i, p := range projects {
		num := fmt.Sprintf("%d ", i+1)
		count := s.CountLabel.Render(fmt.Sprintf("  %d", p.LiveCount))
		var content string
		if i == sel {
			content = s.SelBar.Render(g.SelBar+" ") + s.Accent.Render(num) +
				s.Title.Render(p.Name) + count
		} else {
			content = "  " + s.Muted.Render(num) + s.Dimmed.Render(p.Name) + count
		}
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}
