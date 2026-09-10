package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// seedStepRun puts a review step of an implement / review workflow into
// the fake, mid-run on its second iteration: it was handed "PR #7" and
// routed back here once with feedback. review routes approve -> ship and
// reject -> implement. Returns the step run id.
func seedStepRun(s *fakeStore) int64 {
	s.workflows[1] = workflow.Workflow{ID: 1, Name: "fix a bug"}
	s.steps[10] = workflow.Step{ID: 10, WorkflowID: 1, Name: "implement", Kind: workflow.StepAgent}
	s.steps[11] = workflow.Step{ID: 11, WorkflowID: 1, Name: "review", Kind: workflow.StepAgent}
	s.steps[12] = workflow.Step{ID: 12, WorkflowID: 1, Name: "ship", Kind: workflow.StepAgent}
	s.edges = []workflow.Edge{
		{ID: 1, FromStepID: 10, Outcome: "done", ToStepID: 11},
		{ID: 2, FromStepID: 11, Outcome: "approve", ToStepID: 12},
		{ID: 3, FromStepID: 11, Outcome: "reject", ToStepID: 10},
	}
	s.stepRuns[100] = workflow.StepRun{
		ID: 100, RunID: 1, StepID: 11, Iteration: 2,
		Input: "PR #7", Feedback: "the first pass missed the tests",
	}
	return 100
}

func callToolErr(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("CallTool(%s) succeeded, want an error result", name)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	return names
}

// An ordinary session -- no --step-run-id -- never sees the step tools;
// a step's session gets them on top of the unchanged task surface.
func TestStepToolsOnlyRegisteredWhenBoundToAStepRun(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	sr := seedStepRun(store)

	plain := toolNames(t, dial(t, store, 1))
	if plain["get_workflow_step"] || plain["finish_step"] {
		t.Errorf("session without a step run exposes step tools: %v", plain)
	}

	bound := toolNames(t, dialStep(t, store, 1, sr))
	if !bound["get_workflow_step"] || !bound["finish_step"] {
		t.Errorf("session bound to a step run lacks step tools: %v", bound)
	}
	if !bound["get_current_task"] {
		t.Error("binding to a step run dropped the task tools")
	}
	if len(bound) != len(plain)+2 {
		t.Errorf("bound session has %d tools, want the %d task tools plus 2", len(bound), len(plain))
	}
}

func TestGetWorkflowStepReportsStepInputFeedbackAndOutcomes(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dialStep(t, store, 1, seedStepRun(store))

	got := callTool[stepOut](t, cs, "get_workflow_step", nil)
	if got.Workflow != "fix a bug" || got.Step != "review" || got.Iteration != 2 {
		t.Errorf("get_workflow_step = %+v, want fix a bug / review / iteration 2", got)
	}
	if got.Input != "PR #7" || got.Feedback != "the first pass missed the tests" {
		t.Errorf("input=%q feedback=%q, want the step run's input and feedback", got.Input, got.Feedback)
	}
	if strings.Join(got.Outcomes, ",") != "approve,reject" {
		t.Errorf("outcomes = %v, want [approve reject] from the live edges", got.Outcomes)
	}
	if got.Finished || got.Outcome != "" {
		t.Errorf("unfinished step reports finished=%v outcome=%q", got.Finished, got.Outcome)
	}
}

// A step with no outgoing edges -- the end of a linear workflow -- has
// exactly one allowed outcome, "done", not an empty list.
func TestGetWorkflowStepWithNoEdgesOffersDone(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	seedStepRun(store)
	store.stepRuns[101] = workflow.StepRun{ID: 101, RunID: 1, StepID: 12, Iteration: 1, Input: "PR #7"}
	cs := dialStep(t, store, 1, 101)

	got := callTool[stepOut](t, cs, "get_workflow_step", nil)
	if got.Step != "ship" || strings.Join(got.Outcomes, ",") != "done" {
		t.Errorf("get_workflow_step = %+v, want step ship with outcomes [done]", got)
	}
	// And finish_step accepts nothing else.
	msg := callToolErr(t, cs, "finish_step", map[string]any{"outcome": "approve"})
	if !strings.Contains(msg, `"approve"`) || !strings.Contains(msg, "done") {
		t.Errorf("finish_step(approve) error = %q, want it to name the outcome and offer done", msg)
	}
	callTool[stepOut](t, cs, "finish_step", map[string]any{"outcome": "done", "deliverable": "shipped"})
	if fin := store.stepRuns[101]; fin.Outcome != "done" || fin.Deliverable != "shipped" {
		t.Errorf("step run after finish_step(done) = %+v", fin)
	}
}

