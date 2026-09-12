package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// The agents view (`A`) is the sessions view the detail pane's SESSIONS
// section and the `r` picker are not: every Claude Code session in the
// selected project, across its tasks, in one list. The projects column
// stays on the left and scopes it exactly as it scopes the task list; the
// middle column is the sessions, live ones by default (`C` shows the ended
// ones too); the right column is the session's detail -- for a session
// that ran in a terminal, its task's detail pane, so the body and the
// recap log are one keypress away; for a headless one (a workflow step
// run under the runner), its stream-json log, tail-followed while the
// step runs, exactly as the run view shows it.
//
// The two things a session list is for are here too: `⏎`/`r` joins the
// selected session (resumeGuardedCmd: attach to its tmux session, or
// `claude --resume` it; a headless session is refused while its run is
// live and not paused, since the runner still owns that transcript), and
// `dd` kills it. Kill is a status write, never a row delete: a session
// that ran is history the task keeps (DeleteSession exists only for a
// launch that never became a session). An interactive session is killed
// through its tmux session; a headless one through its run -- SetRunState
// cancelled, which the runner polls for and SIGTERMs the step on -- so
// process ownership stays where it is. Nothing here polls on its own:
// the session poller's tick (sessionsPolledMsg) and the database watcher
// (dbChangedMsg) reload the list, and the log tails off the same ticks.

// agentsView is the state of modeAgents.
type agentsView struct {
	all  []agentRow // every session loaded for the project, sorted
	rows []agentRow // the ones shown: all, or the live ones (showEnded)

	cursor    int
	selectID  int64 // session id to land on after the next load; 0 = keep the index
	showEnded bool  // `C`: list ended sessions too

	// The right pane. For a headless session it is the step's log (the
	// same runLog the run view tails); follow keeps the bottom in view as
	// it grows and raw flips the rendering. For an interactive session it
	// is the task's detail pane, from detail.
	log      runLog
	follow   bool
	raw      bool
	detail   agentDetail
	vp       viewport.Model
	renderer *glamour.TermRenderer
}

// agentRow is one session as the list shows it: the session with its
// task, and -- for a headless one -- the step run and run it belongs to,
// resolved at load so the row can say which workflow and step it ran
// and the pane knows which log to tail. Both stay nil when a read fails;
// the row still shows.
type agentRow struct {
	sess     task.TaskSession
	stepRun  *workflow.StepRun
	run      *workflow.Run
	workflow string // the run's workflow name; "" if unknown
	step     string // the step's name; "" if unknown
}

// headless reports whether the row's session ran under the runner.
func (r agentRow) headless() bool { return r.sess.Headless() }

// agentDetail is the selected interactive session's task, loaded for the
// pane the way loadChildren loads it for the list view's detail pane.
// sessionID says which session it was loaded for, so a load that lands
// after the cursor moved on is recognized and dropped.
type agentDetail struct {
	sessionID int64
	t         task.Task
	children  []task.Task
	blockers  []task.Task
	blocking  []task.Task
	log       []task.LogEntry
	sessions  []task.Session
	runs      []runSummary
	tags      []string
}

// Messages for the view: the list, and the selected interactive session's
// task detail. The log arrives as runLogLoadedMsg, shared with the run
// view, since it is the same read.
type (
	agentsLoadedMsg      struct{ rows []agentRow }
	agentDetailLoadedMsg agentDetail
)

// killSession ends a session's tmux session, and with it the claude
// running inside. A package-level var for the same reason sessionAlive and
// capturePane are: `dd` fires from the same Update path the tests drive,
// and without a seam every kill test would reach for the real tmux server.
var killSession = func(name string) error {
	confPath, err := agent.WriteConfig()
	if err != nil {
		return err
	}
	return agent.KillSession(name, confPath)
}

// agentStatusRank orders the list: the sessions asking for the user first,
// then live work, then the quiet ones -- sessionOrder, with unknown before
// ended. Within a rank, most recently active first (the store's order).
func agentStatusRank(st task.SessionStatus) int {
	if i := slices.Index(sessionOrder, st); i >= 0 {
		if st == task.SessionEnded {
			return len(sessionOrder) + 1
		}
		return i
	}
	return len(sessionOrder) // unknown, or a status tend does not recognize
}

