package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// Watching a workflow run (tend task #183). The TUI never drives a run;
// it reads what the runner writes to SQLite -- the run's state, its step
// runs, their sessions -- and the stream-json log each agent step leaves
// behind. Three surfaces read from here:
//
//   - the detail pane's WORKFLOWS section (renderDetail), one row per run
//     on the task, built from runSummary;
//   - the run view's RUNS sidebar, the same rows as a column to switch
//     between the task's runs without leaving the view;
//   - the run view (modeRun): the task's runs in a sidebar on the left,
//     the watched run's step runs in the middle, the selected step's log
//     on the right, tail-following while it runs. `v` opens it on the
//     task's latest run; h/l move between the three panes.
//
// Controls write exactly what the CLI would: SetRunState for pause and
// cancel (the runner polls for both), FinishStepRun for a gate decision
// (the same call finish_step makes), runner.Resume for a paused run, and
// resumeSessionCmd for a takeover of a paused step's session. Nothing
// here polls on its own timer: the session poller's tick (pollRuns in
// sessions.go) reports run and log changes and the view reloads on it.

// runSummary is one run as the detail pane and picker show it: the row
// plus the names and liveness facts that need extra reads to know.
type runSummary struct {
	run      workflow.Run
	workflow string            // the workflow's name; "" if it could not be read
	step     string            // the current step's name; "" before the first step
	stepRun  *workflow.StepRun // the current step run, nil before the first step
	// lastOutput is the current step's log mtime -- when the step last
	// wrote anything -- zero when there is no log yet. A stat, not a
	// parse: it separates a hung step from a busy one at a glance.
	lastOutput time.Time
	// runnerGone is set for a run whose state says a runner owns it
	// (running, waiting_review) while no tmux session hosts one: it died
	// with its host or was killed, and `tend workflow resume` (or `p`
	// after pausing) is what brings it back. Display only; the poller
	// never writes a state for it, since running-with-no-runner is
	// exactly what resume re-enters.
	runnerGone bool
}

// live reports whether the run is in a non-terminal state.
func (r runSummary) live() bool { return !r.run.State.Terminal() }

// runnerAlive asks tmux whether a run's runner session exists -- the same
// seam sessionAlive is for interactive sessions, and a package-level var
// for the same reason: it fires from loads the tests drive, and without a
// stub every test with a running run would reach for the real tmux
// server. It fails closed like sessionAlive: with no way to ask tmux, a
// runner is assumed present rather than reported gone.
var runnerAlive = func(run workflow.Run) bool {
	confPath, err := agent.WriteConfig()
	if err != nil {
		return true
	}
	return agent.HasSession(agent.RunnerSessionName(run.ID), confPath)
}

// resumeRunner is runner.Resume behind a seam, for the same reason
// launchRunner is one: `p` on a paused run starts a tmux session in
// production and must not in tests.
var resumeRunner = func(ctx context.Context, s runner.LaunchStore, runID int64, dbPath string) (string, error) {
	return runner.Resume(ctx, s, runID, dbPath)
}

// summarizeRuns builds the summaries for a task's runs. Every lookup past
// the run row degrades rather than fails: a workflow that could not be
// read leaves the name empty, a step that could not be read leaves the
// step empty, and the row still shows. Runs the caller passes in are kept
// in their order (ListRunsForTask: newest first).
func (a app) summarizeRuns(ctx context.Context, runs []workflow.Run) []runSummary {
	out := make([]runSummary, 0, len(runs))
	for _, run := range runs {
		s := runSummary{run: run}
		if wf, err := a.store.GetWorkflow(ctx, run.WorkflowID); err == nil {
			s.workflow = wf.Name
		}
		if run.CurrentStepRunID != nil {
			if sr, err := a.store.GetStepRun(ctx, *run.CurrentStepRunID); err == nil {
				s.stepRun = &sr
				if st, err := a.store.GetStep(ctx, sr.StepID); err == nil {
					s.step = st.Name
				}
				s.lastOutput = logModTime(sr.LogPath)
			}
		}
		if run.State == workflow.RunRunning || run.State == workflow.RunWaitingReview {
			s.runnerGone = !runnerAlive(run)
		}
		out = append(out, s)
	}
	return out
}

