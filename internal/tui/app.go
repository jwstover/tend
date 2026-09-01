// Package tui is the Bubble Tea presentation layer. It consumes the
// persistence layer through the Store interface below; all side effects
// run as tea.Cmds so Update stays pure.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/jira"
	"github.com/jwstover/tend/internal/task"
)

// Store is the slice of the persistence layer the TUI needs.
type Store interface {
	AddTaskIn(ctx context.Context, projectID int64, title string) (task.Task, error)
	AddTaskWithBodyIn(ctx context.Context, projectID int64, title, body string) (task.Task, error)
	AddChild(ctx context.Context, parentID int64, title string) (task.Task, error)
	AddLogEntry(ctx context.Context, taskID *int64, body string) (task.LogEntry, error)
	ListLogEntries(ctx context.Context, from, to time.Time) ([]task.LogEntry, error)
	ListTaskLog(ctx context.Context, taskID int64) ([]task.LogEntry, error)
	ListEvents(ctx context.Context, from, to time.Time) ([]task.Event, error)
	ListLive(ctx context.Context, projectID *int64) ([]task.Task, error)
	ListLiveWithCompleted(ctx context.Context, projectID *int64) ([]task.Task, error)
	ListInbox(ctx context.Context, projectID *int64) ([]task.Task, error)
	ListChildren(ctx context.Context, parentID int64) ([]task.Task, error)
	ChildCounts(ctx context.Context) (map[int64]task.ChildCount, error)
	SessionStatuses(ctx context.Context) (map[int64]task.SessionStatus, error)
	CountInbox(ctx context.Context, projectID *int64) (int64, error)
	SetState(ctx context.Context, id int64, st task.State) error
	SetProject(ctx context.Context, taskID, projectID int64) error
	SetTags(ctx context.Context, taskID int64, tags []string) error
	TagsByTask(ctx context.Context) (map[int64][]string, error)
	TagsForTask(ctx context.Context, taskID int64) ([]string, error)
	ListProjects(ctx context.Context) ([]task.Project, error)
	CreateProject(ctx context.Context, name string) (task.Project, error)
	RenameProject(ctx context.Context, id int64, name string) error
	SetProjectArchived(ctx context.Context, id int64, archived bool) error
	DeleteProject(ctx context.Context, id int64) error
	ActiveProjectID(ctx context.Context) (int64, error)
	SetActiveProject(ctx context.Context, id int64) error
	SetPriority(ctx context.Context, id int64, p *int64) error
	SetDue(ctx context.Context, id int64, due *string) error
	SetTitle(ctx context.Context, id int64, title string) error
	SetBody(ctx context.Context, id int64, body string) error
	DeleteTask(ctx context.Context, id int64) error
	CreateSession(ctx context.Context, taskID int64, externalID, cwd, label, tmuxSession string) (task.Session, error)
	ListSessionsForTask(ctx context.Context, taskID int64) ([]task.Session, error)
	TouchSession(ctx context.Context, id int64) error
	UpdateSessionLabel(ctx context.Context, externalID, label string) error
	SetSessionNeedsRecap(ctx context.Context, externalID string, needs bool) error
	ListSessionsNeedingRecap(ctx context.Context) ([]task.Session, error)
	ClaimSessionRecap(ctx context.Context, externalID string) (bool, error)
	SessionsWithTmux(ctx context.Context) ([]task.Session, error)
	SetSessionWorkingIfUnchanged(ctx context.Context, externalID string, prevStatusUpdatedAt time.Time) (bool, error)
	SetSessionIdleIfUnchanged(ctx context.Context, externalID string, prevStatusUpdatedAt time.Time) (bool, error)
}

// Run starts the TUI and blocks until it exits. dbPath is shown on the
// loading frame and also passed to launched/resumed sessions' --mcp-config
// (see agent.WriteMCPConfig) so `tend mcp` can open the same database.
//
// The session poller runs on its own goroutine (pollCtx), stopped when Run
// returns, rather than as a tea.Cmd driven by the Program's event loop: that
// loop is unavailable for the entire time any session is attached through
// tend (tea.ExecProcess pauses it to hand the terminal to the child
// process), which is exactly when a background session's status most needs
// to keep moving. See runSessionPoller.
func Run(ctx context.Context, s Store, dbPath string) error {
	pollCtx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()

	p := tea.NewProgram(newApp(ctx, s, dbPath), tea.WithContext(ctx))
	go runSessionPoller(pollCtx, s, p.Send)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}
	return nil
}

// runSessionPoller ticks pollSessions (sessions.go) on a fixed interval
// until ctx is canceled. send is Program.Send in production; tests inject a
// stub so this is drivable without a real terminal or Program.
//
// Deliberately not a tea.Cmd: a tea.Cmd only ever runs from within the
// Program's own event loop, and that loop is exactly what's paused for as
// long as any session is attached through tend (see Run). A goroutine
// outside that loop is the only way polling — and the status correction it
// does — keeps happening during the one span it matters most: while you're
// mid-conversation with a session and can't see any other task's state.
func runSessionPoller(ctx context.Context, store Store, send func(tea.Msg)) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if pollSessions(ctx, store, pollInterval) {
				send(sessionsPolledMsg{changed: true})
			}
		}
	}
}

type viewMode int

const (
	modeList viewMode = iota
	modeTriage
	modeStandup
)

// pane identifies which column owns the keyboard. It replaces an earlier
// detailFocused bool: with three columns, "focused" is no longer a yes/no
// question, and h/l move along this chain in both directions.
//
// The zero value is paneProjects, which is NOT the default focus -- newApp
// sets paneTasks explicitly.
type pane int

const (
	paneProjects pane = iota
	paneTasks
	paneDetail
)

type promptKind int

const (
	promptNone promptKind = iota
	promptAdd
	promptChild
	promptTags
	promptDue
	promptSessionCwd
	promptRename
	promptNewProject
	promptRenameProject
)

// flashKind picks the glyph + semantic color a footer flash leads with;
// flashPlain renders text only.
type flashKind int

const (
	flashPlain flashKind = iota
	flashDone            // ✓ complete green — done / state changes
	flashAdd             // ✚ inbox orange — captures
	flashEdit            // ✎ accent — body/metadata saves
	flashLink            // ↗ link — opened URLs
)

// flash is a footer status message: an optional semantic glyph and the
// text, rendered in fgDim. Errors keep the red error style. Flashes clear
// on the next keypress; no timer.
type flash struct {
	kind  flashKind
	text  string
	isErr bool
}

// Messages produced by commands.
type (
	tasksLoadedMsg struct {
		mode     viewMode
		tasks    []task.Task
		counts   map[int64]task.ChildCount
		tags     map[int64][]string
		sessions map[int64]task.SessionStatus
		inbox    int64
	}
	childrenLoadedMsg struct {
		parentID int64
		children []task.Task
		log      []task.LogEntry
		sessions []task.Session
	}
	standupLoadedMsg struct {
		notes  []task.LogEntry
		events []task.Event
		live   []task.Task
	}
	// projectsLoadedMsg carries the projects column's contents plus the
	// stored capture target.
	projectsLoadedMsg struct {
		projects []task.Project
		active   int64
	}
	// refreshMsg signals a completed mutation: show status, reload.
	refreshMsg struct{ status flash }
	statusMsg  flash
	errMsg     struct{ err error }

	editorFinishedMsg struct {
		id   int64
		path string
		err  error
	}

	// urlsResolvedMsg carries a task's collected links (body + log
	// entries); openAll skips the picker and opens every one.
	urlsResolvedMsg struct {
		urls    []link
		openAll bool
	}

	// sessionsForPickerMsg carries a freshly loaded session list for the
	// `r` picker (see sessions.go); label is the task title, snapshotted
	// for use as both the picker heading and a new session's -n arg.
	sessionsForPickerMsg struct {
		taskID   int64
		label    string
		sessions []task.Session
	}
	// sessionFinishedMsg reports a launched session's terminal handoff
	// returning; the store row is only written on a clean exit.
	// tmuxSession is the wrapping tmux session's name, "" when the
	// session ran without tmux. backgrounded distinguishes a detach from
	// a real exit — both are a clean return from tea.ExecProcess, so it's
	// resolved by asking tmux whether the session is still alive (see
	// launchSessionCmd).
	sessionFinishedMsg struct {
		taskID       int64
		externalID   string
		cwd          string
		label        string
		tmuxSession  string
		backgrounded bool
		err          error
	}
	// sessionResumedMsg reports a resumed session's terminal handoff
	// returning; last_active_at is only bumped on a clean exit. since is
	// the transcript's line count at the moment it was resumed (see
	// resumeSessionCmd), passed to recapSessionCmd to scope the recap to
	// only what happened after that point.
	sessionResumedMsg struct {
		sessionRowID int64
		taskID       int64
		cwd          string
		externalID   string
		since        int
		backgrounded bool
		err          error
	}
	// recapsDrainedMsg carries the backgrounded sessions this instance
	// successfully claimed the owed recap for (see drainRecapsCmd) —
	// already claimed in the store, so Update's job is only to fire the
	// recap calls and account for them against pendingRecaps.
	recapsDrainedMsg struct{ sessions []task.Session }
	// recapDoneMsg wraps whatever recapSessionCmd produces — including
	// nil, on a swallowed failure — so pendingRecaps can be decremented
	// on every completion, not just the ones that surface a Msg.
	recapDoneMsg struct{ inner tea.Msg }

	// sessionsPolledMsg reports whether pollSessions wrote a status change
	// for at least one session this tick. changed being false is the
	// common case (nothing to see, or a hook already won the race) and
	// deliberately triggers no reload — unlike refreshMsg, a background
	// poll finding nothing new isn't worth a flash or a re-fetch. Sent by
	// runSessionPoller's goroutine via Program.Send, not produced by a
	// tea.Cmd — it originates outside the event loop entirely, which is
	// the whole reason it can still run while a session is attached.
	sessionsPolledMsg struct{ changed bool }
)

