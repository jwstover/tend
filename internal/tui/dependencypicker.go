package tui

import (
	"cmp"
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/task"
)

// The dependency picker is the `b` key: choose which tasks the selected
// one waits on. It is the parent picker's overlay (same box, navigation,
// filter and digit shortcuts) with one difference that changes how it is
// used: it is a multi-select. ⏎ or a digit toggles the row's edge and the
// picker stays open, so "this waits on those three" is one visit rather
// than three. Candidates span every project, since a task may well wait
// on one elsewhere.

// dependencyRow is one candidate: the task, and its project's name when
// that differs from the selected task's (blank otherwise, so rows in the
// same project stay uncluttered).
type dependencyRow struct {
	id      int64
	title   string
	project string
	done    bool // a done task is offered only when it is already a blocker
}

// dependencyCandidatesMsg carries everything openDependencyPicker needs:
// the open tasks, the project names to label the foreign ones, and the
// task's current blockers to pre-check.
type dependencyCandidatesMsg struct {
	task     task.Task
	open     []task.Task
	projects []task.Project
	blockers []task.Task
}

// dependencyToggledMsg reports one edge the store accepted: taskID now
// does (added) or no longer does wait on dependsOnID. title is the
// blocker's, for the flash.
type dependencyToggledMsg struct {
	taskID, dependsOnID int64
	added               bool
	title               string
}

// loadDependencyCandidates fetches the picker's inputs off the loop.
func (a app) loadDependencyCandidates(t task.Task) tea.Cmd {
	return func() tea.Msg {
		open, err := a.store.ListOpenTasks(a.ctx)
		if err != nil {
			return errMsg{err}
		}
		projects, err := a.store.ListProjects(a.ctx)
		if err != nil {
			return errMsg{err}
		}
		blockers, err := a.store.Blockers(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		return dependencyCandidatesMsg{task: t, open: open, projects: projects, blockers: blockers}
	}
}

// dependencyCandidates turns the loaded tasks into the picker's rows:
// every open task but the one being edited, plus any blocker that has
// since been done (it is not "open", but it is still an edge the user may
// want to drop, and it can only be dropped from here). Sorted so the
// current blockers lead, then the task's own project ahead of the others,
// then by id — the rows a visit is most likely about come first and reach
// the digit shortcuts.
func dependencyCandidates(t task.Task, open []task.Task, projects []task.Project,
	checked map[int64]bool, blockers []task.Task) []dependencyRow {
	names := make(map[int64]string, len(projects))
	for _, p := range projects {
		names[p.ID] = p.Name
	}
	row := func(c task.Task) dependencyRow {
		r := dependencyRow{id: c.ID, title: c.Title, done: c.State == task.StateDone}
		if c.ProjectID != t.ProjectID {
			r.project = names[c.ProjectID]
		}
		return r
	}
	rows := make([]dependencyRow, 0, len(open))
	seen := make(map[int64]bool, len(open))
	for _, c := range open {
		if c.ID == t.ID {
			continue
		}
		seen[c.ID] = true
		rows = append(rows, row(c))
	}
	for _, blk := range blockers {
		if !seen[blk.ID] && blk.ID != t.ID {
			rows = append(rows, row(blk))
		}
	}
	// A foreign row is one whose project differs from the task's; the
	// name lookup already encodes that, so an unnamed (deleted) project
	// still sorts as foreign.
	byID := make(map[int64]task.Task, len(open)+len(blockers))
	for _, c := range open {
		byID[c.ID] = c
	}
	for _, c := range blockers {
		byID[c.ID] = c
	}
	slices.SortStableFunc(rows, func(x, y dependencyRow) int {
		if c := cmp.Compare(rank(!checked[x.id]), rank(!checked[y.id])); c != 0 {
			return c
		}
		if c := cmp.Compare(rank(byID[x.id].ProjectID != t.ProjectID),
			rank(byID[y.id].ProjectID != t.ProjectID)); c != 0 {
			return c
		}
		return cmp.Compare(x.id, y.id)
	})
	return rows
}

// rank turns a "sorts later" flag into a comparable 0/1.
func rank(later bool) int {
	if later {
		return 1
	}
	return 0
}

// dependencyRowLabel is what the filter matches: the title and project
// together, so a query matching either field lands in fuzzyFilter's
// substring or in-order-subsequence tier (fuzzyMatch skips whitespace in
// the query, so a cross-field query still works).
func dependencyRowLabel(r dependencyRow) string { return r.title + "  " + r.project }

// openDependencyPicker arms the picker with the loaded candidates, the
// current blockers checked.
func (a *app) openDependencyPicker(msg dependencyCandidatesMsg) {
	checked := make(map[int64]bool, len(msg.blockers))
	for _, blk := range msg.blockers {
		checked[blk.ID] = true
	}
	a.depPickerTaskID = msg.task.ID
	a.depPickerProject = msg.task.ProjectID
	a.depPickerLabel = msg.task.Title
	a.depPickerChecked = checked
	a.depPicker = picker[dependencyRow]{
		open: true, numbered: true, label: dependencyRowLabel,
		items: dependencyCandidates(msg.task, msg.open, msg.projects, checked, msg.blockers),
	}
}

func (a *app) closeDependencyPicker() {
	a.depPickerTaskID = 0
	a.depPickerProject = 0
	a.depPickerLabel = ""
	a.depPickerChecked = nil
	a.depPicker = picker[dependencyRow]{}
}

// handleDependencyPickerKey owns the keyboard while the picker is open:
// type to filter, ↑/↓ (or ctrl+p/ctrl+n) to move, a digit or ⏎ toggles
// the row and keeps the picker up, esc dismisses.
func (a app) handleDependencyPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	taskID := a.depPickerTaskID
	action, idx := a.depPicker.key(msg, a.height)
	switch action {
	case pickerCancel:
		a.closeDependencyPicker()
		return a, nil
	case pickerPick:
		if r, ok := a.depPicker.at(idx); ok {
			return a, a.toggleDependency(taskID, r, a.depPickerChecked[r.id])
		}
	}
	return a, nil
}