// logModTime is when a step's log was last written, zero for no log.
func logModTime(path string) time.Time {
	if path == "" {
		return time.Time{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// runStateCell resolves the glyph and style for a run's state. A live
// state borrows the session glyph its state maps to (workflow.RunState.
// SessionStatus), so a running run in the WORKFLOWS section wears the
// same mark as a working session in SESSIONS above it; the terminal
// states borrow from the task-state family: done is a completion check,
// failed the blocked mark, cancelled the ended dot.
func runStateCell(s Styles, st workflow.RunState) (string, lipgloss.Style) {
	if sst, ok := st.SessionStatus(); ok {
		return sessionStatusCell(s, sst)
	}
	g := s.Glyphs
	switch st {
	case workflow.RunDone:
		return g.State[task.StateDone], s.CheckDone
	case workflow.RunFailed:
		return g.State[task.StateBlocked], s.State[task.StateBlocked]
	default:
		return g.Session[task.SessionEnded], s.Faint
	}
}

// fmtElapsed renders a duration the way a run's clock reads: seconds
// under a minute, minutes under an hour, hours and minutes past that.
func fmtElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// runElapsed is how long a run has been going, or took: started to ended
// when it has ended, started to now otherwise.
func runElapsed(run workflow.Run, now time.Time) time.Duration {
	if run.EndedAt != nil {
		return run.EndedAt.Sub(run.StartedAt)
	}
	return now.Sub(run.StartedAt)
}

// runSummaryLine is the text after the glyph on a WORKFLOWS or picker
// row: workflow · step, then state, elapsed, and the liveness facts. meta
// comes back styled, in metaStyle except for the two facts that must not
// read as quiet: a gate waiting for review, which wears the blocked style
// so it stands out in the detail pane the way the gutter marker does, and
// a runner that is gone.
func runSummaryLine(s Styles, r runSummary, now time.Time, metaStyle lipgloss.Style) (name, meta string) {
	name = r.workflow
	if name == "" {
		name = fmt.Sprintf("run %d", r.run.ID)
	}
	if r.step != "" {
		name += s.Muted.Render(" · ") + r.step
	}
	state := metaStyle.Render(string(r.run.State))
	if r.run.State == workflow.RunWaitingReview {
		state = s.State[task.StateBlocked].Bold(true).Render(string(r.run.State))
	}
	parts := []string{state, metaStyle.Render(fmtElapsed(runElapsed(r.run, now)))}
	if r.live() && !r.lastOutput.IsZero() {
		parts = append(parts, metaStyle.Render("output "+relTime(r.lastOutput, now)))
	}
	if r.runnerGone {
		parts = append(parts, s.State[task.StateBlocked].Render("runner gone"))
	}
	return name, strings.Join(parts, metaStyle.Render(" · "))
}

// --- opening the view (`v`) -----------------------------------------------

// loadRunsForView fetches a task's runs and their summaries off the
// update loop; openRunViewForTask then lands the view on the latest.
func (a app) loadRunsForView(t task.Task) tea.Cmd {
	return func() tea.Msg {
		runs, err := a.store.ListRunsForTask(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		return runsForViewMsg{t: t, runs: a.summarizeRuns(a.ctx, runs)}
	}
}

// openRunViewForTask opens the view on the task's latest run (the runs
// come newest first) with every run in the sidebar, so an older one is a
// j away rather than a picker. A task with no runs flashes instead.
func (a *app) openRunViewForTask(msg runsForViewMsg) tea.Cmd {
	a.runsCache[msg.t.ID] = msg.runs
	if len(msg.runs) == 0 {
		a.status = flash{text: fmt.Sprintf("no workflow runs on #%d — press w to start one", msg.t.ID)}
		return nil
	}
	cmd := a.openRunView(msg.runs[0].run)
	a.rv.runs = msg.runs
	return cmd
}

// --- run view -------------------------------------------------------------

// rvPane is which run-view column owns the keyboard: the task's runs, the
// watched run's steps, or the selected step's log. h and l move left and
// right across them, as they do between every other pair of panes in the
// app.
type rvPane int

const (
	rvPaneRuns  rvPane = iota // the RUNS sidebar
	rvPaneSteps               // the watched run's step runs
	rvPaneLog                 // the selected step's log
)

// runView is the state of modeRun: the run being watched, the task's
// other runs beside it, its step runs, which one is selected, and the
// selected step's log.
type runView struct {
	runID     int64
	run       workflow.Run
	workflow  string
	stepRuns  []workflow.StepRun
	stepNames map[int64]string
	stepKinds map[int64]workflow.StepKind
	// stepOutcomes is each step's edge outcomes, for the gate keys: a
	// decision is only offered among them.
	stepOutcomes map[int64][]string
	runnerGone   bool

	// runs is the sidebar: every run on the watched run's task, newest
	// first, reloaded with the run so their states stay current. runCursor
	// is the sidebar row of the watched run.
	runs      []runSummary
	runCursor int

	cursor int  // selected step run index
	follow bool // the cursor tracks the current step as the run advances
	focus  rvPane
	raw    bool // `v`: raw stream-json lines rather than the rendering
	log    runLog
	vp     viewport.Model
}

// runLog is the selected step's log as read so far. offset is the byte
// past the last complete line consumed, so a tail re-read picks up where
// this left off; a partial trailing line waits for its newline.
type runLog struct {
	stepRunID int64
	path      string
	offset    int64
	rendered  []string
	raw       []string
}

// current is the run's current step run, per current_step_run_id.
func (rv runView) current() (workflow.StepRun, bool) {
	if rv.run.CurrentStepRunID == nil {
		return workflow.StepRun{}, false
	}
	for _, sr := range rv.stepRuns {
		if sr.ID == *rv.run.CurrentStepRunID {
			return sr, true
		}
	}
	return workflow.StepRun{}, false
}

// selected is the step run under the cursor.
func (rv runView) selected() (workflow.StepRun, bool) {
	if rv.cursor < 0 || rv.cursor >= len(rv.stepRuns) {
		return workflow.StepRun{}, false
	}
	return rv.stepRuns[rv.cursor], true
}

// openRunView switches into the view on run and loads it, remembering
// which view to go back to: the agents view, or the list.
func (a *app) openRunView(run workflow.Run) tea.Cmd {
	a.rvBack = modeList
	if a.mode == modeAgents {
		a.rvBack = modeAgents
	}
	a.mode = modeRun
	a.cancelPending = false
	a.rv = runView{runID: run.ID, run: run, follow: true, focus: rvPaneSteps, vp: viewport.New()}
	a.resize()
	return a.loadRunView(run.ID)
}

// switchRun points the open view at another of the task's runs, from the
// sidebar. The sidebar and its focus stay; everything about the watched
// run -- step runs, cursor, log -- starts over, and the load fills it in.
func (a *app) switchRun(run workflow.Run) tea.Cmd {
	if run.ID == a.rv.runID {
		return nil
	}
	a.cancelPending = false
	a.rv.runID, a.rv.run, a.rv.workflow = run.ID, run, ""
	a.rv.stepRuns, a.rv.cursor, a.rv.follow = nil, 0, true
	a.rv.runnerGone = false
	a.rv.log = runLog{}
	a.renderRunLog()
	return a.loadRunView(run.ID)
}

// leaveRunView returns to the view `v` was pressed in.
func (a *app) leaveRunView() tea.Cmd {
	a.cancelPending = false
	if a.rvBack == modeAgents {
		a.startAgents()
		return a.loadAgentSessions()
	}
	a.mode = modeList
	a.resize()
	return a.loadTasks(modeList)
}

// loadRunView fetches everything the view shows except the log: the run,
// its workflow's name, its step runs and their steps' names, kinds and
// outcomes, whether a runner is hosting it, and the task's runs for the
// sidebar (so a run that ends or starts while watching shows up there).
func (a app) loadRunView(runID int64) tea.Cmd {
	return func() tea.Msg {
		run, err := a.store.GetRun(a.ctx, runID)
		if err != nil {
			return errMsg{err}
		}
		msg := runViewLoadedMsg{run: run, stepNames: map[int64]string{},
			stepKinds: map[int64]workflow.StepKind{}, stepOutcomes: map[int64][]string{}}
		if wf, err := a.store.GetWorkflow(a.ctx, run.WorkflowID); err == nil {
			msg.workflow = wf.Name
		}
		// The sidebar degrades rather than fails: with the runs unreadable
		// the view still shows the run it was opened on.
		if runs, err := a.store.ListRunsForTask(a.ctx, run.TaskID); err == nil {
			msg.runs = a.summarizeRuns(a.ctx, runs)
		}
		msg.stepRuns, err = a.store.ListStepRunsForRun(a.ctx, runID)
		if err != nil {
			return errMsg{err}
		}
		for _, sr := range msg.stepRuns {
			if _, seen := msg.stepNames[sr.StepID]; seen {
				continue
			}
			// A step deleted since the run ended still lists its runs; it
			// just has no name to show.
			if st, err := a.store.GetStep(a.ctx, sr.StepID); err == nil {
				msg.stepNames[sr.StepID] = st.Name
				msg.stepKinds[sr.StepID] = st.Kind
			} else {
				msg.stepNames[sr.StepID] = fmt.Sprintf("step %d", sr.StepID)
			}
			if edges, err := a.store.OutgoingEdges(a.ctx, sr.StepID); err == nil {
				for _, e := range edges {
					msg.stepOutcomes[sr.StepID] = append(msg.stepOutcomes[sr.StepID], e.Outcome)
				}
			}
		}
		if run.State == workflow.RunRunning || run.State == workflow.RunWaitingReview {
			msg.runnerGone = !runnerAlive(run)
		}
		return msg
	}
}

// applyRunView installs a loaded run and settles the cursors: the sidebar
// on the watched run, the step cursor on the current step while
// following, else clamped. It then issues the log read for the selected
// step -- a tail from where the last read stopped when it is the same
// step, a fresh read otherwise.
func (a *app) applyRunView(msg runViewLoadedMsg) tea.Cmd {
	rv := &a.rv
	rv.run, rv.workflow, rv.stepRuns = msg.run, msg.workflow, msg.stepRuns
	rv.stepNames, rv.stepKinds, rv.stepOutcomes = msg.stepNames, msg.stepKinds, msg.stepOutcomes
	rv.runnerGone = msg.runnerGone
	if msg.runs != nil {
		rv.runs = msg.runs
	}
	if i := slices.IndexFunc(rv.runs, func(r runSummary) bool { return r.run.ID == rv.runID }); i >= 0 {
		rv.runCursor = i
	}
	rv.runCursor = max(min(rv.runCursor, len(rv.runs)-1), 0)
	if rv.follow {
		if cur, ok := rv.current(); ok {
			rv.cursor = slices.IndexFunc(rv.stepRuns, func(sr workflow.StepRun) bool { return sr.ID == cur.ID })
		}
	}
	rv.cursor = max(min(rv.cursor, len(rv.stepRuns)-1), 0)
	return a.loadSelectedLog()
}

// loadSelectedLog reads the selected step's log: from the last offset
// when it is the step already shown, from the start otherwise. A gate
// has no log, and the pane says so instead.
func (a *app) loadSelectedLog() tea.Cmd {
	sr, ok := a.rv.selected()
	if !ok {
		a.rv.log = runLog{}
		a.renderRunLog()
		return nil
	}
	if sr.ID != a.rv.log.stepRunID || sr.LogPath != a.rv.log.path {
		a.rv.log = runLog{stepRunID: sr.ID, path: sr.LogPath}
		a.renderRunLog()
	}
	if sr.LogPath == "" {
		// No log means the pane is built from the row itself (a gate's
		// decision and feedback), which moves without any file changing,
		// so it is rebuilt on every load rather than only on a new path.
		a.renderRunLog()
		return nil
	}
	return loadRunLog(sr.ID, sr.LogPath, a.rv.log.offset)
}

// loadRunLog reads the log at path from offset to its end and renders the
// complete lines it finds. A missing file is not an error -- the step
// has not written yet -- and a file shorter than offset (rewritten) is
// read again from the start. The resulting message carries the offset it
// started from, so Update can tell an append from a stale read.
func loadRunLog(stepRunID int64, path string, offset int64) tea.Cmd {
	return func() tea.Msg {
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return runLogLoadedMsg{stepRunID: stepRunID, path: path, from: offset, to: offset}
		}
		if err != nil {
			return errMsg{fmt.Errorf("opening step log: %w", err)}
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return errMsg{fmt.Errorf("reading step log: %w", err)}
		}
		if fi.Size() < offset {
			offset = 0
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return errMsg{fmt.Errorf("reading step log: %w", err)}
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return errMsg{fmt.Errorf("reading step log: %w", err)}
		}
		// Only whole lines: a partial trailing line is a step mid-write,
		// and it is picked up once its newline lands.
		end := bytes.LastIndexByte(data, '\n')
		if end < 0 {
			return runLogLoadedMsg{stepRunID: stepRunID, path: path, from: offset, to: offset}
		}
		data = data[:end+1]
		msg := runLogLoadedMsg{stepRunID: stepRunID, path: path, from: offset, to: offset + int64(len(data))}
		for _, line := range bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			msg.raw = append(msg.raw, string(line))
			msg.rendered = append(msg.rendered, agent.RenderStreamLine(line)...)
		}
		return msg
	}
}

// applyRunLog folds a log read into the view. A read for a step no longer
// selected, or from an offset that is not where the log currently ends,
// is stale and dropped -- the selection moved, or a fresher read landed
// first. A read from zero replaces (the file was rewritten or is being
// shown for the first time); any other appends.
func (a *app) applyRunLog(msg runLogLoadedMsg) {
	if a.rv.log.apply(msg) {
		a.renderRunLog()
	}
}

// apply folds a log read into lg, reporting whether it applied. Shared by
// the run view and the agents view, which tail the same logs.
func (lg *runLog) apply(msg runLogLoadedMsg) bool {
	if msg.stepRunID != lg.stepRunID || msg.path != lg.path || (msg.from != lg.offset && msg.from != 0) {
		return false
	}
	if msg.from == 0 {
		lg.rendered, lg.raw = nil, nil
	}
	lg.offset = msg.to
	lg.rendered = append(lg.rendered, msg.rendered...)
	lg.raw = append(lg.raw, msg.raw...)
	return true
}

// renderLogLines styles and wraps log lines to a pane: tool lines in the
// accent when rendered (raw lines are left as they are).
func renderLogLines(lines []string, raw bool, width int, s Styles) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		styled := l
		if !raw && strings.HasPrefix(l, agent.ToolGlyph+" ") {
			styled = s.Accent.Render(l)
		}
		out = append(out, strings.Split(ansi.Wrap(styled, max(width-2, 10), ""), "\n")...)
	}
	return out
}

