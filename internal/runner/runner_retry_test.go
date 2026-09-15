package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/workflow"
)

// failed drives a two-step run into failure at its first step: claude
// reports an error result, so the run ends failed with the step run left
// unfinished and current -- exactly what a retry re-enters. The step's
// log holds that error result, the way a real failed step's does.
func (f *fixture) failed(t *testing.T) (workflow.Run, workflow.StepRun) {
	t.Helper()
	f.step("implement", workflow.StepAgent)
	f.step("ship", workflow.StepAgent)
	f.edge("implement", "done", "ship", nil)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if err := os.MkdirAll(filepath.Dir(req.StepRun.LogPath), 0o755); err != nil {
			t.Fatal(err)
		}
		line := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API overloaded","permission_denials":[]}` + "\n"
		if err := os.WriteFile(req.StepRun.LogPath, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
		return agent.HeadlessResult{Found: true, Subtype: "error_during_execution", IsError: true, Text: "API overloaded"}, nil
	}
	run := f.run()
	if err := f.runner().Run(f.ctx, run.ID, false); !errors.Is(err, ErrRunFailed) {
		t.Fatalf("Run = %v, want ErrRunFailed", err)
	}
	f.exec.calls = nil
	got := f.getRun(run.ID)
	if got.State != workflow.RunFailed || got.CurrentStepRunID == nil {
		t.Fatalf("run = %+v, want failed with a current step", got)
	}
	sr, err := f.s.GetStepRun(f.ctx, *got.CurrentStepRunID)
	if err != nil || sr.Finished() {
		t.Fatalf("current step run = (%+v, %v), want unfinished", sr, err)
	}
	return got, sr
}

// A retried run continues the failed step's own session, told what went
// wrong, rather than settling from the log it already failed on; the
// step run stays the same row and iteration, and the run goes on to
// finish.
func TestRunRetryContinuesFailedStepWithReason(t *testing.T) {
	f := newFixture(t)
	run, sr := f.failed(t)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		return success("recovered: " + req.Prompt), nil
	}

	if err := f.s.RetryRun(f.ctx, run.ID); err != nil {
		t.Fatalf("RetryRun: %v", err)
	}
	if err := f.runner().Run(f.ctx, run.ID, true); err != nil {
		t.Fatalf("Run after retry = %v\n%s", err, f.log)
	}

	reqs := f.exec.requests()
	if len(reqs) != 2 {
		t.Fatalf("exec calls = %d, want the retried implement then ship\n%s", len(reqs), f.log)
	}
	retry := reqs[0]
	if !retry.Resume || retry.StepRun.ID != sr.ID || retry.StepRun.SessionExternalID != sr.SessionExternalID {
		t.Errorf("retry = %+v, want the failed step's own session resumed on the same step run", retry)
	}
	if retry.Prompt == ResumePrompt || !strings.Contains(retry.Prompt, "API overloaded") ||
		!strings.Contains(retry.Prompt, `step "implement"`) || !strings.Contains(retry.Prompt, workflow.FinishStepTool) {
		t.Errorf("retry prompt = %q, want the failure quoted with the hand-off contract", retry.Prompt)
	}
	srs := f.stepRuns(run.ID)
	if len(srs) != 2 || srs[0].ID != sr.ID || srs[0].Iteration != 1 || !strings.HasPrefix(srs[0].Deliverable, "recovered: ") {
		t.Errorf("step runs = %+v, want implement (still iteration 1) settled from the retry, then ship", srs)
	}
	got := f.getRun(run.ID)
	if got.State != workflow.RunDone || got.Error != "" {
		t.Errorf("run = %+v, want done with the failure cleared", got)
	}
	if !strings.Contains(f.log.String(), "retrying after failure: step \"implement\"") {
		t.Errorf("runner log should say the run is a retry and why:\n%s", f.log)
	}
}

