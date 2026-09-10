package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// takeoverRecord is what the takeoverResume stub saw: the session handed
// over and the ref it was marked with. backgrounded shapes the returning
// message, as a user detaching from the session would.
type takeoverRecord struct {
	sess         task.Session
	ref          takeoverRef
	calls        int
	backgrounded bool
}

// stubTakeoverResume replaces the interactive resume with one that records
// the call and yields the message a returning session would, so the
// takeover flow can be driven end to end without claude or tmux.
func stubTakeoverResume(t *testing.T) *takeoverRecord {
	t.Helper()
	rec := &takeoverRecord{}
	prev := takeoverResume
	takeoverResume = func(sess task.Session, dbPath string, ref takeoverRef) tea.Cmd {
		rec.sess, rec.ref = sess, ref
		rec.calls++
		return func() tea.Msg {
			return sessionResumedMsg{sessionRowID: sess.ID, taskID: sess.TaskID, cwd: sess.Cwd,
				externalID: sess.ExternalID, takeover: ref, backgrounded: rec.backgrounded}
		}
	}
	t.Cleanup(func() { takeoverResume = prev })
	return rec
}

// takeoverReturn is the message the flow sees when the taken-over session
// returns cleanly, for tests that start from an already-paused run.
func takeoverReturn(l liveRun, sessRowID int64) sessionResumedMsg {
	return sessionResumedMsg{sessionRowID: sessRowID, taskID: l.tk.ID, cwd: l.run.Cwd,
		externalID: l.stepRun.SessionExternalID, takeover: takeoverRef{runID: l.run.ID, stepRunID: l.stepRun.ID}}
}

// stepSessionRow finds the step's session row.
func stepSessionRow(t *testing.T, l liveRun, m tea.Model) task.Session {
	t.Helper()
	sessions, err := m.(app).store.ListSessionsForTask(context.Background(), l.tk.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	for _, sess := range sessions {
		if sess.ExternalID == l.stepRun.SessionExternalID {
			return sess
		}
	}
	t.Fatalf("no session row for %s", l.stepRun.SessionExternalID)
	return task.Session{}
}

// The acceptance path: `t` on a running step pauses the run, waits for the
// runner to let go, resumes the step's session as a takeover, and on return
// offers the picker; "continue the run" asks for the outcome and a
// deliverable, records them on the step run and starts a fresh runner.
func TestTakeoverPausesRunThenFinishesStepByHand(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, false)
	rec := stubTakeoverResume(t)
	resumed := stubResumeRunner(t)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = drive(t, m, refreshMsg{})
	m = openRun(t, m)

	m = drive(t, m, keyPress('t'))
	if run, _ := s.GetRun(ctx, l.run.ID); run.State != workflow.RunPaused {
		t.Fatalf("run state = %s after t, want paused", run.State)
	}
	if rec.calls != 1 || rec.ref != (takeoverRef{runID: l.run.ID, stepRunID: l.stepRun.ID}) || rec.sess.ExternalID != l.stepRun.SessionExternalID {
		t.Fatalf("takeover resume = %+v (%d calls), want the step's session marked with its run and step run", rec, rec.calls)
	}
	a := m.(app)
	if !a.takeover.open || a.takeover.stage != takeoverChoose || a.takeover.stepRun.ID != l.stepRun.ID {
		t.Fatalf("takeover picker = %+v, want it open on the step's choices", a.takeover)
	}
	if a.pendingRecaps != 0 {
		t.Errorf("pendingRecaps = %d, want no recap fired for a takeover return", a.pendingRecaps)
	}
	content := ansi.Strip(m.View().Content)
	for _, want := range []string{"back from implement", "is paused", "1 continue the run", "2 hand the step back",
		"3 rerun the step", "4 abandon the run", "esc leave the run paused"} {
		if !strings.Contains(content, want) {
			t.Errorf("takeover picker missing %q:\n%s", want, content)
		}
	}

	// continue → the outcome stage lists the step's edge outcomes.
	m = drive(t, m, enter())
	a = m.(app)
	if !a.takeover.open || a.takeover.stage != takeoverOutcome {
		t.Fatalf("picker = %+v, want the outcome stage", a.takeover)
	}
	content = ansi.Strip(m.View().Content)
	for _, want := range []string{"outcome for implement", "1 done", "esc back"} {
		if !strings.Contains(content, want) {
			t.Errorf("outcome stage missing %q:\n%s", want, content)
		}
	}
	// esc backs out to the choices, not out of the picker.
	m = drive(t, m, esc())
	if a := m.(app); !a.takeover.open || a.takeover.stage != takeoverChoose {
		t.Fatalf("picker = %+v after esc, want back on the choices", a.takeover)
	}
	m = drive(t, m, keyPress('1'))
	m = drive(t, m, keyPress('1'))
	a = m.(app)
	if a.takeover.open || !a.modal.Active() || a.modal.kind != modalTakeoverDeliverable || a.modal.extra != "done" || a.modal.target != l.stepRun.ID {
		t.Fatalf("picker=%v modal=%+v, want the deliverable modal for done on the step run", a.takeover.open, a.modal)
	}
	if !strings.Contains(a.modal.title, "implement → done") {
		t.Errorf("modal title = %q, want it to name the step and outcome", a.modal.title)
	}
	m = typeText(t, m, "PR #9")
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	if m.(app).modal.Active() {
		t.Fatal("modal still active after submit")
	}
	waitFor(t, "step finished by hand", func() bool {
		sr, err := s.GetStepRun(ctx, l.stepRun.ID)
		return err == nil && sr.Finished() && sr.Outcome == "done" && sr.Deliverable == "PR #9"
	})
	if resumed.runID != l.run.ID {
		t.Errorf("runner resumed for run %d, want %d", resumed.runID, l.run.ID)
	}
	if a := m.(app); a.status.isErr || !strings.Contains(a.status.text, "resumed") {
		t.Errorf("status = %+v, want a resumed flash", a.status)
	}
}

