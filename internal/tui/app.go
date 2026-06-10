// Package tui is the Bubble Tea presentation layer. It consumes the
// persistence layer through the Store interface below; all side effects
// run as tea.Cmds so Update stays pure.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/td/internal/task"
)

// Store is the slice of the persistence layer the TUI needs.
type Store interface {
	AddTask(ctx context.Context, title string) (task.Task, error)
	AddChild(ctx context.Context, parentID int64, title string) (task.Task, error)
	ListLive(ctx context.Context) ([]task.Task, error)
	ListInbox(ctx context.Context) ([]task.Task, error)
	ListChildren(ctx context.Context, parentID int64) ([]task.Task, error)
	SetState(ctx context.Context, id int64, st task.State) error
	SetProject(ctx context.Context, id int64, project *string) error
	SetDue(ctx context.Context, id int64, due *string) error
	SetBody(ctx context.Context, id int64, body string) error
}

// Run starts the TUI and blocks until it exits.
func Run(ctx context.Context, s Store) error {
	p := tea.NewProgram(newApp(ctx, s), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}
	return nil
}

type viewMode int

const (
	modeList viewMode = iota
	modeTriage
)

type promptKind int

const (
	promptNone promptKind = iota
	promptAdd
	promptChild
	promptProject
	promptDue
	promptPalette
)

// Messages produced by commands.
type (
	tasksLoadedMsg struct {
		mode  viewMode
		tasks []task.Task
	}
	childrenLoadedMsg struct {
		parentID int64
		children []task.Task
	}
	// refreshMsg signals a completed mutation: show status, reload.
	refreshMsg struct{ status string }
	statusMsg  string
	errMsg     struct{ err error }

	editorFinishedMsg struct {
		id   int64
		path string
		err  error
	}
)

type app struct {
	ctx   context.Context
	store Store

	keys   keyMap
	styles Styles

	mode viewMode
	list list.Model

	width, height int

	showDetail bool
	detail     viewport.Model
	detailID   int64 // task currently rendered in the pane; 0 = none
	children   []task.Task
	renderer   *glamour.TermRenderer

	prompt       textinput.Model
	promptKind   promptKind
	promptTarget int64 // task the prompt acts on (project/due/sub-task)

	statePending bool // `c` pressed; next key picks the new state

	status      string
	statusIsErr bool
}

func newApp(ctx context.Context, s Store) app {
	styles := DefaultStyles()
	return app{
		ctx:    ctx,
		store:  s,
		keys:   defaultKeyMap(),
		styles: styles,
		mode:   modeList,
		list:   newTaskList(styles),
		detail: viewport.New(),
		prompt: textinput.New(),
	}
}

func (a app) Init() tea.Cmd {
	return a.loadTasks(a.mode)
}

func (a app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.resize()
		return a, a.syncDetail(true)

	case tea.KeyPressMsg:
		return a.handleKey(msg)

	case tasksLoadedMsg:
		if msg.mode != a.mode {
			return a, nil
		}
		items := toItems(msg.tasks)
		if msg.mode == modeList {
			items = toGroupedItems(msg.tasks)
		}
		cmd := a.list.SetItems(items)
		moveOffHeading(&a.list, 1)
		return a, tea.Batch(cmd, a.syncDetail(true))

	case childrenLoadedMsg:
		if t, ok := a.selected(); ok && t.ID == msg.parentID {
			a.children = msg.children
			a.detail.SetContent(renderDetail(t, a.children, a.renderer, a.styles))
		}
		return a, nil

	case refreshMsg:
		a.status, a.statusIsErr = msg.status, false
		return a, a.loadTasks(a.mode)

	case statusMsg:
		a.status, a.statusIsErr = string(msg), false
		return a, nil

	case errMsg:
		a.status, a.statusIsErr = msg.err.Error(), true
		return a, nil

	case editorFinishedMsg:
		if msg.err != nil {
			os.Remove(msg.path)
			a.status, a.statusIsErr = "editor: "+msg.err.Error(), true
			return a, nil
		}
		return a, a.saveBody(msg.id, msg.path)
	}

	// Everything else (cursor blinks, mouse, paste) flows to the active
	// components.
	var cmds []tea.Cmd
	var cmd tea.Cmd
	if a.promptKind != promptNone {
		a.prompt, cmd = a.prompt.Update(msg)
		cmds = append(cmds, cmd)
	}
	a.list, cmd = a.list.Update(msg)
	cmds = append(cmds, cmd)
	a.detail, cmd = a.detail.Update(msg)
	cmds = append(cmds, cmd)
	return a, tea.Batch(cmds...)
}

