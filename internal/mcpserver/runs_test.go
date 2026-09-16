package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// seedRun puts a two-step run (implement -> review) on task 1 into the
// fake, mirroring seedStepRun's workflow but with real Run/StepRun rows:
// step run 100 (implement) finished done a minute ago, step run 101
// (review) is the current step, still running. Returns the run id.
func seedRun(t *testing.T, s *fakeStore) int64 {
	t.Helper()
	s.workflows[1] = workflow.Workflow{ID: 1, Name: "fix a bug"}
	s.steps[10] = workflow.Step{ID: 10, WorkflowID: 1, Name: "implement", Kind: workflow.StepAgent}
	s.steps[11] = workflow.Step{ID: 11, WorkflowID: 1, Name: "review", Kind: workflow.StepAgent}
	s.edges = []workflow.Edge{{ID: 1, FromStepID: 10, Outcome: "done", ToStepID: 11}}

	start := time.Now().Add(-2 * time.Minute)
	implEnd := start.Add(time.Minute)
	s.stepRuns[100] = workflow.StepRun{
		ID: 100, RunID: 5, StepID: 10, Iteration: 1,
		PromptRendered: "implement it", Input: "", Outcome: "done", Deliverable: "PR #7",
		LogPath: "/tmp/does-not-matter-100.jsonl", StartedAt: start, EndedAt: &implEnd,
	}
	s.stepRuns[101] = workflow.StepRun{
		ID: 101, RunID: 5, StepID: 11, Iteration: 1,
		PromptRendered: "review it", Input: "PR #7",
		LogPath: "/tmp/does-not-matter-101.jsonl", StartedAt: implEnd,
	}
	cur := int64(101)
	s.runs[5] = workflow.Run{
		ID: 5, WorkflowID: 1, TaskID: 1, Cwd: "/repo",
		State: workflow.RunRunning, CurrentStepRunID: &cur,
		StartedAt: start,
	}
	return 5
}

func TestListWorkflowRunsDefaultsToTheBoundTask(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"}, task.Task{ID: 2, Title: "other"})
	seedRun(t, store)
	store.runs[6] = workflow.Run{ID: 6, WorkflowID: 1, TaskID: 2, State: workflow.RunDone, StartedAt: time.Now()}
	cs := dial(t, store, 1)

	got := callTool[runsOut](t, cs, "list_workflow_runs", nil)
	if len(got.Runs) != 1 || got.Runs[0].ID != 5 {
		t.Fatalf("list_workflow_runs (default task) = %+v, want just run 5", got.Runs)
	}
	r := got.Runs[0]
	if r.Workflow != "fix a bug" || r.State != "running" || r.Cwd != "/repo" {
		t.Errorf("run = %+v, want workflow fix a bug, state running, cwd /repo", r)
	}
	if r.CurrentStep == nil || r.CurrentStep.StepRunID != 101 || r.CurrentStep.Step != "review" || r.CurrentStep.Finished {
		t.Errorf("current_step = %+v, want step run 101 (review), unfinished", r.CurrentStep)
	}
	if r.DurationSeconds < 100 {
		t.Errorf("duration_seconds = %d, want at least ~120 (run started 2 minutes ago)", r.DurationSeconds)
	}

	// An explicit task_id reaches another task's runs.
	got2 := callTool[runsOut](t, cs, "list_workflow_runs", map[string]any{"task_id": 2})
	if len(got2.Runs) != 1 || got2.Runs[0].ID != 6 || got2.Runs[0].State != "done" {
		t.Errorf("list_workflow_runs(task_id=2) = %+v, want just run 6, done", got2.Runs)
	}
}

func TestListWorkflowRunsRejectsAnUnknownTask(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)
	// The fake's GetTask error doesn't name the id (the real store's
	// does); the point here is that a bad task_id surfaces GetTask's
	// error rather than an empty list.
	msg := callToolErr(t, cs, "list_workflow_runs", map[string]any{"task_id": 999})
	if !strings.Contains(msg, "no such task") {
		t.Errorf("error = %q, want the GetTask error for an unknown task", msg)
	}
}