// A step with no edges ends the run on any outcome, so continuing skips
// the outcome stage and goes straight to the deliverable as done.
func TestTakeoverContinueWithoutEdgesSkipsOutcomeStage(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, false)
	stubResumeRunner(t)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunPaused)
	edges, err := s.ListEdges(ctx, l.wf.ID)
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	for _, e := range edges {
		if e.FromStepID == l.agent.ID {
			if err := s.DeleteEdge(ctx, e.ID); err != nil {
				t.Fatalf("DeleteEdge: %v", err)
			}
		}
	}
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, takeoverReturn(l, stepSessionRow(t, l, m).ID))
	if !m.(app).takeover.open {
		t.Fatal("picker did not open")
	}
	m = drive(t, m, keyPress('1'))
	a := m.(app)
	if a.takeover.open || !a.modal.Active() || a.modal.extra != workflow.OutcomeDone {
		t.Fatalf("picker=%v modal=%+v, want the deliverable modal for done with no outcome stage", a.takeover.open, a.modal)
	}
}

// A step that called finish_step from inside the taken-over session has
// nothing left to decide: the picker offers continue and abandon only, and
// continue just starts a runner.
func TestTakeoverFinishedStepOffersContinueOnly(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, false)
	resumed := stubResumeRunner(t)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunPaused)
	if err := s.FinishStepRun(ctx, l.stepRun.ID, "done", "PR #8"); err != nil {
		t.Fatalf("FinishStepRun: %v", err)
	}
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, takeoverReturn(l, stepSessionRow(t, l, m).ID))

	a := m.(app)
	if choices := a.takeover.choices(); !a.takeover.open || len(choices) != 2 {
		t.Fatalf("picker = %+v with %d choices, want continue and abandon only", a.takeover, len(choices))
	}
	content := ansi.Strip(m.View().Content)
	if !strings.Contains(content, "handed off as done") || strings.Contains(content, "rerun the step") {
		t.Errorf("picker should say the step handed off and not offer a rerun:\n%s", content)
	}
	m = drive(t, m, keyPress('1'))
	if resumed.runID != l.run.ID {
		t.Errorf("runner resumed for run %d, want %d", resumed.runID, l.run.ID)
	}
	if a := m.(app); a.takeover.open || a.modal.Active() {
		t.Error("continue on a finished step opened something instead of resuming")
	}
	if sr, _ := s.GetStepRun(ctx, l.stepRun.ID); sr.Deliverable != "PR #8" {
		t.Errorf("deliverable = %q, want the session's own hand-off kept", sr.Deliverable)
	}
}