// startAgents switches into the view. Loaded rows are left in place so
// re-entering does not flash empty; loadAgentSessions refreshes them.
func (a *app) startAgents() {
	a.mode = modeAgents
	a.focus = paneTasks
	a.deletePending = false
	if a.av.vp.Width() == 0 {
		a.av.vp = viewport.New()
	}
	a.resize()
}

// leaveAgents returns to the list view.
func (a *app) leaveAgents() tea.Cmd {
	a.mode = modeList
	a.focus = paneTasks
	a.deletePending = false
	a.resize()
	return a.loadTasks(modeList)
}

// selectedAgent is the row under the cursor.
func (a app) selectedAgent() (agentRow, bool) {
	if a.av.cursor < 0 || a.av.cursor >= len(a.av.rows) {
		return agentRow{}, false
	}
	return a.av.rows[a.av.cursor], true
}

// loadAgentSessions fetches the project's sessions and, for each headless
// one, the step run and run it belongs to. Every lookup past the session
// row degrades rather than fails: a step run or run that could not be read
// leaves the row without one, and the row still shows.
func (a app) loadAgentSessions() tea.Cmd {
	filter := a.projectFilter
	return func() tea.Msg {
		sessions, err := a.store.ListSessionsForProject(a.ctx, filter)
		if err != nil {
			return errMsg{err}
		}
		rows := make([]agentRow, 0, len(sessions))
		for _, sess := range sessions {
			row := agentRow{sess: sess}
			if sess.StepRunID != nil {
				if sr, err := a.store.GetStepRun(a.ctx, *sess.StepRunID); err == nil {
					row.stepRun = &sr
					if st, err := a.store.GetStep(a.ctx, sr.StepID); err == nil {
						row.step = st.Name
					}
					if run, err := a.store.GetRun(a.ctx, sr.RunID); err == nil {
						row.run = &run
						if wf, err := a.store.GetWorkflow(a.ctx, run.WorkflowID); err == nil {
							row.workflow = wf.Name
						}
					}
				}
			}
			rows = append(rows, row)
		}
		slices.SortStableFunc(rows, func(x, y agentRow) int {
			return agentStatusRank(x.sess.Status) - agentStatusRank(y.sess.Status)
		})
		return agentsLoadedMsg{rows: rows}
	}
}

// applyAgents installs a loaded list and settles the cursor on the session
// it was on -- by id, never by index, since a status change moves rows --
// or on selectID when a caller asked for one. Then the pane follows.
func (a *app) applyAgents(msg agentsLoadedMsg) tea.Cmd {
	want := a.av.selectID
	a.av.selectID = 0
	if want == 0 {
		if row, ok := a.selectedAgent(); ok {
			want = row.sess.ID
		}
	}
	a.av.all = msg.rows
	a.filterAgents()
	if want != 0 {
		if i := slices.IndexFunc(a.av.rows, func(r agentRow) bool { return r.sess.ID == want }); i >= 0 {
			a.av.cursor = i
		}
	}
	a.av.cursor = max(min(a.av.cursor, len(a.av.rows)-1), 0)
	return a.syncAgentDetail()
}

// filterAgents recomputes the shown rows from the loaded ones: every
// session when showEnded is on, the live ones otherwise.
func (a *app) filterAgents() {
	if a.av.showEnded {
		a.av.rows = a.av.all
		return
	}
	a.av.rows = a.av.rows[:0:0]
	for _, r := range a.av.all {
		if r.sess.Status != task.SessionEnded {
			a.av.rows = append(a.av.rows, r)
		}
	}
}

// hiddenAgents is how many loaded sessions the ended filter is hiding.
func (a app) hiddenAgents() int { return len(a.av.all) - len(a.av.rows) }