func TestGetWorkflowRunReportsStepRunsAndPerStepTotalsWithoutText(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	runID := seedRun(t, store)
	cs := dial(t, store, 1)

	got := callTool[runDetailOut](t, cs, "get_workflow_run", map[string]any{"run_id": runID})
	if got.ID != runID || got.Workflow != "fix a bug" {
		t.Fatalf("get_workflow_run = %+v", got.runOut)
	}
	if len(got.StepRuns) != 2 {
		t.Fatalf("step_runs = %+v, want 2", got.StepRuns)
	}
	implement, review := got.StepRuns[0], got.StepRuns[1]
	if implement.Step != "implement" || !implement.Finished || implement.Outcome != "done" {
		t.Errorf("implement step run = %+v, want finished with outcome done", implement)
	}
	if implement.DeliverableLen != len("PR #7") || implement.Deliverable != "" {
		t.Errorf("implement deliverable_len/deliverable = %d/%q, want length only, no text by default",
			implement.DeliverableLen, implement.Deliverable)
	}
	if review.Step != "review" || review.Finished {
		t.Errorf("review step run = %+v, want unfinished", review)
	}
	if review.InputLen != len("PR #7") || review.Input != "" {
		t.Errorf("review input_len/input = %d/%q, want length only, no text by default",
			review.InputLen, review.Input)
	}
	if len(got.BySteps) != 2 || got.BySteps[0].Step != "implement" || got.BySteps[0].Runs != 1 {
		t.Errorf("by_step = %+v, want implement then review, each run once", got.BySteps)
	}
}

// A step run twice within one run (a review's reject looping back to
// implement) rolls up under one by_step entry, not two: run 70's own
// motivation for this tool was a step that ran many times.
func TestGetWorkflowRunAggregatesByStepAcrossARepeatedStep(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	runID := seedRun(t, store)
	// implement runs a second time, after review, iteration 2 -- the
	// reject loop-back. Both timestamps are pinned (rather than left
	// running to "now") so its duration is deterministic.
	implement2Start := store.stepRuns[100].EndedAt.Add(30 * time.Second)
	implement2End := implement2Start.Add(45 * time.Second)
	store.stepRuns[102] = workflow.StepRun{
		ID: 102, RunID: runID, StepID: 10, Iteration: 2,
		StartedAt: implement2Start, EndedAt: &implement2End,
	}
	cs := dial(t, store, 1)

	got := callTool[runDetailOut](t, cs, "get_workflow_run", map[string]any{"run_id": runID})
	if len(got.StepRuns) != 3 {
		t.Fatalf("step_runs = %+v, want 3 (implement, review, implement again)", got.StepRuns)
	}
	if len(got.BySteps) != 2 {
		t.Fatalf("by_step = %+v, want 2 entries (implement, review), not one per step run", got.BySteps)
	}
	implementTotal, reviewTotal := got.BySteps[0], got.BySteps[1]
	if implementTotal.Step != "implement" || implementTotal.Runs != 2 {
		t.Errorf("by_step[0] = %+v, want implement with 2 runs", implementTotal)
	}
	if reviewTotal.Step != "review" || reviewTotal.Runs != 1 {
		t.Errorf("by_step[1] = %+v, want review with 1 run", reviewTotal)
	}
	// TotalSeconds sums both implement step runs, not just the first.
	if implementTotal.TotalSeconds <= got.StepRuns[0].DurationSeconds {
		t.Errorf("implement total_seconds = %d, want more than its first run's %d duration alone",
			implementTotal.TotalSeconds, got.StepRuns[0].DurationSeconds)
	}
}

// The text-inclusion flags are opt-in per call: asking for deliverables
// does not also pull in inputs, and vice versa.
func TestGetWorkflowRunIncludesTextOnlyWhenAsked(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	runID := seedRun(t, store)
	cs := dial(t, store, 1)

	got := callTool[runDetailOut](t, cs, "get_workflow_run", map[string]any{
		"run_id": runID, "include_deliverables": true,
	})
	if got.StepRuns[0].Deliverable != "PR #7" {
		t.Errorf("deliverable = %q, want PR #7 when include_deliverables is set", got.StepRuns[0].Deliverable)
	}
	if got.StepRuns[1].Input != "" {
		t.Errorf("input = %q, want empty: include_deliverables does not imply include_inputs", got.StepRuns[1].Input)
	}

	got = callTool[runDetailOut](t, cs, "get_workflow_run", map[string]any{
		"run_id": runID, "include_inputs": true,
	})
	if got.StepRuns[1].Input != "PR #7" {
		t.Errorf("input = %q, want PR #7 when include_inputs is set", got.StepRuns[1].Input)
	}
	if got.StepRuns[0].Deliverable != "" {
		t.Errorf("deliverable = %q, want empty: include_inputs does not imply include_deliverables", got.StepRuns[0].Deliverable)
	}
}

func TestGetWorkflowRunWithUnknownRunReportsNotFound(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)
	if msg := callToolErr(t, cs, "get_workflow_run", map[string]any{"run_id": 999}); !strings.Contains(msg, "not found") {
		t.Errorf("error = %q, want not found", msg)
	}
}

