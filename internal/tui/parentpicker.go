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

// The parent picker is the `m` key: choose which task a task hangs
// under, or lift it to the top level. It clones the project picker's
// overlay idiom (same box, same navigation, same Enter/Esc/digit moves)
// and borrows the palette's type-to-filter, because a project can hold
// far more tasks than the digit shortcuts reach.

// parentRow is one candidate: a task's id and its breadcrumb path, or
// nil and TopLevelLabel for the synthetic top-level row.
type parentRow struct {
	id    *int64
	label string
}

// parentMove is a confirmed re-parent waiting for its refreshMsg: which
// task moved, and the branches on either end whose cached children are
// about to be wrong.
type parentMove struct {
	taskID   int64
	from, to *int64
}

// parentCandidatesMsg carries a project's tasks to openParentPicker.
type parentCandidatesMsg struct {
	task task.Task
	all  []task.Task
}

// loadParentCandidates fetches every task in the moving task's project,
// at any depth and in any state, so the picker can offer the whole tree
// and rule out the sub-tree being moved; the loaded list holds only
// the top level.
func (a app) loadParentCandidates(t task.Task) tea.Cmd {
	return func() tea.Msg {
		all, err := a.store.ListProjectTasks(a.ctx, t.ProjectID)
		if err != nil {
			return errMsg{err}
		}
		return parentCandidatesMsg{task: t, all: all}
	}
}

// parentCandidates turns a project's tasks into the picker's rows: the
// top-level row first (unless the task is already there), then every
// task that could legally be its parent, as a breadcrumb path sorted so
// each branch reads top-down. Ruled out are the task itself, its
// descendants (the store would refuse the cycle) and its current parent
// (a no-op).
func parentCandidates(t task.Task, all []task.Task) []parentRow {
	byID := make(map[int64]task.Task, len(all))
	for _, c := range all {
		byID[c.ID] = c
	}
	// ancestors walks a task's parent chain root-ward, capped at the list
	// length so a corrupt cycle can't spin forever.
	ancestors := func(c task.Task) []task.Task {
		var chain []task.Task
		for p := c.ParentID; p != nil && len(chain) < len(all); {
			parent, ok := byID[*p]
			if !ok {
				break
			}
			chain = append(chain, parent)
			p = parent.ParentID
		}
		return chain
	}
	var rows []parentRow
	if t.ParentID != nil {
		rows = append(rows, parentRow{label: task.TopLevelLabel})
	}
	var found []parentRow
	for _, c := range all {
		if c.ID == t.ID || (t.ParentID != nil && c.ID == *t.ParentID) {
			continue
		}
		chain := ancestors(c)
		if slices.ContainsFunc(chain, func(p task.Task) bool { return p.ID == t.ID }) {
			continue
		}
		parts := make([]string, 0, len(chain)+1)
		for i := len(chain) - 1; i >= 0; i-- {
			parts = append(parts, chain[i].Title)
		}
		parts = append(parts, c.Title)
		id := c.ID
		found = append(found, parentRow{id: &id, label: strings.Join(parts, " / ")})
	}
	slices.SortStableFunc(found, func(x, y parentRow) int {
		if c := strings.Compare(x.label, y.label); c != 0 {
			return c
		}
		return cmp.Compare(*x.id, *y.id)
	})
	return append(rows, found...)
}

// openParentPicker arms the picker with the loaded candidates.
func (a *app) openParentPicker(msg parentCandidatesMsg) {
	a.parentPickerOpen = true
	a.parentPickerTaskID = msg.task.ID
	a.parentPickerFrom = msg.task.ParentID
	a.parentPickerLabel = msg.task.Title
	a.parentPickerQuery = ""
	a.parentPickerSel = 0
	a.parentPickerRows = parentCandidates(msg.task, msg.all)
}

func (a *app) closeParentPicker() {
	a.parentPickerOpen = false
	a.parentPickerTaskID = 0
	a.parentPickerFrom = nil
	a.parentPickerLabel = ""
	a.parentPickerQuery = ""
	a.parentPickerSel = 0
	a.parentPickerRows = nil
}

// parentPickerMatches narrows the rows to those whose breadcrumb contains
// the query, case-insensitively. The top-level row is just another row
// here: it stays while nothing is typed and has to match once something is.
func (a app) parentPickerMatches() []parentRow {
	q := strings.ToLower(strings.TrimSpace(a.parentPickerQuery))
	if q == "" {
		return a.parentPickerRows
	}
	var out []parentRow
	for _, r := range a.parentPickerRows {
		if strings.Contains(strings.ToLower(r.label), q) {
			out = append(out, r)
		}
	}
	return out
}

