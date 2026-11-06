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

// filterTasks narrows the top-level task slice to those matching query.
// It runs before groupTasks so the filtered view keeps its headings.
func filterTasks(query string, tasks []task.Task, tags map[int64][]string) []task.Task {
	if strings.TrimSpace(query) == "" {
		return tasks
	}
	out := make([]task.Task, 0, len(tasks))
	for _, t := range tasks {
		if matchesFilter(query, t.Title, tags[t.ID]) {
			out = append(out, t)
		}
	}
	return out
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
	if id != 0 {
		a.selectByID(id)
	}
	return tea.Batch(cmd, a.syncDetail(false))
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
