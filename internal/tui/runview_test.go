package tui

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// stubRunnerAlive pins the runner liveness check, so no test asks the
// real tmux server whether a run's runner exists.
func stubRunnerAlive(t *testing.T, alive bool) {
	t.Helper()
	prev := runnerAlive
	runnerAlive = func(workflow.Run) bool { return alive }
	t.Cleanup(func() { runnerAlive = prev })
}

// stubResumeRunner replaces runner.Resume with one that records the call.
func stubResumeRunner(t *testing.T) *launchRecord {
	t.Helper()
	rec := &launchRecord{}
	prev := resumeRunner
	resumeRunner = func(ctx context.Context, s runner.LaunchStore, runID int64, dbPath string) (string, error) {
		rec.runID, rec.dbPath = runID, dbPath
		return "tend-wf-stub", nil
	}
	t.Cleanup(func() { resumeRunner = prev })
	return rec
}

// liveRun is a run the runner has claimed and started a step of: a task,
// a two-step workflow (an agent step then a gate), the run in state st
// with the agent step's step run current and its session row written --
// exactly what the runner leaves mid-step.
type liveRun struct {
	tk      task.Task
	wf      workflow.Workflow
	agent   workflow.Step
	gate    workflow.Step
	run     workflow.Run
	stepRun workflow.StepRun
}

func newLiveRun(t *testing.T, s *store.Store, st workflow.RunState) liveRun {
	t.Helper()
	ctx := context.Background()
	tk, err := s.AddTask(ctx, "flaky scheduler test")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	wf, err := s.CreateWorkflow(ctx, "ship it", "")
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	agent, err := s.AddStep(ctx, wf.ID, "implement", workflow.StepAgent)
	if err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	gate, err := s.AddStep(ctx, wf.ID, "review", workflow.StepGate)
	if err != nil {
		t.Fatalf("AddStep: %v", err)
	}
	if _, err := s.SetEdge(ctx, agent.ID, "done", gate.ID, nil); err != nil {
		t.Fatalf("SetEdge: %v", err)
	}
	if _, err := s.SetEdge(ctx, gate.ID, "approve", agent.ID, nil); err != nil {
		t.Fatalf("SetEdge: %v", err)
	}
	if _, err := s.SetEdge(ctx, gate.ID, "reject", agent.ID, nil); err != nil {
		t.Fatalf("SetEdge: %v", err)
	}
	run, err := s.CreateRun(ctx, wf.ID, tk.ID, "/tmp/repo")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if ok, err := s.ClaimRun(ctx, run.ID); err != nil || !ok {
		t.Fatalf("ClaimRun = (%v, %v)", ok, err)
	}
	sr, err := s.CreateStepRun(ctx, workflow.StepRun{
		RunID: run.ID, StepID: agent.ID, SessionExternalID: "step-sess", PromptRendered: "Fix it.",
	})
	if err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "step.jsonl")
	if err := s.SetStepRunLogPath(ctx, sr.ID, logPath); err != nil {
		t.Fatalf("SetStepRunLogPath: %v", err)
	}
	sr.LogPath = logPath
	if _, err := s.CreateStepRunSession(ctx, sr.ID, tk.ID, "step-sess", run.Cwd, "implement — "+tk.Title, ""); err != nil {
		t.Fatalf("CreateStepRunSession: %v", err)
	}
	if st != workflow.RunRunning {
		if err := s.SetRunState(ctx, run.ID, st); err != nil {
			t.Fatalf("SetRunState(%s): %v", st, err)
		}
	}
	run, err = s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	return liveRun{tk: tk, wf: wf, agent: agent, gate: gate, run: run, stepRun: sr}
}

// atGate moves the live run on to its gate: the agent step finished, a
// gate step run current, the run waiting for review.
func (l *liveRun) atGate(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.FinishStepRun(ctx, l.stepRun.ID, "done", "PR #7"); err != nil {
		t.Fatalf("FinishStepRun: %v", err)
	}
	sr, err := s.CreateStepRun(ctx, workflow.StepRun{RunID: l.run.ID, StepID: l.gate.ID, Input: "PR #7"})
	if err != nil {
		t.Fatalf("CreateStepRun(gate): %v", err)
	}
	if err := s.SetRunState(ctx, l.run.ID, workflow.RunWaitingReview); err != nil {
		t.Fatalf("SetRunState(waiting_review): %v", err)
	}
	l.stepRun = sr
	if l.run, err = s.GetRun(ctx, l.run.ID); err != nil {
		t.Fatalf("GetRun: %v", err)
	}
}

const stepLogFixture = `{"type":"system","subtype":"init","model":"claude-haiku-4-5-20251001","permissionMode":"acceptEdits"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at the scheduler."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}
`