// "hand the step back" starts a runner with the step untouched, so it
// continues the same session headlessly.
func TestTakeoverHandBackResumesRunner(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, false)
	resumed := stubResumeRunner(t)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunPaused)
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, takeoverReturn(l, stepSessionRow(t, l, m).ID))

	m = drive(t, m, keyPress('2'))
	if resumed.runID != l.run.ID {
		t.Errorf("runner resumed for run %d, want %d", resumed.runID, l.run.ID)
	}
	sr, _ := s.GetStepRun(ctx, l.stepRun.ID)
	if sr.Finished() || sr.SessionExternalID != l.stepRun.SessionExternalID {
		t.Errorf("step run = %+v, want it left unfinished on its session", sr)
	}
	if a := m.(app); !strings.Contains(a.status.text, "handed back") {
		t.Errorf("status = %+v, want a handed-back flash", a.status)
	}
}

// "rerun the step" clears the step run's session (runner.RestartStep's
// contract: the next runner starts the step over) and starts a runner.
func TestTakeoverRerunClearsStepSession(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, false)
	resumed := stubResumeRunner(t)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunPaused)
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, takeoverReturn(l, stepSessionRow(t, l, m).ID))

	m = drive(t, m, keyPress('3'))
	waitFor(t, "step session cleared", func() bool {
		sr, err := s.GetStepRun(ctx, l.stepRun.ID)
		return err == nil && sr.SessionExternalID == "" && !sr.Finished()
	})
	if resumed.runID != l.run.ID {
		t.Errorf("runner resumed for run %d, want %d", resumed.runID, l.run.ID)
	}
	if a := m.(app); !strings.Contains(a.status.text, "rerun") {
		t.Errorf("status = %+v, want a rerun flash", a.status)
	}
}

// "abandon the run" cancels it; esc leaves it paused with a hint.
func TestTakeoverAbandonCancelsAndEscLeavesPaused(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, false)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunPaused)
	m = drive(t, m, refreshMsg{})
	row := stepSessionRow(t, l, m).ID

	m = drive(t, m, takeoverReturn(l, row))
	m = drive(t, m, esc())
	a := m.(app)
	if a.takeover.open || !strings.Contains(a.status.text, "stays paused") {
		t.Fatalf("picker open=%v status=%+v, want it closed with a stays-paused hint", a.takeover.open, a.status)
	}
	if run, _ := s.GetRun(ctx, l.run.ID); run.State != workflow.RunPaused {
		t.Fatalf("run state = %s after esc, want paused", run.State)
	}

	m = drive(t, m, takeoverReturn(l, row))
	m = drive(t, m, keyPress('4'))
	waitFor(t, "run cancelled", func() bool {
		run, err := s.GetRun(ctx, l.run.ID)
		return err == nil && run.State == workflow.RunCancelled
	})
	_ = m
}

// A backgrounded takeover session is still a live claude on the step's
// transcript, so no picker opens and the run stays paused.
func TestTakeoverBackgroundedKeepsRunPaused(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, false)
	rec := stubTakeoverResume(t)
	rec.backgrounded = true
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunPaused)
	m = drive(t, m, refreshMsg{})
	m = openRun(t, m)

	m = drive(t, m, keyPress('t'))
	a := m.(app)
	if a.takeover.open || !strings.Contains(a.status.text, "stays paused") {
		t.Errorf("picker open=%v status=%+v, want no picker and a stays-paused flash", a.takeover.open, a.status)
	}
	if run, _ := s.GetRun(ctx, l.run.ID); run.State != workflow.RunPaused {
		t.Errorf("run state = %s, want paused", run.State)
	}
}