// pollInterval is both how often runSessionPoller re-captures each live
// session's tmux pane, and pollSessions' floor before settling a
// non-working status to idle (see pollSessions in sessions.go) — long
// enough that a hook which was going to fire already would have. No
// backoff for an unchanged pane, since tmux calls are cheap at the
// session counts a single local user actually runs — simpler than the
// added bookkeeping a backoff would need.
const pollInterval = 4 * time.Second

type app struct {
	ctx   context.Context
	store Store

	keys   keyMap
	styles Styles

	mode viewMode
	list list.Model

	// Last list-mode load plus tree state; the flattened item slice is
	// rebuilt from these whenever any of them changes.
	tasks         []task.Task
	counts        map[int64]task.ChildCount
	tags          map[int64][]string           // tags per task, for the list row's #tag cell
	sessionStatus map[int64]task.SessionStatus // latest session status per task, for the list row

	// projectFilter scopes every task query: nil is the projects column's
	// All row, otherwise the selected project.
	projectFilter *int64

	// Projects column state. projectCursor indexes the rendered rows, so
	// 0 is the synthetic All row and a project sits at its index + 1.
	// activeProjectID is the capture target mirrored from the settings
	// table; it is not necessarily the selected row (All changes the
	// selection without changing where new tasks land).
	projects        []task.Project
	projectCursor   int
	activeProjectID int64
	showProjects    bool                      // `[` toggles the column; auto-hidden when narrow
	loadedProjects  bool                      // first projectsLoadedMsg arrived
	expanded        map[int64]bool            // branch disclosure, by task ID, session-scoped
	childCache      map[int64][]task.Task     // loaded children per parent
	logCache        map[int64][]task.LogEntry // loaded task notes, for the detail pane
	sessionsCache   map[int64][]task.Session  // loaded claude sessions per task, for the detail pane

	startCwd string // tend's own working directory at startup; the launch-prompt fallback

	width, height int
	bodyHeight    int   // rows between the chrome rules, set by resize
	inboxCount    int64 // tasks awaiting triage, for the header nudge

	// Triage session: the cards still to process (head = current) and how
	// many left the inbox since entering triage. Both reset on entry.
	triageQueue     []task.Task
	triageProcessed int

	// Standup session: the reporting-window start (local midnight) and
	// the loaded window data. The window always ends at now.
	standupSince  time.Time
	standupNotes  []task.LogEntry
	standupEvents []task.Event
	standupLive   []task.Task
	standupChrono bool // `s` flips the notes pane to chronological; grouped by task otherwise

	// standupHideRecaps hides auto-generated Claude session recap notes
	// (see recapNotePrefix) from the notes pane and the yanked markdown —
	// `C` toggles it. Persists across window/sort changes like
	// standupChrono, not reset per-view like the scroll/disclosure state
	// below.
	standupHideRecaps bool

	// Standup pane scrolling and per-task log disclosure. standupJumpToLatest
	// asks the next standupLoadedMsg to snap the scroll to the newest
	// content — set on entering the view or changing the window/sort, left
	// alone on a background refresh so reading position isn't yanked away.
	standupScroll       int
	standupCursor       int             // focused note-group index, grouped view only
	standupCollapsed    map[string]bool // collapsed note-group keys, see standupGroupKey
	standupJumpToLatest bool

	showDetail bool
	focus      pane // which column owns j/k and the scroll keys
	detail     viewport.Model
	detailID   int64 // task currently rendered in the pane; 0 = none
	renderer   *glamour.TermRenderer

	prompt       textinput.Model
	promptKind   promptKind
	promptTarget int64  // task the prompt acts on (tags/due/sub-task/session)
	sessionLabel string // pending session-cwd prompt's -n arg (task title)

	modal modal // centered floating input (log entries)

	// Session picker overlay: choose an existing session to resume, or
	// launch a new one, for a task.
	sessionPickerOpen     bool
	sessionPickerTaskID   int64
	sessionPickerLabel    string
	sessionPickerSessions []task.Session
	sessionPickerSel      int

	// Command palette overlay: a fuzzy-matched command list anchored just
	// above the footer.
	paletteOpen  bool
	paletteQuery string
	paletteSel   int

	// URL picker overlay: choose one link from a task with multiple links.
	// Project picker overlay: choose which project a task belongs to.
	projectPickerOpen   bool
	projectPickerTaskID int64
	projectPickerLabel  string
	projectPickerSel    int

	urlPickerOpen bool
	urlPickerURLs []link
	urlPickerSel  int

	helpOpen bool // `?` key-reference overlay

	showCompleted bool // C toggles whether completed (done) tasks are loaded

	statePending    bool // `c` pressed; next key picks the new state
	priorityPending bool // `p` pressed; next key picks the new priority
	deletePending   bool // first `d` pressed; a second `d` confirms the delete
	quitPending     bool // `q` pressed while a recap was still running; a second press confirms

	pendingRecaps int // in-flight recapSessionCmd calls; gates the quit confirmation

	loaded bool   // first tasksLoadedMsg arrived; until then, loading frame
	dbPath string // shown on the loading frame; "" hides the line

	status flash
}

func newApp(ctx context.Context, s Store, dbPath string) app {
	styles := DefaultStyles()
	wd, _ := os.Getwd() // best-effort; "" just falls through to an empty cwd prompt
	return app{
		ctx:             ctx,
		store:           s,
		dbPath:          dbPath,
		keys:            defaultKeyMap(),
		styles:          styles,
		mode:            modeList,
		focus:           paneTasks,
		showProjects:    true,
		activeProjectID: task.DefaultProjectID,
		list:            newTaskList(styles),
		expanded:        make(map[int64]bool),
		childCache:      make(map[int64][]task.Task),
		logCache:        make(map[int64][]task.LogEntry),
		sessionsCache:   make(map[int64][]task.Session),
		startCwd:        wd,
		detail:          viewport.New(),
		prompt:          textinput.New(),
		modal:           newModal(),
	}
}

