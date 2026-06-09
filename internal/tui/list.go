package tui

import (
	"fmt"
	"io"
	"strings"

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

// taskDelegate renders one task per line: cursor, id, state badge, title,
// then dimmed project/due markers.
type taskDelegate struct {
	styles Styles
}

func (d taskDelegate) Height() int                               { return 1 }
func (d taskDelegate) Spacing() int                              { return 0 }
func (d taskDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }

func (d taskDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
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

	line := fmt.Sprintf("%s%s %s %s%s",
		cursor,
		d.styles.Dimmed.Render(fmt.Sprintf("%3d", t.ID)),
		d.styles.State[t.State].Render(fmt.Sprintf("%-7s", t.State)),
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