// renderRunLog rebuilds the log viewport's content from the selected
// step's lines, wrapped to the pane, and keeps the bottom in view while
// following.
func (a *app) renderRunLog() {
	rv := &a.rv
	_, _, w := a.runViewWidths()
	lines := rv.log.rendered
	if rv.raw {
		lines = rv.log.raw
	}
	var out []string
	switch {
	case rv.cursor < 0 || rv.cursor >= len(rv.stepRuns):
		out = []string{a.styles.Muted.Render("no step selected")}
	case rv.log.path == "":
		sr := rv.stepRuns[rv.cursor]
		if rv.stepKinds[sr.StepID] == workflow.StepGate {
			out = a.gatePaneLines(sr)
		} else {
			out = []string{a.styles.Muted.Render("no log for this step yet")}
		}
	case len(lines) == 0:
		out = []string{a.styles.Muted.Render("waiting for output…")}
	default:
		out = renderLogLines(lines, rv.raw, w, a.styles)
	}
	for i := range out {
		out[i] = " " + out[i]
	}
	wasBottom := rv.vp.AtBottom()
	rv.vp.SetContent(strings.Join(out, "\n"))
	if rv.follow || wasBottom {
		rv.vp.GotoBottom()
	}
}

// gatePaneLines is what the log pane shows for a gate step: its reviewer
// text, the decision it is waiting on or took (with the feedback that
// went with it), and the keys that apply to the outcomes it routes.
func (a app) gatePaneLines(sr workflow.StepRun) []string {
	s := a.styles
	var out []string
	if sr.Finished() {
		out = append(out, s.Title.Render("gate decided: ")+s.Accent.Render(sr.Outcome))
		if strings.TrimSpace(sr.Deliverable) != "" {
			out = append(out, "", s.SubHeader.Render("FEEDBACK"))
			out = append(out, strings.Split(strings.TrimRight(sr.Deliverable, "\n"), "\n")...)
		}
	} else {
		out = append(out, s.State[task.StateBlocked].Bold(true).Render("gate is waiting for review"))
		outcomes := a.rv.stepOutcomes[sr.StepID]
		if len(outcomes) > 0 {
			out = append(out, s.Muted.Render("routes: "+strings.Join(outcomes, ", ")))
		}
		out = append(out, gateKeyHints(s, outcomes))
	}
	if strings.TrimSpace(sr.Input) != "" {
		out = append(out, "", s.SubHeader.Render("INPUT"))
		out = append(out, strings.Split(strings.TrimRight(sr.Input, "\n"), "\n")...)
	}
	if strings.TrimSpace(sr.PromptRendered) != "" {
		out = append(out, "", s.SubHeader.Render("REVIEWER NOTES"))
		out = append(out, strings.Split(strings.TrimRight(sr.PromptRendered, "\n"), "\n")...)
	}
	return out
}