func (a app) Init() tea.Cmd {
	// Startup is the one moment guaranteed to happen after a host
	// reboot, which is exactly when a session backgrounded before the
	// reboot is owed a recap nobody has settled (see drainRecapsCmd).
	return tea.Batch(a.loadTasks(a.mode), a.loadProjects(), a.drainRecapsCmd())
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
		a.loaded = true
		if msg.mode != a.mode {
			return a, nil
		}
		a.inboxCount = msg.inbox
		a.tags = msg.tags
		if msg.mode == modeTriage {
			// "Processed" means the current card left the inbox; skips and
			// metadata edits keep it in place across reloads.
			if len(a.triageQueue) > 0 && !hasTaskID(msg.tasks, a.triageQueue[0].ID) {
				a.triageProcessed++
			}
			a.triageQueue = mergeTriageQueue(a.triageQueue, msg.tasks)
			return a, nil
		}
		a.tasks, a.counts, a.sessionStatus = msg.tasks, msg.counts, msg.sessions
		// Stale-while-revalidate: rebuild from the cached children now,
		// then re-fetch every expanded branch.
		cmd := tea.Batch(a.rebuildList(), a.reloadExpanded())
		moveOffHeading(&a.list, 1)
		return a, tea.Batch(cmd, a.syncDetail(true))

	case projectsLoadedMsg:
		wantID, hadSelection := int64(0), false
		if p, ok := a.selectedProject(); ok {
			wantID, hadSelection = p.ID, true
		}
		a.projects = msg.projects
		a.activeProjectID = msg.active
		// First load: land on the stored capture target rather than All,
		// so the TUI opens where `tend add` has been putting things.
		if !a.loadedProjects {
			a.loadedProjects = true
			wantID, hadSelection = msg.active, msg.active != task.DefaultProjectID
		}
		a.syncProjectCursor(wantID, hadSelection)
		a.resize()
		return a, a.loadTasks(a.mode)

	case childrenLoadedMsg:
		a.childCache[msg.parentID] = msg.children
		a.logCache[msg.parentID] = msg.log
		a.sessionsCache[msg.parentID] = msg.sessions
		var cmd tea.Cmd
		if a.mode == modeList {
			sel, hadSel := a.selectedNode()
			cmd = a.rebuildList()
			if hadSel {
				a.selectByID(sel.t.ID)
			}
		}
		if a.showDetail && msg.parentID == a.detailID {
			if n, ok := a.selectedNode(); ok && n.t.ID == a.detailID {
				a.renderDetailFor(n.t)
			}
		}
		return a, cmd

	case urlsResolvedMsg:
		switch {
		case len(msg.urls) == 0:
			a.status = flash{text: "no links"}
			return a, nil
		case msg.openAll:
			cmds := make([]tea.Cmd, len(msg.urls))
			for i, u := range msg.urls {
				cmds[i] = openURLCmd(u.url)
			}
			return a, tea.Batch(cmds...)
		case len(msg.urls) == 1:
			return a, openURLCmd(msg.urls[0].url)
		default:
			a.openURLPicker(msg.urls)
			return a, nil
		}

	case sessionsForPickerMsg:
		return a, a.openSessionPicker(msg)

	case sessionFinishedMsg:
		if msg.err != nil {
			a.status = flash{text: "claude: " + msg.err.Error(), isErr: true}
			return a, nil
		}
		// Backgrounded: claude is still running under tmux, so the recap
		// is deliberately skipped — `claude -p --resume` against a live
		// session would put two processes on one session id and one
		// transcript file. The debt is recorded instead, for the
		// SessionEnd hook to settle later.
		if msg.backgrounded {
			return a, a.mutate(flash{kind: flashEdit, text: "session backgrounded"}, func() error {
				if _, err := a.store.CreateSession(a.ctx, msg.taskID, msg.externalID, msg.cwd, msg.label, msg.tmuxSession); err != nil {
					return err
				}
				return a.store.SetSessionNeedsRecap(a.ctx, msg.externalID, true)
			})
		}
		a.pendingRecaps++
		return a, tea.Batch(
			a.mutate(flash{kind: flashEdit, text: "session recorded"}, func() error {
				_, err := a.store.CreateSession(a.ctx, msg.taskID, msg.externalID, msg.cwd, msg.label, msg.tmuxSession)
				return err
			}),
			a.recapSessionCmd(msg.taskID, msg.cwd, msg.externalID, nil),
		)

	case sessionResumedMsg:
		if msg.err != nil {
			a.status = flash{text: "claude: " + msg.err.Error(), isErr: true}
			return a, nil
		}
		if msg.backgrounded {
			return a, a.mutate(flash{kind: flashEdit, text: "session backgrounded"}, func() error {
				if err := a.store.TouchSession(a.ctx, msg.sessionRowID); err != nil {
					return err
				}
				return a.store.SetSessionNeedsRecap(a.ctx, msg.externalID, true)
			})
		}
		a.pendingRecaps++
		return a, tea.Batch(
			a.mutate(flash{kind: flashEdit, text: "session resumed"}, func() error {
				return a.store.TouchSession(a.ctx, msg.sessionRowID)
			}),
			a.recapSessionCmd(msg.taskID, msg.cwd, msg.externalID, &msg.since),
		)

	case recapsDrainedMsg:
		if len(msg.sessions) == 0 {
			return a, nil
		}
		cmds := make([]tea.Cmd, 0, len(msg.sessions))
		for _, sess := range msg.sessions {
			a.pendingRecaps++
			// since is nil: the transcript marker a resume records lives
			// only in the memory of the instance that did the resuming,
			// so a drained recap is unscoped — still far better than no
			// recap at all.
			cmds = append(cmds, a.recapSessionCmd(sess.TaskID, sess.Cwd, sess.ExternalID, nil))
		}
		return a, tea.Batch(cmds...)

	case recapDoneMsg:
		a.pendingRecaps = max(a.pendingRecaps-1, 0)
		if msg.inner == nil {
			return a, nil
		}
		return a.Update(msg.inner)

	case sessionsPolledMsg:
		// Standup mode renders no session markers, so there's nothing
		// there worth reloading for — mirrors refreshMsg's same branch.
		if !msg.changed || a.mode == modeStandup {
			return a, nil
		}
		return a, a.loadTasks(a.mode)

	case standupLoadedMsg:
		if a.mode != modeStandup {
			return a, nil
		}
		a.standupNotes, a.standupEvents, a.standupLive = msg.notes, msg.events, msg.live
		if a.standupJumpToLatest {
			a.standupCursor = max(len(a.standupGroups())-1, 0)
			a.standupScroll = a.standupMaxScroll()
			a.standupJumpToLatest = false
		} else {
			a.standupScroll = min(a.standupScroll, a.standupMaxScroll())
			a.standupCursor = min(a.standupCursor, max(len(a.standupGroups())-1, 0))
		}
		return a, nil

	case refreshMsg:
		a.status = msg.status
		// Every mutation passes through here, including the one that
		// records a just-backgrounded session — so returning from a
		// session is also the moment a previously-owed recap gets
		// settled. The scan is a single indexed query that finds nothing
		// in the common case.
		if a.mode == modeStandup {
			return a, tea.Batch(a.loadStandup(), a.drainRecapsCmd())
		}
		return a, tea.Batch(a.loadTasks(a.mode), a.loadProjects(), a.drainRecapsCmd())

	case statusMsg:
		a.status = flash(msg)
		return a, nil

	case errMsg:
		a.loaded = true // an initial load failure shouldn't strand the loading frame
		a.status = flash{text: msg.err.Error(), isErr: true}
		return a, nil

	case editorFinishedMsg:
		if msg.err != nil {
			os.Remove(msg.path)
			a.status = flash{text: "editor: " + msg.err.Error(), isErr: true}
			return a, nil
		}
		return a, a.saveBody(msg.id, msg.path)
	}

	// Everything else (cursor blinks, mouse, paste) flows to the active
	// components.
	var cmds []tea.Cmd
	var cmd tea.Cmd
	if a.modal.Active() {
		a.modal, cmd = a.modal.Update(msg)
		cmds = append(cmds, cmd)
	}
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
	a.status = flash{}

	// An open URL picker swallows all keys.
	if a.urlPickerOpen {
		return a.handleURLPickerKey(msg)
	}

	// So does an open project picker.
	if a.projectPickerOpen {
		return a.handleProjectPickerKey(msg)
	}

	// An open session picker swallows all keys.
	if a.sessionPickerOpen {
		return a.handleSessionPickerKey(msg)
	}

	// An open palette swallows all keys.
	if a.paletteOpen {
		return a.handlePaletteKey(msg)
	}

	// The help overlay swallows all keys; a few of them close it.
	if a.helpOpen {
		switch msg.String() {
		case "esc", "?", "enter", "q":
			a.helpOpen = false
		}
		return a, nil
	}

	// An open modal swallows all keys.
	if a.modal.Active() {
		switch {
		case msg.String() == "esc":
			a.modal.Close()
			return a, nil
		case a.modal.IsSubmit(msg):
			return a.submitModal()
		default:
			var cmd tea.Cmd
			a.modal, cmd = a.modal.Update(msg)
			return a, cmd
		}
	}

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

	// The standup view owns the keyboard: there is no list beneath it to
	// navigate or mutate, so unhandled keys stop here.
	if a.mode == modeStandup {
		return a.handleStandupKey(msg)
	}

	// A pending `c` chord consumes the next key: a state key applies it,
	// anything else cancels.
	if a.statePending {
		a.statePending = false
		a.resize()
		if st, ok := a.stateForKey(msg); ok {
			if t, selected := a.selected(); selected {
				return a, a.setState(t, st)
			}
		}
		return a, nil
	}

	// A pending `p` chord consumes the next key: a priority key applies
	// it, anything else cancels.
	if a.priorityPending {
		a.priorityPending = false
		a.resize()
		if p, ok := a.priorityForKey(msg); ok {
			if t, selected := a.selected(); selected {
				return a, a.setPriority(t, p)
			}
		}
		return a, nil
	}

	// A pending `d` chord consumes the next key: a second `d` deletes the
	// selection, anything else cancels.
	if a.deletePending {
		a.deletePending = false
		a.resize()
		if key.Matches(msg, a.keys.Delete) {
			// The chord means "delete what is focused": a project in the
			// projects column, otherwise the selected task.
			if a.focus == paneProjects && a.mode == modeList {
				return a, a.deleteSelectedProject()
			}
			if t, selected := a.selected(); selected {
				return a, a.deleteTask(t)
			}
		}
		return a, nil
	}

	// A pending quit confirmation (shown because a session recap or
	// auto-name was still running in the background) consumes the next
	// key: `q`/ctrl+c quits anyway, anything else cancels.
	if a.quitPending {
		a.quitPending = false
		a.resize()
		if key.Matches(msg, a.keys.Quit) {
			return a, tea.Quit
		}
		return a, nil
	}

	// The projects column claims its own keys and lets everything else
	// fall through, so `q`, `:`, `?`, `S` and `i` still work from there.
	if a.focus == paneProjects && a.mode == modeList {
		if model, cmd, handled := a.handleProjectsKey(msg); handled {
			return model, cmd
		}
	}

	switch {
	case key.Matches(msg, a.keys.Quit):
		// ctrl+c always quits; `q` first backs out of any non-default
		// view (triage, the detail pane), then — from the bare list —
		// warns before dropping an in-flight recap/auto-name instead of
		// quitting outright.
		if msg.String() == "q" {
			if a.mode == modeTriage {
				a.mode = modeList
				return a, a.loadTasks(modeList)
			}
			if a.focus == paneDetail {
				a.focus = paneTasks
				return a, nil
			}
			if a.showDetail {
				a.showDetail = false
				a.resize()
				return a, nil
			}
			if a.pendingRecaps > 0 {
				a.quitPending = true
				a.resize()
				return a, nil
			}
		}
		return a, tea.Quit

	case key.Matches(msg, a.keys.Back):
		if a.list.FilterState() != list.Unfiltered {
			var cmd tea.Cmd
			a.list, cmd = a.list.Update(msg) // clears the filter
			return a, cmd
		}
		// Triage leaves before the detail toggle: the pane isn't visible
		// there, so closing it first would be an invisible esc.
		if a.mode == modeTriage {
			a.mode = modeList
			return a, a.loadTasks(modeList)
		}
		// Un-focus the pane before closing it, so esc backs out one step
		// at a time.
		if a.focus == paneDetail {
			a.focus = paneTasks
			return a, nil
		}
		if a.showDetail {
			a.showDetail = false
			a.resize()
			return a, nil
		}
		return a, nil

	case key.Matches(msg, a.keys.Triage):
		if a.mode == modeTriage {
			a.mode = modeList
		} else {
			a.startTriage()
		}
		return a, a.loadTasks(a.mode)

	case key.Matches(msg, a.keys.Standup):
		a.startStandup()
		return a, a.loadStandup()

	case key.Matches(msg, a.keys.Note):
		return a, a.modal.Open(modalLog, true, "note", 0, "")

	case key.Matches(msg, a.keys.ToggleDetail):
		if a.mode == modeTriage {
			a.status = flash{text: "no detail pane in triage"}
			return a, nil
		}
		return a.toggleDetail()

	case key.Matches(msg, a.keys.ToggleProjects):
		if a.mode != modeList {
			return a, nil
		}
		return a.toggleProjects()

	case key.Matches(msg, a.keys.ExpandToggle):
		// In triage ⏎ skips: the current card moves to the back of this
		// session's queue.
		if a.mode == modeTriage {
			a.skipCurrent()
			return a, nil
		}
		if n, ok := a.selectedNode(); ok && n.total > 0 {
			return a.toggleExpand(n.t.ID)
		}
		// On a leaf ⏎ keeps its old detail-toggle muscle memory.
		return a.toggleDetail()

	case key.Matches(msg, a.keys.ExpandOpen) && a.mode == modeList:
		n, ok := a.selectedNode()
		if !ok {
			return a, nil
		}
		if n.total > 0 && !a.expanded[n.t.ID] {
			return a.toggleExpand(n.t.ID)
		}
		// Nothing to expand: l/→ opens the detail pane if it's closed
		// and moves focus into it, mirroring vim window navigation.
		// Skipped in the full-width layout, where the pane would own
		// every key anyway.
		probe := a
		probe.showDetail = true
		if _, _, _, full := probe.paneWidths(); !full {
			wasOpen := a.showDetail
			a.showDetail = true
			a.focus = paneDetail
			a.resize()
			if !wasOpen {
				return a, a.syncDetail(true)
			}
		}
		return a, nil

	case key.Matches(msg, a.keys.ExpandClose) && a.mode == modeList:
		// h/← first backs focus out of the detail pane, same direction
		// it moved in.
		if a.focus == paneDetail {
			a.focus = paneTasks
			return a, nil
		}
		n, ok := a.selectedNode()
		if !ok {
			// An empty list has nothing to collapse either.
			a.focusProjects()
			return a, nil
		}
		if a.expanded[n.t.ID] {
			return a.toggleExpand(n.t.ID)
		}
		// On an unexpanded child, close the branch it sits in and land
		// on its parent.
		if n.t.ParentID != nil {
			pid := *n.t.ParentID
			delete(a.expanded, pid)
			cmd := a.rebuildList()
			a.selectByID(pid)
			return a, tea.Batch(cmd, a.syncDetail(false))
		}
		// Nothing left to collapse: h moves to the projects column,
		// mirroring the way l falls through to the detail pane.
		a.focusProjects()
		return a, nil

	case key.Matches(msg, a.keys.ToggleDone) && a.mode == modeList:
		if n, ok := a.selectedNode(); ok {
			// A done sub-task un-checks; everything else completes.
			if n.t.ParentID != nil && n.t.State == task.StateDone {
				return a, a.setState(n.t, task.StateTodo)
			}
			return a, a.setState(n.t, task.StateDone)
		}
		return a, nil

	case key.Matches(msg, a.keys.ToggleCompleted) && a.mode == modeList:
		a.showCompleted = !a.showCompleted
		text := "completed tasks hidden"
		if a.showCompleted {
			text = "completed tasks shown"
		}
		a.status = flash{text: text}
		// Completed tasks are a different query, so reload rather than
		// re-flatten the cached slice; tasksLoadedMsg rebuilds the list.
		return a, a.loadTasks(a.mode)

	case key.Matches(msg, a.keys.QuickAdd):
		return a, a.openPrompt(promptAdd, "add: ", 0)

	case key.Matches(msg, a.keys.AddSub):
		if t, ok := a.selected(); ok {
			return a, a.openPrompt(promptChild, fmt.Sprintf("sub-task of #%d: ", t.ID), t.ID)
		}
		return a, nil

	case key.Matches(msg, a.keys.Palette):
		a.openPalette()
		return a, nil

	case key.Matches(msg, a.keys.Help):
		a.helpOpen = true
		return a, nil

	case key.Matches(msg, a.keys.ChangeState):
		if _, ok := a.selected(); ok {
			a.statePending = true
			a.resize()
		}
		return a, nil

	case key.Matches(msg, a.keys.ChangePriority):
		if _, ok := a.selected(); ok {
			a.priorityPending = true
			a.resize()
		}
		return a, nil

	case key.Matches(msg, a.keys.Delete) && a.mode == modeList:
		if _, ok := a.selected(); ok {
			a.deletePending = true
			a.resize()
		}
		return a, nil

	case key.Matches(msg, a.keys.MoveProject) && a.mode == modeList:
		if t, ok := a.selected(); ok {
			a.openProjectPicker(t)
		}
		return a, nil

	case key.Matches(msg, a.keys.SetTags):
		if t, ok := a.selected(); ok {
			// Seeded with the current tags so editing one doesn't mean
			// retyping the rest: the prompt replaces the whole list.
			return a, a.openPromptWith(promptTags,
				fmt.Sprintf("tags for #%d (space separated, empty clears): ", t.ID),
				task.FormatTags(a.tags[t.ID]), t.ID)
		}
		return a, nil

	case key.Matches(msg, a.keys.Rename):
		if t, ok := a.selected(); ok {
			return a, a.openPromptWith(promptRename, fmt.Sprintf("rename #%d: ", t.ID), t.Title, t.ID)
		}
		return a, nil

	case key.Matches(msg, a.keys.EditBody):
		if t, ok := a.selected(); ok {
			return a, editBodyCmd(t)
		}
		return a, nil

	case key.Matches(msg, a.keys.LogEntry):
		if t, ok := a.selected(); ok {
			return a, a.modal.Open(modalLog, true, fmt.Sprintf("note — #%d", t.ID), t.ID, "")
		}
		return a, nil

	case key.Matches(msg, a.keys.OpenURL):
		if t, ok := a.selected(); ok {
			return a, a.resolveURLs(t, false)
		}
		return a, nil

	case key.Matches(msg, a.keys.OpenAllURLs):
		if t, ok := a.selected(); ok {
			return a, a.resolveURLs(t, true)
		}
		return a, nil

	case key.Matches(msg, a.keys.Sessions):
		if t, ok := a.selected(); ok {
			return a, a.loadSessionsForPicker(t)
		}
		return a, nil
	}

	// Triage has no list to navigate; unconsumed keys stop here.
	if a.mode == modeTriage {
		model, cmd, _ := a.handleTriageKey(msg)
		return model, cmd
	}

	// The pane owns scrolling when explicitly focused, or when it has
	// replaced the list outright (full-width layout) and there's nothing
	// else a key like j/k could mean.
	_, _, _, full := a.paneWidths()
	if a.focus == paneDetail || (a.showDetail && full) {
		var cmd tea.Cmd
		a.detail, cmd = a.detail.Update(msg)
		return a, cmd
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

// statePanel renders the which-key panel for the pending `c` chord, with
// each key cap in its target state's color.
func (a app) statePanel() string {
	bindings := []struct {
		b     key.Binding
		style lipgloss.Style
	}{
		{a.keys.SetTodo, a.styles.State[task.StateTodo]},
		{a.keys.SetDoing, a.styles.State[task.StateDoing]},
		{a.keys.SetBlocked, a.styles.State[task.StateBlocked]},
		{a.keys.SetDone, a.styles.State[task.StateDone]},
		{a.keys.SetSomeday, a.styles.State[task.StateSomeday]},
		{a.keys.Cancel, a.styles.Dimmed},
	}
	entries := make([]panelEntry, 0, len(bindings))
	for _, e := range bindings {
		h := e.b.Help()
		entries = append(entries, panelEntry{key: h.Key, desc: h.Desc, keyStyle: e.style})
	}
	return renderKeyPanel(a.styles, a.width, "state", entries)
}

func (a app) setState(t task.Task, st task.State) tea.Cmd {
	return a.mutate(flash{kind: flashDone, text: fmt.Sprintf("#%d → %s", t.ID, st)}, func() error {
		return a.store.SetState(a.ctx, t.ID, st)
	})
}

// priorityForKey maps a priority-mutation key to its stored value; nil
// with ok means clear.
func (a app) priorityForKey(msg tea.KeyPressMsg) (*int64, bool) {
	val := func(n int64) (*int64, bool) { return &n, true }
	switch {
	case key.Matches(msg, a.keys.PriorityA):
		return val(1)
	case key.Matches(msg, a.keys.PriorityB):
		return val(2)
	case key.Matches(msg, a.keys.PriorityC):
		return val(3)
	case key.Matches(msg, a.keys.PriorityD):
		return val(4)
	case key.Matches(msg, a.keys.PriorityNone):
		return nil, true
	}
	return nil, false
}

// priorityPanel renders the which-key panel for the pending `p` chord,
// with each key cap in its priority's color.
func (a app) priorityPanel() string {
	bindings := []struct {
		b     key.Binding
		style lipgloss.Style
	}{
		{a.keys.PriorityA, a.styles.Priority[1]},
		{a.keys.PriorityB, a.styles.Priority[2]},
		{a.keys.PriorityC, a.styles.Priority[3]},
		{a.keys.PriorityD, a.styles.Priority[4]},
		{a.keys.PriorityNone, a.styles.Dimmed},
		{a.keys.Cancel, a.styles.Dimmed},
	}
	entries := make([]panelEntry, 0, len(bindings))
	for _, e := range bindings {
		h := e.b.Help()
		entries = append(entries, panelEntry{key: h.Key, desc: h.Desc, keyStyle: e.style})
	}
	return renderKeyPanel(a.styles, a.width, "priority", entries)
}

// deletePanel renders the which-key panel for the pending `d` chord: a
// second `d` deletes, anything else cancels.
func (a app) deletePanel() string {
	label, desc := "delete", "delete"
	if a.focus == paneProjects && a.mode == modeList {
		// Deleting a project never deletes work, and the panel says so:
		// the store reassigns its tasks to Unsorted first.
		label = "delete project"
		desc = "delete; tasks move to Unsorted"
	}
	entries := []panelEntry{
		{key: "d", desc: desc, keyStyle: a.styles.State[task.StateDone]},
		{key: "esc", desc: "cancel", keyStyle: a.styles.Dimmed},
	}
	return renderKeyPanel(a.styles, a.width, label, entries)
}

// quitPanel renders the which-key panel for the pending quit
// confirmation: shown instead of quitting outright when a session
// recap/auto-name is still running, since exiting mid-call silently
// drops it — nothing tracks or resumes it (see recapSessionCmd).
func (a app) quitPanel() string {
	title := fmt.Sprintf("%d session recaps still running — quit anyway?", a.pendingRecaps)
	if a.pendingRecaps == 1 {
		title = "1 session recap still running — quit anyway?"
	}
	entries := []panelEntry{
		{key: "q", desc: "quit anyway", keyStyle: a.styles.Error},
		{key: "esc", desc: "cancel", keyStyle: a.styles.Dimmed},
	}
	return renderKeyPanel(a.styles, a.width, title, entries)
}

func (a app) setPriority(t task.Task, p *int64) tea.Cmd {
	text := fmt.Sprintf("#%d priority cleared", t.ID)
	if p != nil {
		text = fmt.Sprintf("#%d priority → %s", t.ID, task.PriorityLetter(p))
	}
	return a.mutate(flash{kind: flashEdit, text: text}, func() error {
		return a.store.SetPriority(a.ctx, t.ID, p)
	})
}

func (a app) deleteTask(t task.Task) tea.Cmd {
	text := fmt.Sprintf("#%d deleted", t.ID)
	if t.ParentID == nil {
		// Top-level deletes take their sub-tasks with them (ON DELETE
		// CASCADE); drop the stale branch from the session caches.
		delete(a.expanded, t.ID)
		delete(a.childCache, t.ID)
		delete(a.logCache, t.ID)
		delete(a.sessionsCache, t.ID)
	}
	return a.mutate(flash{kind: flashDone, text: text}, func() error {
		return a.store.DeleteTask(a.ctx, t.ID)
	})
}

// node is the selection-relevant view of a row: the task itself, how deep
// it sits, and its disclosure state.
type node struct {
	t        task.Task
	depth    int
	total    int64
	expanded bool
}

// startTriage switches into triage and resets the session: progress
// starts at zero and the queue refills from the next inbox load.
func (a *app) startTriage() {
	a.mode = modeTriage
	a.triageQueue, a.triageProcessed = nil, 0
}

// selectedNode returns the node under the cursor — a top-level task or an
// expanded sub-task at any depth. In triage every action targets the
// current card instead of a list row.
func (a app) selectedNode() (node, bool) {
	if a.mode == modeTriage {
		if len(a.triageQueue) == 0 {
			return node{}, false
		}
		t := a.triageQueue[0]
		return node{t: t}, true
	}
	switch it := a.list.SelectedItem().(type) {
	case listItem:
		return node{t: it.t, total: it.total, expanded: it.expanded}, true
	case childItem:
		return node{t: it.t, depth: it.depth, total: it.total, expanded: it.expanded}, true
	}
	return node{}, false
}

// selected returns the task under the cursor; mutations act on the node
// itself, wherever it sits in the tree.
func (a app) selected() (task.Task, bool) {
	n, ok := a.selectedNode()
	return n.t, ok
}

// toggleDetail shows or hides the detail pane.
func (a app) toggleDetail() (tea.Model, tea.Cmd) {
	a.showDetail = !a.showDetail
	a.focus = paneTasks
	a.resize()
	if a.showDetail {
		return a, a.syncDetail(true)
	}
	return a, nil
}

// toggleExpand flips a branch open or closed, keeps the cursor on the
// node, and lets rebuildList fetch children on first expansion.
func (a app) toggleExpand(id int64) (tea.Model, tea.Cmd) {
	if a.expanded[id] {
		delete(a.expanded, id)
	} else {
		a.expanded[id] = true
	}
	cmd := a.rebuildList()
	a.selectByID(id)
	return a, tea.Batch(cmd, a.syncDetail(false))
}

// rebuildList re-flattens the item slice from the loaded tasks, the
// expansion set, and the children cache, and kicks off loads for any
// expanded branch whose children aren't cached yet.
func (a *app) rebuildList() tea.Cmd {
	// The status map rides on the delegate rather than on each item the
	// way counts does: counts drives disclosure logic as well as
	// rendering, whereas a session marker is purely visual, so the
	// renderer is the one thing that needs it.
	a.list.SetDelegate(taskDelegate{styles: a.styles, sessions: a.sessionStatus, tags: a.tags})
	cmds := []tea.Cmd{a.list.SetItems(toGroupedItems(a.tasks, a.counts, a.expanded, a.childCache, a.tags))}
	for id := range a.expanded {
		if _, ok := a.childCache[id]; !ok {
			cmds = append(cmds, a.loadChildren(id))
		}
	}
	return tea.Batch(cmds...)
}

// reloadExpanded re-fetches every expanded branch (after a mutation the
// cached children may be stale).
func (a app) reloadExpanded() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(a.expanded))
	for id := range a.expanded {
		cmds = append(cmds, a.loadChildren(id))
	}
	return tea.Batch(cmds...)
}

