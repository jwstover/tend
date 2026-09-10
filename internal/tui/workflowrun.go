package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// Running a workflow on a task: the `w` key. The run is driven headlessly
// by the runner (internal/runner, `tend workflow run`) in its own tmux
// session; the TUI only creates the run and starts that process, then
// gets the terminal straight back. Watching the run is `v` (runview.go);
// the runner's own output is in its tmux session and runner.log.
//
// The flow is three messages long, each produced by a Cmd so the store is
// never touched from Update:
//
//  1. `w` lists the workflows (workflowsForRunMsg) and opens a picker in
//     the session/project picker mould.
//  2. Picking one checks it can run at all -- it has steps, every step's
//     prompt renders, claude and tmux are on $PATH -- (workflowRunReadyMsg),
//     then prompts for a cwd, defaulting to the task's most recent session
//     directory like a plain launch does.
//  3. Submitting the cwd writes the run (pending) and launches its runner
//     in tmux (workflowRunStartedMsg), which claims the run and takes it
//     from there.

// workflowRunRequest is the validated intent to run one workflow on one
// task.
type workflowRunRequest struct {
	t task.Task
	w workflow.Workflow
}

// checkInstalled is agent.CheckInstalled behind a seam, for the same
// reason runRecap is one: the launch path fires from the Update loop the
// tests drive, and this repo's own dev machine has claude installed while
// CI does not, so a direct call would make the tests' outcome depend on
// the host. tmuxInstalled and launchRunner are seams for the same reason.
var (
	checkInstalled = agent.CheckInstalled
	tmuxInstalled  = agent.TmuxInstalled
	launchRunner   = func(ctx context.Context, s runner.LaunchStore, runID int64, dbPath string) (string, error) {
		return runner.Launch(ctx, s, runID, dbPath, false)
	}
)

// loadWorkflowsForRun fetches the workflows to choose from, off the
// update loop, and opens the picker once loaded.
func (a app) loadWorkflowsForRun(t task.Task) tea.Cmd {
	return func() tea.Msg {
		wfs, err := a.store.ListWorkflows(a.ctx)
		if err != nil {
			return errMsg{err}
		}
		return workflowsForRunMsg{t: t, workflows: wfs}
	}
}

// openWorkflowRunPicker arms the picker. With no workflows there is
// nothing to choose, so it says where to make one instead.
func (a *app) openWorkflowRunPicker(msg workflowsForRunMsg) tea.Cmd {
	if len(msg.workflows) == 0 {
		a.status = flash{text: "no workflows yet — press W to create one"}
		return nil
	}
	a.wfRunPickerOpen = true
	a.wfRunPickerTask = msg.t
	a.wfRunPickerWorkflows = msg.workflows
	a.wfRunPickerSel = 0
	return nil
}

func (a *app) closeWorkflowRunPicker() {
	a.wfRunPickerOpen = false
	a.wfRunPickerTask = task.Task{}
	a.wfRunPickerWorkflows = nil
	a.wfRunPickerSel = 0
}

// handleWorkflowRunPickerKey owns the keyboard while the picker is open:
// arrows or ctrl-n/ctrl-p move, a digit picks directly, Enter picks the
// highlight, esc dismisses.
func (a app) handleWorkflowRunPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	wfs := a.wfRunPickerWorkflows
	pick := func(idx int) (tea.Model, tea.Cmd) {
		t := a.wfRunPickerTask
		a.closeWorkflowRunPicker()
		if idx < 0 || idx >= len(wfs) {
			return a, nil
		}
		return a, a.checkWorkflowRunnableCmd(t, wfs[idx])
	}

	switch msg.String() {
	case "esc":
		a.closeWorkflowRunPicker()
		return a, nil
	case "enter":
		return pick(a.wfRunPickerSel)
	case "up", "ctrl+p":
		if a.wfRunPickerSel > 0 {
			a.wfRunPickerSel--
		}
		return a, nil
	case "down", "ctrl+n":
		if a.wfRunPickerSel < len(wfs)-1 {
			a.wfRunPickerSel++
		}
		return a, nil
	}
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		if idx := int(msg.Text[0] - '1'); idx < len(wfs) {
			return pick(idx)
		}
	}
	return a, nil
}

