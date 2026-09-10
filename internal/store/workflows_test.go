package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/workflow"
)

// mustWorkflow creates a workflow or fails the test.
func mustWorkflow(t *testing.T, s *Store, name string) workflow.Workflow {
	t.Helper()
	w, err := s.CreateWorkflow(context.Background(), name, "")
	if err != nil {
		t.Fatalf("CreateWorkflow(%q): %v", name, err)
	}
	return w
}

// mustStep appends an agent step or fails the test.
func mustStep(t *testing.T, s *Store, workflowID int64, name string) workflow.Step {
	t.Helper()
	st, err := s.AddStep(context.Background(), workflowID, name, workflow.StepAgent)
	if err != nil {
		t.Fatalf("AddStep(%q): %v", name, err)
	}
	return st
}

// mustRun creates a run for a fresh task or fails the test.
func mustRun(t *testing.T, s *Store, workflowID int64) workflow.Run {
	t.Helper()
	tk := mustAdd(t, s, "task under test")
	run, err := s.CreateRun(context.Background(), workflowID, tk.ID, "/tmp/work")
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

func TestWorkflowCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	w, err := s.CreateWorkflow(ctx, "  fix a bug  ", "the usual")
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if w.Name != "fix a bug" || w.Description != "the usual" {
		t.Errorf("created = %+v, want the trimmed name and the description", w)
	}
	if _, err := s.CreateWorkflow(ctx, "   ", ""); !errors.Is(err, workflow.ErrEmptyName) {
		t.Errorf("CreateWorkflow(blank) = %v, want ErrEmptyName", err)
	}
	// Unique is NOCASE, so this is the same workflow.
	if _, err := s.CreateWorkflow(ctx, "FIX A BUG", ""); err == nil {
		t.Error("CreateWorkflow with a case-variant duplicate should fail")
	}

	found, err := s.WorkflowByName(ctx, "Fix A Bug")
	if err != nil {
		t.Fatalf("WorkflowByName: %v", err)
	}
	if found.ID != w.ID {
		t.Errorf("WorkflowByName resolved to %d, want %d", found.ID, w.ID)
	}
	if _, err := s.WorkflowByName(ctx, "nope"); !errors.Is(err, workflow.ErrWorkflowNotFound) {
		t.Errorf("WorkflowByName(unknown) = %v, want ErrWorkflowNotFound", err)
	}
	if _, err := s.GetWorkflow(ctx, 9999); !errors.Is(err, workflow.ErrWorkflowNotFound) {
		t.Errorf("GetWorkflow(unknown) = %v, want ErrWorkflowNotFound", err)
	}

	if err := s.RenameWorkflow(ctx, w.ID, "fix a bug, carefully"); err != nil {
		t.Fatalf("RenameWorkflow: %v", err)
	}
	if err := s.SetWorkflowDescription(ctx, w.ID, "slower"); err != nil {
		t.Fatalf("SetWorkflowDescription: %v", err)
	}
	got, err := s.GetWorkflow(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if got.Name != "fix a bug, carefully" || got.Description != "slower" {
		t.Errorf("after update = %+v", got)
	}

	if err := s.DeleteWorkflow(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	}
	if _, err := s.GetWorkflow(ctx, w.ID); !errors.Is(err, workflow.ErrWorkflowNotFound) {
		t.Errorf("GetWorkflow(deleted) = %v, want ErrWorkflowNotFound", err)
	}
}

func TestListWorkflowsCountsSteps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	two := mustWorkflow(t, s, "two steps")
	mustStep(t, s, two.ID, "a")
	mustStep(t, s, two.ID, "b")
	mustWorkflow(t, s, "empty")

	list, err := s.ListWorkflows(ctx)
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListWorkflows returned %d, want 2", len(list))
	}
	// Ordered by name: "empty" before "two steps".
	if list[0].Name != "empty" || list[0].StepCount != 0 {
		t.Errorf("list[0] = %+v, want empty with 0 steps", list[0])
	}
	if list[1].Name != "two steps" || list[1].StepCount != 2 {
		t.Errorf("list[1] = %+v, want two steps with 2 steps", list[1])
	}
}

func TestStepsAppendReorderAndUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	w := mustWorkflow(t, s, "wf")

	first := mustStep(t, s, w.ID, "implement")
	second := mustStep(t, s, w.ID, "review")
	third := mustStep(t, s, w.ID, "ship")
	if first.SortOrder >= second.SortOrder || second.SortOrder >= third.SortOrder {
		t.Errorf("AddStep must append: sort orders %d, %d, %d", first.SortOrder, second.SortOrder, third.SortOrder)
	}
	if first.Kind != workflow.StepAgent || first.PromptMD != "" || first.Model != "" {
		t.Errorf("new step = %+v, want an agent step with empty prompt and model", first)
	}

	if _, err := s.AddStep(ctx, w.ID, "  ", workflow.StepAgent); !errors.Is(err, workflow.ErrEmptyName) {
		t.Errorf("AddStep(blank) = %v, want ErrEmptyName", err)
	}
	if _, err := s.AddStep(ctx, w.ID, "x", workflow.StepKind("human")); err == nil {
		t.Error("AddStep with an unknown kind should fail")
	}

	// Reverse the order and confirm ListSteps follows sort_order.
	if err := s.ReorderSteps(ctx, w.ID, []int64{third.ID, second.ID, first.ID}); err != nil {
		t.Fatalf("ReorderSteps: %v", err)
	}
	steps, err := s.ListSteps(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 3 || steps[0].ID != third.ID || steps[2].ID != first.ID {
		t.Errorf("ListSteps after reorder = %v, want ship, review, implement", stepNames(steps))
	}
	// A partial list, a foreign step, and a duplicate are all refused.
	if err := s.ReorderSteps(ctx, w.ID, []int64{first.ID}); err == nil {
		t.Error("ReorderSteps with a partial list should fail")
	}
	other := mustWorkflow(t, s, "other")
	foreign := mustStep(t, s, other.ID, "foreign")
	if err := s.ReorderSteps(ctx, w.ID, []int64{foreign.ID, second.ID, first.ID}); err == nil {
		t.Error("ReorderSteps with a step from another workflow should fail")
	}
	if err := s.ReorderSteps(ctx, w.ID, []int64{first.ID, first.ID, second.ID}); err == nil {
		t.Error("ReorderSteps with a duplicate id should fail")
	}

	// UpdateStep writes the editable attributes; SetStepPrompt the prompt alone.
	second.Name = "adversarial review"
	second.Kind = workflow.StepGate
	second.Model = "opus"
	second.PermissionMode = "plan"
	second.PromptMD = "Review {{.Input}}"
	if err := s.UpdateStep(ctx, second); err != nil {
		t.Fatalf("UpdateStep: %v", err)
	}
	got, err := s.GetStep(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetStep: %v", err)
	}
	if got.Name != "adversarial review" || got.Kind != workflow.StepGate || got.Model != "opus" ||
		got.PermissionMode != "plan" || got.PromptMD != "Review {{.Input}}" {
		t.Errorf("after UpdateStep = %+v", got)
	}
	if err := s.SetStepPrompt(ctx, second.ID, "# new prompt"); err != nil {
		t.Fatalf("SetStepPrompt: %v", err)
	}
	if got, _ = s.GetStep(ctx, second.ID); got.PromptMD != "# new prompt" {
		t.Errorf("PromptMD after SetStepPrompt = %q", got.PromptMD)
	}
	// The single-attribute setters touch one column each and leave the
	// rest of the row as it was.
	if err := s.SetStepKind(ctx, second.ID, workflow.StepAgent); err != nil {
		t.Fatalf("SetStepKind: %v", err)
	}
	if err := s.SetStepModel(ctx, second.ID, "haiku"); err != nil {
		t.Fatalf("SetStepModel: %v", err)
	}
	if err := s.SetStepPermissionMode(ctx, second.ID, "acceptEdits"); err != nil {
		t.Fatalf("SetStepPermissionMode: %v", err)
	}
	got, _ = s.GetStep(ctx, second.ID)
	if got.Kind != workflow.StepAgent || got.Model != "haiku" || got.PermissionMode != "acceptEdits" ||
		got.Name != "adversarial review" || got.PromptMD != "# new prompt" {
		t.Errorf("after single-attribute setters = %+v", got)
	}
	if err := s.SetStepKind(ctx, second.ID, workflow.StepKind("human")); err == nil {
		t.Error("SetStepKind with an unknown kind should fail")
	}
	if _, err := s.GetStep(ctx, 9999); !errors.Is(err, workflow.ErrStepNotFound) {
		t.Errorf("GetStep(unknown) = %v, want ErrStepNotFound", err)
	}
}