// selectByID moves the cursor onto the row holding the given task, if
// it's visible.
func (a *app) selectByID(id int64) {
	for i, it := range a.list.VisibleItems() {
		if r, ok := it.(rowItem); ok && r.rowTask().ID == id {
			a.list.Select(i)
			return
		}
	}
}

// syncDetail points the detail pane at the task under the cursor, at
// whatever depth it sits. With force, it reloads even if the same task is
// already shown (after a mutation or resize); otherwise moving onto a
// different task loads that task's own children, log and sessions.
func (a *app) syncDetail(force bool) tea.Cmd {
	if !a.showDetail {
		return nil
	}
	n, ok := a.selectedNode()
	if !ok {
		a.detailID = 0
		a.detail.SetContent(a.styles.Dimmed.Render("nothing selected"))
		return nil
	}
	if force || n.t.ID != a.detailID {
		if n.t.ID != a.detailID {
			// A genuinely different task starts scrolled to the top; a
			// same-task refresh (resize, new log entry) leaves reading
			// position alone.
			a.detail.GotoTop()
		}
		a.detailID = n.t.ID
		return a.loadChildren(n.t.ID)
	}
	a.renderDetailFor(n.t)
	return nil
}

// renderDetailFor renders the pane for one task from the caches. The task
// may sit at any depth; a sub-task gets the same pane as a top-level task.
func (a *app) renderDetailFor(t task.Task) {
	_, _, detailW, _ := a.paneWidths()
	a.detail.SetContent(renderDetail(t, a.childCache[t.ID], a.logCache[t.ID],
		a.sessionsCache[t.ID], a.tags[t.ID], a.renderer, a.styles, detailW))
}

