package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/task"
)

// confirmation is an operation waiting on a `y`: archiving or deleting a
// project, or deleting a task. Each is reached by keys that are easy to hit
// by accident (`A` is shift-`a`, `dd` is muscle memory) and is costly to
// get wrong, so none runs until a panel has named its target and `y` has
// agreed.
type confirmation struct {
	title string // panel title, naming the target
	desc  string // what `y` does, in the panel's key row
	// run builds the mutation `y` fires. It is deferred rather than a
	// ready tea.Cmd because building some of them (deleteTask) drops
	// session caches, which must not happen for a cancelled confirmation.
	run func(a app) tea.Cmd
}

// handleConfirmKey consumes the key after a confirmation was armed: `y`
// runs the operation it names, anything else cancels. Reports false when
// no confirmation is pending.
func (a *app) handleConfirmKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if a.confirm == nil {
		return nil, false
	}
	c := *a.confirm
	a.confirm = nil
	a.resize()
	if msg.String() == "y" {
		return c.run(*a), true
	}
	return nil, true
}

// confirmPanel renders the which-key panel for a pending confirmation:
// `y` goes ahead, anything else cancels.
func (a app) confirmPanel() string {
	if a.confirm == nil {
		return ""
	}
	entries := []panelEntry{
		{key: "y", desc: a.confirm.desc, keyStyle: a.styles.Error},
		{key: "esc", desc: "cancel", keyStyle: a.styles.Dimmed},
	}
	return renderKeyPanel(a.styles, a.width, a.confirm.title, entries)
}

// armTaskDelete asks before deleting t. The panel names the task and, for
// a parent, how many sub-tasks cascade away with it, so a `dd` that
// landed on the wrong row is caught before anything is lost.
func (a *app) armTaskDelete(t task.Task) {
	desc := "delete"
	if t.ParentID == nil {
		switch n := a.counts[t.ID].Total; n {
		case 0:
		case 1:
			desc = "delete, with its 1 sub-task"
		default:
			desc = fmt.Sprintf("delete, with its %d sub-tasks", n)
		}
	}
	a.confirm = &confirmation{
		title: fmt.Sprintf("delete #%d %s?", t.ID, t.Title),
		desc:  desc,
		run:   func(a app) tea.Cmd { return a.deleteTask(t) },
	}
	a.resize()
}