func stepNames(steps []workflow.Step) []string {
	out := make([]string, 0, len(steps))
	for _, st := range steps {
		out = append(out, st.Name)
	}
	return out
}

func TestEdgesUpsertNormalizeAndCascade(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	w := mustWorkflow(t, s, "wf")
	implement := mustStep(t, s, w.ID, "implement")
	review := mustStep(t, s, w.ID, "review")
	ship := mustStep(t, s, w.ID, "ship")

	three := int64(3)
	if _, err := s.SetEdge(ctx, implement.ID, "done", review.ID, nil); err != nil {
		t.Fatalf("SetEdge: %v", err)
	}
	// Outcome is trimmed and case-folded so an agent's "Reject" matches.
	back, err := s.SetEdge(ctx, review.ID, "  Reject ", implement.ID, &three)
	if err != nil {
		t.Fatalf("SetEdge(reject): %v", err)
	}
	if back.Outcome != "reject" || back.MaxIterations == nil || *back.MaxIterations != 3 {
		t.Errorf("edge = %+v, want outcome reject with max 3", back)
	}
	if _, err := s.SetEdge(ctx, review.ID, "approve", ship.ID, nil); err != nil {
		t.Fatalf("SetEdge(approve): %v", err)
	}

	// Same (from, outcome) again re-points rather than duplicating.
	repointed, err := s.SetEdge(ctx, review.ID, "APPROVE", implement.ID, nil)
	if err != nil {
		t.Fatalf("SetEdge(re-point): %v", err)
	}
	if repointed.ToStepID != implement.ID {
		t.Errorf("re-pointed edge goes to %d, want %d", repointed.ToStepID, implement.ID)
	}
	out, err := s.OutgoingEdges(ctx, review.ID)
	if err != nil {
		t.Fatalf("OutgoingEdges: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("review has %d outgoing edges, want 2 (approve, reject)", len(out))
	}
	all, err := s.ListEdges(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("workflow has %d edges, want 3", len(all))
	}

	// Rejections.
	if _, err := s.SetEdge(ctx, implement.ID, "   ", review.ID, nil); !errors.Is(err, workflow.ErrEmptyOutcome) {
		t.Errorf("SetEdge(blank outcome) = %v, want ErrEmptyOutcome", err)
	}
	zero := int64(0)
	if _, err := s.SetEdge(ctx, implement.ID, "retry", review.ID, &zero); err == nil {
		t.Error("SetEdge with max_iterations 0 should fail")
	}
	other := mustWorkflow(t, s, "other")
	foreign := mustStep(t, s, other.ID, "foreign")
	if _, err := s.SetEdge(ctx, implement.ID, "escape", foreign.ID, nil); !errors.Is(err, workflow.ErrCrossWorkflowEdge) {
		t.Errorf("SetEdge across workflows = %v, want ErrCrossWorkflowEdge", err)
	}
	if _, err := s.SetEdge(ctx, implement.ID, "x", 9999, nil); !errors.Is(err, workflow.ErrStepNotFound) {
		t.Errorf("SetEdge to a missing step = %v, want ErrStepNotFound", err)
	}

	// Deleting an edge, then a step: the step takes every edge touching it.
	if err := s.DeleteEdge(ctx, repointed.ID); err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	if err := s.DeleteStep(ctx, review.ID); err != nil {
		t.Fatalf("DeleteStep: %v", err)
	}
	all, _ = s.ListEdges(ctx, w.ID)
	if len(all) != 0 {
		t.Errorf("edges left after deleting the step they touch: %+v", all)
	}
}

func TestRunLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	w := mustWorkflow(t, s, "wf")
	run := mustRun(t, s, w.ID)

	if run.State != workflow.RunPending || run.EndedAt != nil || run.CurrentStepRunID != nil {
		t.Errorf("new run = %+v, want pending with nothing ended or current", run)
	}

	// Claim is a CAS: exactly one of two runners wins.
	got, err := s.ClaimRun(ctx, run.ID)
	if err != nil || !got {
		t.Fatalf("ClaimRun = (%v, %v), want (true, nil)", got, err)
	}
	if got, _ = s.ClaimRun(ctx, run.ID); got {
		t.Error("a second ClaimRun on a running run must lose")
	}
	// Paused is claimable again (resume), running is not.
	if err := s.SetRunState(ctx, run.ID, workflow.RunPaused); err != nil {
		t.Fatalf("SetRunState(paused): %v", err)
	}
	if got, _ = s.ClaimRun(ctx, run.ID); !got {
		t.Error("ClaimRun on a paused run should win")
	}

	if err := s.SetRunTmuxSession(ctx, run.ID, "tend-wf-1"); err != nil {
		t.Fatalf("SetRunTmuxSession: %v", err)
	}
	active, err := s.ListActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ListActiveRuns: %v", err)
	}
	if len(active) != 1 || active[0].TmuxSession != "tend-wf-1" || active[0].State != workflow.RunRunning {
		t.Errorf("ListActiveRuns = %+v, want the one running run", active)
	}

	if err := s.SetRunState(ctx, run.ID, workflow.RunState("bogus")); err == nil {
		t.Error("SetRunState with an unknown state should fail")
	}
	if err := s.SetRunState(ctx, run.ID, workflow.RunDone); err != nil {
		t.Fatalf("SetRunState(done): %v", err)
	}
	ended, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if ended.State != workflow.RunDone || ended.EndedAt == nil {
		t.Errorf("ended run = %+v, want done with ended_at set", ended)
	}
	// Terminal is final.
	if err := s.SetRunState(ctx, run.ID, workflow.RunRunning); !errors.Is(err, workflow.ErrRunEnded) {
		t.Errorf("SetRunState out of a terminal state = %v, want ErrRunEnded", err)
	}
	if got, _ = s.ClaimRun(ctx, run.ID); got {
		t.Error("ClaimRun on an ended run must lose")
	}
	if err := s.SetRunState(ctx, 9999, workflow.RunRunning); !errors.Is(err, workflow.ErrRunNotFound) {
		t.Errorf("SetRunState(unknown) = %v, want ErrRunNotFound", err)
	}
	if active, _ = s.ListActiveRuns(ctx); len(active) != 0 {
		t.Errorf("ListActiveRuns after the run ended = %+v, want none", active)
	}
	byTask, err := s.ListRunsForTask(ctx, run.TaskID)
	if err != nil {
		t.Fatalf("ListRunsForTask: %v", err)
	}
	if len(byTask) != 1 || byTask[0].ID != run.ID {
		t.Errorf("ListRunsForTask = %+v, want the one run", byTask)
	}
}

func TestStepRunsIterateAndFinishOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	w := mustWorkflow(t, s, "wf")
	implement := mustStep(t, s, w.ID, "implement")
	review := mustStep(t, s, w.ID, "review")
	run := mustRun(t, s, w.ID)

	first, err := s.CreateStepRun(ctx, workflow.StepRun{
		RunID: run.ID, StepID: implement.ID, SessionExternalID: "sess-1",
		PromptRendered: "do the thing", Model: "sonnet", PermissionMode: "acceptEdits",
		Iteration: 42, // ignored: derived by the store
	})
	if err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	if first.Iteration != 1 || first.Finished() || first.PromptRendered != "do the thing" || first.Model != "sonnet" {
		t.Errorf("first step run = %+v, want iteration 1, unfinished, with what ran recorded", first)
	}
	// The run now points at it.
	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.CurrentStepRunID == nil || *got.CurrentStepRunID != first.ID {
		t.Errorf("run.CurrentStepRunID = %v, want %d", got.CurrentStepRunID, first.ID)
	}

	// Finish once with a padded outcome; the outcome is normalized.
	if err := s.FinishStepRun(ctx, first.ID, " Done ", "PR #12"); err != nil {
		t.Fatalf("FinishStepRun: %v", err)
	}
	fin, err := s.GetStepRun(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if fin.Outcome != workflow.OutcomeDone || fin.Deliverable != "PR #12" || !fin.Finished() {
		t.Errorf("finished step run = %+v", fin)
	}
	// Twice is refused and the first outcome stands.
	if err := s.FinishStepRun(ctx, first.ID, "reject", "nope"); !errors.Is(err, workflow.ErrStepRunFinished) {
		t.Errorf("second FinishStepRun = %v, want ErrStepRunFinished", err)
	}
	if fin, _ = s.GetStepRun(ctx, first.ID); fin.Outcome != "done" || fin.Deliverable != "PR #12" {
		t.Errorf("first outcome did not stand: %+v", fin)
	}
	if err := s.FinishStepRun(ctx, first.ID, "  ", ""); !errors.Is(err, workflow.ErrEmptyOutcome) {
		t.Errorf("FinishStepRun(blank) = %v, want ErrEmptyOutcome", err)
	}
	if err := s.FinishStepRun(ctx, 9999, "done", ""); !errors.Is(err, workflow.ErrStepRunNotFound) {
		t.Errorf("FinishStepRun(unknown) = %v, want ErrStepRunNotFound", err)
	}

	// review runs, rejects, implement runs again: iteration 2, review
	// iteration 1 -- counted per (run, step).
	rev, err := s.CreateStepRun(ctx, workflow.StepRun{RunID: run.ID, StepID: review.ID, Input: "PR #12"})
	if err != nil {
		t.Fatalf("CreateStepRun(review): %v", err)
	}
	if rev.Iteration != 1 || rev.Input != "PR #12" {
		t.Errorf("review step run = %+v, want iteration 1 with the input carried", rev)
	}
	if err := s.FinishStepRun(ctx, rev.ID, "reject", "tests missing"); err != nil {
		t.Fatalf("FinishStepRun(review): %v", err)
	}
	again, err := s.CreateStepRun(ctx, workflow.StepRun{RunID: run.ID, StepID: implement.ID})
	if err != nil {
		t.Fatalf("CreateStepRun(implement again): %v", err)
	}
	if again.Iteration != 2 {
		t.Errorf("second implement iteration = %d, want 2", again.Iteration)
	}
	if err := s.SetStepRunLogPath(ctx, again.ID, "/data/runs/1/3.jsonl"); err != nil {
		t.Fatalf("SetStepRunLogPath: %v", err)
	}
	if err := s.SetStepRunSession(ctx, again.ID, "sess-3"); err != nil {
		t.Fatalf("SetStepRunSession: %v", err)
	}
	if got, _ := s.GetStepRun(ctx, again.ID); got.LogPath != "/data/runs/1/3.jsonl" || got.SessionExternalID != "sess-3" {
		t.Errorf("after SetStepRunLogPath/Session = %+v", got)
	}

	all, err := s.ListStepRunsForRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListStepRunsForRun: %v", err)
	}
	if len(all) != 3 || all[0].ID != first.ID || all[2].ID != again.ID {
		t.Errorf("ListStepRunsForRun = %d step runs in order %v, want 3 oldest first", len(all), stepRunIDs(all))
	}
	if _, err := s.GetStepRun(ctx, 9999); !errors.Is(err, workflow.ErrStepRunNotFound) {
		t.Errorf("GetStepRun(unknown) = %v, want ErrStepRunNotFound", err)
	}
}

