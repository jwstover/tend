package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/workflow"
)

// The takeover contract from tend task #184: after RestartStep clears a
// paused step's session id, the resuming runner starts the step over on
// the same step run -- fresh session, recorded prompt, still iteration 1
// -- without ever trying to continue the session the user took over.
func TestRunResumeStartsOverWhenStepHasNoSession(t *testing.T) {
	f := newFixture(t)
	run, sr := f.crashed(t)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if req.Resume {
			t.Errorf("exec asked to resume %+v; a step with no session has nothing to resume", req)
		}
		return success("done fresh: " + req.Prompt), nil
	}

	if err := RestartStep(f.ctx, f.s, sr.ID); err != nil {
		t.Fatalf("RestartStep: %v", err)
	}
	if got, _ := f.s.GetStepRun(f.ctx, sr.ID); got.SessionExternalID != "" || got.Finished() {
		t.Fatalf("step run after RestartStep = %+v, want unfinished with no session", got)
	}
	if err := f.runner().Run(f.ctx, run.ID, true); err != nil {
		t.Fatalf("Run with takeover = %v\n%s", err, f.log)
	}

	reqs := f.exec.requests()
	if len(reqs) != 2 {
		t.Fatalf("exec calls = %d, want the fresh restart then ship", len(reqs))
	}
	restart := reqs[0]
	if restart.Resume || restart.Prompt != "the original prompt" || restart.StepRun.ID != sr.ID {
		t.Errorf("restart = %+v, want the same step run started over with its recorded prompt", restart)
	}
	if restart.StepRun.SessionExternalID == "" || restart.StepRun.SessionExternalID == "sess-crashed" {
		t.Errorf("restart session id = %q, want a fresh one, not the taken-over session", restart.StepRun.SessionExternalID)
	}
	if reqs[1].Resume || reqs[1].StepRun.StepID != f.steps["ship"].ID {
		t.Errorf("second attempt = %+v, want a fresh ship step", reqs[1])
	}

	srs := f.stepRuns(run.ID)
	if len(srs) != 2 {
		t.Fatalf("step runs = %+v, want implement then ship", srs)
	}
	impl := srs[0]
	if impl.ID != sr.ID || impl.Iteration != 1 || impl.SessionExternalID != restart.StepRun.SessionExternalID ||
		impl.Deliverable != "done fresh: the original prompt" {
		t.Errorf("implement step run = %+v, want the same row, still iteration 1, on the new session with a deliverable", impl)
	}
	if srs[1].StepID != f.steps["ship"].ID || srs[1].Input != "done fresh: the original prompt" {
		t.Errorf("ship step run = %+v, want fed from the restarted implement", srs[1])
	}
	if got := f.getRun(run.ID); got.State != workflow.RunDone {
		t.Errorf("run = %+v, want done", got)
	}
	if sessions := f.sessions(); len(sessions) != 3 {
		t.Errorf("sessions = %d, want the taken-over one, its replacement, and ship's", len(sessions))
	}
}

// A result already in the log is what a crashed step settles from -- but
// only a step someone owns. With no session id the log is not consulted:
// whatever the earlier attempt wrote, the step runs again.
func TestRunResumeIgnoresLoggedResultWhenStepHasNoSession(t *testing.T) {
	f := newFixture(t)
	run, sr := f.crashed(t)
	if err := os.MkdirAll(filepath.Dir(sr.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	logLine := `{"type":"result","subtype":"success","is_error":false,"result":"PR #7 from the taken-over session","permission_denials":[]}` + "\n"
	if err := os.WriteFile(sr.LogPath, []byte(logLine), 0o644); err != nil {
		t.Fatal(err)
	}
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		return success("done fresh"), nil
	}

	if err := RestartStep(f.ctx, f.s, sr.ID); err != nil {
		t.Fatalf("RestartStep: %v", err)
	}
	if err := f.runner().Run(f.ctx, run.ID, true); err != nil {
		t.Fatalf("Run with takeover = %v\n%s", err, f.log)
	}

	reqs := f.exec.requests()
	if len(reqs) != 2 || reqs[0].Resume || reqs[0].StepRun.ID != sr.ID || reqs[0].Prompt != "the original prompt" {
		t.Fatalf("exec calls = %+v, want the step run fresh then ship, not settled from the log", reqs)
	}
	srs := f.stepRuns(run.ID)
	if len(srs) != 2 || srs[0].Deliverable != "done fresh" || srs[1].Input != "done fresh" {
		t.Errorf("step runs = %+v, want implement settled from the new attempt and ship fed from it", srs)
	}
	// The log was left alone for the new attempt to append to.
	if b, err := os.ReadFile(sr.LogPath); err != nil || string(b) != logLine {
		t.Errorf("log after the restart = %q (err %v), want the earlier attempt's line kept", b, err)
	}
}

// A step run with an outcome is history: rerunning it would be a new
// iteration, which only the edges can create.
func TestRestartStepRefusesFinishedStepRun(t *testing.T) {
	f := newFixture(t)
	_, sr := f.crashed(t)
	if err := f.s.FinishStepRun(f.ctx, sr.ID, "done", "PR #7"); err != nil {
		t.Fatalf("FinishStepRun: %v", err)
	}

	err := RestartStep(f.ctx, f.s, sr.ID)
	if !errors.Is(err, workflow.ErrStepRunFinished) {
		t.Fatalf("RestartStep on a finished step run = %v, want ErrStepRunFinished", err)
	}
	got, err := f.s.GetStepRun(f.ctx, sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionExternalID != "sess-crashed" || got.Outcome != "done" {
		t.Errorf("step run = %+v, want its session id and outcome untouched", got)
	}
}