// Projects-column geometry. The width is fixed: a project name plus its
// count, and nothing that benefits from more room.
const (
	projectsPaneWidth = 20
	// Below this the column hides itself entirely -- the detail split
	// already collapses to a single full-width pane here, and three
	// columns inside 100 cells leaves none of them readable.
	projectsPaneMinWidth = 100
	// The task list needs at least this much to be worth splitting with
	// the detail pane; see paneWidths.
	detailSplitMinWidth = 100
)

// projectsVisible reports whether the projects column is on screen. It is
// list-mode only: triage and standup own the full width.
func (a app) projectsVisible() bool {
	if !a.showProjects || a.mode != modeList || a.width < projectsPaneMinWidth {
		return false
	}
	// Never let the projects column be the thing that pushes the detail
	// pane into replacing the task list. The list is the primary surface;
	// a narrow terminal gives up the projects column first.
	if a.showDetail && a.width-projectsPaneWidth-1 < detailSplitMinWidth {
		return false
	}
	return true
}

// paneWidths computes the column widths for the current terminal width.
// full reports that the detail pane replaces the task list entirely (the
// split would crush both below detailSplitMinWidth). Because
// projectsVisible refuses to be the cause of that collapse, full always
// implies projW == 0.
func (a app) paneWidths() (projW, listW, detailW int, full bool) {
	rest := a.width
	if a.projectsVisible() {
		projW = projectsPaneWidth
		rest = a.width - projW - 1 // the divider column
	}
	switch {
	case !a.showDetail:
		return projW, rest, 0, false
	case rest >= 120:
		listW = rest * 46 / 100 // 46 / 54 split
		return projW, listW, rest - listW - 1, false
	case rest >= detailSplitMinWidth:
		listW = rest / 2 // tighter split
		return projW, listW, rest - listW - 1, false
	default:
		// The detail pane replaces the list. The list still gets a width
		// so its own state (filtering, wrapping) stays sane off screen.
		return projW, rest, rest, true
	}
}