// openRun drives `v` on the selected task through to the run view.
func openRun(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	m = drive(t, m, keyPress('v'))
	if a := m.(app); a.mode != modeRun {
		t.Fatalf("mode = %v after v, status %+v; want the run view", a.mode, a.status)
	}
	return m
}

// --- poller ---------------------------------------------------------------

// pollRuns reports a change when a run moves -- state, current step, its
// log growing -- or ends, and nothing when the snapshot is unchanged.
func TestPollRunsReportsMovement(t *testing.T) {
	ctx := context.Background()
	_, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)

	snap, changed := pollRuns(ctx, s, nil)
	if !changed || len(snap) != 1 {
		t.Fatalf("first tick = (%v, %v), want a change with one live run", snap, changed)
	}
	if snap, changed = pollRuns(ctx, s, snap); changed {
		t.Error("an unchanged run reported a change")
	}

	// The step writes to its log.
	if err := os.WriteFile(l.stepRun.LogPath, []byte(stepLogFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if snap, changed = pollRuns(ctx, s, snap); !changed {
		t.Error("a growing step log did not report a change")
	}

	// The run moves to the gate: new current step and state.
	l.atGate(t, s)
	if snap, changed = pollRuns(ctx, s, snap); !changed {
		t.Error("a state/step change did not report a change")
	}
	if snap, changed = pollRuns(ctx, s, snap); changed {
		t.Error("an unchanged gate reported a change")
	}

	// The run ends and leaves the active set.
	if err := s.SetRunState(ctx, l.run.ID, workflow.RunCancelled); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}
	if snap, changed = pollRuns(ctx, s, snap); !changed || len(snap) != 0 {
		t.Errorf("an ending run = (%v, %v), want a change and an empty snapshot", snap, changed)
	}
}

// --- gutter and detail ----------------------------------------------------

// A task with a running run and only a 'starting' step session reads
// working in the gutter, and one waiting at a gate with no session at all
// reads blocked -- the run's state, not the session rows, is what shows.
func TestListGutterShowsRunState(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, true)
	m, s := newTestApp(t)
	running := newLiveRun(t, s, workflow.RunRunning)
	gate, err := s.AddTask(ctx, "waiting on a review")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	gateRun, err := s.CreateRun(ctx, running.wf.ID, gate.ID, "/tmp/repo")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.SetRunState(ctx, gateRun.ID, workflow.RunWaitingReview); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}

	m = drive(t, m, refreshMsg{})
	a := m.(app)
	if got := a.sessionStatus[running.tk.ID]; got != task.SessionWorking {
		t.Errorf("running run's task status = %q, want working", got)
	}
	if got := a.sessionStatus[gate.ID]; got != task.SessionBlocked {
		t.Errorf("gate run's task status = %q, want blocked", got)
	}
	content := ansi.Strip(m.View().Content)
	g := a.styles.Glyphs.Session
	for _, row := range strings.Split(content, "\n") {
		switch {
		case strings.Contains(row, running.tk.Title) && !strings.Contains(row, g[task.SessionWorking]):
			t.Errorf("running task's row has no working marker: %q", row)
		case strings.Contains(row, gate.Title) && !strings.Contains(row, g[task.SessionBlocked]):
			t.Errorf("gate task's row has no blocked marker: %q", row)
		}
	}
}

// The detail pane lists the task's runs under WORKFLOWS with the workflow,
// current step, state and -- for a run whose runner is gone -- says so.
func TestDetailPaneShowsWorkflowsSection(t *testing.T) {
	stubRunnerAlive(t, false)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	m = drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress(']'))

	a := m.(app)
	if runs := a.runsCache[l.tk.ID]; len(runs) != 1 || runs[0].run.ID != l.run.ID || !runs[0].runnerGone {
		t.Fatalf("runsCache = %+v, want the live run marked runner-gone", runs)
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"WORKFLOWS", "ship it", "implement", "running", "runner gone", "v to watch"} {
		if !strings.Contains(content, want) {
			t.Errorf("detail pane missing %q:\n%s", want, content)
		}
	}
}

// --- takeover guard -------------------------------------------------------

// `r` on a step session whose run is live and not paused is refused, so a
// second claude never lands on a transcript the runner still owns.
func TestResumeStepSessionRefusedWhileRunLive(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	m = drive(t, m, refreshMsg{})
	m = stepR(t, m)
	if !m.(app).sessionPickerOpen {
		t.Fatal("session picker not open")
	}

	m = drive(t, m, keyPress('1'))
	a := m.(app)
	if !a.status.isErr || !strings.Contains(a.status.text, "pause the run first") {
		t.Errorf("status = %+v, want a refusal that says to pause the run", a.status)
	}

	// Paused: the guard lets the resume through to the claude/tmux check,
	// which is as far as a test can follow it.
	if err := s.SetRunState(ctx, l.run.ID, workflow.RunPaused); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}
	prev := checkInstalled
	t.Cleanup(func() { checkInstalled = prev })
	m = drive(t, m, refreshMsg{})
	m = stepR(t, m)
	m = drive(t, m, keyPress('1'))
	if a := m.(app); a.status.isErr && strings.Contains(a.status.text, "pause the run first") {
		t.Errorf("status = %+v; a paused run's step session must not be refused by the guard", a.status)
	}
}