// gateKeyHints is the key line under a waiting gate: `a` and `x` when the
// gate routes approve and reject (or has no edges, when either ends the
// run), and `o` for the picker whenever it routes anything else.
func gateKeyHints(s Styles, outcomes []string) string {
	direct := func(o string) bool { return len(outcomes) == 0 || slices.Contains(outcomes, o) }
	var parts []string
	if direct(workflow.OutcomeApprove) {
		parts = append(parts, s.FooterKey.Render("a")+s.Muted.Render(" approve"))
	}
	if direct(workflow.OutcomeReject) {
		parts = append(parts, s.FooterKey.Render("x")+s.Muted.Render(" reject with feedback"))
	}
	if slices.ContainsFunc(outcomes, func(o string) bool {
		return o != workflow.OutcomeApprove && o != workflow.OutcomeReject
	}) {
		parts = append(parts, s.FooterKey.Render("o")+s.Muted.Render(" pick outcome"))
	}
	return strings.Join(parts, "   ")
}

// --- keys -----------------------------------------------------------------

// handleRunViewKey owns the keyboard in the run view.
func (a app) handleRunViewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// The pending `c` chord: a second `c` cancels the run, anything else
	// backs out. A cancel kills the step and ends the run for good, so it
	// costs two presses like `dd`.
	if a.cancelPending {
		a.cancelPending = false
		a.resize()
		if key.Matches(msg, a.keys.CancelRun) {
			return a, a.cancelRun()
		}
		return a, nil
	}

	switch {
	case key.Matches(msg, a.keys.Quit) && msg.String() != "q":
		return a, tea.Quit
	case key.Matches(msg, a.keys.Quit):
		return a, a.leaveRunView()
	case key.Matches(msg, a.keys.Back):
		// esc backs out of the log pane before leaving the view.
		if a.rv.focus == rvPaneLog {
			a.rv.focus = rvPaneSteps
			return a, nil
		}
		return a, a.leaveRunView()
	case key.Matches(msg, a.keys.Help):
		a.helpOpen = true
		return a, nil
	case key.Matches(msg, a.keys.Palette):
		a.openPalette()
		return a, nil
	case key.Matches(msg, a.keys.Note):
		return a, a.modal.Open(modalLog, true, "note", 0, "")

	// h / l walk the panes: runs → steps → log and back. tab keeps its
	// steps ↔ log flip (from the sidebar it lands on the steps).
	case key.Matches(msg, a.keys.ExpandClose):
		if a.rv.focus > rvPaneRuns {
			a.rv.focus--
		}
		return a, nil
	case key.Matches(msg, a.keys.ExpandOpen):
		if a.rv.focus < rvPaneLog {
			a.rv.focus++
		}
		return a, nil
	case msg.String() == "tab", key.Matches(msg, a.keys.ExpandToggle):
		if a.rv.focus == rvPaneLog {
			a.rv.focus = rvPaneSteps
		} else {
			a.rv.focus = rvPaneLog
		}
		return a, nil
	case key.Matches(msg, a.keys.RawLog):
		a.rv.raw = !a.rv.raw
		a.renderRunLog()
		return a, nil

	case key.Matches(msg, a.keys.CancelRun):
		if a.rv.run.State.Terminal() {
			a.status = flash{text: fmt.Sprintf("run %d has already %s", a.rv.runID, a.rv.run.State)}
			return a, nil
		}
		a.cancelPending = true
		a.resize()
		return a, nil
	case key.Matches(msg, a.keys.PauseRun):
		return a, a.pauseOrResumeRun()
	case key.Matches(msg, a.keys.Approve):
		return a, a.decideGate(workflow.OutcomeApprove)
	case key.Matches(msg, a.keys.Reject):
		return a, a.decideGate(workflow.OutcomeReject)
	case key.Matches(msg, a.keys.Outcome):
		return a, a.pickGateOutcome()
	case key.Matches(msg, a.keys.Takeover):
		return a, a.takeoverStep()
	}

	switch a.rv.focus {
	case rvPaneRuns:
		return a.handleRunListKey(msg)
	case rvPaneLog:
		return a.handleRunLogKey(msg)
	}
	return a.handleRunStepsKey(msg)
}