// paneSplits returns the columns carrying a vertical divider, left to
// right, so the horizontal rules can tee into them.
func (a app) paneSplits() []int {
	projW, listW, _, full := a.paneWidths()
	var splits []int
	x := 0
	if projW > 0 {
		splits = append(splits, projW)
		x = projW + 1
	}
	if a.showDetail && !full {
		splits = append(splits, x+listW)
	}
	return splits
}

// verticalDivider is one full-height pane divider, accented when it abuts
// the focused column.
func (a app) verticalDivider(focused bool) string {
	style := a.styles.Rule
	if focused {
		style = a.styles.Accent
	}
	return strings.TrimSuffix(strings.Repeat(
		style.Render(a.styles.Glyphs.RuleV)+"\n", max(a.bodyHeight, 1)), "\n")
}

func (a *app) resize() {
	const chromeTop = 2 // header + top rule
	bottomHeight := 2   // bottom rule + footer line
	if a.statePending {
		// The list must shrink by exactly the panel's height; bubbles
		// list pads its view to the height it was given. The panel's own
		// top border doubles as the bottom rule.
		bottomHeight = max(lipgloss.Height(a.statePanel()), 1)
	}
	if a.priorityPending {
		bottomHeight = max(lipgloss.Height(a.priorityPanel()), 1)
	}
	if a.deletePending {
		bottomHeight = max(lipgloss.Height(a.deletePanel()), 1)
	}
	if a.quitPending {
		bottomHeight = max(lipgloss.Height(a.quitPanel()), 1)
	}
	a.bodyHeight = max(a.height-chromeTop-bottomHeight, 1)
	// A narrowing terminal can take the projects column away underneath
	// the cursor; focus must not stay on a pane nobody can see.
	if a.focus == paneProjects && !a.projectsVisible() {
		a.focus = paneTasks
	}
	_, listWidth, detailWidth, _ := a.paneWidths()
	if a.showDetail {
		a.detail.SetWidth(max(detailWidth, 10))
		a.detail.SetHeight(a.bodyHeight)
		a.renderer, _ = newBodyRenderer(detailWidth - 2)
	}
	a.list.SetSize(listWidth, a.bodyHeight)
	a.modal.SetSize(a.width, a.height)
}

// --- prompt ---

func (a *app) openPrompt(kind promptKind, label string, target int64) tea.Cmd {
	return a.openPromptWith(kind, label, "", target)
}

// openPromptWith is openPrompt seeded with an initial value (used by
// rename, where the field starts on the current title), cursor at the end.
func (a *app) openPromptWith(kind promptKind, label, value string, target int64) tea.Cmd {
	a.promptKind = kind
	a.promptTarget = target
	a.prompt.Reset()
	a.prompt.Prompt = label
	a.prompt.SetValue(value)
	a.prompt.CursorEnd()
	return a.prompt.Focus()
}

func (a *app) closePrompt() {
	a.promptKind = promptNone
	a.promptTarget = 0
	a.sessionLabel = ""
	a.prompt.Reset()
	a.prompt.Blur()
}

func (a app) submitPrompt() (tea.Model, tea.Cmd) {
	kind, target, label := a.promptKind, a.promptTarget, a.sessionLabel
	value := strings.TrimSpace(a.prompt.Value())
	a.closePrompt()

	switch kind {
	case promptAdd:
		if value == "" {
			return a, nil
		}
		return a, a.captureTask(value)
	case promptChild:
		if value == "" {
			return a, nil
		}
		// Open the parent's branch so the new sub-task is visible after
		// the refresh.
		a.expanded[target] = true
		delete(a.childCache, target)
		return a, a.mutate(flash{kind: flashAdd, text: "added sub-task: " + value}, func() error {
			_, err := a.store.AddChild(a.ctx, target, value)
			return err
		})
	case promptTags:
		tags := task.ParseTags(value)
		text := "tags cleared"
		if len(tags) > 0 {
			text = "tags → " + task.FormatTags(tags)
		}
		return a, a.mutate(flash{kind: flashEdit, text: text}, func() error {
			return a.store.SetTags(a.ctx, target, tags)
		})
	case promptDue:
		var d *string
		text := "due cleared"
		if value != "" {
			d = &value
			text = "due → " + value
		}
		return a, a.mutate(flash{kind: flashEdit, text: text}, func() error {
			return a.store.SetDue(a.ctx, target, d)
		})
	case promptSessionCwd:
		if value == "" {
			return a, nil
		}
		return a, launchSessionCmd(target, value, label, a.dbPath)
	case promptNewProject:
		if value == "" {
			return a, nil
		}
		return a, a.mutate(flash{kind: flashAdd, text: "project: " + value}, func() error {
			_, err := a.store.CreateProject(a.ctx, value)
			return err
		})
	case promptRenameProject:
		if value == "" {
			return a, nil
		}
		return a, a.mutate(flash{kind: flashEdit, text: "renamed to " + value}, func() error {
			return a.store.RenameProject(a.ctx, target, value)
		})
	case promptRename:
		// A blank title is a no-op; renaming to nothing would strand the row.
		if value == "" {
			return a, nil
		}
		return a, a.mutate(flash{kind: flashEdit, text: fmt.Sprintf("#%d renamed", target)}, func() error {
			return a.store.SetTitle(a.ctx, target, value)
		})
	}
	return a, nil
}