// --- run view -------------------------------------------------------------

// `v` opens the run view on a task's single run: the step list with the
// current step, the log rendered from the step's stream-json, and `l`
// flipping to the raw lines.
func TestRunViewShowsStepsAndLog(t *testing.T) {
	stubRunnerAlive(t, true)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	if err := os.WriteFile(l.stepRun.LogPath, []byte(stepLogFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = drive(t, m, refreshMsg{})
	m = openRun(t, m)

	a := m.(app)
	if a.rv.runID != l.run.ID || len(a.rv.stepRuns) != 1 || a.rv.cursor != 0 || !a.rv.follow {
		t.Fatalf("run view = %+v, want run %d with its one step selected and followed", a.rv, l.run.ID)
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"RUN " + strconv.FormatInt(l.run.ID, 10), "ship it", "STEPS", "implement", "running",
		"LOG", "Looking at the scheduler.", "⚙ Bash: go test ./...", "following"} {
		if !strings.Contains(content, want) {
			t.Errorf("run view missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, `"type":"assistant"`) {
		t.Errorf("rendered view leaks raw JSON:\n%s", content)
	}

	m = drive(t, m, keyPress('l'))
	content = ansi.Strip(m.View().Content)
	if !strings.Contains(content, `"type":"assistant"`) || !strings.Contains(content, "raw") {
		t.Errorf("raw toggle did not show the stream-json lines:\n%s", content)
	}

	// The log grows; the poll tick reloads the view and the tail appends.
	more := `{"type":"assistant","message":{"content":[{"type":"text","text":"All green."}]}}` + "\n"
	f, err := os.OpenFile(l.stepRun.LogPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(more); err != nil {
		t.Fatal(err)
	}
	f.Close()
	m = drive(t, m, keyPress('l'))
	m = drive(t, m, sessionsPolledMsg{changed: true})
	a = m.(app)
	if got := strings.Join(a.rv.log.rendered, "\n"); !strings.Contains(got, "Looking at the scheduler.") || !strings.HasSuffix(got, "All green.") {
		t.Errorf("tailed log = %q, want the earlier lines kept and the new one appended", got)
	}

	// esc leaves for the list.
	m = drive(t, m, esc())
	if m.(app).mode != modeList {
		t.Error("esc did not leave the run view")
	}
}

// `p` pauses a running run and resumes a paused one; `cc` cancels.
func TestRunViewPauseResumeCancel(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, true)
	rec := stubResumeRunner(t)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	m = drive(t, m, refreshMsg{})
	m = openRun(t, m)

	m = drive(t, m, keyPress('p'))
	waitFor(t, "run paused", func() bool {
		run, err := s.GetRun(ctx, l.run.ID)
		return err == nil && run.State == workflow.RunPaused
	})
	m = drive(t, m, refreshMsg{})
	if a := m.(app); a.rv.run.State != workflow.RunPaused {
		t.Errorf("view state = %s, want paused after the reload", a.rv.run.State)
	}

	m = drive(t, m, keyPress('p'))
	if rec.runID != l.run.ID {
		t.Errorf("resume launched for run %d, want %d", rec.runID, l.run.ID)
	}
	if a := m.(app); a.status.isErr || !strings.Contains(a.status.text, "resumed") {
		t.Errorf("status = %+v, want a resumed flash", a.status)
	}

	// A single c only arms the chord.
	if err := s.SetRunState(ctx, l.run.ID, workflow.RunRunning); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress('c'))
	if a := m.(app); !a.cancelPending {
		t.Fatal("first c did not arm the cancel chord")
	}
	if run, _ := s.GetRun(ctx, l.run.ID); run.State != workflow.RunRunning {
		t.Fatalf("a single c changed the run to %s", run.State)
	}
	m = drive(t, m, esc())
	if a := m.(app); a.cancelPending || a.mode != modeRun {
		t.Fatal("esc did not back out of the cancel chord in place")
	}
	m = drive(t, m, keyPress('c'))
	m = drive(t, m, keyPress('c'))
	waitFor(t, "run cancelled", func() bool {
		run, err := s.GetRun(ctx, l.run.ID)
		return err == nil && run.State == workflow.RunCancelled
	})
	_ = m
}

// `a` and `x` decide the gate the run is waiting at through the same
// one-shot FinishStepRun the finish_step tool uses; a decision the gate's
// edges do not route is refused.
func TestRunViewGateDecision(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, true)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	l.atGate(t, s)
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = drive(t, m, refreshMsg{})
	m = openRun(t, m)

	a := m.(app)
	if len(a.rv.stepRuns) != 2 || a.rv.cursor != 1 {
		t.Fatalf("run view = cursor %d over %d step runs, want the gate (2nd) selected", a.rv.cursor, len(a.rv.stepRuns))
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"gate is waiting for review", "approve, reject", "PR #7", "waiting_review"} {
		if !strings.Contains(content, want) {
			t.Errorf("gate pane missing %q:\n%s", want, content)
		}
	}

	m = drive(t, m, keyPress('a'))
	waitFor(t, "gate approved", func() bool {
		sr, err := s.GetStepRun(ctx, l.stepRun.ID)
		return err == nil && sr.Finished() && sr.Outcome == "approve"
	})
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress('x'))
	if a := m.(app); !strings.Contains(a.status.text, "already decided") {
		t.Errorf("status = %+v, want a refusal for a decided gate", a.status)
	}
	if sr, _ := s.GetStepRun(ctx, l.stepRun.ID); sr.Outcome != "approve" {
		t.Errorf("gate outcome = %q after x, want the first decision to stand", sr.Outcome)
	}
}

// A gate whose edges name other outcomes refuses approve/reject with the
// outcomes it does route, rather than ending the run on an edge-less one.
func TestRunViewGateRefusesUnroutedOutcome(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, true)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	edges, err := s.ListEdges(ctx, l.wf.ID)
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	for _, e := range edges {
		if e.FromStepID == l.gate.ID {
			if err := s.DeleteEdge(ctx, e.ID); err != nil {
				t.Fatalf("DeleteEdge: %v", err)
			}
		}
	}
	if _, err := s.SetEdge(ctx, l.gate.ID, "ship", l.agent.ID, nil); err != nil {
		t.Fatalf("SetEdge: %v", err)
	}
	l.atGate(t, s)
	m = drive(t, m, refreshMsg{})
	m = openRun(t, m)

	m = drive(t, m, keyPress('a'))
	a := m.(app)
	if !a.status.isErr || !strings.Contains(a.status.text, "routes ship") {
		t.Errorf("status = %+v, want a refusal naming the gate's outcomes", a.status)
	}
	if sr, _ := s.GetStepRun(ctx, l.stepRun.ID); sr.Finished() {
		t.Error("an unrouted approve finished the gate")
	}
}