// handleRunListKey moves the sidebar cursor; each move switches the view
// to that run, the way the projects column reloads the list as its cursor
// moves.
func (a app) handleRunListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	move := func(to int) (tea.Model, tea.Cmd) {
		to = max(min(to, len(a.rv.runs)-1), 0)
		if to == a.rv.runCursor || to >= len(a.rv.runs) {
			return a, nil
		}
		a.rv.runCursor = to
		return a, a.switchRun(a.rv.runs[to].run)
	}
	switch {
	case key.Matches(msg, a.keys.ScrollDown):
		return move(a.rv.runCursor + 1)
	case key.Matches(msg, a.keys.ScrollUp):
		return move(a.rv.runCursor - 1)
	}
	switch msg.String() {
	case "G":
		return move(len(a.rv.runs) - 1)
	case "g":
		return move(0)
	}
	return a, nil
}

// handleRunStepsKey moves the step cursor. Moving it by hand turns
// following off; landing back on the current step turns it on again, so
// the view resumes tracking the run without a separate key.
func (a app) handleRunStepsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	move := func(to int) (tea.Model, tea.Cmd) {
		to = max(min(to, len(a.rv.stepRuns)-1), 0)
		if to == a.rv.cursor {
			return a, nil
		}
		a.rv.cursor = to
		cur, ok := a.rv.current()
		a.rv.follow = ok && a.rv.stepRuns[to].ID == cur.ID
		return a, a.loadSelectedLog()
	}
	switch {
	case key.Matches(msg, a.keys.ScrollDown):
		return move(a.rv.cursor + 1)
	case key.Matches(msg, a.keys.ScrollUp):
		return move(a.rv.cursor - 1)
	case key.Matches(msg, a.keys.PageDown):
		a.rv.vp.PageDown()
		return a, nil
	case key.Matches(msg, a.keys.PageUp):
		a.rv.vp.PageUp()
		return a, nil
	}
	switch msg.String() {
	case "G":
		return move(len(a.rv.stepRuns) - 1)
	case "g":
		return move(0)
	}
	return a, nil
}

// handleRunLogKey scrolls the log. Scrolling away from the bottom stops
// the tail from yanking the view; scrolling back to it resumes.
func (a app) handleRunLogKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, a.keys.ScrollDown):
		a.rv.vp.ScrollDown(1)
	case key.Matches(msg, a.keys.ScrollUp):
		a.rv.vp.ScrollUp(1)
	case key.Matches(msg, a.keys.PageDown):
		a.rv.vp.PageDown()
	case key.Matches(msg, a.keys.PageUp):
		a.rv.vp.PageUp()
	default:
		switch msg.String() {
		case "G":
			a.rv.vp.GotoBottom()
		case "g":
			a.rv.vp.GotoTop()
		default:
			return a, nil
		}
	}
	if cur, ok := a.rv.current(); ok {
		if sel, ok := a.rv.selected(); ok && sel.ID == cur.ID {
			a.rv.follow = a.rv.vp.AtBottom()
		}
	}
	return a, nil
}

// --- controls -------------------------------------------------------------

// cancelRun writes cancelled; the runner kills the step on its next poll.
func (a app) cancelRun() tea.Cmd {
	runID := a.rv.runID
	return a.mutate(flash{kind: flashDone, text: fmt.Sprintf("run %d cancelled", runID)}, func() error {
		return a.store.SetRunState(a.ctx, runID, workflow.RunCancelled)
	})
}

// pauseOrResumeRun pauses a live run (the runner stops the step, leaving
// its session resumable) or resumes a paused one by starting a fresh
// runner, the way `tend workflow resume` does.
func (a app) pauseOrResumeRun() tea.Cmd {
	run := a.rv.run
	switch run.State {
	case workflow.RunRunning, workflow.RunWaitingReview:
		return a.mutate(flash{kind: flashEdit, text: fmt.Sprintf("run %d paused", run.ID)}, func() error {
			return a.store.SetRunState(a.ctx, run.ID, workflow.RunPaused)
		})
	case workflow.RunPaused:
		return func() tea.Msg {
			name, err := resumeRunner(a.ctx, a.store, run.ID, a.dbPath)
			if err != nil {
				return errMsg{err}
			}
			return refreshMsg{status: flash{kind: flashAdd, text: fmt.Sprintf("run %d resumed (%s)", run.ID, name)}}
		}
	case workflow.RunPending:
		return statusCmd(flash{text: fmt.Sprintf("run %d has not been claimed by a runner yet", run.ID)})
	}
	return statusCmd(flash{text: fmt.Sprintf("run %d has already %s", run.ID, run.State)})
}

// waitingGate is the gate step run the run is parked at, or the reason it
// is not: the current step is not a gate, it has already been decided, or
// the run is not waiting for review (paused, say, with the gate still
// current). Every gate control starts here.
func (a app) waitingGate() (workflow.StepRun, flash, bool) {
	cur, ok := a.rv.current()
	if !ok || a.rv.stepKinds[cur.StepID] != workflow.StepGate {
		return workflow.StepRun{}, flash{text: "the current step is not a gate"}, false
	}
	if cur.Finished() {
		return workflow.StepRun{}, flash{text: fmt.Sprintf("gate already decided: %s", cur.Outcome)}, false
	}
	if a.rv.run.State != workflow.RunWaitingReview {
		return workflow.StepRun{}, flash{text: fmt.Sprintf("run %d is %s, not waiting for review", a.rv.runID, a.rv.run.State)}, false
	}
	return cur, flash{}, true
}

