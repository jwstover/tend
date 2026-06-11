package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// handleTriageKey processes the fast single-key mutations available while
// triaging the inbox. The third return reports whether the key was
// consumed; unconsumed keys fall through to list navigation.
func (a *app) handleTriageKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	t, ok := a.selected()
	if !ok {
		return *a, nil, false
	}

	if st, ok := a.stateForKey(msg); ok {
		return *a, a.setState(t, st), true
	}

	switch {
	case key.Matches(msg, a.keys.SetProject):
		cmd := a.openPrompt(promptProject, fmt.Sprintf("project for #%d (empty clears): ", t.ID), t.ID)
		return *a, cmd, true
	case key.Matches(msg, a.keys.SetDue):
		cmd := a.openPrompt(promptDue, fmt.Sprintf("due for #%d, YYYY-MM-DD (empty clears): ", t.ID), t.ID)
		return *a, cmd, true
	}
	return *a, nil, false
}
