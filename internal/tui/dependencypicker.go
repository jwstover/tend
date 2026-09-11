package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

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

// openDependencyPicker arms the picker with the loaded candidates, the
// current blockers checked.
func (a *app) openDependencyPicker(msg dependencyCandidatesMsg) {
	checked := make(map[int64]bool, len(msg.blockers))
	for _, blk := range msg.blockers {
		checked[blk.ID] = true
	}
	a.depPickerOpen = true
	a.depPickerTaskID = msg.task.ID
	a.depPickerProject = msg.task.ProjectID
	a.depPickerLabel = msg.task.Title
	a.depPickerQuery = ""
	a.depPickerSel = 0
	a.depPickerChecked = checked
	a.depPickerRows = dependencyCandidates(msg.task, msg.open, msg.projects, checked, msg.blockers)
}

func (a *app) closeDependencyPicker() {
	a.depPickerOpen = false
	a.depPickerTaskID = 0
	a.depPickerProject = 0
	a.depPickerLabel = ""
	a.depPickerQuery = ""
	a.depPickerSel = 0
	a.depPickerRows = nil
	a.depPickerChecked = nil
}

// dependencyPickerMatches narrows the rows to those whose title or
// project name contains the query, case-insensitively.
func (a app) dependencyPickerMatches() []dependencyRow {
	q := strings.ToLower(strings.TrimSpace(a.depPickerQuery))
	if q == "" {
		return a.depPickerRows
	}
	var out []dependencyRow
	for _, r := range a.depPickerRows {
		if strings.Contains(strings.ToLower(r.title), q) ||
			strings.Contains(strings.ToLower(r.project), q) {
			out = append(out, r)
		}
	}
	return out
}

// handleDependencyPickerKey owns the keyboard while the picker is open:
// type to filter, ↑/↓ (or ctrl+p/ctrl+n) to move, a digit or ⏎ toggles
// the row and keeps the picker up, esc dismisses.
func (a app) handleDependencyPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := a.dependencyPickerMatches()
	toggle := func(r dependencyRow) (tea.Model, tea.Cmd) {
		return a, a.toggleDependency(a.depPickerTaskID, r, a.depPickerChecked[r.id])
	}

	switch msg.String() {
	case "esc":
		a.closeDependencyPicker()
		return a, nil
	case "enter":
		if sel := a.depPickerSel; sel >= 0 && sel < len(rows) {
			return toggle(rows[sel])
		}
		return a, nil
	case "up", "ctrl+p":
		if a.depPickerSel > 0 {
			a.depPickerSel--
		}
		return a, nil
	case "down", "ctrl+n":
		if a.depPickerSel < len(rows)-1 {
			a.depPickerSel++
		}
		return a, nil
	case "backspace":
		if r := []rune(a.depPickerQuery); len(r) > 0 {
			a.depPickerQuery = string(r[:len(r)-1])
		}
		a.depPickerSel = 0
		return a, nil
	}
	// A digit 1-9 toggles that visible row directly, like the project picker.
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		if idx := int(msg.Text[0] - '1'); idx < len(rows) {
			return toggle(rows[idx])
		}
		return a, nil
	}
	if msg.Text != "" {
		a.depPickerQuery += msg.Text
		a.depPickerSel = 0
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
	if a.depPickerOpen && a.depPickerTaskID == msg.taskID && a.depPickerChecked != nil {
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
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)

	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	title := truncTail(a.depPickerLabel, max(w-40, 10), g.Ellipsis)
	head := s.Accent.Bold(true).Render(g.CaretClosed+" ") +
		s.Title.Render("dependencies of ") +
		s.Dimmed.Render(fmt.Sprintf("#%d %s", a.depPickerTaskID, title))
	hint := s.FooterKey.Render("esc") + s.Muted.Render(" closes")
	head += strings.Repeat(" ", max(w-5-lipgloss.Width(head)-lipgloss.Width(hint), 1)) + hint
	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(head))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))
	lines = append(lines, row(s.Accent.Bold(true).Render("❯ ")+
		s.Title.Render(a.depPickerQuery)+s.Accent.Render("▏")))

	rows := a.dependencyPickerMatches()
	switch {
	case len(a.depPickerRows) == 0:
		lines = append(lines, row("  "+s.Muted.Render("no other open tasks")))
	case len(rows) == 0:
		lines = append(lines, row("  "+s.Muted.Render("no matching tasks")))
	}
	sel := min(a.depPickerSel, len(rows)-1)
	for i, r := range rows {
		num := fmt.Sprintf("%d ", i+1)
		box := s.CheckOpen.Render(g.BoxUnchecked)
		if a.depPickerChecked[r.id] {
			box = s.CheckDone.Render(g.BoxChecked)
		}
		id := fmt.Sprintf("#%d", r.id)
		// Title, project and id share the row: the title gives way first.
		fixed := 2 + len(num) + lipgloss.Width(box) + 1 + 2 + len(id)
		if r.project != "" {
			fixed += 2 + runeWidth(r.project)
		}
		label := truncTail(r.title, max(w-5-fixed, 10), g.Ellipsis)
		titleStyle, projStyle := s.Dimmed, s.Faint
		if i == sel {
			titleStyle, projStyle = s.Title, s.Muted
		}
		if r.done {
			titleStyle = s.SubDoneText
		}
		content := "  " + s.Muted.Render(num)
		if i == sel {
			content = s.SelBar.Render(g.SelBar+" ") + s.Accent.Render(num)
		}
		content += box + " " + titleStyle.Render(label)
		if r.project != "" {
			content += "  " + projStyle.Render(r.project)
		}
		content += "  " + s.DetailFaint.Render(id)
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}