// The interactive POC ends a run with its one step: FinishRunAtStep
// finishes the step run and marks the run done together, and a repeat
// call from another observer of the same session ending is a no-op.
func TestFinishRunAtStepEndsRunOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	w := mustWorkflow(t, s, "fix a bug")
	st := mustStep(t, s, w.ID, "fix")
	run := mustRun(t, s, w.ID)
	if err := s.SetRunState(ctx, run.ID, workflow.RunRunning); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}
	sr, err := s.CreateStepRun(ctx, workflow.StepRun{RunID: run.ID, StepID: st.ID, PromptRendered: "go"})
	if err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	if err := s.FinishRunAtStep(ctx, sr.ID, "Done", "PR #7"); err != nil {
		t.Fatalf("FinishRunAtStep: %v", err)
	}
	fin, err := s.GetStepRun(ctx, sr.ID)
	if err != nil {
		t.Fatalf("GetStepRun: %v", err)
	}
	if !fin.Finished() || fin.Outcome != workflow.OutcomeDone || fin.Deliverable != "PR #7" {
		t.Errorf("step run after FinishRunAtStep = %+v, want finished with done / PR #7", fin)
	}
	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != workflow.RunDone || got.EndedAt == nil {
		t.Errorf("run after FinishRunAtStep = %+v, want done with ended_at set", got)
	}

	// A second observer of the same ending changes nothing and gets nil.
	if err := s.FinishRunAtStep(ctx, sr.ID, "reject", "late"); err != nil {
		t.Errorf("repeat FinishRunAtStep = %v, want nil", err)
	}
	if fin, _ = s.GetStepRun(ctx, sr.ID); fin.Outcome != workflow.OutcomeDone || fin.Deliverable != "PR #7" {
		t.Errorf("first outcome did not stand: %+v", fin)
	}

	if err := s.FinishRunAtStep(ctx, sr.ID, "  ", ""); !errors.Is(err, workflow.ErrEmptyOutcome) {
		t.Errorf("FinishRunAtStep(blank) = %v, want ErrEmptyOutcome", err)
	}
	if err := s.FinishRunAtStep(ctx, 9999, "done", ""); !errors.Is(err, workflow.ErrStepRunNotFound) {
		t.Errorf("FinishRunAtStep(unknown) = %v, want ErrStepRunNotFound", err)
	}
}

