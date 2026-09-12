package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// Taking over a workflow step (tend task #184): moving from watching a run
// to driving it, over the existing resume path. A headless step is one
// claude session (`claude -p --session-id`), and the same session resumes
// interactively (`claude --resume`) once nothing else is driving it -- so
// a takeover is: make the runner let go, then resume the step's session
// exactly as `r` resumes any session.
//
//  1. `t` in the run view (or `r` on a paused run's step session) writes
//     paused on the run. The runner polls for that, SIGTERMs its claude
//     (whose transcript stays resumable) and exits; its tmux session
//     going away is how the TUI knows it has let go (waitRunnerGone). A
//     run whose runner had already died skips straight through.
//  2. The step's session is resumed interactively, tmux-wrapped, with the
//     step's MCP tools bound (finish_step works by hand too), and the
//     terminal handed over -- resumeSessionCmd with a takeoverRef.
//  3. On return the takeover picker asks what the run should do now:
//     continue (pick this step's outcome and a deliverable, then a fresh
//     runner routes on it; or just resume when finish_step was already
//     called from inside the session), hand the step back for the runner
//     to finish headlessly, rerun the step from scratch on a fresh
//     session (runner.RestartStep), or abandon the run. Every resume goes
//     through runner.Resume, the same path as `tend workflow resume`, so
//     a takeover of a run whose runner died behaves like a resume after.
//
// A gate has no session; taking one over is approving or rejecting it,
// and `t` says so.

// takeoverRef names the step a session resume was a takeover of. The zero
// value means an ordinary resume; it rides on sessionResumedMsg so Update
// can tell the two apart when the terminal comes back.
type takeoverRef struct {
	runID     int64
	stepRunID int64
}

// set reports whether the ref marks a takeover.
func (r takeoverRef) set() bool { return r.stepRunID != 0 }

// takeoverResume is resumeSessionCmd behind a seam: the takeover path
// fires from Update paths the tests drive, and this repo's dev machine has
// claude installed, so without a stub a test would hand the terminal to a
// real claude. Tests swap in a Cmd that yields the returning message.
var takeoverResume = func(sess task.Session, dbPath, systemPrompt string, ref takeoverRef) tea.Cmd {
	return resumeSessionCmd(sess, dbPath, systemPrompt, ref)
}

// How long a takeover waits for a paused run's runner to exit before
// giving up, and how often it asks. The runner notices a pause within its
// poll (runner.DefaultPoll), then its claude gets agent's SIGTERM grace
// before a SIGKILL; the timeout covers both with room to spare, and a
// slow host just leaves the run paused with a flash saying to try again.
// Variables so a test can exercise the timeout without waiting it out.
var (
	runnerStopTimeout = 20 * time.Second
	runnerStopPoll    = 250 * time.Millisecond
)

// waitRunnerGone blocks until no tmux session hosts run's runner, or the
// timeout passes. It is what makes a takeover safe: until the runner has
// exited, its headless claude may still be writing the step's transcript,
// and an interactive claude on top of it is exactly the two-processes-
// one-transcript hazard the session machinery exists to avoid. A run
// whose runner already died returns at once.
func waitRunnerGone(ctx context.Context, run workflow.Run) error {
	deadline := time.Now().Add(runnerStopTimeout)
	for {
		if !runnerAlive(run) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("run %d is paused but its runner (%s) has not stopped after %s; press t again once it has",
				run.ID, agent.RunnerSessionName(run.ID), runnerStopTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(runnerStopPoll):
		}
	}
}

// --- `t` ------------------------------------------------------------------

