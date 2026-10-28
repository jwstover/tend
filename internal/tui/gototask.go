package tui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/task"
)

// Go to task is the `gt` chord: type a task id into a modal and the list
// lands on that task with the detail pane open, whatever stood in the way
// -- another project selected in the column, completed tasks hidden, or
// the task sitting collapsed under a parent (at any depth).

// gotoTaskMsg carries a looked-up task and the chain above it: ancestors
// runs from its parent up to root, the top-level task whose list row the
// target hangs under (root is the target itself when it has no parent).
type gotoTaskMsg struct {
	target    task.Task
	root      task.Task
	ancestors []int64
}

// openGotoTask opens the single-line id prompt.
func (a *app) openGotoTask() tea.Cmd {
	return a.modal.Open(modalGotoTask, false, "go to task #", 0, "")
}

// submitGotoTask parses the typed id ("42" or "#42") and looks it up. An
// empty submit just closes the modal, like esc.
func (a app) submitGotoTask(value string) (tea.Model, tea.Cmd) {
	if value == "" {
		return a, nil
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(value, "#"), 10, 64)
	if err != nil || id <= 0 {
		a.status = flash{text: fmt.Sprintf("not a task id: %q", value), isErr: true}
		return a, nil
	}
	return a, a.gotoTaskCmd(id)
}

// gotoTaskCmd loads the task and walks its parents up to the top level,
// off the update loop.
func (a app) gotoTaskCmd(id int64) tea.Cmd {
	return func() tea.Msg {
		t, err := a.store.GetTask(a.ctx, id)
		if errors.Is(err, task.ErrTaskNotFound) {
			return statusMsg(flash{text: fmt.Sprintf("no task #%d", id), isErr: true})
		}
		if err != nil {
			return errMsg{err}
		}
		msg := gotoTaskMsg{target: t, root: t}
		// The store refuses parent cycles; the seen set only keeps a
		// corrupt row from spinning this loop forever.
		seen := map[int64]bool{t.ID: true}
		for msg.root.ParentID != nil && !seen[*msg.root.ParentID] {
			p, err := a.store.GetTask(a.ctx, *msg.root.ParentID)
			if err != nil {
				return errMsg{fmt.Errorf("loading the parents of task %d: %w", id, err)}
			}
			seen[p.ID] = true
			msg.ancestors = append(msg.ancestors, p.ID)
			msg.root = p
		}
		return msg
	}
}

// applyGotoTask makes the target's row reachable and asks the reload to
// land on it: the column moves to the task's project unless All already
// shows it, completed tasks come into view when the top-level row would
// otherwise be filtered out, and every ancestor opens. The cursor follows
// through pendingSelectID, since a freshly opened branch only gets its
// rows once its children load, a message or more later.
func (a app) applyGotoTask(msg gotoTaskMsg) (tea.Model, tea.Cmd) {
	if a.mode != modeList {
		return a, nil
	}
	root := msg.root
	// Only the top-level row is filtered by the live-view query; children
	// of an open branch are listed in every state. A snoozed root is out
	// of every list, completed or not, so there is no row to land on.
	if root.SnoozeUntil != nil && *root.SnoozeUntil > time.Now().UTC().Format(time.DateOnly) {
		what := fmt.Sprintf("#%d is", msg.target.ID)
		if root.ID != msg.target.ID {
			what = fmt.Sprintf("#%d sits under #%d, which is", msg.target.ID, root.ID)
		}
		a.status = flash{text: fmt.Sprintf("%s snoozed until %s", what, *root.SnoozeUntil)}
		return a, nil
	}

	var notes []string
	if !a.showCompleted && (root.State.Closed() || root.State == task.StateSomeday) {
		a.showCompleted = true
		notes = append(notes, "completed shown")
	}
	if a.list.FilterState() != list.Unfiltered {
		a.list.ResetFilter()
	}
	for _, id := range msg.ancestors {
		a.expanded[id] = true
	}
	a.pendingSelectID = msg.target.ID
	a.focus = paneTasks
	a.showDetail = true
	a.resize()

	// loadTasks reads showCompleted when it is called, so the project move
	// (which issues the reload) comes after the toggle above.
	cmd := a.loadTasks(a.mode)
	if a.projectFilter != nil && *a.projectFilter != root.ProjectID {
		cmd = a.scopeToProject(root.ProjectID)
		if p, ok := a.selectedProject(); ok {
			notes = append(notes, "in "+p.Name)
		}
	}

	text := fmt.Sprintf("#%d %s", msg.target.ID, msg.target.Title)
	if len(notes) > 0 {
		text += " · " + strings.Join(notes, " · ")
	}
	a.status = flash{text: text}
	return a, cmd
}

// scopeToProject points the projects column at a project and reloads the
// list for it, showing the archived shelf first if that is where the
// project sits. A project the column cannot show falls back to All.
func (a *app) scopeToProject(id int64) tea.Cmd {
	isID := func(p task.Project) bool { return p.ID == id }
	if !a.showArchived && slices.ContainsFunc(a.archivedProjects(), isID) {
		a.showArchived = true
	}
	row := allProjectsRow
	if i := slices.IndexFunc(a.visibleProjects(), isID); i >= 0 {
		row = i + 1
	}
	return a.setProjectCursor(row)
}