// submitModal performs the action the open modal was collecting input
// for; the modal itself only owns presentation and text entry.
func (a app) submitModal() (tea.Model, tea.Cmd) {
	kind, target := a.modal.kind, a.modal.target
	value := a.modal.Value()
	a.modal.Close()

	switch kind {
	case modalLog:
		if value == "" {
			return a, nil
		}
		var taskID *int64
		text := "note logged"
		if target != 0 {
			taskID = &target
			text = fmt.Sprintf("note logged on #%d", target)
		}
		return a, a.mutate(flash{kind: flashAdd, text: text}, func() error {
			_, err := a.store.AddLogEntry(a.ctx, taskID, value)
			return err
		})
	}
	return a, nil
}

// --- commands (all store I/O happens here, off the update loop) ---

func (a app) loadTasks(mode viewMode) tea.Cmd {
	return func() tea.Msg {
		var (
			tasks []task.Task
			err   error
		)
		switch {
		case mode == modeTriage:
			tasks, err = a.store.ListInbox(a.ctx, a.projectFilter)
		case a.showCompleted:
			tasks, err = a.store.ListLiveWithCompleted(a.ctx, a.projectFilter)
		default:
			tasks, err = a.store.ListLive(a.ctx, a.projectFilter)
		}
		if err != nil {
			return errMsg{err}
		}
		counts, err := a.store.ChildCounts(a.ctx)
		if err != nil {
			return errMsg{err}
		}
		// The inbox nudge counts the same population the triage view would
		// process, so it follows the project filter too.
		inbox, err := a.store.CountInbox(a.ctx, a.projectFilter)
		if err != nil {
			return errMsg{err}
		}
		tags, err := a.store.TagsByTask(a.ctx)
		if err != nil {
			return errMsg{err}
		}
		// A session status is decoration on the row, so a failure here
		// degrades to "no marker" rather than failing the whole load and
		// leaving the user with no list at all.
		statuses, err := a.store.SessionStatuses(a.ctx)
		if err != nil {
			statuses = nil
		}
		return tasksLoadedMsg{mode: mode, tasks: tasks, counts: counts, tags: tags,
			sessions: statuses, inbox: inbox}
	}
}

// resolveURLs collects a task's links — body plus log entries — off the
// update loop, since the log may not be cached (the detail pane loads it
// lazily). The resulting message opens, picks, or flashes.
func (a app) resolveURLs(t task.Task, openAll bool) tea.Cmd {
	return func() tea.Msg {
		log, err := a.store.ListTaskLog(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		return urlsResolvedMsg{urls: taskURLs(t, log), openAll: openAll}
	}
}

func (a app) loadChildren(parentID int64) tea.Cmd {
	return func() tea.Msg {
		children, err := a.store.ListChildren(a.ctx, parentID)
		if err != nil {
			return errMsg{err}
		}
		log, err := a.store.ListTaskLog(a.ctx, parentID)
		if err != nil {
			return errMsg{err}
		}
		sessions, err := a.store.ListSessionsForTask(a.ctx, parentID)
		if err != nil {
			return errMsg{err}
		}
		return childrenLoadedMsg{parentID: parentID, children: children, log: log, sessions: sessions}
	}
}

// mutate wraps a store mutation: run it, then report status and refresh.
// captureTask builds the Cmd for a quick-add capture. A pasted Jira
// issue URL is expanded like `tend add`: key + fetched summary as the
// title, link in the body. The lookup runs inside the Cmd's goroutine,
// so a slow or unreachable Jira never blocks the UI, and any lookup
// failure degrades to the bare key with the reason in the flash.
// captureProjectID is where a task captured in the TUI lands: the project
// the column is on. The All row falls back to the default project -- All
// is a way of looking at everything, not a place to put a new task.
//
// Read from the selection rather than from the stored setting, so what a
// capture does is exactly what the screen shows.
func (a app) captureProjectID() int64 {
	if p, ok := a.selectedProject(); ok {
		return p.ID
	}
	return task.DefaultProjectID
}

func (a app) captureTask(value string) tea.Cmd {
	projectID := a.captureProjectID()
	iss, ok := jira.ParseIssueURL(value)
	if !ok {
		return a.mutate(flash{kind: flashAdd, text: "captured to inbox: " + value}, func() error {
			_, err := a.store.AddTaskIn(a.ctx, projectID, value)
			return err
		})
	}
	return func() tea.Msg {
		title, warn := jira.Expand(a.ctx, iss)
		if _, err := a.store.AddTaskWithBodyIn(a.ctx, projectID, title, iss.URL+"\n"); err != nil {
			return errMsg{err}
		}
		text := "captured to inbox: " + title
		if warn != nil {
			text = fmt.Sprintf("captured %s (%v)", iss.Key, warn)
		}
		return refreshMsg{status: flash{kind: flashAdd, text: text}}
	}
}

func (a app) mutate(status flash, fn func() error) tea.Cmd {
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
		return refreshMsg{status: flash{kind: flashEdit, text: "body saved"}}
	}
}

// --- view ---

func (a app) View() tea.View {
	if !a.loaded {
		v := tea.NewView(a.loadingFrame())
		v.AltScreen = true
		v.WindowTitle = "tend"
		return v
	}

	splits := a.paneSplits() // columns carrying a vertical divider

	var body string
	switch a.mode {
	case modeTriage:
		splits = nil
		body = a.triageView()
	case modeStandup:
		at, _ := a.standupWidths()
		splits = []int{at}
		body = a.standupView()
	default:
		body = a.listBody()
	}

	frame := a.headerLine() + "\n" + a.ruleLine(splits, a.styles.Glyphs.TeeDown) + "\n" +
		body + "\n" + a.bottomChrome(splits)
	if a.modal.Active() {
		box := a.modal.View(a.styles)
		x := max((a.width-lipgloss.Width(box))/2, 0)
		y := max((a.height-lipgloss.Height(box))/2, 0)
		// Layer positions are only honored through a Compositor; composing
		// raw layers onto a canvas draws them all at the origin.
		frame = lipgloss.NewCompositor(
			lipgloss.NewLayer(frame),
			lipgloss.NewLayer(box).X(x).Y(y).Z(1),
		).Render()
	}
	// Palette and help splice in just above the footer, over the bottom
	// body rows. A panel taller than the screen loses its top rows, like
	// the design's splice.
	if a.paletteOpen || a.helpOpen || a.urlPickerOpen || a.sessionPickerOpen ||
		a.projectPickerOpen {
		box := a.paletteView()
		switch {
		case a.helpOpen:
			box = a.helpView()
		case a.urlPickerOpen:
			box = a.urlPickerView()
		case a.sessionPickerOpen:
			box = a.sessionPickerView()
		case a.projectPickerOpen:
			box = a.projectPickerView()
		}
		rows := strings.Split(box, "\n")
		if maxRows := max(a.height-1, 1); len(rows) > maxRows {
			rows = rows[len(rows)-maxRows:]
			box = strings.Join(rows, "\n")
		}
		y := max(a.height-1-len(rows), 0)
		frame = lipgloss.NewCompositor(
			lipgloss.NewLayer(frame),
			lipgloss.NewLayer(box).X(0).Y(y).Z(1),
		).Render()
	}

	v := tea.NewView(frame)
	v.AltScreen = true
	v.WindowTitle = "tend"
	return v
}