func (a app) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	a.status = ""

	// An open prompt swallows all keys.
	if a.promptKind != promptNone {
		switch msg.String() {
		case "esc":
			a.closePrompt()
			return a, nil
		case "enter":
			return a.submitPrompt()
		default:
			var cmd tea.Cmd
			a.prompt, cmd = a.prompt.Update(msg)
			return a, cmd
		}
	}

	// While typing a `/` filter, the list owns the keyboard.
	if a.list.SettingFilter() {
		var cmd tea.Cmd
		a.list, cmd = a.list.Update(msg)
		return a, cmd
	}

	// A pending `c` chord consumes the next key: a state key applies it,
	// anything else cancels.
	if a.statePending {
		a.statePending = false
		if st, ok := a.stateForKey(msg); ok {
			if t, selected := a.selected(); selected {
				return a, a.setState(t, st)
			}
		}
		return a, nil
	}

	switch {
	case key.Matches(msg, a.keys.Quit):
		return a, tea.Quit

	case key.Matches(msg, a.keys.Back):
		if a.list.FilterState() != list.Unfiltered {
			var cmd tea.Cmd
			a.list, cmd = a.list.Update(msg) // clears the filter
			return a, cmd
		}
		if a.showDetail {
			a.showDetail = false
			a.resize()
			return a, nil
		}
		if a.mode == modeTriage {
			a.mode = modeList
			return a, a.loadTasks(modeList)
		}
		return a, nil

	case key.Matches(msg, a.keys.Triage):
		if a.mode == modeTriage {
			a.mode = modeList
		} else {
			a.mode = modeTriage
		}
		return a, a.loadTasks(a.mode)

	case key.Matches(msg, a.keys.ToggleDetail):
		a.showDetail = !a.showDetail
		a.resize()
		if a.showDetail {
			return a, a.syncDetail(true)
		}
		return a, nil

	case key.Matches(msg, a.keys.QuickAdd):
		return a, a.openPrompt(promptAdd, "add: ", 0)

	case key.Matches(msg, a.keys.AddSub):
		if t, ok := a.selected(); ok {
			return a, a.openPrompt(promptChild, fmt.Sprintf("sub-task of #%d: ", t.ID), t.ID)
		}
		return a, nil

	case key.Matches(msg, a.keys.Palette):
		return a, a.openPrompt(promptPalette, ": ", 0)

	case key.Matches(msg, a.keys.ChangeState):
		if _, ok := a.selected(); ok {
			a.statePending = true
		}
		return a, nil

	case key.Matches(msg, a.keys.EditBody):
		if t, ok := a.selected(); ok {
			return a, editBodyCmd(t)
		}
		return a, nil

	case key.Matches(msg, a.keys.OpenURL):
		if t, ok := a.selected(); ok {
			if urls := extractURLs(t.BodyMD); len(urls) > 0 {
				return a, openURLCmd(urls[0])
			}
			a.status = "no links in body"
		}
		return a, nil

	case key.Matches(msg, a.keys.OpenAllURLs):
		if t, ok := a.selected(); ok {
			urls := extractURLs(t.BodyMD)
			if len(urls) == 0 {
				a.status = "no links in body"
				return a, nil
			}
			cmds := make([]tea.Cmd, len(urls))
			for i, u := range urls {
				cmds[i] = openURLCmd(u)
			}
			return a, tea.Batch(cmds...)
		}
		return a, nil
	}

	if a.mode == modeTriage {
		if model, cmd, handled := a.handleTriageKey(msg); handled {
			return model, cmd
		}
	}

	// Let the list handle navigation (j/k, g/G, /, paging).
	var cmd tea.Cmd
	a.list, cmd = a.list.Update(msg)
	return a, tea.Batch(cmd, a.syncDetail(false))
}

