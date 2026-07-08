package tui

import "charm.land/bubbles/v2/key"

// keyMap holds every binding the app handles itself; list navigation
// (j/k, g/G, /, paging) is the bubbles list component's own keymap.
type keyMap struct {
	Quit         key.Binding
	Back         key.Binding
	Cancel       key.Binding
	ToggleDetail key.Binding
	Triage       key.Binding
	QuickAdd     key.Binding
	AddSub       key.Binding
	Palette      key.Binding
	Help         key.Binding
	EditBody     key.Binding
	LogEntry     key.Binding
	OpenURL      key.Binding
	OpenAllURLs  key.Binding
	ChangeState  key.Binding
	Delete       key.Binding // first `d` of the `dd` delete chord

	// Tree expansion in the list view.
	ExpandToggle key.Binding // ⏎/Tab flips a branch (⏎ falls back to detail on leaves)
	ExpandOpen   key.Binding
	ExpandClose  key.Binding
	ToggleDone   key.Binding // x/space on the selected node

	ChangePriority key.Binding

	// State mutations: single keys in triage mode, the second key of the
	// `c` chord everywhere else.
	SetTodo    key.Binding
	SetDoing   key.Binding
	SetBlocked key.Binding
	SetDone    key.Binding
	SetSomeday key.Binding
	SetProject key.Binding
	SetDue     key.Binding

	// Priority mutations: the second key of the `p` chord.
	PriorityA    key.Binding
	PriorityB    key.Binding
	PriorityC    key.Binding
	PriorityD    key.Binding
	PriorityNone key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Quit:         key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Back:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Cancel:       key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		ToggleDetail: key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "detail")),
		Triage:       key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "triage")),
		QuickAdd:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "add")),
		AddSub:       key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "sub-task")),
		Palette:      key.NewBinding(key.WithKeys(":", "ctrl+p"), key.WithHelp(":", "palette")),
		Help:         key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		EditBody:     key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit body")),
		LogEntry:     key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "log entry")),
		OpenURL:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open link(s)")),
		OpenAllURLs:  key.NewBinding(key.WithKeys("O"), key.WithHelp("O", "open all links")),
		ChangeState:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "change state")),
		Delete:       key.NewBinding(key.WithKeys("d"), key.WithHelp("dd", "delete")),

		ExpandToggle: key.NewBinding(key.WithKeys("enter", "tab"), key.WithHelp("⏎", "expand")),
		ExpandOpen:   key.NewBinding(key.WithKeys("l", "right"), key.WithHelp("l", "expand")),
		ExpandClose:  key.NewBinding(key.WithKeys("h", "left"), key.WithHelp("h", "collapse")),
		ToggleDone:   key.NewBinding(key.WithKeys("x", "space"), key.WithHelp("x", "done")),

		ChangePriority: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "priority")),

		SetTodo:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "todo")),
		SetDoing:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "doing")),
		SetBlocked: key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "blocked")),
		SetDone:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "done")),
		SetSomeday: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "someday")),
		SetProject: key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "project")),
		SetDue:     key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "due")),

		PriorityA:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "A (highest)")),
		PriorityB:    key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "B")),
		PriorityC:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "C")),
		PriorityD:    key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "D")),
		PriorityNone: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "none")),
	}
}
