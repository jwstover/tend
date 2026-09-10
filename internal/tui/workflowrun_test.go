package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/store"
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

// stubTmuxInstalled does the same for tmux, which the runner needs.
func stubTmuxInstalled(t *testing.T, installed bool) {
	t.Helper()
	prev := tmuxInstalled
	tmuxInstalled = func() bool { return installed }
	t.Cleanup(func() { tmuxInstalled = prev })
}

// launchRecord is what a stubbed launchRunner saw.
type launchRecord struct {
	runID  int64
	dbPath string
}

// stubLaunchRunner replaces the tmux launch with one that records the
// call and, like the real thing, stores the tmux session name on the run;
// err, when set, is what the launch fails with instead.
func stubLaunchRunner(t *testing.T, err error) *launchRecord {
	t.Helper()
	rec := &launchRecord{}
	prev := launchRunner
	launchRunner = func(ctx context.Context, s runner.LaunchStore, runID int64, dbPath string) (string, error) {
		rec.runID, rec.dbPath = runID, dbPath
		if err != nil {
			return "", err
		}
		name := "tend-wf-stub"
		return name, s.SetRunTmuxSession(ctx, runID, name)
	}
	t.Cleanup(func() { launchRunner = prev })
	return rec
}

// runnable stubs everything the pre-flight checks shell out for.
func runnable(t *testing.T) {
	t.Helper()
	stubClaudeInstalled(t)
	stubTmuxInstalled(t, true)
}

// oneStepWorkflow authors a workflow with a single agent step carrying
// prompt, model and permission mode.
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
	w, _ := oneStepWorkflow(t, s, "fix a bug", "Fix it.")
	if _, err := s.AddStep(ctx, w.ID, "ship", workflow.StepAgent); err != nil {
		t.Fatalf("AddStep: %v", err)
	}
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
	for _, want := range []string{"run workflow on", "flaky scheduler test", "fix a bug", "2 steps"} {
		if !strings.Contains(content, want) {
			t.Errorf("picker missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "runner") {
		t.Errorf("picker still marks multi-step workflows as needing the runner:\n%s", content)
	}

	m = drive(t, m, esc())
	if m.(app).wfRunPickerOpen {
		t.Error("picker still open after esc")
	}
}

// Any graph runs now: a multi-step workflow with a gate reaches the cwd
// prompt, prefilled with the task's last session directory like a plain
// launch.
func TestRunWorkflowMultiStepPromptsForCwdWithLastSessionDir(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, tk.ID, "ext-1", "/tmp/last-work", tk.Title, ""); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	w, _ := oneStepWorkflow(t, s, "review loop", "Implement {{.Task.Title}}.")
	if _, err := s.AddStep(ctx, w.ID, "gate", workflow.StepGate); err != nil { // no prompt: allowed for a gate
		t.Fatalf("AddStep: %v", err)
	}
	runnable(t)
	m = drive(t, m, refreshMsg{})
	m = stepW(t, m)

	m = drive(t, m, keyPress('1'))
	a := m.(app)
	if a.promptKind != promptWorkflowCwd {
		t.Fatalf("promptKind = %v, status %+v; want promptWorkflowCwd", a.promptKind, a.status)
	}
	if a.wfRunPending == nil || a.wfRunPending.w.Name != "review loop" {
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
	if runs, _ := s.ListRunsForTask(ctx, tk.ID); len(runs) != 0 {
		t.Errorf("dismissing the prompt left %d runs", len(runs))
	}
}

// The acceptance path from the TUI's side: submitting the cwd writes a
// pending run, starts its runner, records the runner's tmux session, and
// returns to the TUI with a flash naming the run. No step run is written
// here -- that is the runner's job.
func TestRunWorkflowSubmitCreatesRunAndStartsRunner(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "flaky scheduler test")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	w, _ := oneStepWorkflow(t, s, "fix a bug", "Fix #{{.Task.ID}} in {{.Cwd}}.")
	runnable(t)
	rec := stubLaunchRunner(t, nil)
	m = drive(t, m, refreshMsg{})
	m = stepW(t, m)
	m = drive(t, m, enter()) // pick → cwd prompt

	a := m.(app)
	a.prompt.SetValue("/tmp/repo")
	m = drive(t, a, enter())
	a = m.(app)
	if a.promptKind != promptNone {
		t.Error("prompt still open after submit")
	}
	runs, err := s.ListRunsForTask(ctx, tk.ID)
	if err != nil {
		t.Fatalf("ListRunsForTask: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want one", runs)
	}
	run := runs[0]
	if run.WorkflowID != w.ID || run.Cwd != "/tmp/repo" || run.State != workflow.RunPending || run.TmuxSession != "tend-wf-stub" {
		t.Errorf("run = %+v, want pending on %s in /tmp/repo with the runner's tmux session", run, w.Name)
	}
	if rec.runID != run.ID {
		t.Errorf("runner launched for run %d, want %d", rec.runID, run.ID)
	}
	if srs, _ := s.ListStepRunsForRun(ctx, run.ID); len(srs) != 0 {
		t.Errorf("the TUI wrote %d step runs; the runner owns those", len(srs))
	}
	if a.status.isErr || !strings.Contains(a.status.text, "fix a bug") || !strings.Contains(a.status.text, "tend-wf-stub") {
		t.Errorf("status = %+v, want a flash naming the workflow and its tmux session", a.status)
	}
}