// listBody composes the list-mode columns left to right: the projects
// pane, the task list, and the detail pane, in whatever combination is
// currently visible.
func (a app) listBody() string {
	projW, _, _, full := a.paneWidths()

	var cols []string
	if projW > 0 {
		cols = append(cols, a.projectsView(), a.verticalDivider(a.focus == paneProjects))
	}
	switch {
	case a.showDetail && full:
		// The detail pane replaces the list outright; projW is 0 here.
		cols = append(cols, a.detail.View())
	case a.showDetail:
		cols = append(cols, a.list.View(),
			a.verticalDivider(a.focus == paneDetail), a.detail.View())
	default:
		cols = append(cols, a.list.View())
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
}

// headerLine renders `  tend  ·  <view>` with the inbox nudge and shown
// count right-aligned.
func (a app) headerLine() string {
	s := a.styles
	left := s.HeaderApp.Render("  tend") + s.HeaderSep.Render("  ·  ")
	switch a.mode {
	case modeTriage:
		left += s.State[task.StateInbox].Bold(true).Render("triage")
	case modeStandup:
		left += s.HeaderView.Render("standup")
	default:
		left += s.HeaderView.Render("live")
	}
	// Name the project the view is scoped to. The projects column usually
	// says this, but it hides on a narrow terminal and triage never shows
	// it at all -- so without this you cannot tell whether you are
	// triaging one project or everything. Standup is deliberately global,
	// so it stays unqualified.
	if a.mode != modeStandup {
		if p, ok := a.selectedProject(); ok {
			left += s.HeaderSep.Render("  ·  ") + s.HeaderView.Render(p.Name)
		}
	}

	right := ""
	switch {
	case a.mode == modeStandup:
		right = s.CountLabel.Render(task.WindowLabel(a.standupSince, time.Now())) + "  "
	case a.mode == modeTriage && len(a.triageQueue) > 0:
		total := a.triageProcessed + len(a.triageQueue)
		right = s.CountNum.Render(fmt.Sprintf("%d of %d", a.triageProcessed+1, total)) +
			s.CountLabel.Render("  processing inbox") + "  "
	case a.mode == modeTriage:
		if a.inboxCount == 0 {
			right = s.InboxZero.Render("inbox zero") + "  "
		}
	default:
		if a.inboxCount > 0 {
			right = s.InboxNudge.Render(fmt.Sprintf("%s %d in inbox",
				s.Glyphs.State[task.StateInbox], a.inboxCount)) + "     "
		}
		right += s.CountNum.Render(fmt.Sprintf("%d", taskCount(a.list.Items()))) +
			s.CountLabel.Render(" shown") + "  "
	}

	gap := a.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

// ruleLine draws a full-width horizontal rule, teed into each pane
// divider column with the given glyph. splits must be ascending; columns
// outside the terminal are ignored.
func (a app) ruleLine(splits []int, join string) string {
	g := a.styles.Glyphs
	w := max(a.width, 1)

	var b strings.Builder
	x := 0
	for _, at := range splits {
		if at < x || at >= w {
			continue
		}
		b.WriteString(strings.Repeat(g.RuleH, at-x))
		b.WriteString(join)
		x = at + 1
	}
	b.WriteString(strings.Repeat(g.RuleH, max(w-x, 0)))
	return a.styles.Rule.Render(b.String())
}

// bottomChrome is everything under the body: normally a rule plus the
// footer line; the which-key panels carry their own top border instead.
func (a app) bottomChrome(splits []int) string {
	if a.statePending {
		return a.statePanel()
	}
	if a.priorityPending {
		return a.priorityPanel()
	}
	if a.deletePending {
		return a.deletePanel()
	}
	if a.quitPending {
		return a.quitPanel()
	}
	return a.ruleLine(splits, a.styles.Glyphs.TeeUp) + "\n" + a.footer()
}

func (a app) footer() string {
	if a.promptKind != promptNone {
		return a.styles.PromptLabel.Render("") + a.prompt.View()
	}
	if a.status.text != "" {
		if a.status.isErr {
			return a.styles.Error.Render(a.status.text)
		}
		line := "  "
		if glyph, style := a.flashDecoration(a.status.kind); glyph != "" {
			line += style.Bold(true).Render(glyph) + " "
		}
		return line + a.styles.Dimmed.Render(a.status.text)
	}
	hints := [][2]string{
		{"j/k", "move"}, {"]", "detail"}, {"n", "add"}, {"c", "state"},
		{"/", "search"}, {":", "palette"}, {"i", "triage"}, {"?", "help"}, {"q", "quit"},
	}
	_, _, _, full := a.paneWidths()
	switch {
	case a.focus == paneProjects && a.mode == modeList:
		hints = [][2]string{
			{"j/k", "switch project"}, {"l/⏎", "to tasks"}, {"n", "new"}, {"R", "rename"},
			{"dd", "delete"}, {"A", "archive"}, {"?", "help"}, {"q", "quit"},
		}
	case a.focus == paneDetail:
		hints = [][2]string{
			{"j/k", "scroll"}, {"h/esc", "back to list"}, {":", "palette"}, {"?", "help"}, {"q", "quit"},
		}
	case a.showDetail && full:
		hints = [][2]string{
			{"j/k", "scroll"}, {"]", "close"}, {":", "palette"}, {"?", "help"}, {"q", "quit"},
		}
	default:
		n, ok := a.selectedNode()
		switch {
		case ok && a.mode == modeList && n.total > 0:
			verb := "expand"
			if n.expanded {
				verb = "collapse"
			}
			hints = append([][2]string{hints[0], {"⏎", verb}}, hints[1:]...)
		case ok:
			probe := a
			probe.showDetail = true
			if _, _, _, probeFull := probe.paneWidths(); !probeFull {
				verb := "focus pane"
				if !a.showDetail {
					verb = "open + focus pane"
				}
				hints = append([][2]string{hints[0], {"l", verb}}, hints[1:]...)
			}
		}
	}
	if a.mode == modeTriage {
		hints = [][2]string{
			{"t/d/b", "set state"}, {"x", "done"}, {"s", "someday"},
			{"e", "edit"}, {"⏎", "skip"}, {"esc", "back"},
		}
		if len(a.triageQueue) == 0 {
			hints = [][2]string{{"esc/q", "back"}, {":", "palette"}}
		}
	}
	if a.mode == modeStandup {
		sort := "by time"
		if a.standupChrono {
			sort = "by task"
		}
		recaps := "hide recaps"
		if a.standupHideRecaps {
			recaps = "show recaps"
		}
		hints = [][2]string{
			{"n", "note"}, {"y", "yank"}, {"h/l", "window"}, {"s", sort}, {"C", recaps},
			{"j/k", "scroll"}, {"tab", "expand"}, {"esc/q", "back"}, {"?", "help"},
		}
	}
	return a.hintLine(hints)
}

// flashDecoration maps a flash kind to its leading glyph and semantic
// color; flashPlain renders no glyph.
func (a app) flashDecoration(k flashKind) (string, lipgloss.Style) {
	g, s := a.styles.Glyphs, a.styles
	switch k {
	case flashDone:
		return g.State[task.StateDone], s.CheckDone
	case flashAdd:
		return g.Plus, s.State[task.StateInbox]
	case flashEdit:
		return g.Pen, s.Accent
	case flashLink:
		return g.Link, s.Link
	}
	return "", s.Normal
}

// loadingFrame is the calm pre-load screen shown until the first
// tasksLoadedMsg lands — data arrives via the initial command, so this is
// on screen for a frame or two at most.
func (a app) loadingFrame() string {
	s, g := a.styles, a.styles.Glyphs
	w, h := max(a.width, 1), max(a.bodyHeight, 1)

	content := []string{
		centerLine(s.Accent.Bold(true).Render(g.State[task.StateDoing])+
			s.Dimmed.Render("  loading tasks…"), w),
	}
	if a.dbPath != "" {
		content = append(content, "",
			centerLine(s.Muted.Render("reading "+tildePath(a.dbPath)), w))
	}
	top := max((h-len(content))/2, 0)
	lines := make([]string, 0, h)
	for range top {
		lines = append(lines, "")
	}
	lines = append(lines, content...)
	for len(lines) < h {
		lines = append(lines, "")
	}
	return a.headerLine() + "\n" + a.ruleLine(nil, "") + "\n" +
		strings.Join(lines[:h], "\n") + "\n" + a.ruleLine(nil, "") + "\n"
}

// tildePath abbreviates the home directory for display.
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(p, home); ok {
			return "~" + rest
		}
	}
	return p
}

// hintLine renders footer hints as accent-bold key + muted label pairs,
// three spaces apart. Pairs that don't fit the terminal width are dropped
// whole rather than clipped mid-word.
func (a app) hintLine(pairs [][2]string) string {
	s := a.styles
	line := ""
	for _, p := range pairs {
		sep := "   "
		if line == "" {
			sep = "  "
		}
		part := sep + s.FooterKey.Render(p[0]) + s.FooterDesc.Render(" "+p[1])
		if a.width > 0 && lipgloss.Width(line)+lipgloss.Width(part) > a.width {
			break
		}
		line += part
	}
	return line
}