func stepRunIDs(runs []workflow.StepRun) []int64 {
	out := make([]int64, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.ID)
	}
	return out
}

// The definition is read live by the runner, so it must not be deleted
// from under a run that is still going -- and the refusal has to name the
// run so the user knows what to go and finish.
func TestDeleteWorkflowRefusedWhileARunIsLive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	w := mustWorkflow(t, s, "wf")
	st := mustStep(t, s, w.ID, "only")
	run := mustRun(t, s, w.ID)
	if _, err := s.CreateStepRun(ctx, workflow.StepRun{RunID: run.ID, StepID: st.ID}); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	for _, live := range []workflow.RunState{workflow.RunPending, workflow.RunRunning, workflow.RunWaitingReview, workflow.RunPaused} {
		if err := s.SetRunState(ctx, run.ID, live); err != nil {
			t.Fatalf("SetRunState(%s): %v", live, err)
		}
		err := s.DeleteWorkflow(ctx, w.ID)
		if !errors.Is(err, workflow.ErrInUse) {
			t.Errorf("DeleteWorkflow with a %s run = %v, want ErrInUse", live, err)
		} else if !strings.Contains(err.Error(), "run "+strconv.FormatInt(run.ID, 10)) {
			t.Errorf("DeleteWorkflow error %q does not name run %d", err, run.ID)
		}
		if err := s.DeleteStep(ctx, st.ID); !errors.Is(err, workflow.ErrInUse) {
			t.Errorf("DeleteStep with a %s run = %v, want ErrInUse", live, err)
		}
	}
	// Still there.
	if _, err := s.GetWorkflow(ctx, w.ID); err != nil {
		t.Errorf("workflow vanished despite the refusal: %v", err)
	}

	// Once the run ends, both deletes go through, and the run's history
	// goes with its definition (cascade), while the task itself stays.
	if err := s.SetRunState(ctx, run.ID, workflow.RunCancelled); err != nil {
		t.Fatalf("SetRunState(cancelled): %v", err)
	}
	if err := s.DeleteStep(ctx, st.ID); err != nil {
		t.Errorf("DeleteStep after the run ended: %v", err)
	}
	if err := s.DeleteWorkflow(ctx, w.ID); err != nil {
		t.Errorf("DeleteWorkflow after the run ended: %v", err)
	}
	if _, err := s.GetRun(ctx, run.ID); !errors.Is(err, workflow.ErrRunNotFound) {
		t.Errorf("GetRun after its workflow was deleted = %v, want ErrRunNotFound", err)
	}
	if _, err := s.GetTask(ctx, run.TaskID); err != nil {
		t.Errorf("the task must survive its workflow's deletion: %v", err)
	}
}