// syncAgentDetail points the right pane at the selected session. A
// headless session's log is tailed from where the last read stopped when
// it is the step already shown, and read from the start otherwise; an
// interactive session's task is (re)loaded, so the pane picks up new log
// entries and sub-tasks on every reload of the view.
func (a *app) syncAgentDetail() tea.Cmd {
	row, ok := a.selectedAgent()
	if !ok {
		a.av.log = runLog{}
		a.av.detail = agentDetail{}
		a.renderAgentPane()
		return nil
	}
	if row.headless() {
		a.av.detail = agentDetail{}
		var path string
		if row.stepRun != nil {
			path = row.stepRun.LogPath
		}
		if a.av.log.stepRunID != *row.sess.StepRunID || a.av.log.path != path {
			a.av.log = runLog{stepRunID: *row.sess.StepRunID, path: path}
			a.av.follow = true
		}
		a.renderAgentPane()
		if path == "" {
			return nil
		}
		return loadRunLog(a.av.log.stepRunID, path, a.av.log.offset)
	}
	a.av.log = runLog{}
	if a.av.detail.sessionID != row.sess.ID {
		a.av.detail = agentDetail{}
		a.av.vp.GotoTop()
	}
	a.renderAgentPane()
	return a.loadAgentDetail(row.sess.Session)
}