// checkWorkflowRunnableCmd decides whether the picked workflow can run
// before anything is written: it needs at least one step, every step's
// prompt must render (the runner would fail the run on the same error,
// but here a typo reads as an authoring problem rather than a failed
// run), and claude and tmux must both be on $PATH, since the runner
// lives in tmux and runs claude. Anything missing is refused with a
// flash that names it, so the picker never leads to a half-made run.
// The task's sessions are loaded alongside to seed the cwd prompt.
func (a app) checkWorkflowRunnableCmd(t task.Task, w workflow.Workflow) tea.Cmd {
	return func() tea.Msg {
		steps, err := a.store.ListSteps(a.ctx, w.ID)
		if err != nil {
			return errMsg{err}
		}
		if len(steps) == 0 {
			return statusMsg{isErr: true, text: fmt.Sprintf("%s has no steps — press W to add one", w.Name)}
		}
		for _, st := range steps {
			if st.Kind == workflow.StepGate && st.PromptMD == "" {
				continue
			}
			if err := workflow.ValidatePrompt(st.PromptMD); err != nil {
				return statusMsg{isErr: true, text: fmt.Sprintf("%s / %s: %v", w.Name, st.Name, err)}
			}
		}
		if err := checkInstalled(); err != nil {
			return errMsg{err}
		}
		if !tmuxInstalled() {
			return errMsg{runner.ErrNoTmux}
		}
		sessions, err := a.store.ListSessionsForTask(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		return workflowRunReadyMsg{
			req:        workflowRunRequest{t: t, w: w},
			defaultCwd: a.defaultCwd(sessions, t.ProjectID),
		}
	}
}

// openWorkflowCwdPrompt collects the working directory for the run,
// prefilled with the best guess so enter alone accepts it. The request
// waits in wfRunPending until the prompt is submitted or dismissed.
func (a *app) openWorkflowCwdPrompt(msg workflowRunReadyMsg) tea.Cmd {
	req := msg.req
	a.wfRunPending = &req
	cmd := a.openPrompt(promptWorkflowCwd,
		fmt.Sprintf("cwd to run %s on #%d: ", req.w.Name, req.t.ID), req.t.ID)
	a.prompt.SetValue(msg.defaultCwd)
	a.prompt.CursorEnd()
	return cmd
}

// startWorkflowRunCmd writes the run and starts its runner. The run is
// created pending; the runner claims it from inside its tmux session. A
// runner that cannot be started fails the run on the spot -- a pending
// run nobody will ever claim would otherwise sit there looking live --
// and the error is what the user sees.
func (a app) startWorkflowRunCmd(req workflowRunRequest, cwd string) tea.Cmd {
	return func() tea.Msg {
		run, err := a.store.CreateRun(a.ctx, req.w.ID, req.t.ID, cwd)
		if err != nil {
			return errMsg{err}
		}
		name, err := launchRunner(a.ctx, a.store, run.ID, a.dbPath)
		if err != nil {
			if ferr := a.store.FailRun(a.ctx, run.ID, err.Error()); ferr != nil {
				return errMsg{fmt.Errorf("%w (and failing run %d: %v)", err, run.ID, ferr)}
			}
			return errMsg{err}
		}
		return workflowRunStartedMsg{req: req, run: run, tmuxSession: name}
	}
}

// workflowRunPickerView renders the chooser box in the project picker's
// mould: a title naming the task, then the numbered workflows with their
// step counts.
func (a app) workflowRunPickerView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)

	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	title := truncTail(a.wfRunPickerTask.Title, max(w-30, 10), g.Ellipsis)
	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.Accent.Bold(true).Render("⚡ ")+
		s.Title.Render("run workflow on ")+s.Dimmed.Render(title)+
		s.Muted.Render("  ⏎ or type a number")))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))

	wfs := a.wfRunPickerWorkflows
	sel := min(a.wfRunPickerSel, len(wfs)-1)
	for i, wf := range wfs {
		num := fmt.Sprintf("%d ", i+1)
		meta := fmt.Sprintf("%d steps", wf.StepCount)
		switch wf.StepCount {
		case 0:
			meta = "no steps"
		case 1:
			meta = "1 step"
		}
		var content string
		if i == sel {
			content = s.SelBar.Render(g.SelBar+" ") + s.Accent.Render(num) +
				s.Title.Render(wf.Name) + "  " + s.Muted.Render(meta)
		} else {
			content = "  " + s.Muted.Render(num) + s.Dimmed.Render(wf.Name) + "  " + s.Muted.Render(meta)
		}
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}