// stateForKey maps a state-mutation key to its workflow state.
func (a app) stateForKey(msg tea.KeyPressMsg) (task.State, bool) {
	switch {
	case key.Matches(msg, a.keys.SetTodo):
		return task.StateTodo, true
	case key.Matches(msg, a.keys.SetDoing):
		return task.StateDoing, true
	case key.Matches(msg, a.keys.SetBlocked):
		return task.StateBlocked, true
	case key.Matches(msg, a.keys.SetDone):
		return task.StateDone, true
	case key.Matches(msg, a.keys.SetSomeday):
		return task.StateSomeday, true
	}
	return "", false
}

func (a app) setState(t task.Task, st task.State) tea.Cmd {
	return a.mutate(fmt.Sprintf("#%d → %s", t.ID, st), func() error {
		return a.store.SetState(a.ctx, t.ID, st)
	})
}

// selected returns the task under the cursor.
func (a app) selected() (task.Task, bool) {
	it, ok := a.list.SelectedItem().(listItem)
	if !ok {
		return task.Task{}, false
	}
	return it.t, true
}

// syncDetail reloads the detail pane for the current selection. With
// force, it reloads even if the same task is already shown (after a
// mutation or resize).
func (a *app) syncDetail(force bool) tea.Cmd {
	if !a.showDetail {
		return nil
	}
	t, ok := a.selected()
	if !ok {
		a.detailID = 0
		a.detail.SetContent(a.styles.Dimmed.Render("nothing selected"))
		return nil
	}
	if !force && t.ID == a.detailID {
		return nil
	}
	a.detailID = t.ID
	return a.loadChildren(t.ID)
}

func (a *app) resize() {
	const headerHeight, footerHeight = 1, 1
	bodyHeight := max(a.height-headerHeight-footerHeight, 1)
	listWidth := a.width
	if a.showDetail {
		listWidth = a.width / 2
		detailWidth := max(a.width-listWidth-3, 10) // divider + padding
		a.detail.SetWidth(detailWidth)
		a.detail.SetHeight(bodyHeight)
		a.renderer, _ = newBodyRenderer(detailWidth - 2)
	}
	a.list.SetSize(listWidth, bodyHeight)
}

// --- prompt ---

func (a *app) openPrompt(kind promptKind, label string, target int64) tea.Cmd {
	a.promptKind = kind
	a.promptTarget = target
	a.prompt.Reset()
	a.prompt.Prompt = label
	return a.prompt.Focus()
}

func (a *app) closePrompt() {
	a.promptKind = promptNone
	a.promptTarget = 0
	a.prompt.Reset()
	a.prompt.Blur()
}

func (a app) submitPrompt() (tea.Model, tea.Cmd) {
	kind, target := a.promptKind, a.promptTarget
	value := strings.TrimSpace(a.prompt.Value())
	a.closePrompt()

	switch kind {
	case promptAdd:
		if value == "" {
			return a, nil
		}
		return a, a.mutate("added: "+value, func() error {
			_, err := a.store.AddTask(a.ctx, value)
			return err
		})
	case promptChild:
		if value == "" {
			return a, nil
		}
		return a, a.mutate("added sub-task: "+value, func() error {
			_, err := a.store.AddChild(a.ctx, target, value)
			return err
		})
	case promptProject:
		var p *string
		status := "project cleared"
		if value != "" {
			p = &value
			status = "project → " + value
		}
		return a, a.mutate(status, func() error {
			return a.store.SetProject(a.ctx, target, p)
		})
	case promptDue:
		var d *string
		status := "due cleared"
		if value != "" {
			d = &value
			status = "due → " + value
		}
		return a, a.mutate(status, func() error {
			return a.store.SetDue(a.ctx, target, d)
		})
	case promptPalette:
		return a.runPaletteCommand(value)
	}
	return a, nil
}