// takeoverStep is `t` in the run view. It refuses what cannot be taken
// over -- a gate (approve or reject it instead), a step that already
// handed off (`p` resumes the run), a run no runner has started -- and
// otherwise pauses the run and hands the step's session over
// (takeoverCmd). A running run is paused here; a paused one goes straight
// to the wait for its runner, since `p` may have been pressed a moment
// ago and the runner may still be shutting the step down.
func (a *app) takeoverStep() tea.Cmd {
	run := a.rv.run
	cur, ok := a.rv.current()
	if !ok {
		return statusCmd(flash{text: fmt.Sprintf("run %d has no current step to take over", run.ID)})
	}
	name := a.rv.stepNames[cur.StepID]
	if a.rv.stepKinds[cur.StepID] == workflow.StepGate {
		return statusCmd(flash{text: fmt.Sprintf("%s is a gate and has no session — approve (a) or reject (x) it instead", name)})
	}
	if cur.Finished() {
		return statusCmd(flash{text: fmt.Sprintf("%s has already handed off (%s); p resumes the run", name, cur.Outcome)})
	}
	if cur.SessionExternalID == "" {
		return statusCmd(flash{text: fmt.Sprintf("%s has no session yet to take over", name)})
	}
	switch run.State {
	case workflow.RunRunning, workflow.RunPaused:
	case workflow.RunPending:
		return statusCmd(flash{text: fmt.Sprintf("run %d has not been claimed by a runner yet", run.ID)})
	default:
		return statusCmd(flash{text: fmt.Sprintf("run %d is %s; nothing to take over", run.ID, run.State)})
	}
	if run.State == workflow.RunRunning {
		a.status = flash{kind: flashEdit, text: fmt.Sprintf("pausing run %d — waiting for its runner to let go of %s…", run.ID, name)}
	} else {
		a.status = flash{kind: flashEdit, text: fmt.Sprintf("taking over %s of run %d…", name, run.ID)}
	}
	return a.takeoverCmd(run, cur)
}

// takeoverCmd pauses a running run, waits for its runner to exit, finds
// the step's session row and resumes it as a takeover. Everything here
// reads or writes the store or waits on tmux, so it is one Cmd; the
// resume's own message (tea.ExecProcess's) is what it yields.
func (a app) takeoverCmd(run workflow.Run, cur workflow.StepRun) tea.Cmd {
	return func() tea.Msg {
		if run.State == workflow.RunRunning {
			if err := a.store.SetRunState(a.ctx, run.ID, workflow.RunPaused); err != nil {
				return errMsg{err}
			}
		}
		if err := waitRunnerGone(a.ctx, run); err != nil {
			return statusMsg{isErr: true, text: err.Error()}
		}
		sessions, err := a.store.ListSessionsForTask(a.ctx, run.TaskID)
		if err != nil {
			return errMsg{err}
		}
		for _, sess := range sessions {
			if sess.ExternalID == cur.SessionExternalID {
				return takeoverResume(sess, a.dbPath, a.sessionBriefPrompt(sess.TaskID), takeoverRef{runID: run.ID, stepRunID: cur.ID})()
			}
		}
		return statusMsg{isErr: true, text: fmt.Sprintf("no session row for step run %d", cur.ID)}
	}
}

// --- back from the session ------------------------------------------------

// takeoverReturned handles the terminal coming back from a takeover. An
// error is reported and the run stays paused. A backgrounded session is
// still a live claude on the step's transcript, so nothing may resume the
// run yet: the flash says how to get back in. A clean return touches the
// session and re-reads the run for the picker (loadTakeoverPicker).
//
// No recap fires here, unlike an ordinary resume: recapSessionCmd is a
// `claude -p --resume` on the same session id, which would race the
// runner's own headless resume if the step is handed back, and the step's
// outcome and deliverable are the record of what the session did.
func (a *app) takeoverReturned(msg sessionResumedMsg) tea.Cmd {
	ref := msg.takeover
	if msg.err != nil {
		a.status = flash{text: "claude: " + msg.err.Error(), isErr: true}
		return nil
	}
	if msg.backgrounded {
		return a.mutate(flash{kind: flashEdit, text: fmt.Sprintf(
			"session backgrounded; run %d stays paused — t reattaches, p resumes once the session has exited", ref.runID)},
			func() error { return a.store.TouchSession(a.ctx, msg.sessionRowID) })
	}
	return func() tea.Msg {
		if err := a.store.TouchSession(a.ctx, msg.sessionRowID); err != nil {
			return errMsg{err}
		}
		return a.loadTakeoverPicker(ref)()
	}
}

