package tui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/td/internal/task"
)

// listItem adapts a task.Task to the bubbles list.Item interface.
type listItem struct {
	t task.Task
}

// FilterValue feeds the list's built-in `/` filtering.
func (i listItem) FilterValue() string {
	v := i.t.Title
	if i.t.Project != nil {
		v += " " + *i.t.Project
	}
	return v
}

func toItems(tasks []task.Task) []list.Item {
	items := make([]list.Item, len(tasks))
	for i, t := range tasks {
		items[i] = listItem{t: t}
	}
	return items
}

// sectionItem is a non-selectable heading row that labels the state group
// below it. An empty FilterValue keeps headings out of `/` filter results.
type sectionItem struct {
	state task.State
	count int
}

func (i sectionItem) FilterValue() string { return "" }

// stateOrder is the display order of section groups: active work first,
// then the queue, then everything waiting.
var stateOrder = []task.State{
	task.StateDoing,
	task.StateTodo,
	task.StateBlocked,
	task.StateInbox,
	task.StateSomeday,
	task.StateDone,
}

// toGroupedItems lays tasks out under one section heading per state, in
// stateOrder, preserving the store's ordering within each group.
func toGroupedItems(tasks []task.Task) []list.Item {
	groups := make(map[task.State][]task.Task)
	for _, t := range tasks {
		groups[t.State] = append(groups[t.State], t)
	}
	items := make([]list.Item, 0, len(tasks)+len(stateOrder))
	for _, s := range stateOrder {
		group := groups[s]
		if len(group) == 0 {
			continue
		}
		items = append(items, sectionItem{state: s, count: len(group)})
		for _, t := range group {
			items = append(items, listItem{t: t})
		}
		delete(groups, s)
	}
	// States missing from stateOrder still get rendered rather than
	// silently dropped.
	for _, t := range tasks {
		if _, leftover := groups[t.State]; leftover {
			items = append(items, listItem{t: t})
		}
	}
	return items
}

// taskCount reports how many items are real tasks (not section headings).
func taskCount(items []list.Item) int {
	n := 0
	for _, it := range items {
		if _, ok := it.(listItem); ok {
			n++
		}
	}
	return n
}

// moveOffHeading nudges the selection onto the nearest task when it sits
// on a section heading, preferring direction dir (+1 down, -1 up).
func moveOffHeading(m *list.Model, dir int) {
	items := m.VisibleItems()
	idx := m.Index()
	if idx < 0 || idx >= len(items) {
		return
	}
	if _, heading := items[idx].(sectionItem); !heading {
		return
	}
	for _, d := range []int{dir, -dir} {
		for j := idx + d; 0 <= j && j < len(items); j += d {
			if _, heading := items[j].(sectionItem); !heading {
				m.Select(j)
				return
			}
		}
	}
}

// taskDelegate renders one task per line: cursor, id, state badge, title,
// then dimmed project/due markers.
type taskDelegate struct {
	styles Styles
}

func (d taskDelegate) Height() int  { return 1 }
func (d taskDelegate) Spacing() int { return 0 }

// Update runs after the list has handled navigation; if the cursor landed
// on a section heading, keep it moving in the direction of travel.
func (d taskDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd {
	dir := 1
	if k, ok := msg.(tea.KeyPressMsg); ok &&
		(key.Matches(k, m.KeyMap.CursorUp) || key.Matches(k, m.KeyMap.PrevPage)) {
		dir = -1
	}
	moveOffHeading(m, dir)
	return nil
}

func (d taskDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	if sec, ok := item.(sectionItem); ok {
		heading := fmt.Sprintf("%s %s",
			d.styles.State[sec.state].Render(strings.ToUpper(string(sec.state))),
			d.styles.Dimmed.Render(fmt.Sprintf("(%d)", sec.count)),
		)
		fmt.Fprint(w, lipgloss.NewStyle().MaxWidth(m.Width()).Render(heading))
		return
	}
	it, ok := item.(listItem)
	if !ok {
		return
	}
	t := it.t

	cursor := "  "
	titleStyle := d.styles.Normal
	if index == m.Index() {
		cursor = d.styles.Cursor.Render("> ")
		titleStyle = d.styles.Selected
	}

	title := t.Title
	if t.ParentID != nil {
		title = "↳ " + title
	}

	var meta strings.Builder
	if t.Project != nil {
		meta.WriteString(" @" + *t.Project)
	}
	if t.Due != nil {
		meta.WriteString(" due:" + *t.Due)
	}

	line := fmt.Sprintf("%s%s %s%s",
		cursor,
		d.styles.Dimmed.Render(fmt.Sprintf("%3d", t.ID)),
		titleStyle.Render(title),
		d.styles.Dimmed.Render(meta.String()),
	)
	fmt.Fprint(w, lipgloss.NewStyle().MaxWidth(m.Width()).Render(line))
}

// newTaskList builds a list component configured as a bare task list; the
// app draws its own header and help line.
func newTaskList(styles Styles) list.Model {
	l := list.New(nil, taskDelegate{styles: styles}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	return l
}