// A step fixed after the failure runs fixed: the retry picks up the
// step's current model and permission mode on the step run.
func TestRunRetryRefreshesStepSettings(t *testing.T) {
	f := newFixture(t)
	run, sr := f.failed(t)
	if sr.PermissionMode == "bypassPermissions" || sr.Model == "opus" {
		t.Fatalf("step run ran with %q / %q; the test needs the fix to differ", sr.Model, sr.PermissionMode)
	}
	if err := f.s.SetStepPermissionMode(f.ctx, f.steps["implement"].ID, "bypassPermissions"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetStepModel(f.ctx, f.steps["implement"].ID, "opus"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetStepAdvisorModel(f.ctx, f.steps["implement"].ID, "fable"); err != nil {
		t.Fatal(err)
	}
	f.exec.handle = nil

	if err := f.s.RetryRun(f.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.runner().Run(f.ctx, run.ID, true); err != nil {
		t.Fatalf("Run after retry = %v\n%s", err, f.log)
	}
	retry := f.exec.requests()[0]
	if retry.StepRun.PermissionMode != "bypassPermissions" || retry.StepRun.Model != "opus" || retry.StepRun.AdvisorModel != "fable" {
		t.Errorf("retry ran with model %q / permission mode %q / advisor %q, want the step's new opus / bypassPermissions / fable",
			retry.StepRun.Model, retry.StepRun.PermissionMode, retry.StepRun.AdvisorModel)
	}
	if got, _ := f.s.GetStepRun(f.ctx, sr.ID); got.PermissionMode != "bypassPermissions" || got.Model != "opus" || got.AdvisorModel != "fable" {
		t.Errorf("step run row = %+v, want the refreshed settings recorded", got)
	}
	if !strings.Contains(f.log.String(), `permission mode "bypassPermissions"`) {
		t.Errorf("runner log should note the new settings:\n%s", f.log)
	}
}

// With the step run disowned (RestartStep, the --fresh half of retry) the
// step starts over from its recorded prompt on a new session: no resume
// turn, no retry prompt, same step run.
func TestRunRetryFreshStartsTheStepOver(t *testing.T) {
	f := newFixture(t)
	run, sr := f.failed(t)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if req.Resume {
			t.Errorf("exec asked to resume %+v; a fresh retry has nothing to resume", req)
		}
		return success("fresh: " + req.Prompt), nil
	}

	if err := RestartStep(f.ctx, f.s, sr.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.RetryRun(f.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.runner().Run(f.ctx, run.ID, true); err != nil {
		t.Fatalf("Run after fresh retry = %v\n%s", err, f.log)
	}
	reqs := f.exec.requests()
	if len(reqs) != 2 {
		t.Fatalf("exec calls = %d, want the fresh implement then ship", len(reqs))
	}
	fresh := reqs[0]
	if fresh.StepRun.ID != sr.ID || fresh.Prompt != sr.PromptRendered || fresh.StepRun.SessionExternalID == sr.SessionExternalID || fresh.StepRun.SessionExternalID == "" {
		t.Errorf("fresh attempt = %+v, want the same step run's recorded prompt under a new session", fresh)
	}
	if srs := f.stepRuns(run.ID); len(srs) != 2 || srs[0].Iteration != 1 {
		t.Errorf("step runs = %+v, want implement still iteration 1, then ship", srs)
	}
}

// A retried step that cannot be continued (its session is gone) is
// started over, as a crashed one would be; a retry that fails again is a
// failure like any other, with the new reason on the row.
func TestRunRetryFallsBackAndCanFailAgain(t *testing.T) {
	f := newFixture(t)
	run, sr := f.failed(t)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if req.Resume {
			return agent.HeadlessResult{}, errors.New("No conversation found with session ID")
		}
		res := success("Permission is pending.")
		res.PermissionDenials = 3
		return res, nil
	}

	if err := f.s.RetryRun(f.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	err := f.runner().Run(f.ctx, run.ID, true)
	if !errors.Is(err, ErrRunFailed) || !strings.Contains(err.Error(), "3 tool call(s) were denied") {
		t.Fatalf("Run after retry = %v, want ErrRunFailed for the new reason\n%s", err, f.log)
	}
	reqs := f.exec.requests()
	if len(reqs) != 2 || !reqs[0].Resume || reqs[1].Resume || reqs[1].StepRun.ID != sr.ID {
		t.Errorf("exec calls = %+v, want the resume attempt then a fresh start on the same step run", reqs)
	}
	got := f.getRun(run.ID)
	if got.State != workflow.RunFailed || !strings.Contains(got.Error, "were denied") || strings.Contains(got.Error, "API overloaded") {
		t.Errorf("run = %+v, want failed with only the new reason", got)
	}
	// And it can be retried again from there.
	if err := f.s.RetryRun(f.ctx, run.ID); err != nil {
		t.Errorf("a second RetryRun = %v, want allowed", err)
	}
}

// A failure that left no unfinished step -- an edge's max_iterations
// exceeded -- is re-evaluated from where the run stood, so raising the
// bound and retrying lets the run go on.
func TestRunRetryReevaluatesEdgesWhenNoStepIsUnfinished(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	f.step("review", workflow.StepAgent)
	f.edge("implement", "done", "review", nil)
	one := int64(1)
	f.edge("review", "reject", "implement", &one)
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if req.StepRun.StepID == f.steps["review"].ID {
			if err := f.s.FinishStepRun(f.ctx, req.StepRun.ID, "reject", "not yet"); err != nil {
				t.Fatal(err)
			}
		}
		return success("ok"), nil
	}
	run := f.run()
	err := f.runner().Run(f.ctx, run.ID, false)
	if !errors.Is(err, ErrRunFailed) || !strings.Contains(err.Error(), "the edge allows 1") {
		t.Fatalf("Run = %v, want the max_iterations failure", err)
	}

	// Raise the bound, then retry: the same outcome now routes.
	two := int64(2)
	if _, err := f.s.SetEdge(f.ctx, f.steps["review"].ID, "reject", f.steps["implement"].ID, &two); err != nil {
		t.Fatal(err)
	}
	f.exec.handle = func(_ context.Context, req StepExec) (agent.HeadlessResult, error) {
		if req.StepRun.StepID == f.steps["review"].ID {
			if err := f.s.FinishStepRun(f.ctx, req.StepRun.ID, "approve", "ship it"); err != nil {
				t.Fatal(err)
			}
		}
		return success("ok"), nil
	}
	if err := f.s.RetryRun(f.ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.runner().Run(f.ctx, run.ID, true); err != nil {
		t.Fatalf("Run after retry = %v\n%s", err, f.log)
	}
	srs := f.stepRuns(run.ID)
	if len(srs) != 4 || srs[2].StepID != f.steps["implement"].ID || srs[2].Iteration != 2 || srs[3].Outcome != "approve" {
		t.Errorf("step runs = %+v, want implement, review(reject), implement #2, review(approve)", srs)
	}
	if got := f.getRun(run.ID); got.State != workflow.RunDone {
		t.Errorf("run = %+v, want done", got)
	}
}

// Retry itself only touches a failed run; everything else is refused
// before tmux is even consulted.
func TestRetryRefusesRunsThatHaveNotFailed(t *testing.T) {
	f := newFixture(t)
	f.step("implement", workflow.StepAgent)
	run := f.run()
	for _, st := range []workflow.RunState{workflow.RunPending, workflow.RunRunning, workflow.RunPaused, workflow.RunCancelled} {
		if st != workflow.RunPending {
			if err := f.s.SetRunState(f.ctx, run.ID, st); err != nil {
				t.Fatalf("SetRunState(%s): %v", st, err)
			}
		}
		_, err := Retry(f.ctx, f.s, run.ID, "/tmp/tend.db", false)
		if !errors.Is(err, workflow.ErrRunNotFailed) || !strings.Contains(err.Error(), string(st)) {
			t.Errorf("Retry on a %s run = %v, want ErrRunNotFailed naming the state", st, err)
		}
	}
}
