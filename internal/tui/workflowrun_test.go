package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// stubClaudeInstalled makes the launch path believe claude is on $PATH
// for the duration of a test, whatever the host has.
func stubClaudeInstalled(t *testing.T) {
	t.Helper()
	prev := checkInstalled
	checkInstalled = func() error { return nil }
	t.Cleanup(func() { checkInstalled = prev })
}

// oneStepWorkflow authors a workflow with a single agent step carrying
// prompt, model and permission mode -- the shape the POC runs.
func oneStepWorkflow(t *testing.T, s *store.Store, name, prompt string) (workflow.Workflow, workflow.Step) {
	t.Helper()
	ctx := context.Background()
	w, err := s.CreateWorkflow(ctx, name, "")
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	st, err := s.AddStep(ctx, w.ID, "fix", workflow.StepAgent)
	if err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	st.PromptMD, st.Model, st.PermissionMode = prompt, "opus", "acceptEdits"
	if err := s.UpdateStep(ctx, st); err != nil {
		t.Fatalf("UpdateStep: %v", err)
	}
	return w, st
}

// stepW presses `w` on the selected task and runs the resulting load by
// hand, the same "step once" idiom as stepR.
func stepW(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	m2, cmd := m.Update(keyPress('w'))
	if cmd == nil {
		t.Fatal("w did not produce a load command")
	}
	m3, _ := m2.Update(cmd())
	return m3
}

func TestRunWorkflowKeyWithNoWorkflowsFlashes(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "do the thing"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = stepW(t, m)
	a := m.(app)
	if a.wfRunPickerOpen {
		t.Error("picker should not open with no workflows")
	}
	if !strings.Contains(a.status.text, "no workflows") {
		t.Errorf("status = %q, want a no-workflows hint", a.status.text)
	}
}

func TestRunWorkflowPickerOpensAndDismisses(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "flaky scheduler test")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	oneStepWorkflow(t, s, "fix a bug", "Fix it.")
	m = drive(t, m, refreshMsg{})

	m = stepW(t, m)
	a := m.(app)
	if !a.wfRunPickerOpen {
		t.Fatal("picker not open with a workflow to pick")
	}
	if a.wfRunPickerTask.ID != tk.ID {
		t.Errorf("picker task = #%d, want #%d", a.wfRunPickerTask.ID, tk.ID)
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"run workflow on", "flaky scheduler test", "fix a bug", "1 step"} {
		if !strings.Contains(content, want) {
			t.Errorf("picker missing %q:\n%s", want, content)
		}
	}

	m = drive(t, m, esc())
	if m.(app).wfRunPickerOpen {
		t.Error("picker still open after esc")
	}
}

// A workflow with more than one step needs the runner (#179); picking one
// is refused with a flash rather than half-launching it.
func TestRunWorkflowRefusesMultiStepWorkflow(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "do the thing"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	w, _ := oneStepWorkflow(t, s, "review loop", "Implement.")
	if _, err := s.AddStep(ctx, w.ID, "review", workflow.StepAgent); err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = stepW(t, m)

	m = drive(t, m, enter())
	a := m.(app)
	if a.wfRunPickerOpen {
		t.Error("picker still open after picking")
	}
	if a.promptKind != promptNone {
		t.Errorf("promptKind = %v, want none: a multi-step workflow must not reach the cwd prompt", a.promptKind)
	}
	if !a.status.isErr || !strings.Contains(a.status.text, "runner") {
		t.Errorf("status = %+v, want an error flash pointing at the runner", a.status)
	}
	runs, err := s.ListRunsForTask(ctx, a.wfRunPickerTask.ID)
	if err == nil && len(runs) != 0 {
		t.Errorf("a refused workflow created %d runs", len(runs))
	}
}