// The picker is dropped when the run is no longer paused at that step by
// the time the session returns -- someone resumed it from the CLI, say.
func TestTakeoverReturnAfterRunMovedOnFlashes(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, false)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunPaused)
	m = drive(t, m, refreshMsg{})
	row := stepSessionRow(t, l, m).ID
	if ok, err := s.ClaimRun(ctx, l.run.ID); err != nil || !ok {
		t.Fatalf("ClaimRun = (%v, %v)", ok, err)
	}
	m = drive(t, m, takeoverReturn(l, row))
	a := m.(app)
	if a.takeover.open || !strings.Contains(a.status.text, "a runner already has it") {
		t.Errorf("picker open=%v status=%+v, want no picker and a note that a runner has the run", a.takeover.open, a.status)
	}
}

// `t` refuses what cannot be taken over: a gate (decide it instead), a step
// that already handed off, and a runner that will not stop.
func TestTakeoverRefusals(t *testing.T) {
	ctx := context.Background()
	stubRunnerAlive(t, true)
	rec := stubTakeoverResume(t)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunRunning)
	m = drive(t, m, refreshMsg{})
	m = openRun(t, m)

	// The runner never lets go: the run is paused, but the flash says the
	// runner is still there, and no session is resumed.
	prevTimeout, prevPoll := runnerStopTimeout, runnerStopPoll
	runnerStopTimeout, runnerStopPoll = 30*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { runnerStopTimeout, runnerStopPoll = prevTimeout, prevPoll })
	m = drive(t, m, keyPress('t'))
	a := m.(app)
	if !a.status.isErr || !strings.Contains(a.status.text, "has not stopped") || rec.calls != 0 {
		t.Errorf("status = %+v, calls = %d; want a runner-not-stopped error and no resume", a.status, rec.calls)
	}
	if run, _ := s.GetRun(ctx, l.run.ID); run.State != workflow.RunPaused {
		t.Errorf("run state = %s, want paused even though the takeover gave up", run.State)
	}

	// A step that handed off: p resumes, t has nothing to take.
	if err := s.FinishStepRun(ctx, l.stepRun.ID, "done", ""); err != nil {
		t.Fatalf("FinishStepRun: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress('t'))
	if a := m.(app); !strings.Contains(a.status.text, "already handed off") {
		t.Errorf("status = %+v, want an already-handed-off refusal", a.status)
	}

	// A gate: approve or reject instead.
	if _, err := s.CreateStepRun(ctx, workflow.StepRun{RunID: l.run.ID, StepID: l.gate.ID}); err != nil {
		t.Fatalf("CreateStepRun(gate): %v", err)
	}
	if err := s.SetRunState(ctx, l.run.ID, workflow.RunWaitingReview); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = drive(t, m, keyPress('t'))
	if a := m.(app); !strings.Contains(a.status.text, "approve (a) or reject (x)") {
		t.Errorf("status = %+v, want a gate refusal pointing at a/x", a.status)
	}
	if rec.calls != 0 {
		t.Errorf("takeover resume called %d times, want none", rec.calls)
	}
}

// `r` on a paused run's step session from the session picker is the same
// takeover: it is marked with the run and step run so the picker opens on
// return, and the ordinary recap does not fire.
func TestResumeStepSessionOfPausedRunIsTakeover(t *testing.T) {
	stubRunnerAlive(t, false)
	rec := stubTakeoverResume(t)
	m, s := newTestApp(t)
	l := newLiveRun(t, s, workflow.RunPaused)
	m = drive(t, m, refreshMsg{})
	m = stepR(t, m)
	if !m.(app).sessionPickerOpen {
		t.Fatal("session picker not open")
	}
	m = drive(t, m, keyPress('1'))
	if rec.calls != 1 || rec.ref != (takeoverRef{runID: l.run.ID, stepRunID: l.stepRun.ID}) {
		t.Fatalf("takeover resume = %+v (%d calls), want the step session resumed as a takeover", rec, rec.calls)
	}
	a := m.(app)
	if !a.takeover.open || a.pendingRecaps != 0 {
		t.Errorf("picker open=%v pendingRecaps=%d, want the picker and no recap", a.takeover.open, a.pendingRecaps)
	}
}