// loadTakeoverPicker re-reads the run and step run a takeover was of and
// yields the picker's contents, or a flash when there is nothing left to
// decide: the run ended or was resumed elsewhere while the session was
// open, or moved on to another step.
func (a app) loadTakeoverPicker(ref takeoverRef) tea.Cmd {
	return func() tea.Msg {
		run, err := a.store.GetRun(a.ctx, ref.runID)
		if err != nil {
			return errMsg{err}
		}
		sr, err := a.store.GetStepRun(a.ctx, ref.stepRunID)
		if err != nil {
			return errMsg{err}
		}
		switch {
		case run.State.Terminal():
			return refreshMsg{status: flash{text: fmt.Sprintf("run %d has %s meanwhile; nothing to continue", run.ID, run.State)}}
		case run.State != workflow.RunPaused:
			return refreshMsg{status: flash{text: fmt.Sprintf("run %d is %s; a runner already has it", run.ID, run.State)}}
		case run.CurrentStepRunID == nil || *run.CurrentStepRunID != sr.ID:
			return refreshMsg{status: flash{text: fmt.Sprintf("run %d has moved on from the step you took over", run.ID)}}
		}
		msg := takeoverReturnedMsg{run: run, stepRun: sr, stepName: fmt.Sprintf("step %d", sr.StepID)}
		if st, err := a.store.GetStep(a.ctx, sr.StepID); err == nil {
			msg.stepName = st.Name
		}
		if edges, err := a.store.OutgoingEdges(a.ctx, sr.StepID); err == nil {
			for _, e := range edges {
				msg.outcomes = append(msg.outcomes, e.Outcome)
			}
		}
		return msg
	}
}

// --- picker ---------------------------------------------------------------

// takeoverStage is which list the picker shows: what to do with the run,
// or which outcome to finish the step with.
type takeoverStage int

const (
	takeoverChoose takeoverStage = iota
	takeoverOutcome
)

// takeoverPicker is the overlay's state: the paused run and the step run
// the session belonged to, as re-read when the session returned.
type takeoverPicker struct {
	open     bool
	run      workflow.Run
	stepRun  workflow.StepRun
	stepName string
	outcomes []string
	stage    takeoverStage
	sel      int
}

// takeoverChoice is one row of the picker's first stage.
type takeoverChoice struct {
	label, desc string
	act         func(a *app) tea.Cmd
}

// choices is the first stage's rows. A step that already handed off --
// finish_step was called from inside the session -- has nothing left to
// decide but whether to go on, so the hand-back and rerun rows are left
// out and continuing needs no outcome.
func (p takeoverPicker) choices() []takeoverChoice {
	run, sr := p.run, p.stepRun
	// Every row but the outcome stage's entry point closes the picker as
	// it acts; chooseTakeoverOutcome moves to that stage instead.
	closing := func(f func(a *app) tea.Cmd) func(a *app) tea.Cmd {
		return func(a *app) tea.Cmd {
			a.closeTakeoverPicker()
			return f(a)
		}
	}
	var out []takeoverChoice
	if sr.Finished() {
		out = append(out, takeoverChoice{
			label: "continue the run",
			desc:  fmt.Sprintf("%s handed off as %s; a runner routes on it", p.stepName, sr.Outcome),
			act:   closing(func(a *app) tea.Cmd { return a.resumeRunCmd(run.ID, "") }),
		})
	} else {
		out = append(out,
			takeoverChoice{
				label: "continue the run",
				desc:  "finish " + p.stepName + " by hand: pick its outcome, paste a deliverable, and a runner routes on it",
				act:   func(a *app) tea.Cmd { return a.chooseTakeoverOutcome() },
			},
			takeoverChoice{
				label: "hand the step back",
				desc:  "a runner continues this session headlessly and finishes the step",
				act:   closing(func(a *app) tea.Cmd { return a.resumeRunCmd(run.ID, p.stepName+" handed back") }),
			},
			takeoverChoice{
				label: "rerun the step",
				desc:  "a fresh session on the same step run; the runner starts " + p.stepName + " over",
				act:   closing(func(a *app) tea.Cmd { return a.rerunStepCmd(run.ID, sr.ID, p.stepName) }),
			})
	}
	return append(out, takeoverChoice{
		label: "abandon the run",
		desc:  fmt.Sprintf("cancel run %d; nothing else runs", run.ID),
		act:   closing(func(a *app) tea.Cmd { return a.cancelPausedRunCmd(run.ID) }),
	})
}