// A step that never ran within a live run is free to go even while the
// run is live: the refusal is about step runs, not membership.
func TestDeleteStepUnreferencedByTheLiveRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	w := mustWorkflow(t, s, "wf")
	ran := mustStep(t, s, w.ID, "ran")
	unused := mustStep(t, s, w.ID, "never ran")
	run := mustRun(t, s, w.ID)
	if _, err := s.CreateStepRun(ctx, workflow.StepRun{RunID: run.ID, StepID: ran.ID}); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	if err := s.SetRunState(ctx, run.ID, workflow.RunRunning); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}
	if err := s.DeleteStep(ctx, unused.ID); err != nil {
		t.Errorf("DeleteStep(unreferenced) = %v, want success", err)
	}
	if err := s.DeleteStep(ctx, ran.ID); !errors.Is(err, workflow.ErrInUse) {
		t.Errorf("DeleteStep(referenced) = %v, want ErrInUse", err)
	}
}

// Runs cascade with their task, like sessions; a session bound to a step
// run loses only the back-pointer, not itself.
func TestDeletingTheTaskCascadesRunsAndUnbindsSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	w := mustWorkflow(t, s, "wf")
	st := mustStep(t, s, w.ID, "only")
	run := mustRun(t, s, w.ID)
	sr, err := s.CreateStepRun(ctx, workflow.StepRun{RunID: run.ID, StepID: st.ID, SessionExternalID: "sess-1"})
	if err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	// A session on a *different* task, bound to this step run, so it
	// survives the task delete and we can watch the pointer clear.
	other := mustAdd(t, s, "other task")
	sess, err := s.CreateStepRunSession(ctx, sr.ID, other.ID, "sess-1", "/tmp", "label", "")
	if err != nil {
		t.Fatalf("CreateStepRunSession: %v", err)
	}
	if sess.StepRunID == nil || *sess.StepRunID != sr.ID {
		t.Fatalf("session.StepRunID = %v, want %d", sess.StepRunID, sr.ID)
	}
	plain, err := s.CreateSession(ctx, other.ID, "sess-2", "/tmp", "label", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if plain.StepRunID != nil {
		t.Errorf("an ordinary session has StepRunID %v, want nil", plain.StepRunID)
	}

	if err := s.DeleteTask(ctx, run.TaskID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if _, err := s.GetRun(ctx, run.ID); !errors.Is(err, workflow.ErrRunNotFound) {
		t.Errorf("GetRun after its task was deleted = %v, want ErrRunNotFound", err)
	}
	if _, err := s.GetStepRun(ctx, sr.ID); !errors.Is(err, workflow.ErrStepRunNotFound) {
		t.Errorf("GetStepRun after its run was deleted = %v, want ErrStepRunNotFound", err)
	}
	sessions, err := s.ListSessionsForTask(ctx, other.ID)
	if err != nil {
		t.Fatalf("ListSessionsForTask: %v", err)
	}
	for _, got := range sessions {
		if got.StepRunID != nil {
			t.Errorf("session %s still points at step run %d after it was deleted", got.ExternalID, *got.StepRunID)
		}
	}
	if len(sessions) != 2 {
		t.Errorf("the other task's sessions were lost: %d left, want 2", len(sessions))
	}
	// The definition is untouched by a run going away.
	if _, err := s.GetWorkflow(ctx, w.ID); err != nil {
		t.Errorf("workflow lost when a run was deleted: %v", err)
	}
}

func TestDuplicateWorkflowCopiesStepsAndRemapsEdges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	w := mustWorkflow(t, s, "original")
	if err := s.SetWorkflowDescription(ctx, w.ID, "desc"); err != nil {
		t.Fatalf("SetWorkflowDescription: %v", err)
	}
	implement := mustStep(t, s, w.ID, "implement")
	review := mustStep(t, s, w.ID, "review")
	implement.PromptMD = "Implement {{.Task.Title}}"
	implement.Model = "opus"
	if err := s.UpdateStep(ctx, implement); err != nil {
		t.Fatalf("UpdateStep: %v", err)
	}
	two := int64(2)
	if _, err := s.SetEdge(ctx, implement.ID, "done", review.ID, nil); err != nil {
		t.Fatalf("SetEdge: %v", err)
	}
	if _, err := s.SetEdge(ctx, review.ID, "reject", implement.ID, &two); err != nil {
		t.Fatalf("SetEdge: %v", err)
	}

	if _, err := s.DuplicateWorkflow(ctx, w.ID, "original"); err == nil {
		t.Error("DuplicateWorkflow onto an existing name should fail")
	}
	if _, err := s.DuplicateWorkflow(ctx, 9999, "ghost"); !errors.Is(err, workflow.ErrWorkflowNotFound) {
		t.Errorf("DuplicateWorkflow(unknown) = %v, want ErrWorkflowNotFound", err)
	}

	dup, err := s.DuplicateWorkflow(ctx, w.ID, "copy")
	if err != nil {
		t.Fatalf("DuplicateWorkflow: %v", err)
	}
	if dup.Name != "copy" || dup.Description != "desc" || dup.StepCount != 2 {
		t.Errorf("duplicate = %+v", dup)
	}
	steps, err := s.ListSteps(ctx, dup.ID)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 2 || steps[0].Name != "implement" || steps[0].PromptMD != "Implement {{.Task.Title}}" || steps[0].Model != "opus" {
		t.Errorf("copied steps = %+v", steps)
	}
	edges, err := s.ListEdges(ctx, dup.ID)
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("copied %d edges, want 2", len(edges))
	}
	copiedIDs := map[int64]bool{steps[0].ID: true, steps[1].ID: true}
	for _, e := range edges {
		if !copiedIDs[e.FromStepID] || !copiedIDs[e.ToStepID] {
			t.Errorf("edge %+v points outside the copy (steps %v)", e, copiedIDs)
		}
	}
	// The loop-back edge kept its bound: review(copy) -reject-> implement(copy), max 2.
	var sawReject bool
	for _, e := range edges {
		if e.Outcome == "reject" {
			sawReject = true
			if e.FromStepID != steps[1].ID || e.ToStepID != steps[0].ID || e.MaxIterations == nil || *e.MaxIterations != 2 {
				t.Errorf("reject edge = %+v, want review -> implement max 2", e)
			}
		}
	}
	if !sawReject {
		t.Error("the reject edge was not copied")
	}
	// The original is untouched.
	if orig, _ := s.ListEdges(ctx, w.ID); len(orig) != 2 {
		t.Errorf("original has %d edges after duplication, want 2", len(orig))
	}
}
