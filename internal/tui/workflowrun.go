package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// Running a workflow on a task, interactively: the `w` key. This is the
// proof of concept for agent workflows (tend task #176) -- a one-step
// workflow launched as an ordinary claude session whose first message is
// the step's rendered prompt template. There is no runner and no headless
// mode here; a workflow with more than one step is refused with a flash
// pointing at the runner (#179) until that exists.
//
// The flow is three messages long, each produced by a Cmd so the store is
// never touched from Update:
//
//  1. `w` lists the workflows (workflowsForRunMsg) and opens a picker in
//     the session/project picker mould.
//  2. Picking one loads its steps and checks it is runnable this way
//     (workflowRunReadyMsg), then prompts for a cwd, defaulting to the
//     task's most recent session directory like a plain launch does.
//  3. Submitting the cwd renders the prompt and writes the run and step
//     run rows (workflowRunPreparedMsg), and Update hands the terminal to
//     claude through the same tea.ExecProcess path launchSessionCmd uses.
//
// When the handoff returns, sessionFinishedMsg carries the run and step
// run ids, so a real exit ends the run and a backgrounded one is settled
// later by whichever path first sees the session gone.

// workflowRunRequest is the validated intent to run one workflow on one
// task: the single agent step it will launch is resolved up front so the
// cwd prompt and the launch never re-derive it.
type workflowRunRequest struct {
	t    task.Task
	w    workflow.Workflow
	step workflow.Step
}

// label is the session's -n name: the workflow, then the task, so the
// SESSIONS section reads "fix a bug — flaky scheduler test" rather than a
// second copy of the task title.
func (r workflowRunRequest) label() string {
	return r.w.Name + " — " + r.t.Title
}

// checkInstalled is agent.CheckInstalled behind a seam, for the same
// reason runRecap is one: the launch path fires from the Update loop the
// tests drive, and this repo's own dev machine has claude installed while
// CI does not, so a direct call would make the tests' outcome depend on
// the host.
var checkInstalled = agent.CheckInstalled

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