// decideGate is `a` and `x`: it settles the waiting gate on outcome when
// the gate's edges route it. A gate that routes other outcomes instead
// opens the outcome picker over the ones it does, so a decision never
// silently ends the run for want of an edge. A gate with no edges at all
// accepts anything -- ending the run is what its author asked for.
func (a *app) decideGate(outcome string) tea.Cmd {
	cur, why, ok := a.waitingGate()
	if !ok {
		return statusCmd(why)
	}
	outcomes := a.rv.stepOutcomes[cur.StepID]
	if len(outcomes) > 0 && !slices.Contains(outcomes, outcome) {
		a.openGatePicker(cur, outcomes)
		return statusCmd(flash{text: fmt.Sprintf("%s routes %s, not %s — pick one",
			a.rv.stepNames[cur.StepID], strings.Join(outcomes, ", "), outcome)})
	}
	return a.applyGateOutcome(cur, outcome)
}

// applyGateOutcome records outcome on the gate's step run. A reject asks
// for feedback first (modalGateFeedback): the text becomes the gate's
// deliverable, which the runner hands to the step the reject edge loops
// back to as its {{.Feedback}}. Any other outcome is recorded at once
// with no deliverable, so the gate passes its input through.
func (a *app) applyGateOutcome(cur workflow.StepRun, outcome string) tea.Cmd {
	name := a.rv.stepNames[cur.StepID]
	if outcome == workflow.OutcomeReject {
		return a.modal.Open(modalGateFeedback, true, fmt.Sprintf("reject %s — feedback for the next step", name), cur.ID, outcome)
	}
	return a.finishGate(cur.ID, name, outcome, "")
}

// finishGate is the write every gate decision ends in: the same one-shot
// FinishStepRun the finish_step tool makes. The runner polls the row and
// routes on the outcome.
func (a app) finishGate(stepRunID int64, name, outcome, feedback string) tea.Cmd {
	text := fmt.Sprintf("%s: %s", name, outcome)
	if feedback != "" {
		text += " with feedback"
	}
	return a.mutate(flash{kind: flashDone, text: text}, func() error {
		return a.store.FinishStepRun(a.ctx, stepRunID, outcome, feedback)
	})
}

// pickGateOutcome is `o`: the picker over every outcome the waiting gate
// routes, for gates whose edges go beyond approve/reject (or for anyone
// who would rather see the choices than remember the keys).
func (a *app) pickGateOutcome() tea.Cmd {
	cur, why, ok := a.waitingGate()
	if !ok {
		return statusCmd(why)
	}
	outcomes := a.rv.stepOutcomes[cur.StepID]
	if len(outcomes) == 0 {
		return statusCmd(flash{text: fmt.Sprintf("%s has no edges; a (approve) or x (reject) ends the run",
			a.rv.stepNames[cur.StepID])})
	}
	a.openGatePicker(cur, outcomes)
	return nil
}

// --- gate outcome picker --------------------------------------------------

// openGatePicker arms the picker over outcomes for the gate step run cur.
func (a *app) openGatePicker(cur workflow.StepRun, outcomes []string) {
	a.gatePickerOpen, a.gatePickerStepRunID, a.gatePickerSel = true, cur.ID, 0
	a.gatePickerOutcomes = slices.Clone(outcomes)
}

func (a *app) closeGatePicker() {
	a.gatePickerOpen, a.gatePickerStepRunID, a.gatePickerSel = false, 0, 0
	a.gatePickerOutcomes = nil
}

// handleGatePickerKey owns the keyboard while the picker is open, in the
// other pickers' mould: arrows or ctrl-n/ctrl-p move, a digit picks
// directly, Enter picks the highlight, esc dismisses. A pick lands on the
// gate the picker was opened for; if the run has moved on meanwhile
// (someone else decided it), the pick is dropped rather than misapplied.
func (a app) handleGatePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	outcomes := a.gatePickerOutcomes
	pick := func(idx int) (tea.Model, tea.Cmd) {
		stepRunID := a.gatePickerStepRunID
		a.closeGatePicker()
		if idx < 0 || idx >= len(outcomes) {
			return a, nil
		}
		cur, why, ok := a.waitingGate()
		if !ok {
			return a, statusCmd(why)
		}
		if cur.ID != stepRunID {
			return a, statusCmd(flash{text: "the run moved on; the gate you were deciding is gone"})
		}
		return a, a.applyGateOutcome(cur, outcomes[idx])
	}
	switch msg.String() {
	case "esc":
		a.closeGatePicker()
		return a, nil
	case "enter":
		return pick(a.gatePickerSel)
	case "up", "ctrl+p", "k":
		if a.gatePickerSel > 0 {
			a.gatePickerSel--
		}
		return a, nil
	case "down", "ctrl+n", "j":
		if a.gatePickerSel < len(outcomes)-1 {
			a.gatePickerSel++
		}
		return a, nil
	}
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		if idx := int(msg.Text[0] - '1'); idx < len(outcomes) {
			return pick(idx)
		}
	}
	return a, nil
}

// gatePickerView renders the chooser: the gate named in the title, then
// its outcomes numbered in edge order. reject is marked as the one that
// asks for feedback.
func (a app) gatePickerView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)

	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	name := "gate"
	for _, sr := range a.rv.stepRuns {
		if sr.ID == a.gatePickerStepRunID {
			name = truncTail(a.rv.stepNames[sr.StepID], max(w-30, 10), g.Ellipsis)
		}
	}
	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.State[task.StateBlocked].Bold(true).Render(g.Session[task.SessionBlocked]+" ")+
		s.Title.Render("decide ")+s.Dimmed.Render(name)+
		s.Muted.Render("  ⏎ or type a number")))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))

	sel := min(a.gatePickerSel, len(a.gatePickerOutcomes)-1)
	for i, o := range a.gatePickerOutcomes {
		num := fmt.Sprintf("%d ", i+1)
		label := o
		if o == workflow.OutcomeReject {
			label += s.Muted.Render("  asks for feedback")
		}
		var content string
		if i == sel {
			content = s.SelBar.Render(g.SelBar+" ") + s.Accent.Render(num) + s.Title.Render(label)
		} else {
			content = "  " + s.Muted.Render(num) + s.Dimmed.Render(label)
		}
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}