// The acceptance line for #180: the outcome written through finish_step
// is the one the runner routes on. The outcome is normalized the way the
// store does it, so "Approve" matches the edge authored as "approve".
func TestFinishStepWritesNormalizedOutcomeAndDeliverable(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	sr := seedStepRun(store)
	cs := dialStep(t, store, 1, sr)

	got := callTool[stepOut](t, cs, "finish_step", map[string]any{
		"outcome": " Approve ", "deliverable": "LGTM, tests cover the regression",
	})
	if !got.Finished || got.Outcome != "approve" || got.Deliverable != "LGTM, tests cover the regression" {
		t.Errorf("finish_step = %+v, want finished with outcome approve and the deliverable", got)
	}
	fin := store.stepRuns[sr]
	if fin.Outcome != "approve" || fin.Deliverable != "LGTM, tests cover the regression" || !fin.Finished() {
		t.Errorf("step run after finish_step = %+v, want the hand-off on the row", fin)
	}
}

// An empty deliverable is allowed: the runner then carries the step's own
// input forward, the way a gate approve does.
func TestFinishStepDeliverableIsOptional(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	sr := seedStepRun(store)
	cs := dialStep(t, store, 1, sr)

	got := callTool[stepOut](t, cs, "finish_step", map[string]any{"outcome": "approve"})
	if !got.Finished || got.Outcome != "approve" || got.Deliverable != "" {
		t.Errorf("finish_step without a deliverable = %+v", got)
	}
}

func TestFinishStepRejectsAnOutcomeWithNoEdge(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	sr := seedStepRun(store)
	cs := dialStep(t, store, 1, sr)

	msg := callToolErr(t, cs, "finish_step", map[string]any{"outcome": "ship it", "deliverable": "x"})
	if !strings.Contains(msg, `"ship it"`) || !strings.Contains(msg, "approve, reject") {
		t.Errorf("error = %q, want it to name the bad outcome and list approve, reject", msg)
	}
	if store.stepRuns[sr].Finished() {
		t.Error("a rejected outcome finished the step run")
	}

	if msg := callToolErr(t, cs, "finish_step", map[string]any{"outcome": "   "}); !strings.Contains(msg, "empty") {
		t.Errorf("blank outcome error = %q, want it to say the outcome is empty", msg)
	}
}

// finish_step is a one-shot hand-off: the first outcome stands and the
// second call says so.
func TestFinishStepTwiceIsAnError(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	sr := seedStepRun(store)
	cs := dialStep(t, store, 1, sr)

	callTool[stepOut](t, cs, "finish_step", map[string]any{"outcome": "reject", "deliverable": "needs tests"})
	msg := callToolErr(t, cs, "finish_step", map[string]any{"outcome": "approve", "deliverable": "fine"})
	if !strings.Contains(msg, "already finished") || !strings.Contains(msg, `"reject"`) {
		t.Errorf("second finish_step error = %q, want 'already finished' naming the standing outcome", msg)
	}
	if fin := store.stepRuns[sr]; fin.Outcome != "reject" || fin.Deliverable != "needs tests" {
		t.Errorf("first outcome did not stand: %+v", fin)
	}

	// get_workflow_step still works and now reports the hand-off.
	got := callTool[stepOut](t, cs, "get_workflow_step", nil)
	if !got.Finished || got.Outcome != "reject" || got.Deliverable != "needs tests" {
		t.Errorf("get_workflow_step after finishing = %+v", got)
	}
}

// The step run is pinned by --step-run-id; a stale or wrong id is an
// error the agent sees, not a silent no-op.
func TestStepToolsWithUnknownStepRunReportIt(t *testing.T) {
	store := newFakeStore(task.Task{ID: 1, Title: "bound"})
	cs := dialStep(t, store, 1, 999)

	if msg := callToolErr(t, cs, "get_workflow_step", nil); !strings.Contains(msg, "not found") {
		t.Errorf("get_workflow_step error = %q, want not found", msg)
	}
	if msg := callToolErr(t, cs, "finish_step", map[string]any{"outcome": "done"}); !strings.Contains(msg, "not found") {
		t.Errorf("finish_step error = %q, want not found", msg)
	}
}