// A one-step workflow leads to the cwd prompt, prefilled with the task's
// last session directory, like a plain launch.
func TestRunWorkflowOneStepPromptsForCwdWithLastSessionDir(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, tk.ID, "ext-1", "/tmp/last-work", tk.Title, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	oneStepWorkflow(t, s, "fix a bug", "Fix it.")
	m = drive(t, m, refreshMsg{})
	m = stepW(t, m)

	m = drive(t, m, keyPress('1'))
	a := m.(app)
	if a.promptKind != promptWorkflowCwd {
		t.Fatalf("promptKind = %v, want promptWorkflowCwd", a.promptKind)
	}
	if a.wfRunPending == nil || a.wfRunPending.w.Name != "fix a bug" {
		t.Fatalf("wfRunPending = %+v, want the picked workflow", a.wfRunPending)
	}
	if a.prompt.Value() != "/tmp/last-work" {
		t.Errorf("prompt prefilled with %q, want the last session's cwd", a.prompt.Value())
	}

	m = drive(t, m, esc())
	a = m.(app)
	if a.promptKind != promptNone || a.wfRunPending != nil {
		t.Error("esc should close the prompt and drop the pending request")
	}
}

// The acceptance path, short of the terminal handoff: submitting the cwd
// renders the step prompt against the task and cwd, and records a running
// run with one step run holding exactly what will be launched.
func TestRunWorkflowSubmitRendersPromptAndRecordsRun(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "flaky scheduler test")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetBody(ctx, tk.ID, "It fails one run in ten."); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	w, st := oneStepWorkflow(t, s, "fix a bug",
		"Fix #{{.Task.ID}} ({{.Task.Title}}) in {{.Cwd}}.\n\n{{.Task.Body}}")
	stubClaudeInstalled(t)
	m = drive(t, m, refreshMsg{})
	m = stepW(t, m)
	m = drive(t, m, enter()) // pick → cwd prompt

	a := m.(app)
	a.prompt.SetValue("/tmp/repo")
	// Step once: the prepare Cmd is run by hand and its message inspected,
	// but never fed back, since Update would answer it with the exec.
	m2, cmd := a.Update(enter())
	if cmd == nil {
		t.Fatal("submitting the cwd produced no command")
	}
	if m2.(app).promptKind != promptNone {
		t.Error("prompt still open after submit")
	}
	prepared, ok := cmd().(workflowRunPreparedMsg)
	if !ok {
		t.Fatalf("prepare produced %T, want workflowRunPreparedMsg", cmd())
	}

	wantPrompt := "Fix #" + strconv.FormatInt(tk.ID, 10) + " (flaky scheduler test) in /tmp/repo.\n\nIt fails one run in ten."
	if prepared.stepRun.PromptRendered != wantPrompt {
		t.Errorf("rendered prompt = %q, want %q", prepared.stepRun.PromptRendered, wantPrompt)
	}
	if prepared.stepRun.Model != "opus" || prepared.stepRun.PermissionMode != "acceptEdits" {
		t.Errorf("step run = %+v, want the step's model and permission mode recorded", prepared.stepRun)
	}
	if prepared.stepRun.SessionExternalID == "" {
		t.Error("step run has no session id; the launch must pin one up front")
	}
	if prepared.cwd != "/tmp/repo" || prepared.req.w.ID != w.ID || prepared.req.step.ID != st.ID {
		t.Errorf("prepared = %+v, want the picked workflow, step and cwd", prepared)
	}

	run, err := s.GetRun(ctx, prepared.run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.State != workflow.RunRunning || run.TaskID != tk.ID || run.Cwd != "/tmp/repo" {
		t.Errorf("stored run = %+v, want running on #%d in /tmp/repo", run, tk.ID)
	}
	if run.CurrentStepRunID == nil || *run.CurrentStepRunID != prepared.stepRun.ID {
		t.Errorf("run.CurrentStepRunID = %v, want %d", run.CurrentStepRunID, prepared.stepRun.ID)
	}
	sr, err := s.GetStepRun(ctx, prepared.stepRun.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if sr.PromptRendered != wantPrompt || sr.Finished() {
		t.Errorf("stored step run = %+v, want the rendered prompt, unfinished", sr)
	}
}

// A prompt that does not render is an authoring error: it is reported by
// step, and no run is written for it.
func TestRunWorkflowRenderErrorLeavesNoRun(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	oneStepWorkflow(t, s, "typo", "Fix {{.Task.Nope}}.")
	stubClaudeInstalled(t)
	m = drive(t, m, refreshMsg{})
	m = stepW(t, m)
	m = drive(t, m, enter())

	m = drive(t, m, enter()) // accept the prefilled cwd
	a := m.(app)
	if !a.status.isErr || !strings.Contains(a.status.text, "typo / fix") {
		t.Errorf("status = %+v, want an error flash naming the workflow and step", a.status)
	}
	runs, err := s.ListRunsForTask(ctx, tk.ID)
	if err != nil {
		t.Fatalf("ListRunsForTask: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("a render failure created %d runs, want none", len(runs))
	}
}

// When claude is missing the flash says so before any row is written.
func TestRunWorkflowWithoutClaudeLeavesNoRun(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	oneStepWorkflow(t, s, "fix a bug", "Fix it.")
	prev := checkInstalled
	checkInstalled = func() error { return errors.New("claude: not found") }
	t.Cleanup(func() { checkInstalled = prev })
	m = drive(t, m, refreshMsg{})
	m = stepW(t, m)
	m = drive(t, m, enter())

	m = drive(t, m, enter())
	if a := m.(app); !a.status.isErr || !strings.Contains(a.status.text, "not found") {
		t.Errorf("status = %+v, want the missing-claude error", a.status)
	}
	if runs, _ := s.ListRunsForTask(ctx, tk.ID); len(runs) != 0 {
		t.Errorf("a launch with no claude created %d runs, want none", len(runs))
	}
}

// preparedRun writes a running run with one step run directly, standing
// in for prepareWorkflowRunCmd so the ending can be tested on its own.
func preparedRun(t *testing.T, s *store.Store, tk task.Task) (workflow.Run, workflow.StepRun) {
	t.Helper()
	ctx := context.Background()
	w, st := oneStepWorkflow(t, s, "fix a bug", "Fix it.")
	run, err := s.CreateRun(ctx, w.ID, tk.ID, "/tmp/repo")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.SetRunState(ctx, run.ID, workflow.RunRunning); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}
	sr, err := s.CreateStepRun(ctx, workflow.StepRun{
		RunID: run.ID, StepID: st.ID, SessionExternalID: "ext-wf", PromptRendered: "Fix it.",
	})
	if err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	return run, sr
}

// launchedStep writes the row launchWorkflowStepCmd writes ahead of its
// handoff: the step's session, bound to its step run.
func launchedStep(t *testing.T, s *store.Store, sr workflow.StepRun, tk task.Task, tmux string) task.Session {
	t.Helper()
	sess, err := s.CreateStepRunSession(context.Background(), sr.ID, tk.ID, sr.SessionExternalID,
		"/tmp/repo", "fix a bug — "+tk.Title, tmux)
	if err != nil {
		t.Fatalf("CreateStepRunSession: %v", err)
	}
	return sess
}

// finishedStep is finished for a workflow step's session: the run and
// step run ids ride along so the ending is recorded against them.
func finishedStep(sess task.Session, run workflow.Run, sr workflow.StepRun) sessionFinishedMsg {
	msg := finished(sess)
	msg.runID, msg.stepRunID = run.ID, sr.ID
	return msg
}

// The launch half for a workflow step: the session row is written ahead
// of the handoff, bound to its step run and already starting, so the
// step's hooks have a row from its first turn like any other session.
func TestLaunchWorkflowStepCmdWritesStartingRowBeforeHandoff(t *testing.T) {
	isolateLaunchFiles(t)
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	run, sr := preparedRun(t, s, tk)
	w, _ := oneStepWorkflow(t, s, "another", "unused")
	req := workflowRunRequest{t: tk, w: w, step: workflow.Step{ID: sr.StepID, Name: "fix"}}

	msg := m.(app).launchWorkflowStepCmd(workflowRunPreparedMsg{req: req, cwd: "/tmp/repo", run: run, stepRun: sr})()
	if e, ok := msg.(errMsg); ok {
		t.Fatalf("launch produced an error instead of the handoff: %v", e.err)
	}
	if msg == nil {
		t.Fatal("launch produced no message; want the exec handoff")
	}

	sessions, err := s.ListSessionsForTask(ctx, tk.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want the row written ahead of the handoff", sessions)
	}
	sess := sessions[0]
	if sess.ExternalID != sr.SessionExternalID || sess.StepRunID == nil || *sess.StepRunID != sr.ID {
		t.Errorf("session = %+v, want it under the step run's session id and bound to step run %d", sess, sr.ID)
	}
	if sess.Status != task.SessionStarting {
		t.Errorf("Status = %q, want %q right after launch", sess.Status, task.SessionStarting)
	}
	if sess.Label != req.label() || sess.Cwd != "/tmp/repo" {
		t.Errorf("session = %+v, want the step's label and cwd", sess)
	}
	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != workflow.RunRunning {
		t.Errorf("run state = %s, want still running while the step is up", got.State)
	}
}

// A workflow session's handoff returning for real keeps the session
// against its step run and ends the run.
func TestWorkflowSessionFinishedEndsRun(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	run, sr := preparedRun(t, s, tk)
	sess := launchedStep(t, s, sr, tk, "")
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, finishedStep(sess, run, sr))

	waitFor(t, "run ended", func() bool {
		got, err := s.GetRun(ctx, run.ID)
		return err == nil && got.State == workflow.RunDone
	})
	fin, err := s.GetStepRun(ctx, sr.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if !fin.Finished() || fin.Outcome != workflow.OutcomeDone {
		t.Errorf("step run = %+v, want finished with done", fin)
	}
	sessions, err := s.ListSessionsForTask(ctx, tk.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	if len(sessions) != 1 || sessions[0].StepRunID == nil || *sessions[0].StepRunID != sr.ID {
		t.Errorf("sessions = %+v, want one bound to step run %d", sessions, sr.ID)
	}
	_ = m
}

// Backgrounding a workflow session leaves the run running -- the step is
// not over -- with the recap debt recorded like any other session.
func TestWorkflowSessionBackgroundedKeepsRunRunning(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	run, sr := preparedRun(t, s, tk)
	sess := launchedStep(t, s, sr, tk, "tend-ext-wf")
	stubSessionAlive(t, true) // still running under tmux: the drain must leave it alone
	m = drive(t, m, refreshMsg{})

	msg := finishedStep(sess, run, sr)
	msg.backgrounded = true
	m = drive(t, m, msg)

	waitFor(t, "session recorded", func() bool {
		sessions, err := s.ListSessionsForTask(ctx, tk.ID)
		return err == nil && len(sessions) == 1 && sessions[0].NeedsRecap
	})
	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != workflow.RunRunning {
		t.Errorf("run state after backgrounding = %s, want running", got.State)
	}
	_ = m
}

// Once a backgrounded workflow session is found dead, the recap drain is
// the first to know its step ended, and it ends the run along with
// settling the recap.
func TestDrainEndsRunOfDeadWorkflowSession(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	run, sr := preparedRun(t, s, tk)
	if _, err := s.CreateStepRunSession(ctx, sr.ID, tk.ID, "ext-wf", "/tmp/repo", "fix a bug", "tend-ext-wf"); err != nil {
		t.Fatalf("CreateStepRunSession: %v", err)
	}
	if err := s.SetSessionNeedsRecap(ctx, "ext-wf", true); err != nil {
		t.Fatalf("SetSessionNeedsRecap: %v", err)
	}
	stubSessionAlive(t, false)

	m = drive(t, m, refreshMsg{})

	waitFor(t, "run ended by the drain", func() bool {
		got, err := s.GetRun(ctx, run.ID)
		return err == nil && got.State == workflow.RunDone
	})
	fin, err := s.GetStepRun(ctx, sr.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if !fin.Finished() {
		t.Errorf("step run = %+v, want finished", fin)
	}
	_ = m
}

// A launch that errors out marks the run failed instead of leaving it
// running forever, and takes back the session row written ahead of the
// handoff so no phantom session is left behind it.
func TestWorkflowSessionLaunchErrorFailsRun(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	run, sr := preparedRun(t, s, tk)
	sess := launchedStep(t, s, sr, tk, "tend-ext-wf")
	m = drive(t, m, refreshMsg{})

	msg := finishedStep(sess, run, sr)
	msg.err = context.DeadlineExceeded
	m = drive(t, m, msg)
	if !m.(app).status.isErr {
		t.Error("expected an error flash after a failed launch")
	}
	waitFor(t, "run failed", func() bool {
		got, err := s.GetRun(ctx, run.ID)
		return err == nil && got.State == workflow.RunFailed
	})
	waitFor(t, "launch row deleted", func() bool {
		sessions, err := s.ListSessionsForTask(ctx, tk.ID)
		return err == nil && len(sessions) == 0
	})
}