// `t` refuses a takeover until the run is paused, since the runner still
// owns the step's session until then.
func TestRunViewTakeoverNeedsPausedRun(t *testing.T) {
	stubRunnerAlive(t, true)
	m, s := newTestApp(t)
	newLiveRun(t, s, workflow.RunRunning)
	m = drive(t, m, refreshMsg{})
	m = openRun(t, m)

	m = drive(t, m, keyPress('t'))
	if a := m.(app); !strings.Contains(a.status.text, "pause run") {
		t.Errorf("status = %+v, want a hint to pause first", a.status)
	}
}

// With several runs on a task, `v` opens a picker; a digit picks one.
func TestRunPickerOpensForSeveralRuns(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, true)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	old, err := s.CreateRun(ctx, l.wf.ID, l.tk.ID, "/tmp/repo")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.SetRunState(ctx, old.ID, workflow.RunDone); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	m = drive(t, m, keyPress('v'))
	a := m.(app)
	if !a.runPickerOpen || len(a.runPickerRuns) != 2 {
		t.Fatalf("picker open=%v runs=%d, want open over two runs", a.runPickerOpen, len(a.runPickerRuns))
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"workflow runs on", "running", "done"} {
		if !strings.Contains(content, want) {
			t.Errorf("run picker missing %q:\n%s", want, content)
		}
	}
	m = drive(t, m, keyPress('2'))
	a = m.(app)
	if a.runPickerOpen || a.mode != modeRun {
		t.Fatalf("picker open=%v mode=%v after picking, want the run view", a.runPickerOpen, a.mode)
	}
	if picked := a.runPickerRuns; picked != nil {
		t.Error("picker state not cleared")
	}
}

// A task with no runs flashes instead of opening anything.
func TestViewRunWithNoRunsFlashes(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	if _, err := s.AddTask(ctx, "nothing ran here"); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress('v'))
	a := m.(app)
	if a.mode != modeList || a.runPickerOpen || !strings.Contains(a.status.text, "no workflow runs") {
		t.Errorf("mode=%v picker=%v status=%+v, want a no-runs flash in the list", a.mode, a.runPickerOpen, a.status)
	}
}