// loadAgentDetail fetches everything the list view's detail pane shows
// for the session's task, tagged with the session it was loaded for.
func (a app) loadAgentDetail(sess task.Session) tea.Cmd {
	return func() tea.Msg {
		t, err := a.store.GetTask(a.ctx, sess.TaskID)
		if err != nil {
			return errMsg{err}
		}
		children, err := a.store.ListChildren(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		log, err := a.store.ListTaskLog(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		sessions, err := a.store.ListSessionsForTask(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		runs, err := a.store.ListRunsForTask(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		tags, err := a.store.TagsForTask(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		blockers, err := a.store.Blockers(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		blocking, err := a.store.Blocking(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		return agentDetailLoadedMsg{sessionID: sess.ID, t: t, children: children, blockers: blockers,
			blocking: blocking, log: log, sessions: sessions, runs: a.summarizeRuns(a.ctx, runs), tags: tags}
	}
}

// applyAgentDetail installs a loaded task detail if the cursor is still
// on the session it was loaded for.
func (a *app) applyAgentDetail(msg agentDetailLoadedMsg) {
	row, ok := a.selectedAgent()
	if !ok || row.sess.ID != msg.sessionID {
		return
	}
	a.av.detail = agentDetail(msg)
	a.renderAgentPane()
}

// applyAgentLog folds a log read into the pane, dropping a stale one the
// same way the run view does.
func (a *app) applyAgentLog(msg runLogLoadedMsg) {
	if a.av.log.apply(msg) {
		a.renderAgentPane()
	}
}

// --- keys -----------------------------------------------------------------

// handleAgentsKey owns the keyboard in the agents view. The `dd` chord is
// handled here rather than in handleKey's shared branch because what it
// does depends on the pane: a project in the projects column, otherwise
// the selected session is killed.
func (a app) handleAgentsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if a.deletePending {
		a.deletePending = false
		a.resize()
		if key.Matches(msg, a.keys.Delete) {
			if a.focus == paneProjects {
				a.armProjectDelete()
				return a, nil
			}
			return a, a.killSelectedAgent()
		}
		return a, nil
	}

	// The projects column claims its own keys first, exactly as in the
	// list view; everything it declines falls through to the view's own.
	if a.focus == paneProjects {
		if model, cmd, handled := a.handleProjectsKey(msg); handled {
			return model, cmd
		}
	}

	switch {
	// ctrl+c always quits; `q` closes the view like esc.
	case key.Matches(msg, a.keys.Quit) && msg.String() != "q":
		return a, tea.Quit
	case key.Matches(msg, a.keys.Quit), key.Matches(msg, a.keys.Agents):
		return a, a.leaveAgents()
	case key.Matches(msg, a.keys.Back):
		// esc backs out one pane at a time, like the detail pane.
		if a.focus == paneDetail {
			a.focus = paneTasks
			return a, nil
		}
		return a, a.leaveAgents()

	case key.Matches(msg, a.keys.Help):
		a.helpOpen = true
		return a, nil
	case key.Matches(msg, a.keys.Palette):
		a.openPalette()
		return a, nil
	case key.Matches(msg, a.keys.Note):
		return a, a.modal.Open(modalLog, true, "note", 0, "")
	case key.Matches(msg, a.keys.ToggleProjects):
		return a.toggleProjects()

	case key.Matches(msg, a.keys.ToggleCompleted):
		a.av.showEnded = !a.av.showEnded
		text := "ended sessions hidden"
		if a.av.showEnded {
			text = "ended sessions shown"
		}
		a.status = flash{text: text}
		sel, hadSel := a.selectedAgent()
		a.filterAgents()
		if hadSel {
			if i := slices.IndexFunc(a.av.rows, func(r agentRow) bool { return r.sess.ID == sel.sess.ID }); i >= 0 {
				a.av.cursor = i
			}
		}
		a.av.cursor = max(min(a.av.cursor, len(a.av.rows)-1), 0)
		return a, a.syncAgentDetail()

	case key.Matches(msg, a.keys.Sessions), key.Matches(msg, a.keys.ExpandToggle) && msg.String() != "tab":
		return a, a.joinSelectedAgent()
	// `v` from the list watches the run; in the pane it is the raw-log
	// toggle (RawLog shares the key), handled by handleAgentPaneKey.
	case key.Matches(msg, a.keys.ViewRun) && a.focus != paneDetail:
		if row, ok := a.selectedAgent(); ok && row.run != nil {
			return a, a.openRunView(*row.run)
		}
		a.status = flash{text: "not a workflow step session"}
		return a, nil
	case key.Matches(msg, a.keys.Delete):
		if _, ok := a.selectedAgent(); ok {
			a.deletePending = true
			a.resize()
		}
		return a, nil
	case msg.String() == "tab":
		if a.focus == paneDetail {
			a.focus = paneTasks
		} else if _, _, detailW := a.agentsWidths(); detailW > 0 {
			a.focus = paneDetail
		}
		return a, nil
	}

	if a.focus == paneDetail {
		return a.handleAgentPaneKey(msg)
	}
	return a.handleAgentListKey(msg)
}

// handleAgentListKey moves the session cursor and hands focus to the
// neighbouring columns: h to the projects column, l into the pane.
func (a app) handleAgentListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	move := func(to int) (tea.Model, tea.Cmd) {
		to = max(min(to, len(a.av.rows)-1), 0)
		if to == a.av.cursor {
			return a, nil
		}
		a.av.cursor = to
		return a, a.syncAgentDetail()
	}
	switch {
	case key.Matches(msg, a.keys.ScrollDown):
		return move(a.av.cursor + 1)
	case key.Matches(msg, a.keys.ScrollUp):
		return move(a.av.cursor - 1)
	case key.Matches(msg, a.keys.ExpandOpen):
		if _, _, detailW := a.agentsWidths(); detailW > 0 {
			a.focus = paneDetail
		}
		return a, nil
	case key.Matches(msg, a.keys.ExpandClose):
		a.focusProjects()
		return a, nil
	}
	switch msg.String() {
	case "G":
		return move(len(a.av.rows) - 1)
	case "g":
		return move(0)
	}
	return a, nil
}

// handleAgentPaneKey scrolls the right pane. For a headless session's log,
// scrolling away from the bottom stops the tail from yanking the view and
// scrolling back to it resumes, as in the run view; `v` flips raw and `h`
// hands the keyboard back to the sessions.
func (a app) handleAgentPaneKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, a.keys.ExpandClose):
		a.focus = paneTasks
		return a, nil
	case key.Matches(msg, a.keys.RawLog):
		a.av.raw = !a.av.raw
		a.renderAgentPane()
		return a, nil
	case key.Matches(msg, a.keys.ScrollDown):
		a.av.vp.ScrollDown(1)
	case key.Matches(msg, a.keys.ScrollUp):
		a.av.vp.ScrollUp(1)
	case key.Matches(msg, a.keys.PageDown):
		a.av.vp.PageDown()
	case key.Matches(msg, a.keys.PageUp):
		a.av.vp.PageUp()
	default:
		switch msg.String() {
		case "G":
			a.av.vp.GotoBottom()
		case "g":
			a.av.vp.GotoTop()
		default:
			return a, nil
		}
	}
	if row, ok := a.selectedAgent(); ok && row.headless() {
		a.av.follow = a.av.vp.AtBottom()
	}
	return a, nil
}

// --- controls -------------------------------------------------------------

// joinSelectedAgent hands the terminal to the selected session, through
// the same guard the `r` picker uses: a headless session is refused while
// its run is live and not paused.
func (a app) joinSelectedAgent() tea.Cmd {
	row, ok := a.selectedAgent()
	if !ok {
		return nil
	}
	return a.resumeGuardedCmd(row.sess.Session)
}

// killSelectedAgent ends the selected session. An interactive one is
// killed through its tmux session, then marked ended -- the status write
// is what the list shows, and the poller would make the same one on its
// next tick once has-session says no. A headless one is ended through its
// run: cancelling it makes the runner SIGTERM the step, so the process
// stays the runner's to stop. A headless session whose run is not running
// anything (paused or over) has no process to kill, so only its status
// moves. Never DeleteSession: a session that ran is the task's history.
func (a app) killSelectedAgent() tea.Cmd {
	row, ok := a.selectedAgent()
	if !ok {
		return nil
	}
	sess := row.sess.Session
	a.av.selectID = sess.ID
	if sess.Headless() {
		if row.run != nil && !row.run.State.Terminal() && row.run.State != workflow.RunPaused {
			runID := row.run.ID
			return a.mutate(flash{kind: flashDone, text: fmt.Sprintf("run %d cancelled — the runner is stopping its step", runID)},
				func() error { return a.store.SetRunState(a.ctx, runID, workflow.RunCancelled) })
		}
		if sess.Status == task.SessionEnded {
			return statusCmd(flash{text: "session already ended"})
		}
		return a.mutate(flash{kind: flashDone, text: "session marked ended"}, func() error {
			return a.store.SetSessionStatus(a.ctx, sess.ExternalID, task.SessionEnded)
		})
	}
	// A row from before tmux-backed sessions has no stored name; derive
	// the one it would have had, as resumeSessionCmd does.
	name := sess.TmuxSession
	if name == "" {
		name = agent.SessionName(sess.ExternalID)
	}
	return a.mutate(flash{kind: flashDone, text: "killed " + sess.Label}, func() error {
		// A session that is not there is already in the desired state, so
		// tmux's refusal is not an error worth stopping for; the status
		// write below is what matters.
		_ = killSession(name)
		return a.store.SetSessionStatus(a.ctx, sess.ExternalID, task.SessionEnded)
	})
}

// --- view -----------------------------------------------------------------

// agentsWidths splits the body: the projects column when it is on screen,
// then the sessions and the pane. Below the split width the pane is
// dropped rather than crushed, and the sessions take the room.
func (a app) agentsWidths() (projW, listW, detailW int) {
	rest := max(a.width, 20)
	if a.projectsVisible() {
		projW = projectsPaneWidth
		rest -= projW + 1
	}
	if rest < detailSplitMinWidth {
		return projW, rest, 0
	}
	listW = rest * 45 / 100
	return projW, listW, rest - listW - 1
}

// agentsSplits is the divider columns, for the horizontal rules to tee.
func (a app) agentsSplits() []int {
	projW, listW, detailW := a.agentsWidths()
	var splits []int
	x := 0
	if projW > 0 {
		splits = append(splits, projW)
		x = projW + 1
	}
	if detailW > 0 {
		splits = append(splits, x+listW)
	}
	return splits
}

// agentsBody renders the columns fitted to bodyHeight rows.
func (a app) agentsBody() string {
	h := max(a.bodyHeight, 1)
	projW, listW, detailW := a.agentsWidths()
	var cols []string
	if projW > 0 {
		cols = append(cols, a.projectsView(), a.verticalDivider(a.focus == paneProjects))
	}
	cols = append(cols, strings.Join(fitPane(a.agentListLines(listW), listW, h, a.agentListScroll(h, listW)), "\n"))
	if detailW > 0 {
		cols = append(cols, a.verticalDivider(a.focus == paneDetail),
			strings.Join(fitPane(a.agentPaneLines(), detailW, h, 0), "\n"))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
}

// agentListHeaderRows is how many lines precede the first session row.
const agentListHeaderRows = 2

// agentListScroll keeps the selected row on screen when the list outgrows
// the body.
func (a app) agentListScroll(height, width int) int {
	total := len(a.agentListLines(width))
	if total <= height {
		return 0
	}
	line := agentListHeaderRows + a.av.cursor
	if line < height {
		return 0
	}
	return min(line-height+1, total-height)
}

// agentListLines lays out the sessions column: heading, then one row per
// session -- status glyph, label, its task, and how long since it was
// last active, with a headless marker for a workflow step's session --
// in the picker's mould, since that is the row the user already reads.
func (a app) agentListLines(width int) []string {
	s, g := a.styles, a.styles.Glyphs
	focused := a.focus == paneTasks
	heading := "  " + s.SubHeader.Render("SESSIONS")
	if n := a.hiddenAgents(); n > 0 {
		heading += s.Faint.Render(fmt.Sprintf("  %d ended hidden", n))
	}
	lines := []string{"", heading}

	if len(a.av.rows) == 0 {
		scope := "any project"
		if p, ok := a.selectedProject(); ok {
			scope = p.Name
		}
		lines = append(lines, "", "  "+s.Muted.Render("no live sessions in "+scope))
		if a.hiddenAgents() > 0 {
			lines = append(lines, "  "+s.Muted.Render("press ")+s.FooterKey.Render("C")+
				s.Muted.Render(" to show ended sessions"))
		} else {
			lines = append(lines, "  "+s.Muted.Render("press ")+s.FooterKey.Render("r")+
				s.Muted.Render(" on a task to launch one"))
		}
		return lines
	}

	now := time.Now()
	for i, r := range a.av.rows {
		selected := i == a.av.cursor
		gutter, gutterStyle := "  ", s.Normal
		if selected {
			gutter, gutterStyle = g.SelBar+" ", s.SelBar
		}
		mark, markStyle := sessionStatusCell(s, r.sess.Status)
		right := relTime(r.sess.LastActiveAt, now)
		if r.headless() {
			right = "headless · " + right
		}
		labelStyle, taskStyle := s.Dimmed, s.Faint
		switch {
		case selected && focused:
			labelStyle, taskStyle = s.Title.Bold(true), s.Dimmed
		case selected:
			labelStyle, taskStyle = s.Title, s.Dimmed
		}
		// gutter, glyph and a space, then the text, two spaces, the age.
		textW := max(width-runeWidth(gutter)-2-runeWidth(right)-2, 8)
		label := truncTail(r.sess.Label, textW, g.Ellipsis)
		text := labelStyle.Render(label)
		if rem := textW - runeWidth(label) - 3; rem >= 4 {
			text += s.Muted.Render(" · ") + taskStyle.Render(truncTail(r.sess.TaskTitle, rem, g.Ellipsis))
		}
		gap := max(width-runeWidth(gutter)-2-lipgloss.Width(text)-runeWidth(right), 1)
		lines = append(lines, gutterStyle.Render(gutter)+markStyle.Render(mark)+" "+text+
			strings.Repeat(" ", gap)+s.Faint.Render(right))
	}
	return lines
}

// agentPaneHeading is the block above the pane's scrolling content: what
// the session is, whose it is, where it ran, and when.
func (a app) agentPaneHeading(width int) []string {
	s := a.styles
	row, ok := a.selectedAgent()
	if !ok {
		return []string{"", "  " + s.Muted.Render("no session selected")}
	}
	sess := row.sess
	now := time.Now()
	mark, markStyle := sessionStatusCell(s, sess.Status)
	status := markStyle.Render(mark) + " " + markStyle.Bold(true).Render(string(sess.Status))
	if row.headless() {
		status += s.Muted.Render(" · headless")
	} else if sess.TmuxSession != "" {
		status += s.Muted.Render(" · tmux " + sess.TmuxSession)
	}
	field := func(name, value string) string {
		return "  " + s.DetailLabel.Render(fmt.Sprintf("%-8s", name)) + value
	}
	lines := []string{
		"",
		"  " + status,
		"  " + s.Title.Bold(true).Render(truncTail(sess.Label, max(width-4, 10), s.Glyphs.Ellipsis)),
		field("task", s.DetailID.Render(fmt.Sprintf("#%d", sess.TaskID))+"  "+
			s.Dimmed.Render(truncTail(sess.TaskTitle, max(width-20, 10), s.Glyphs.Ellipsis))),
		field("cwd", s.Dimmed.Render(tildePath(sess.Cwd))),
	}
	if row.run != nil {
		name := row.workflow
		if name == "" {
			name = "workflow"
		}
		runMark, runStyle := runStateCell(s, row.run.State)
		text := s.Dimmed.Render(fmt.Sprintf("%d · %s", row.run.ID, name))
		if row.step != "" {
			text += s.Dimmed.Render(" · " + row.step)
		}
		text += "  " + runStyle.Render(runMark+" "+string(row.run.State)) +
			s.Muted.Render("  v to watch")
		lines = append(lines, field("run", text))
	}
	lines = append(lines,
		field("id", s.Faint.Render(sess.ExternalID)),
		field("started", s.DetailFaint.Render(sess.StartedAt.Local().Format("Jan _2 15:04")+
			" · active "+relTime(sess.LastActiveAt, now))),
		"")
	if row.headless() {
		mode := "rendered"
		if a.av.raw {
			mode = "raw"
		}
		if a.av.follow {
			mode += " · following"
		}
		heading := "  " + s.SubHeader.Render("LOG")
		gap := max(width-lipgloss.Width(heading)-lipgloss.Width(mode)-2, 1)
		lines = append(lines, heading+strings.Repeat(" ", gap)+s.Faint.Render(mode))
	}
	return lines
}

// agentPaneLines is the whole right pane: the heading block, then the
// viewport with the log or the task detail.
func (a app) agentPaneLines() []string {
	_, _, detailW := a.agentsWidths()
	lines := a.agentPaneHeading(detailW)
	return append(lines, strings.Split(a.av.vp.View(), "\n")...)
}

// renderAgentPane rebuilds the pane's viewport for the selected session:
// its log for a headless one (bottom kept in view while following), its
// task's detail pane otherwise. Sized here rather than in resize because
// the heading above it varies with the row.
func (a *app) renderAgentPane() {
	_, _, detailW := a.agentsWidths()
	if detailW <= 0 {
		return
	}
	heading := a.agentPaneHeading(detailW)
	a.av.vp.SetWidth(max(detailW, 10))
	a.av.vp.SetHeight(max(a.bodyHeight-len(heading), 1))

	row, ok := a.selectedAgent()
	var out []string
	switch {
	case !ok:
		out = nil
	case row.headless():
		lines := a.av.log.rendered
		if a.av.raw {
			lines = a.av.log.raw
		}
		switch {
		case a.av.log.path == "":
			out = []string{a.styles.Muted.Render("no log for this step")}
		case len(lines) == 0:
			out = []string{a.styles.Muted.Render("waiting for output…")}
		default:
			out = renderLogLines(lines, a.av.raw, detailW, a.styles)
		}
		for i := range out {
			out[i] = " " + out[i]
		}
		wasBottom := a.av.vp.AtBottom()
		a.av.vp.SetContent(strings.Join(out, "\n"))
		if a.av.follow || wasBottom {
			a.av.vp.GotoBottom()
		}
		return
	case a.av.detail.sessionID != row.sess.ID:
		out = []string{"  " + a.styles.Muted.Render("loading task…")}
	default:
		d := a.av.detail
		out = []string{renderDetail(d.t, d.children, d.blockers, d.blocking, d.log, d.sessions, d.runs, d.tags,
			a.av.renderer, a.styles, detailW)}
	}
	a.av.vp.SetContent(strings.Join(out, "\n"))
}

// agentsHints is the footer for the view, per focused pane.
func (a app) agentsHints() [][2]string {
	switch a.focus {
	case paneProjects:
		return [][2]string{
			{"j/k", "switch project"}, {"l/⏎", "to sessions"}, {"[", "hide"}, {"esc/q", "back"}, {"?", "help"},
		}
	case paneDetail:
		hints := [][2]string{{"j/k", "scroll"}, {"h/esc", "to sessions"}}
		if row, ok := a.selectedAgent(); ok && row.headless() {
			hints = append(hints, [2]string{"v", "raw"})
		}
		return append(hints, [2]string{"⏎", "join"}, [2]string{"dd", "kill"}, [2]string{"?", "help"})
	}
	hints := [][2]string{{"j/k", "move"}, {"⏎/r", "join"}, {"dd", "kill"}}
	if row, ok := a.selectedAgent(); ok && row.run != nil {
		hints = append(hints, [2]string{"v", "watch run"})
	}
	ended := "show ended"
	if a.av.showEnded {
		ended = "hide ended"
	}
	return append(hints, [2]string{"C", ended}, [2]string{"h/l", "projects / pane"},
		[2]string{"esc/q", "back"}, [2]string{"?", "help"})
}