// openTakeoverPicker arms the picker from the re-read run.
func (a *app) openTakeoverPicker(msg takeoverReturnedMsg) {
	a.takeover = takeoverPicker{open: true, run: msg.run, stepRun: msg.stepRun,
		stepName: msg.stepName, outcomes: msg.outcomes}
}

func (a *app) closeTakeoverPicker() {
	a.takeover = takeoverPicker{}
}

// chooseTakeoverOutcome moves to the outcome stage. A step with no edges
// ends the run on any outcome, so there is nothing to choose: it goes
// straight to the deliverable as done.
func (a *app) chooseTakeoverOutcome() tea.Cmd {
	if len(a.takeover.outcomes) == 0 {
		return a.askTakeoverDeliverable(workflow.OutcomeDone)
	}
	a.takeover.stage, a.takeover.sel = takeoverOutcome, 0
	return nil
}

// askTakeoverDeliverable closes the picker and opens the deliverable
// modal for the chosen outcome; submitModal finishes the step from it.
func (a *app) askTakeoverDeliverable(outcome string) tea.Cmd {
	p := a.takeover
	a.closeTakeoverPicker()
	return a.modal.Open(modalTakeoverDeliverable, true,
		fmt.Sprintf("%s → %s — deliverable for the next step (optional)", p.stepName, outcome), p.stepRun.ID, outcome)
}

// handleTakeoverPickerKey owns the keyboard while the picker is open, in
// the other pickers' mould: arrows, j/k or ctrl-n/ctrl-p move, a digit
// picks directly, Enter picks the highlight. esc backs out of the outcome
// stage to the choices, and out of the choices altogether -- the run
// stays paused, and the flash says how to come back to it.
func (a app) handleTakeoverPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := a.takeover
	rows := len(p.choices())
	if p.stage == takeoverOutcome {
		rows = len(p.outcomes)
	}
	pick := func(idx int) (tea.Model, tea.Cmd) {
		if idx < 0 || idx >= rows {
			return a, nil
		}
		if p.stage == takeoverOutcome {
			return a, a.askTakeoverDeliverable(p.outcomes[idx])
		}
		return a, p.choices()[idx].act(&a)
	}
	switch msg.String() {
	case "esc":
		if p.stage == takeoverOutcome {
			a.takeover.stage, a.takeover.sel = takeoverChoose, 0
			return a, nil
		}
		a.closeTakeoverPicker()
		a.status = flash{text: fmt.Sprintf("run %d stays paused — t takes the step over again, p resumes it", p.run.ID)}
		return a, nil
	case "enter":
		return pick(p.sel)
	case "up", "ctrl+p", "k":
		if a.takeover.sel > 0 {
			a.takeover.sel--
		}
		return a, nil
	case "down", "ctrl+n", "j":
		if a.takeover.sel < rows-1 {
			a.takeover.sel++
		}
		return a, nil
	}
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		return pick(int(msg.Text[0] - '1'))
	}
	return a, nil
}