// checkWorkflowRunnableCmd loads the picked workflow's steps and decides
// whether this interactive path can run it: exactly one step, and an
// agent step (a gate has no session to launch). Anything else is refused
// with a flash that says why, so the picker never leads to a half-made
// run. The task's sessions are loaded alongside to seed the cwd prompt.
func (a app) checkWorkflowRunnableCmd(t task.Task, w workflow.Workflow) tea.Cmd {
	return func() tea.Msg {
		steps, err := a.store.ListSteps(a.ctx, w.ID)
		if err != nil {
			return errMsg{err}
		}
		switch {
		case len(steps) == 0:
			return statusMsg{isErr: true, text: fmt.Sprintf("%s has no steps — press W to add one", w.Name)}
		case len(steps) > 1:
			return statusMsg{isErr: true, text: fmt.Sprintf(
				"%s has %d steps — multi-step workflows need the runner (task #179), which doesn't exist yet",
				w.Name, len(steps))}
		case steps[0].Kind != workflow.StepAgent:
			return statusMsg{isErr: true, text: fmt.Sprintf(
				"%s's only step is a gate — nothing to launch interactively", w.Name)}
		}
		sessions, err := a.store.ListSessionsForTask(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		return workflowRunReadyMsg{
			req:        workflowRunRequest{t: t, w: w, step: steps[0]},
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

// prepareWorkflowRunCmd is the point of no return short of the launch
// itself: it renders the step's prompt against the task and cwd, and on
// success writes the run (already `running` -- this path is its own
// runner) and its one step run recording exactly what is about to be
// launched. Everything that can fail without side effects is checked
// first: claude on $PATH, and the template rendering, whose error names
// the step so a typo in a prompt reads as an authoring problem, not a
// launch failure. A render failure leaves no rows behind.
func (a app) prepareWorkflowRunCmd(req workflowRunRequest, cwd string) tea.Cmd {
	return func() tea.Msg {
		if err := checkInstalled(); err != nil {
			return errMsg{err}
		}
		edges, err := a.store.OutgoingEdges(a.ctx, req.step.ID)
		if err != nil {
			return errMsg{err}
		}
		outcomes := make([]string, 0, len(edges))
		for _, e := range edges {
			outcomes = append(outcomes, e.Outcome)
		}
		prompt, err := workflow.RenderPrompt(req.step.PromptMD, workflow.PromptData{
			Task:      workflow.PromptTask{ID: req.t.ID, Title: req.t.Title, Body: req.t.BodyMD},
			Cwd:       cwd,
			Iteration: 1,
			Outcomes:  outcomes,
		})
		if err != nil {
			return statusMsg{isErr: true, text: fmt.Sprintf("%s / %s: %v", req.w.Name, req.step.Name, err)}
		}
		sessionID, err := agent.NewSessionID()
		if err != nil {
			return errMsg{err}
		}

		run, err := a.store.CreateRun(a.ctx, req.w.ID, req.t.ID, cwd)
		if err != nil {
			return errMsg{err}
		}
		if err := a.store.SetRunState(a.ctx, run.ID, workflow.RunRunning); err != nil {
			return errMsg{err}
		}
		run.State = workflow.RunRunning
		stepRun, err := a.store.CreateStepRun(a.ctx, workflow.StepRun{
			RunID:             run.ID,
			StepID:            req.step.ID,
			SessionExternalID: sessionID,
			PromptRendered:    prompt,
			Model:             req.step.Model,
			PermissionMode:    req.step.PermissionMode,
		})
		if err != nil {
			return errMsg{err}
		}
		return workflowRunPreparedMsg{req: req, cwd: cwd, run: run, stepRun: stepRun}
	}
}

// launchWorkflowStepCmd is launchSessionCmd for a prepared step run: the
// same MCP config, hook settings and tmux wrapping, plus the step's
// rendered prompt, model and permission mode, under the session id the
// step run was recorded with. The resulting sessionFinishedMsg carries
// the run and step run ids so the ending is recorded against them.
func (a app) launchWorkflowStepCmd(msg workflowRunPreparedMsg) tea.Cmd {
	req, sr := msg.req, msg.stepRun
	mcpPath, mcpCleanup, _ := agent.WriteMCPConfig(req.t.ID, a.dbPath)
	hooksPath, hooksCleanup, _ := agent.WriteHookSettings(a.dbPath)
	label := req.label()
	c, tmuxName, confPath := wrapInTmux(agent.LaunchCmdWith(msg.cwd, sr.SessionExternalID, label, mcpPath, hooksPath,
		agent.LaunchOpts{Prompt: sr.PromptRendered, Model: sr.Model, PermissionMode: sr.PermissionMode}),
		sr.SessionExternalID)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		bg := err == nil && agent.HasSession(tmuxName, confPath)
		if !bg {
			mcpCleanup()
			hooksCleanup()
		}
		return sessionFinishedMsg{
			taskID:       req.t.ID,
			externalID:   sr.SessionExternalID,
			cwd:          msg.cwd,
			label:        label,
			tmuxSession:  tmuxName,
			backgrounded: bg,
			err:          err,
			runID:        msg.run.ID,
			stepRunID:    sr.ID,
		}
	})
}

// recordSession writes the session row for a finished launch: bound to
// its step run when the session was a workflow step, plain otherwise.
func (a app) recordSession(msg sessionFinishedMsg) error {
	if msg.stepRunID != 0 {
		_, err := a.store.CreateStepRunSession(a.ctx, msg.stepRunID, msg.taskID,
			msg.externalID, msg.cwd, msg.label, msg.tmuxSession)
		return err
	}
	_, err := a.store.CreateSession(a.ctx, msg.taskID, msg.externalID, msg.cwd, msg.label, msg.tmuxSession)
	return err
}

// failWorkflowRunCmd marks a run failed after its launch errored. The
// error flash is already showing, so this refreshes silently: a store
// error here is reported, success is not.
func (a app) failWorkflowRunCmd(runID int64) tea.Cmd {
	return func() tea.Msg {
		if err := a.store.SetRunState(a.ctx, runID, workflow.RunFailed); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// finishStepRunIfAny ends the run of a session that ran a workflow step,
// and is a no-op for an ordinary session -- the shape sessionResumedMsg
// needs, where the step run is optional.
func (a app) finishStepRunIfAny(stepRunID *int64) error {
	if stepRunID == nil {
		return nil
	}
	return a.store.FinishRunAtStep(a.ctx, *stepRunID, workflow.OutcomeDone, "")
}

// finishStepRunCmd is finishStepRunIfAny as a Cmd, for the recap drain,
// which learns about a backgrounded workflow session ending outside any
// terminal handoff. Silent on success like failWorkflowRunCmd: the drain
// is housekeeping, not something the user asked for.
func (a app) finishStepRunCmd(stepRunID int64) tea.Cmd {
	return func() tea.Msg {
		if err := a.store.FinishRunAtStep(a.ctx, stepRunID, workflow.OutcomeDone, ""); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

// workflowRunPickerView renders the chooser box in the project picker's
// mould: a title naming the task, then the numbered workflows with their
// step counts. A workflow this path cannot run is marked so the refusal
// is not a surprise.
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
		default:
			meta += " · needs the runner"
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