// takeoverStep hands the current step's session to the user: the run
// must be paused first, so the runner has let go of the session, and the
// step must be an agent step with a session row. It then resumes that
// session exactly as `r` does, tmux-wrapped, with the terminal handed
// over. Resuming the run afterwards (`p`) continues the same session.
func (a app) takeoverStep() tea.Cmd {
	run := a.rv.run
	cur, ok := a.rv.current()
	if !ok || cur.SessionExternalID == "" {
		return statusCmd(flash{text: "the current step has no session to take over"})
	}
	if run.State != workflow.RunPaused {
		return statusCmd(flash{text: fmt.Sprintf("pause run %d first (p), then take over its step", run.ID)})
	}
	return func() tea.Msg {
		sessions, err := a.store.ListSessionsForTask(a.ctx, run.TaskID)
		if err != nil {
			return errMsg{err}
		}
		for _, sess := range sessions {
			if sess.ExternalID == cur.SessionExternalID {
				return resumeSessionCmd(sess, a.dbPath)()
			}
		}
		return statusMsg{isErr: true, text: fmt.Sprintf("no session row for step run %d", cur.ID)}
	}
}

// statusCmd flashes without touching the store.
func statusCmd(f flash) tea.Cmd {
	return func() tea.Msg { return statusMsg(f) }
}

// cancelPanel is the which-key panel for the pending `c` chord.
func (a app) cancelPanel() string {
	entries := []panelEntry{
		{key: "c", desc: fmt.Sprintf("cancel run %d", a.rv.runID), keyStyle: a.styles.State[task.StateBlocked]},
		{key: "esc", desc: "keep running", keyStyle: a.styles.Dimmed},
	}
	return renderKeyPanel(a.styles, a.width, "cancel run", entries)
}

// --- view -----------------------------------------------------------------

// runViewWidths splits the body three ways: the runs sidebar on the left
// (the projects column's width, the same kind of thing), the steps in the
// middle, the log on the right, a divider between each pair.
func (a app) runViewWidths() (sideW, stepsW, logW int) {
	w := max(a.width, 40)
	sideW = projectsPaneWidth
	rest := w - sideW - 1
	stepsW = max(min(rest*2/5, 48), 16)
	return sideW, stepsW, rest - stepsW - 1
}

// runViewSplits is the divider columns, for the horizontal rules to tee.
func (a app) runViewSplits() []int {
	sideW, stepsW, _ := a.runViewWidths()
	return []int{sideW, sideW + 1 + stepsW}
}

// sizeRunViewport fits the log viewport under the log pane's heading.
func (a *app) sizeRunViewport() {
	_, _, logW := a.runViewWidths()
	a.rv.vp.SetWidth(max(logW, 10))
	a.rv.vp.SetHeight(max(a.bodyHeight-runLogHeadingRows, 1))
}

// runLogHeadingRows is the blank line plus heading above the log.
const runLogHeadingRows = 2

// runViewBody renders the three panes fitted to bodyHeight rows. The
// divider to the left of the focused pane wears the accent, so the eye
// finds the keyboard.
func (a app) runViewBody() string {
	h := max(a.bodyHeight, 1)
	sideW, stepsW, logW := a.runViewWidths()
	side := fitPane(a.runSidebarLines(sideW), sideW, h, a.runSidebarScroll(h, sideW))
	steps := fitPane(a.runStepsLines(stepsW), stepsW, h, a.runStepsScroll(h, stepsW))
	log := fitPane(append(a.runLogHeading(logW), strings.Split(a.rv.vp.View(), "\n")...), logW, h, 0)
	divider := func(focused bool) string {
		style := a.styles.Rule
		if focused {
			style = a.styles.Accent
		}
		return strings.TrimSuffix(strings.Repeat(style.Render(a.styles.Glyphs.RuleV)+"\n", h), "\n")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		strings.Join(side, "\n"), divider(a.rv.focus == rvPaneSteps),
		strings.Join(steps, "\n"), divider(a.rv.focus == rvPaneLog),
		strings.Join(log, "\n"))
}

// runSidebarHeaderRows is how many lines precede the first sidebar row.
const runSidebarHeaderRows = 2

// runSidebarScroll keeps the watched run's row on screen when the sidebar
// outgrows the body.
func (a app) runSidebarScroll(height, width int) int {
	total := len(a.runSidebarLines(width))
	if total <= height {
		return 0
	}
	line := runSidebarHeaderRows + a.rv.runCursor
	if line < height {
		return 0
	}
	return min(line-height+1, total-height)
}

// runSidebarLines lays out the RUNS column: a heading naming the task,
// then one row per run newest first -- its state glyph, the workflow's
// name, and the run id right-aligned so two runs of one workflow tell
// apart. The watched run carries the selection bar; the bar is bold while
// the sidebar has the keyboard.
func (a app) runSidebarLines(width int) []string {
	s, g := a.styles, a.styles.Glyphs
	rv := a.rv
	focused := rv.focus == rvPaneRuns
	lines := []string{"", "  " + s.SubHeader.Render("RUNS") + s.Dimmed.Render(fmt.Sprintf(" · #%d", rv.run.TaskID))}
	if len(rv.runs) == 0 {
		return append(lines, "  "+s.Muted.Render("loading…"))
	}
	for i, r := range rv.runs {
		selected := i == rv.runCursor
		gutter, gutterStyle := "  ", s.Normal
		if selected {
			gutter, gutterStyle = g.SelBar+" ", s.SelBar
		}
		mark, markStyle := runStateCell(s, r.run.State)
		name := r.workflow
		if name == "" {
			name = "workflow"
		}
		id := fmt.Sprintf("#%d", r.run.ID)
		nameStyle := s.Dimmed
		switch {
		case selected && focused:
			nameStyle = s.Title.Bold(true)
		case selected:
			nameStyle = s.Title
		}
		nameW := max(width-runeWidth(gutter)-2-runeWidth(id)-1, 1)
		label := truncTail(name, nameW, g.Ellipsis)
		gap := max(width-runeWidth(gutter)-2-runeWidth(label)-runeWidth(id), 0)
		lines = append(lines, gutterStyle.Render(gutter)+markStyle.Render(mark)+" "+nameStyle.Render(label)+
			strings.Repeat(" ", gap)+s.CountLabel.Render(id))
	}
	return lines
}

