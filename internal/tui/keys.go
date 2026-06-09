package tui

import "charm.land/bubbles/v2/key"

// keyMap holds every binding the app handles itself; list navigation
// (j/k, g/G, /, paging) is the bubbles list component's own keymap.
type keyMap struct {
	Quit         key.Binding
	Back         key.Binding
	ToggleDetail key.Binding
	Triage       key.Binding
	QuickAdd     key.Binding
	AddSub       key.Binding
	Palette      key.Binding
	EditBody     key.Binding
	OpenURL      key.Binding
	OpenAllURLs  key.Binding

	// Triage-mode state mutations.
	SetTodo    key.Binding
	SetDoing   key.Binding
	SetBlocked key.Binding
	SetDone    key.Binding
	SetSomeday key.Binding
	SetProject key.Binding
	SetDue     key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Quit:         key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Back:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		ToggleDetail: key.NewBinding(key.WithKeys("]", "enter"), key.WithHelp("]", "detail")),
		Triage:       key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "triage")),
		QuickAdd:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "add")),
		AddSub:       key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "sub-task")),
		Palette:      key.NewBinding(key.WithKeys(":", "ctrl+p"), key.WithHelp(":", "palette")),
		EditBody:     key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit body")),
		OpenURL:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open link")),
		OpenAllURLs:  key.NewBinding(key.WithKeys("O"), key.WithHelp("O", "open all links")),

		SetTodo:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "todo")),
		SetDoing:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "doing")),
		SetBlocked: key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "blocked")),
		SetDone:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "done")),
		SetSomeday: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "someday")),
		SetProject: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "project")),
		SetDue:     key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "due")),
	}
}