// runPaletteCommand executes a `:` command.
// TODO(owner): fuzzy-matched palette with suggestions.
func (a app) runPaletteCommand(input string) (tea.Model, tea.Cmd) {
	name, rest, _ := strings.Cut(input, " ")
	switch name {
	case "":
		return a, nil
	case "q", "quit":
		return a, tea.Quit
	case "triage", "inbox":
		a.mode = modeTriage
		return a, a.loadTasks(modeTriage)
	case "list", "tasks":
		a.mode = modeList
		return a, a.loadTasks(modeList)
	case "add", "a":
		if rest == "" {
			return a, nil
		}
		return a, a.mutate("added: "+rest, func() error {
			_, err := a.store.AddTask(a.ctx, rest)
			return err
		})
	default:
		a.status, a.statusIsErr = "unknown command: "+name, true
		return a, nil
	}
}

// --- commands (all store I/O happens here, off the update loop) ---

func (a app) loadTasks(mode viewMode) tea.Cmd {
	return func() tea.Msg {
		var (
			tasks []task.Task
			err   error
		)
		if mode == modeTriage {
			tasks, err = a.store.ListInbox(a.ctx)
		} else {
			tasks, err = a.store.ListLive(a.ctx)
		}
		if err != nil {
			return errMsg{err}
		}
		return tasksLoadedMsg{mode: mode, tasks: tasks}
	}
}

func (a app) loadChildren(parentID int64) tea.Cmd {
	return func() tea.Msg {
		children, err := a.store.ListChildren(a.ctx, parentID)
		if err != nil {
			return errMsg{err}
		}
		return childrenLoadedMsg{parentID: parentID, children: children}
	}
}

// mutate wraps a store mutation: run it, then report status and refresh.
func (a app) mutate(status string, fn func() error) tea.Cmd {
	return func() tea.Msg {
		if err := fn(); err != nil {
			return errMsg{err}
		}
		return refreshMsg{status: status}
	}
}

func (a app) saveBody(id int64, path string) tea.Cmd {
	return func() tea.Msg {
		defer os.Remove(path)
		b, err := os.ReadFile(path)
		if err != nil {
			return errMsg{fmt.Errorf("reading edited body: %w", err)}
		}
		if err := a.store.SetBody(a.ctx, id, string(b)); err != nil {
			return errMsg{err}
		}
		return refreshMsg{status: "body saved"}
	}
}

// --- view ---

func (a app) View() tea.View {
	n := taskCount(a.list.Items())
	label := fmt.Sprintf("tasks (%d)", n)
	if a.mode == modeTriage {
		label = fmt.Sprintf("triage — inbox (%d)", n)
	}
	header := a.styles.Header.Render("td") + a.styles.HeaderAccent.Render("· "+label)

	body := a.list.View()
	if a.showDetail {
		body = lipgloss.JoinHorizontal(lipgloss.Top, body,
			a.styles.DetailBorder.Render(a.detail.View()))
	}

	v := tea.NewView(header + "\n" + body + "\n" + a.footer())
	v.AltScreen = true
	v.WindowTitle = "td"
	return v
}

func (a app) footer() string {
	if a.promptKind != promptNone {
		return a.styles.PromptLabel.Render("") + a.prompt.View()
	}
	if a.statePending {
		return a.styles.Status.Render("state → t todo · d doing · b blocked · x done · s someday · esc cancel")
	}
	if a.status != "" {
		if a.statusIsErr {
			return a.styles.Error.Render(a.status)
		}
		return a.styles.Status.Render(a.status)
	}
	help := "j/k nav · / search · n add · a sub-task · c state · ]/enter detail · e edit · o links · i triage · : palette · q quit"
	if a.mode == modeTriage {
		help = "t todo · d doing · b blocked · x done · s someday · p project · u due · e edit · esc back"
	}
	return a.styles.Help.Render(help)
}
