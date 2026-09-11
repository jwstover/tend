package tui

import "charm.land/bubbles/v2/key"

// keyMap holds every binding the app handles itself; list navigation
// (j/k, g/G, /, paging) is the bubbles list component's own keymap.
type keyMap struct {
	Quit           key.Binding
	Back           key.Binding
	Cancel         key.Binding
	ToggleDetail   key.Binding
	ToggleProjects key.Binding
	Triage         key.Binding
	Standup        key.Binding
	QuickAdd       key.Binding
	AddSub         key.Binding
	Rename         key.Binding
	Palette        key.Binding
	Help           key.Binding
	EditBody       key.Binding
	Sessions       key.Binding // launch/resume a Claude Code session on the selected task
	Agents         key.Binding // open the agents view: every session in the project (agents.go)
	RunWorkflow    key.Binding // run a workflow on the selected task (workflowrun.go)
	ViewRun        key.Binding // watch the selected task's workflow run (runview.go)
	LogEntry       key.Binding // note attached to the selected task
	Note           key.Binding // freestanding standup note, from anywhere
	Yank           key.Binding // copy the standup markdown (standup view)
	SortToggle     key.Binding // flip grouped/chronological notes (standup view)
	ToggleRecaps   key.Binding // hide/show Claude session recap notes (standup view)
	OpenURL        key.Binding
	OpenAllURLs    key.Binding
	ChangeState    key.Binding
	Delete         key.Binding // first `d` of the `dd` delete chord
	MoveProject    key.Binding // move the selected task to another project
	MoveParent     key.Binding // move the selected task under another task, or to the top level
	Archive        key.Binding // archive/restore the selected project
	ProjectCwd     key.Binding // set the selected project's default cwd for new sessions

	// Workflows authoring view (workflows.go). `n`, `R`, `e` and `dd`
	// reuse QuickAdd, Rename, EditBody and Delete there; in the edges
	// pane `n`, `e` and `dd` add, edit and delete an edge.
	Workflows      key.Binding // open the view
	Duplicate      key.Binding // copy the selected workflow under a new name
	StepModel      key.Binding // model picker for the selected step
	StepPermission key.Binding // permission-mode picker for the selected step
	StepKind       key.Binding // flip the selected step between agent and gate
	StepDown       key.Binding // move the selected step later in the order
	StepUp         key.Binding // move the selected step earlier in the order
	Validate       key.Binding // check the selected workflow's graph and prompts

	// Run view (runview.go). j/k, tab, g/G, esc reuse the shared bindings;
	// these are the run controls, each a write the CLI could make too.
	CancelRun key.Binding // `cc` chord: write cancelled; the runner kills the step
	PauseRun  key.Binding // pause a live run / resume a paused one
	Approve   key.Binding // approve the gate the run is waiting at
	Reject    key.Binding // reject it, with feedback for the step it loops back to
	Outcome   key.Binding // pick any of the gate's edge outcomes
	Takeover  key.Binding // pause the run and resume the current step's session interactively (takeover.go)
	RawLog    key.Binding // `v` (verbose): raw stream-json instead of the rendering

	// Tree expansion in the list view.
	ExpandToggle key.Binding // ⏎/Tab flips a branch (⏎ falls back to detail on leaves)
	ExpandOpen   key.Binding
	ExpandClose  key.Binding
	ToggleDone   key.Binding // x/space on the selected node

	// Pane scrolling (standup view).
	ScrollUp   key.Binding
	ScrollDown key.Binding
	PageUp     key.Binding
	PageDown   key.Binding

	ToggleCompleted key.Binding // C shows/hides the completed (done) and someday sections

	// List grouping: `g` opens the chord, the second key picks the grouping.
	// `gg` keeps its vim meaning (top of the list) as the chord's fourth key.
	GroupBy         key.Binding
	GroupByState    key.Binding
	GroupByPriority key.Binding
	GroupByAgent    key.Binding
	GoTop           key.Binding

	ChangePriority key.Binding

	// State mutations: single keys in triage mode, the second key of the
	// `c` chord everywhere else.
	SetTodo    key.Binding
	SetDoing   key.Binding
	SetReview  key.Binding // v: `r` is taken by sessions in the list, and the triage grid shares these keys
	SetBlocked key.Binding
	SetDone    key.Binding
	SetSomeday key.Binding
	SetTags    key.Binding
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
		Quit:           key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Back:           key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Cancel:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		ToggleDetail:   key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "detail")),
		ToggleProjects: key.NewBinding(key.WithKeys("["), key.WithHelp("[", "projects")),
		Triage:         key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "triage")),
		Standup:        key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "standup")),
		QuickAdd:       key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "add")),
		AddSub:         key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "sub-task")),
		Rename:         key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "rename")),
		Palette:        key.NewBinding(key.WithKeys(":", "ctrl+p"), key.WithHelp(":", "palette")),
		Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		EditBody:       key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit body")),
		Sessions:       key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "sessions")),
		// `A` is archive only while the projects column is focused, which
		// claims its keys first; from the task list it is free, and it is
		// the one letter the view's name starts with.
		Agents:       key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "agents")),
		RunWorkflow:  key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "run workflow")),
		ViewRun:      key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "watch run")),
		LogEntry:     key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "note on task")),
		Note:         key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "note")),
		Yank:         key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "yank standup")),
		SortToggle:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
		ToggleRecaps: key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "recaps")),
		OpenURL:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open link(s)")),
		OpenAllURLs:  key.NewBinding(key.WithKeys("O"), key.WithHelp("O", "open all links")),
		ChangeState:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "change state")),
		Delete:       key.NewBinding(key.WithKeys("d"), key.WithHelp("dd", "delete")),
		MoveProject:  key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "move to project")),
		MoveParent:   key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "move to parent")),
		Archive:      key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "archive")),
		ProjectCwd:   key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "default cwd")),

		Workflows:      key.NewBinding(key.WithKeys("W"), key.WithHelp("W", "workflows")),
		Duplicate:      key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "duplicate")),
		StepModel:      key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "model")),
		StepPermission: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "permission mode")),
		// `t` (type) rather than the `k` the task sketch named: `k` is
		// "up" in every pane of this app, and `J`/`K` reorder right here.
		StepKind: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "agent / gate")),
		StepDown: key.NewBinding(key.WithKeys("J"), key.WithHelp("J", "move down")),
		StepUp:   key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "move up")),
		Validate: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "validate")),

		CancelRun: key.NewBinding(key.WithKeys("c"), key.WithHelp("cc", "cancel run")),
		PauseRun:  key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause / resume")),
		Approve:   key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "approve gate")),
		Reject:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "reject gate")),
		Outcome:   key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "pick gate outcome")),
		Takeover:  key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "take over step")),
		// `v` for verbose: `l` is "pane to the right" everywhere in the app,
		// so it cannot double as a toggle in a log pane.
		RawLog: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "raw log")),

		ExpandToggle: key.NewBinding(key.WithKeys("enter", "tab"), key.WithHelp("⏎", "expand")),
		ExpandOpen:   key.NewBinding(key.WithKeys("l", "right"), key.WithHelp("l", "expand")),
		ExpandClose:  key.NewBinding(key.WithKeys("h", "left"), key.WithHelp("h", "collapse")),
		ToggleDone:   key.NewBinding(key.WithKeys("x", "space"), key.WithHelp("x", "done")),

		ScrollUp:   key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("k", "scroll up")),
		ScrollDown: key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("j", "scroll down")),
		PageUp:     key.NewBinding(key.WithKeys("pgup", "ctrl+u"), key.WithHelp("pgup", "page up")),
		PageDown:   key.NewBinding(key.WithKeys("pgdown", "ctrl+d"), key.WithHelp("pgdown", "page down")),

		ToggleCompleted: key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "completed")),

		GroupBy:         key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "group by")),
		GroupByState:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "state")),
		GroupByPriority: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "priority")),
		GroupByAgent:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "agent status")),
		GoTop:           key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "top of list")),

		ChangePriority: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "priority")),

		SetTodo:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "todo")),
		SetDoing:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "doing")),
		SetReview:  key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "in review")),
		SetBlocked: key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "blocked")),
		SetDone:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "done")),
		SetSomeday: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "someday")),
		SetTags:    key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "tags")),
		SetDue:     key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "due")),

		PriorityA:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "A (highest)")),
		PriorityB:    key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "B")),
		PriorityC:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "C")),
		PriorityD:    key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "D")),
		PriorityNone: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "none")),
	}
}