// runStepsScroll keeps the selected step row on screen when the list
// outgrows the body.
func (a app) runStepsScroll(height, width int) int {
	total := len(a.runStepsLines(width))
	if total <= height {
		return 0
	}
	line := runStepsHeaderRows + a.rv.cursor
	if line < height {
		return 0
	}
	return min(line-height+1, total-height)
}

// runStepsHeaderRows is how many lines precede the first step row.
const runStepsHeaderRows = 5

// runStepsLines lays out the left pane: the run's heading and status
// lines, then one row per step run -- number, name, a loop-back marker
// for a repeat, and its outcome or, for the current step, the run's state.
func (a app) runStepsLines(width int) []string {
	s, g := a.styles, a.styles.Glyphs
	rv := a.rv
	now := time.Now().UTC()

	name := rv.workflow
	if name == "" {
		name = "workflow"
	}
	mark, markStyle := runStateCell(s, rv.run.State)
	lines := []string{
		"",
		"  " + s.SubHeader.Render(fmt.Sprintf("RUN %d", rv.runID)) + s.Dimmed.Render(" · "+name),
	}
	status := markStyle.Render(mark) + " " + markStyle.Render(string(rv.run.State)) +
		s.Muted.Render(" · "+fmtElapsed(runElapsed(rv.run, now)))
	if cur, ok := rv.current(); ok && !rv.run.State.Terminal() {
		if mod := logModTime(cur.LogPath); !mod.IsZero() {
			status += s.Muted.Render(" · output " + relTime(mod, now))
		}
	}
	if rv.runnerGone {
		status += "  " + s.State[task.StateBlocked].Bold(true).Render("runner gone")
	}
	lines = append(lines, "  "+status)
	if rv.run.Error != "" {
		for _, l := range strings.Split(ansi.Wrap(rv.run.Error, max(width-4, 10), ""), "\n") {
			lines = append(lines, "  "+s.Error.Render(l))
		}
	}
	lines = append(lines, "", "  "+s.SubHeader.Render("STEPS"))
	for len(lines) < runStepsHeaderRows {
		lines = append(lines, "")
	}

	if len(rv.stepRuns) == 0 {
		lines = append(lines, "  "+s.Muted.Render("no step has started yet"))
		return lines
	}
	cur, hasCur := rv.current()
	focused := rv.focus == rvPaneSteps
	for i, sr := range rv.stepRuns {
		selected := i == rv.cursor
		isCur := hasCur && sr.ID == cur.ID
		gutter, gutterStyle := "  ", s.Normal
		if selected {
			gutter, gutterStyle = g.SelBar+" ", s.SelBar
		}
		num := fmt.Sprintf("%d  ", i+1)
		label := rv.stepNames[sr.StepID]
		if sr.Iteration > 1 {
			label += fmt.Sprintf(" ↺%d", sr.Iteration)
		}
		var meta string
		var metaStyle lipgloss.Style
		switch {
		case sr.Finished():
			meta, metaStyle = sr.Outcome, s.CheckDone
			if sr.EndedAt != nil {
				meta += " · " + fmtElapsed(sr.EndedAt.Sub(sr.StartedAt))
			}
		case isCur:
			glyph, st := runStateCell(s, rv.run.State)
			meta, metaStyle = glyph+" "+string(rv.run.State)+" · "+fmtElapsed(now.Sub(sr.StartedAt)), st
		default:
			meta, metaStyle = "unfinished", s.Muted
		}
		nameStyle := s.Dimmed
		switch {
		case isCur && selected && focused:
			nameStyle = s.Accent.Bold(true)
		case isCur:
			nameStyle = s.Accent
		case selected && focused:
			nameStyle = s.Title.Bold(true)
		case selected:
			nameStyle = s.Title
		}
		nameW := max(width-runeWidth(gutter)-runeWidth(num)-runeWidth(meta)-3, 6)
		label = truncTail(label, nameW, g.Ellipsis)
		gap := max(width-runeWidth(gutter)-runeWidth(num)-runeWidth(label)-runeWidth(meta)-1, 1)
		lines = append(lines, gutterStyle.Render(gutter)+s.Muted.Render(num)+nameStyle.Render(label)+
			strings.Repeat(" ", gap)+metaStyle.Render(meta))
	}
	return lines
}

// runLogHeading names the step whose log the right pane shows and how.
func (a app) runLogHeading(width int) []string {
	s := a.styles
	heading := "  " + s.SubHeader.Render("LOG")
	if sr, ok := a.rv.selected(); ok {
		heading += s.Dimmed.Render(" · " + a.rv.stepNames[sr.StepID])
		if sr.Iteration > 1 {
			heading += s.Dimmed.Render(fmt.Sprintf(" ↺%d", sr.Iteration))
		}
	}
	mode := "rendered"
	if a.rv.raw {
		mode = "raw"
	}
	if a.rv.follow {
		mode += " · following"
	}
	gap := max(width-lipgloss.Width(heading)-lipgloss.Width(mode)-2, 1)
	return []string{"", heading + strings.Repeat(" ", gap) + s.Faint.Render(mode)}
}

// runViewHints is the footer for the view, per focused pane.
func (a app) runViewHints() [][2]string {
	var hints [][2]string
	switch a.rv.focus {
	case rvPaneRuns:
		hints = [][2]string{{"j/k", "switch run"}, {"l", "to steps"}}
	case rvPaneLog:
		hints = [][2]string{{"j/k", "scroll"}, {"h", "to steps"}}
	default:
		hints = [][2]string{{"j/k", "steps"}, {"h/l", "runs / log"}}
	}
	switch a.rv.run.State {
	case workflow.RunPaused:
		hints = append(hints, [2]string{"p", "resume"}, [2]string{"t", "take over step"})
	case workflow.RunWaitingReview:
		hints = append(hints, [2]string{"a/x/o", "approve / reject / pick"}, [2]string{"p", "pause"})
	case workflow.RunRunning:
		hints = append(hints, [2]string{"p", "pause"})
	}
	if !a.rv.run.State.Terminal() {
		hints = append(hints, [2]string{"cc", "cancel"})
	}
	return append(hints, [2]string{"v", "raw"}, [2]string{"esc/q", "back"}, [2]string{"?", "help"})
}