// A runner that cannot be started fails the run with the reason rather
// than leaving a pending run nobody will claim, and the error is shown.
func TestRunWorkflowLaunchFailureFailsRun(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	tk, err := s.AddTask(ctx, "do the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	oneStepWorkflow(t, s, "fix a bug", "Fix it.")
	runnable(t)
	stubLaunchRunner(t, errors.New("starting runner for run 1 in tmux: duplicate session"))
	m = drive(t, m, refreshMsg{})
	m = stepW(t, m)
	m = drive(t, m, enter())
	m = drive(t, m, enter()) // accept the prefilled cwd

	a := m.(app)
	if !a.status.isErr || !strings.Contains(a.status.text, "duplicate session") {
		t.Errorf("status = %+v, want the launch error", a.status)
	}
	runs, _ := s.ListRunsForTask(ctx, tk.ID)
	if len(runs) != 1 || runs[0].State != workflow.RunFailed || !strings.Contains(runs[0].Error, "duplicate session") {
		t.Errorf("runs = %+v, want one failed run carrying the reason", runs)
	}
}

// Pre-flight refusals happen at pick time, before the cwd prompt, and
// leave no run behind.
func TestRunWorkflowRefusals(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		setup  func(t *testing.T)
		want   string
	}{
		{
			name:   "prompt does not render",
			prompt: "Fix {{.Task.Nope}}.",
			setup:  runnable,
			want:   "typo / fix",
		},
		{
			name:   "claude missing",
			prompt: "Fix it.",
			setup: func(t *testing.T) {
				stubTmuxInstalled(t, true)
				prev := checkInstalled
				checkInstalled = func() error { return errors.New("claude: not found") }
				t.Cleanup(func() { checkInstalled = prev })
			},
			want: "claude: not found",
		},
		{
			name:   "tmux missing",
			prompt: "Fix it.",
			setup: func(t *testing.T) {
				stubClaudeInstalled(t)
				stubTmuxInstalled(t, false)
			},
			want: "tmux",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, s := newTestApp(t)
			tk, err := s.AddTask(ctx, "do the thing")
			if err != nil {
				t.Fatalf("AddTask: %v", err)
			}
			oneStepWorkflow(t, s, "typo", tc.prompt)
			tc.setup(t)
			m = drive(t, m, refreshMsg{})
			m = stepW(t, m)
			m = drive(t, m, enter())

			a := m.(app)
			if a.promptKind != promptNone {
				t.Errorf("promptKind = %v, want none: a refused workflow must not reach the cwd prompt", a.promptKind)
			}
			if !a.status.isErr || !strings.Contains(a.status.text, tc.want) {
				t.Errorf("status = %+v, want an error flash mentioning %q", a.status, tc.want)
			}
			if runs, _ := s.ListRunsForTask(ctx, tk.ID); len(runs) != 0 {
				t.Errorf("a refused workflow created %d runs", len(runs))
			}
		})
	}
}

// A workflow with no steps is refused with a hint at the authoring view.
func TestRunWorkflowWithNoStepsIsRefused(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "do the thing"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateWorkflow(ctx, "empty", ""); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	runnable(t)
	m = drive(t, m, refreshMsg{})
	m = stepW(t, m)
	m = drive(t, m, enter())
	if a := m.(app); !a.status.isErr || !strings.Contains(a.status.text, "no steps") {
		t.Errorf("status = %+v, want a no-steps refusal", a.status)
	}
}