// toggleDependency writes one edge: removes it when the row is checked,
// adds it otherwise. Not `mutate`, because a refreshMsg would be the
// wrong follow-up here — the picker needs to know which row to flip, and
// the check must not move until the store has agreed. A refusal (a cycle,
// say) comes back as errMsg like any other mutation, so the status line
// shows why and the row stays as it was.
func (a app) toggleDependency(taskID int64, r dependencyRow, checked bool) tea.Cmd {
	return func() tea.Msg {
		if checked {
			if err := a.store.RemoveDependency(a.ctx, taskID, r.id); err != nil {
				return errMsg{err}
			}
			return dependencyToggledMsg{taskID: taskID, dependsOnID: r.id, added: false, title: r.title}
		}
		if err := a.store.AddDependency(a.ctx, taskID, r.id); err != nil {
			return errMsg{err}
		}
		return dependencyToggledMsg{taskID: taskID, dependsOnID: r.id, added: true, title: r.title}
	}
}

// applyDependencyToggle runs on a confirmed edge: flip the row's check
// (only if the picker is still up for the same task — the user may have
// closed it before the write landed), flash what changed, and reload the
// way refreshMsg does, so the list's dependency cells and the detail
// pane's BLOCKED BY / BLOCKS catch up. The rows keep their opening order:
// re-sorting on every toggle would move the row out from under the
// cursor mid-visit.
func (a *app) applyDependencyToggle(msg dependencyToggledMsg) tea.Cmd {
	if a.depPicker.open && a.depPickerTaskID == msg.taskID && a.depPickerChecked != nil {
		a.depPickerChecked[msg.dependsOnID] = msg.added
	}
	text := fmt.Sprintf("#%d no longer waits on #%d", msg.taskID, msg.dependsOnID)
	if msg.added {
		text = fmt.Sprintf("#%d now waits on #%d: %s", msg.taskID, msg.dependsOnID, msg.title)
	}
	a.status = flash{kind: flashEdit, text: text}
	a.liveReloadDeferred = false
	return a.reloadCmd()
}

// dependencyPickerView renders the chooser box: a title row naming the
// task, a divider, the filter prompt, then the numbered candidates with
// the current one marked and each blocker checked.
func (a app) dependencyPickerView() string {
	s, g := a.styles, a.styles.Glyphs
	title := truncTail(a.depPickerLabel, max(a.width-40, 10), g.Ellipsis)
	return a.depPicker.render(s, a.width, a.height, pickerView[dependencyRow]{
		icon:  s.Accent.Bold(true).Render(g.CaretClosed + " "),
		title: s.Title.Render("dependencies of ") + s.Dimmed.Render(fmt.Sprintf("#%d %s", a.depPickerTaskID, title)),
		right: s.FooterKey.Render("esc") + s.Muted.Render(" closes"),
		row: func(r dependencyRow, selected bool, w int) string {
			box := s.CheckOpen.Render(g.BoxUnchecked)
			if a.depPickerChecked[r.id] {
				box = s.CheckDone.Render(g.BoxChecked)
			}
			id := fmt.Sprintf("#%d", r.id)
			// Title, project and id share the row: the title gives way first.
			fixed := lipgloss.Width(box) + 1 + 2 + len(id)
			if r.project != "" {
				fixed += 2 + runeWidth(r.project)
			}
			label := truncTail(r.title, max(w-fixed, 10), g.Ellipsis)
			titleStyle, projStyle := s.Dimmed, s.Faint
			if selected {
				titleStyle, projStyle = s.Title, s.Muted
			}
			if r.done {
				titleStyle = s.SubDoneText
			}
			content := box + " " + titleStyle.Render(label)
			if r.project != "" {
				content += "  " + projStyle.Render(r.project)
			}
			content += "  " + s.DetailFaint.Render(id)
			return content
		},
		empty:   "no other open tasks",
		noMatch: "no matching tasks",
	})
}
