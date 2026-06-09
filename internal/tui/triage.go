package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/td/internal/task"
)

// handleTriageKey processes the fast single-key mutations available while
// triaging the inbox. The third return reports whether the key was
// consumed; unconsumed keys fall through to list navigation.
func (a *app) handleTriageKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	t, ok := a.selected()
	if !ok {
		return *a, nil, false
	}

	setState := func(st task.State) tea.Cmd {
		return a.mutate(fmt.Sprintf("#%d → %s", t.ID, st), func() error {
			return a.store.SetState(a.ctx, t.ID, st)
		})
	}

	switch {
	case key.Matches(msg, a.keys.SetTodo):
		return *a, setState(task.StateTodo), true
	case key.Matches(msg, a.keys.SetDoing):
		return *a, setState(task.StateDoing), true
	case key.Matches(msg, a.keys.SetBlocked):
		return *a, setState(task.StateBlocked), true
	case key.Matches(msg, a.keys.SetDone):
		return *a, setState(task.StateDone), true
	case key.Matches(msg, a.keys.SetSomeday):
		return *a, setState(task.StateSomeday), true
	case key.Matches(msg, a.keys.SetProject):
		cmd := a.openPrompt(promptProject, fmt.Sprintf("project for #%d (empty clears): ", t.ID), t.ID)
		return *a, cmd, true
	case key.Matches(msg, a.keys.SetDue):
		cmd := a.openPrompt(promptDue, fmt.Sprintf("due for #%d, YYYY-MM-DD (empty clears): ", t.ID), t.ID)
		return *a, cmd, true
	}
	return *a, nil, false
}
