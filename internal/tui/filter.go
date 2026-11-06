package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/task"
)

// matchesFilter reports whether a task matches the `/` filter query. The
// query is split into words that must ALL appear, as case-insensitive
// substrings, somewhere in the title or the tags. It is deliberately not
// fuzzy and does not look at the body. A blank query matches everything.
func matchesFilter(query, title string, tags []string) bool {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return true
	}
	hay := strings.ToLower(title)
	for _, tg := range tags {
		hay += " " + strings.ToLower(tg)
	}
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// filterView is the list's shape under an active `/` query: the top-level
// tasks to show, and for each branch the sub-tasks that make it visible.
//
// A task is shown when it matches or something under it does. The ancestors
// of a match that don't match themselves are dim: context only, so real
// hits stand apart from scaffolding. Branches open for as long as the query
// does, without touching the user's own expand set, so clearing the filter
// restores the tree as they left it.
type filterView struct {
	top      []task.Task
	children map[int64][]task.Task
	expanded map[int64]bool
	dim      map[int64]bool
}

// buildFilterView applies query across the whole loaded task slice, at any
// depth. Only live sub-tasks are in tasks, so a closed one is not matched.
func buildFilterView(query string, tasks []task.Task, tags map[int64][]string) filterView {
	byParent := make(map[int64][]task.Task)
	for _, t := range tasks {
		if t.ParentID != nil {
			byParent[*t.ParentID] = append(byParent[*t.ParentID], t)
		}
	}
	fv := filterView{
		children: make(map[int64][]task.Task),
		expanded: make(map[int64]bool),
		dim:      make(map[int64]bool),
	}
	// visit reports whether t or anything under it matches, recording the
	// visible children as it unwinds. The seen set guards a corrupt cycle.
	seen := make(map[int64]bool)
	var visit func(t task.Task) bool
	visit = func(t task.Task) bool {
		if seen[t.ID] {
			return false
		}
		seen[t.ID] = true
		self := matchesFilter(query, t.Title, tags[t.ID])
		var kept []task.Task
		for _, c := range byParent[t.ID] {
			if visit(c) {
				kept = append(kept, c)
			}
		}
		if len(kept) > 0 {
			fv.children[t.ID] = kept
			fv.expanded[t.ID] = true
		}
		if !self && len(kept) > 0 {
			fv.dim[t.ID] = true
		}
		return self || len(kept) > 0
	}
	for _, t := range tasks {
		if t.ParentID == nil && visit(t) {
			fv.top = append(fv.top, t)
		}
	}
	return fv
}

// startFilter begins editing the filter query, keeping any existing one.
func (a *app) startFilter() {
	a.filtering = true
}

// clearFilter drops the query and stops editing, returning the commands
// that rebuild the list around the task that was selected.
func (a *app) clearFilter() tea.Cmd {
	a.filter = ""
	a.filtering = false
	return a.refilter()
}

// refilter rebuilds the list for the current query, keeping the cursor on
// the same task when it is still visible.
func (a *app) refilter() tea.Cmd {
	id := int64(0)
	if r, ok := a.list.SelectedItem().(rowItem); ok {
		id = r.rowTask().ID
	}
	cmd := a.rebuildList()
	if id == 0 || !a.selectByID(id) {
		a.selectFirstTask()
	}
	return tea.Batch(cmd, a.syncDetail(false))
}

// selectFirstTask puts the cursor on the first task row, for when the
// previously selected task is no longer visible. The rebuilt list's cursor
// is just an index and may be sitting on a heading or past the end.
func (a *app) selectFirstTask() {
	for i, it := range a.list.VisibleItems() {
		if _, ok := it.(rowItem); ok {
			a.list.Select(i)
			return
		}
	}
}

// handleFilterKey edits the query while the filter owns the keyboard:
// text appends, backspace trims, enter keeps the filter and returns the
// keyboard, esc clears it.
func (a app) handleFilterKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return a, a.clearFilter()
	case "enter":
		a.filtering = false
		return a, nil
	case "backspace":
		if r := []rune(a.filter); len(r) > 0 {
			a.filter = string(r[:len(r)-1])
		}
	case "ctrl+u":
		a.filter = ""
	default:
		if msg.Text == "" {
			return a, nil
		}
		a.filter += msg.Text
	}
	return a, a.refilter()
}