// handleParentPickerKey owns the keyboard while the picker is open: type
// to filter, ↑/↓ (or ctrl+p/ctrl+n) to move, a digit or ⏎ picks, esc
// dismisses.
func (a app) handleParentPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := a.parentPickerMatches()
	pick := func(r parentRow) (tea.Model, tea.Cmd) {
		id := a.parentPickerTaskID
		a.pendingMove = &parentMove{taskID: id, from: a.parentPickerFrom, to: r.id}
		a.closeParentPicker()
		return a, a.moveTaskToParent(id, r.id, r.label)
	}

	switch msg.String() {
	case "esc":
		a.closeParentPicker()
		return a, nil
	case "enter":
		if sel := a.parentPickerSel; sel >= 0 && sel < len(rows) {
			return pick(rows[sel])
		}
		a.closeParentPicker()
		return a, nil
	case "up", "ctrl+p":
		if a.parentPickerSel > 0 {
			a.parentPickerSel--
		}
		return a, nil
	case "down", "ctrl+n":
		if a.parentPickerSel < len(rows)-1 {
			a.parentPickerSel++
		}
		return a, nil
	case "backspace":
		if r := []rune(a.parentPickerQuery); len(r) > 0 {
			a.parentPickerQuery = string(r[:len(r)-1])
		}
		a.parentPickerSel = 0
		return a, nil
	}
	// A digit 1-9 picks that visible row directly, like the project picker.
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		if idx := int(msg.Text[0] - '1'); idx < len(rows) {
			return pick(rows[idx])
		}
		return a, nil
	}
	if msg.Text != "" {
		a.parentPickerQuery += msg.Text
		a.parentPickerSel = 0
	}
	return a, nil
}

// moveTaskToParent re-parents a task, and its whole sub-tree, under
// another task (nil lifts it to the top level). The caller has already
// recorded the move in pendingMove, so the reload this triggers can fix
// the tree caches up and follow the task to its new row.
func (a app) moveTaskToParent(taskID int64, parentID *int64, label string) tea.Cmd {
	text := fmt.Sprintf("#%d moved under %s", taskID, label)
	if parentID == nil {
		text = fmt.Sprintf("#%d moved to top level", taskID)
	}
	return a.mutate(flash{kind: flashEdit, text: text}, func() error {
		return a.store.SetParent(a.ctx, taskID, parentID)
	})
}

// settlePendingMove runs on the refreshMsg after a re-parent landed. The
// children cached for the old and new parents are both stale, so drop
// them rather than show the task in two places until the branches
// reload; the new parent opens so the task stays in view, and the
// cursor is asked to follow it once its row exists.
func (a *app) settlePendingMove() {
	mv := a.pendingMove
	if mv == nil {
		return
	}
	a.pendingMove = nil
	if mv.from != nil {
		delete(a.childCache, *mv.from)
	}
	if mv.to != nil {
		delete(a.childCache, *mv.to)
		a.expanded[*mv.to] = true
	}
	a.pendingSelectID = mv.taskID
}

// parentPickerView renders the chooser box: a title row naming the task,
// a divider, the filter prompt, then the numbered candidates with the
// current one marked.
func (a app) parentPickerView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)

	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	title := truncTail(a.parentPickerLabel, max(w-30, 10), g.Ellipsis)
	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.Accent.Bold(true).Render(g.CaretClosed+" ")+
		s.Title.Render("move ")+s.Dimmed.Render(title)+
		s.Muted.Render("  to parent")))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))
	lines = append(lines, row(s.Accent.Bold(true).Render("❯ ")+
		s.Title.Render(a.parentPickerQuery)+s.Accent.Render("▏")))

	rows := a.parentPickerMatches()
	switch {
	case len(a.parentPickerRows) == 0:
		lines = append(lines, row("  "+s.Muted.Render("no other tasks in this project")))
	case len(rows) == 0:
		lines = append(lines, row("  "+s.Muted.Render("no matching tasks")))
	}
	sel := min(a.parentPickerSel, len(rows)-1)
	for i, r := range rows {
		num := fmt.Sprintf("%d ", i+1)
		label := truncTail(r.label, max(w-12, 10), g.Ellipsis)
		var content string
		if i == sel {
			content = s.SelBar.Render(g.SelBar+" ") + s.Accent.Render(num) + s.Title.Render(label)
		} else {
			content = "  " + s.Muted.Render(num) + s.Dimmed.Render(label)
		}
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}