// takeoverPickerView renders the overlay in the gate picker's mould: a
// title naming the step and the paused run, then the numbered rows of the
// current stage, each choice with a line saying what it does.
func (a app) takeoverPickerView() string {
	s, g := a.styles, a.styles.Glyphs
	p := a.takeover
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)

	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}
	name := truncTail(p.stepName, max(w-40, 10), g.Ellipsis)
	title := s.Title.Render("back from ") + s.Accent.Render(name) +
		s.Dimmed.Render(fmt.Sprintf("  run %d is paused", p.run.ID))
	if p.stage == takeoverOutcome {
		title = s.Title.Render("outcome for ") + s.Accent.Render(name) +
			s.Dimmed.Render("  the edge it picks is where the run goes next")
	}
	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.Accent.Bold(true).Render("⚡ ")+title+s.Muted.Render("  ⏎ or type a number")))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))

	type item struct{ label, desc string }
	var items []item
	if p.stage == takeoverOutcome {
		for _, o := range p.outcomes {
			items = append(items, item{label: o})
		}
	} else {
		for _, c := range p.choices() {
			items = append(items, item{c.label, c.desc})
		}
	}
	sel := min(p.sel, len(items)-1)
	for i, it := range items {
		num := fmt.Sprintf("%d ", i+1)
		var content string
		if i == sel {
			content = s.SelBar.Render(g.SelBar+" ") + s.Accent.Render(num) + s.Title.Bold(true).Render(it.label)
		} else {
			content = "  " + s.Muted.Render(num) + s.Dimmed.Render(it.label)
		}
		if it.desc != "" {
			content += s.Muted.Render("  " + truncTail(it.desc, max(w-12-lipgloss.Width(content), 10), g.Ellipsis))
		}
		lines = append(lines, row(content))
	}
	back := "esc leave the run paused"
	if p.stage == takeoverOutcome {
		back = "esc back"
	}
	lines = append(lines, row(s.Muted.Render(back)))
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}

// --- actions --------------------------------------------------------------

// finishTakenOverStep records the outcome and deliverable chosen for a
// step finished by hand -- the same one-shot FinishStepRun finish_step
// makes -- then starts a runner, which sees the finished step and routes
// on it. A step finished meanwhile (from inside a backgrounded session,
// say) keeps its own outcome and the run is resumed regardless.
func (a app) finishTakenOverStep(stepRunID int64, outcome, deliverable string) tea.Cmd {
	return func() tea.Msg {
		sr, err := a.store.GetStepRun(a.ctx, stepRunID)
		if err != nil {
			return errMsg{err}
		}
		note := outcome
		if sr.Finished() {
			note = fmt.Sprintf("%s (already handed off)", sr.Outcome)
		} else if err := a.store.FinishStepRun(a.ctx, stepRunID, outcome, deliverable); err != nil {
			return errMsg{err}
		}
		return a.resumeRunCmd(sr.RunID, "step finished as "+note)()
	}
}

// resumeRunCmd starts a fresh runner for the paused run (runner.Resume,
// the `tend workflow resume` path, which refuses a run whose runner is
// still alive or that has ended). what, when set, prefixes the flash.
func (a app) resumeRunCmd(runID int64, what string) tea.Cmd {
	return func() tea.Msg {
		name, err := resumeRunner(a.ctx, a.store, runID, a.dbPath)
		if err != nil {
			return errMsg{err}
		}
		text := fmt.Sprintf("run %d resumed (%s)", runID, name)
		if what != "" {
			text = what + " · " + text
		}
		return refreshMsg{status: flash{kind: flashAdd, text: text}}
	}
}

// rerunStepCmd asks the next runner to start the step over on a fresh
// session (runner.RestartStep clears the step run's session id, which
// resumeStep reads as "begin a new attempt"), then starts that runner.
func (a app) rerunStepCmd(runID, stepRunID int64, stepName string) tea.Cmd {
	return func() tea.Msg {
		if err := runner.RestartStep(a.ctx, a.store, stepRunID); err != nil {
			return errMsg{err}
		}
		return a.resumeRunCmd(runID, stepName+" will be rerun")()
	}
}

// cancelPausedRunCmd is the picker's abandon: the run ends cancelled, and
// with no runner alive there is no step left to kill.
func (a app) cancelPausedRunCmd(runID int64) tea.Cmd {
	return a.mutate(flash{kind: flashDone, text: fmt.Sprintf("run %d cancelled", runID)}, func() error {
		return a.store.SetRunState(a.ctx, runID, workflow.RunCancelled)
	})
}