// A minimal but realistic stream-json log: one result event with usage,
// cost, a permission denial and a sub-agent, plus one top-level tool
// call. Good enough to exercise get_step_run_usage end to end; the exact
// parsing is agent.ParseUsage's own tests.
const usageStepLog = `{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5"}
{"type":"assistant","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test"}}]}}
{"type":"user","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"duration_ms":500,"duration_api_ms":400,"total_cost_usd":0.42,"usage":{"input_tokens":5,"output_tokens":50,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000},"modelUsage":{"claude-opus-5":{"inputTokens":5,"outputTokens":50,"cacheReadInputTokens":1000,"cacheCreationInputTokens":100,"costUSD":0.42}},"permission_denials":[{"tool_name":"Edit"}],"subagent_stats":{"spawned":1,"by_type":{"general-purpose":1}},"result":"done"}
`

func writeUsageLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "step.jsonl")
	if err := os.WriteFile(path, []byte(usageStepLog), 0o644); err != nil {
		t.Fatalf("writing test log: %v", err)
	}
	return path
}

func TestGetStepRunUsageTalliesTheLog(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedRun(t, store)
	logPath := writeUsageLog(t)
	sr := store.stepRuns[100]
	sr.LogPath = logPath
	store.stepRuns[100] = sr
	cs := dial(t, store, 1)

	got := callTool[usageOut](t, cs, "get_step_run_usage", map[string]any{"step_run_id": 100})
	if got.Step != "implement" || got.LogPath != logPath {
		t.Errorf("usage = %+v, want step implement and the log path", got)
	}
	if got.Results != 1 || got.Turns != 3 || got.CostUSD != 0.42 {
		t.Errorf("results/turns/cost = %d/%d/%v, want 1/3/0.42", got.Results, got.Turns, got.CostUSD)
	}
	if got.InputTokens != 5 || got.OutputTokens != 50 || got.CacheReadTokens != 1000 || got.CacheCreationTokens != 100 {
		t.Errorf("tokens = %+v, want in5/out50/create100/read1000", got)
	}
	if got.PermissionDenials != 1 {
		t.Errorf("permission_denials = %d, want 1", got.PermissionDenials)
	}
	if got.SubagentsSpawned != 1 || got.SubagentsByType["general-purpose"] != 1 {
		t.Errorf("sub-agents = %d %v, want 1 general-purpose", got.SubagentsSpawned, got.SubagentsByType)
	}
	m, ok := got.Models["claude-opus-5"]
	if !ok || m.CostUSD != 0.42 || m.OutputTokens != 50 {
		t.Errorf("models[claude-opus-5] = %+v (present %v), want cost 0.42 and 50 output tokens", m, ok)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "Bash" || got.ToolCalls[0].Calls != 1 {
		t.Errorf("tool_calls = %+v, want Bash x1", got.ToolCalls)
	}
}

func TestGetStepRunUsageWithNoLogNamesWhy(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	store.workflows[1] = workflow.Workflow{ID: 1, Name: "wf"}
	store.steps[20] = workflow.Step{ID: 20, WorkflowID: 1, Name: "review", Kind: workflow.StepGate}
	store.steps[21] = workflow.Step{ID: 21, WorkflowID: 1, Name: "implement", Kind: workflow.StepAgent}
	store.stepRuns[200] = workflow.StepRun{ID: 200, RunID: 9, StepID: 20}
	store.stepRuns[201] = workflow.StepRun{ID: 201, RunID: 9, StepID: 21}
	cs := dial(t, store, 1)

	if msg := callToolErr(t, cs, "get_step_run_usage", map[string]any{"step_run_id": 200}); !strings.Contains(msg, "gate") {
		t.Errorf("gate step run error = %q, want it to say it's a gate", msg)
	}
	if msg := callToolErr(t, cs, "get_step_run_usage", map[string]any{"step_run_id": 201}); !strings.Contains(msg, "no log") {
		t.Errorf("no-log agent step run error = %q, want it to say there is no log", msg)
	}
}

func TestGetStepRunUsageWithMissingLogFileReportsIt(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedRun(t, store)
	sr := store.stepRuns[100]
	sr.LogPath = filepath.Join(t.TempDir(), "gone.jsonl")
	store.stepRuns[100] = sr
	cs := dial(t, store, 1)

	if msg := callToolErr(t, cs, "get_step_run_usage", map[string]any{"step_run_id": 100}); !strings.Contains(msg, "gone") {
		t.Errorf("error = %q, want it to say the log is gone", msg)
	}
}

func TestGetStepRunUsageWithUnknownStepRunReportsNotFound(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dial(t, store, 1)
	if msg := callToolErr(t, cs, "get_step_run_usage", map[string]any{"step_run_id": 999}); !strings.Contains(msg, "not found") {
		t.Errorf("error = %q, want not found", msg)
	}
}

// The run inspection tools are registered for every session, the same
// convention as the workflow authoring tools.
func TestRunToolsRegisteredForEverySession(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	names := toolNames(t, dial(t, store, 1))
	for _, want := range []string{"list_workflow_runs", "get_workflow_run", "get_step_run_usage"} {
		if !names[want] {
			t.Errorf("ordinary session lacks run tool %q", want)
		}
	}
}
