package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/workflow"
)

// stepOut is a workflow step run rendered for a session executing it:
// where it sits in the workflow, what it was handed, and how it may
// finish. Outcome and Deliverable are filled once the step has been
// finished (by finish_step, or by the runner's fallback).
type stepOut struct {
	Workflow    string   `json:"workflow"`
	Step        string   `json:"step"`
	Iteration   int64    `json:"iteration"`
	Input       string   `json:"input,omitempty"`
	Feedback    string   `json:"feedback,omitempty"`
	Outcomes    []string `json:"outcomes"`
	Finished    bool     `json:"finished"`
	Outcome     string   `json:"outcome,omitempty"`
	Deliverable string   `json:"deliverable,omitempty"`
}

// registerStepTools adds the two tools a workflow step's session gets on
// top of the task surface: reading its step and handing off its result.
// Only called when `tend mcp` was started with --step-run-id, so an
// ordinary session never sees them. stepRunID is pinned, not a default:
// unlike the task tools there is no override, because a step's session
// finishing some other step is never what anyone meant.
func registerStepTools(srv *mcp.Server, store Store, stepRunID int64) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_workflow_step",
		Description: "Get the workflow step this session is executing: the workflow and step " +
			"names, which iteration of the step this is, the input handed forward by the " +
			"previous step, the feedback from the step that routed back here (if any), and " +
			"the outcomes finish_step accepts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, stepOut, error) {
		return fetchStep(ctx, store, stepRunID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "finish_step",
		Description: "Hand off this step's result: the outcome that picks the next step (one of " +
			"the outcomes get_workflow_step lists) and the deliverable the next step receives " +
			"as its input. Call it once, when the work is done; a second call is an error. It " +
			"does not end the session -- finish your turn normally afterwards.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Outcome     string `json:"outcome" jsonschema:"one of the outcomes get_workflow_step lists for this step"`
		Deliverable string `json:"deliverable,omitempty" jsonschema:"what the next step receives as its input: a PR link, a summary, a review verdict. Empty passes this step's own input along unchanged"`
	}) (*mcp.CallToolResult, stepOut, error) {
		sr, err := store.GetStepRun(ctx, stepRunID)
		if err != nil {
			return nil, stepOut{}, err
		}
		if sr.Finished() {
			return nil, stepOut{}, fmt.Errorf("%w: outcome %q stands", workflow.ErrStepRunFinished, sr.Outcome)
		}
		outcome, err := workflow.NormalizeOutcome(in.Outcome)
		if err != nil {
			return nil, stepOut{}, err
		}
		allowed, err := allowedOutcomes(ctx, store, sr.StepID)
		if err != nil {
			return nil, stepOut{}, err
		}
		if !contains(allowed, outcome) {
			return nil, stepOut{}, fmt.Errorf("outcome %q is not one this step routes; use one of: %s",
				outcome, strings.Join(allowed, ", "))
		}
		if err := store.FinishStepRun(ctx, stepRunID, outcome, in.Deliverable); err != nil {
			return nil, stepOut{}, err
		}
		return fetchStep(ctx, store, stepRunID)
	})
}

// allowedOutcomes is the set finish_step accepts for a step: the outcomes
// of its live edges, or just "done" when it has none (the last step of a
// linear workflow), so the agent has exactly one right answer rather
// than an empty list. The runner treats any outcome with no edge as the
// end of the run, so this is stricter than what it would tolerate; the
// point is to catch a misspelt or invented outcome at the hand-off,
// where the agent can still correct it, instead of ending the run.
func allowedOutcomes(ctx context.Context, store Store, stepID int64) ([]string, error) {
	edges, err := store.OutgoingEdges(ctx, stepID)
	if err != nil {
		return nil, err
	}
	if len(edges) == 0 {
		return []string{workflow.OutcomeDone}, nil
	}
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.Outcome)
	}
	return out, nil
}

// fetchStep loads and renders the bound step run, the common tail of
// both step tools.
func fetchStep(ctx context.Context, store Store, stepRunID int64) (*mcp.CallToolResult, stepOut, error) {
	sr, err := store.GetStepRun(ctx, stepRunID)
	if err != nil {
		return nil, stepOut{}, err
	}
	step, err := store.GetStep(ctx, sr.StepID)
	if err != nil {
		return nil, stepOut{}, err
	}
	wf, err := store.GetWorkflow(ctx, step.WorkflowID)
	if err != nil {
		// The step's definition may be edited while the run is live, but
		// the store refuses to delete anything an active run references,
		// so a missing workflow here is a real inconsistency, not a race.
		if errors.Is(err, workflow.ErrWorkflowNotFound) {
			return nil, stepOut{}, fmt.Errorf("step %q: %w", step.Name, err)
		}
		return nil, stepOut{}, err
	}
	outcomes, err := allowedOutcomes(ctx, store, step.ID)
	if err != nil {
		return nil, stepOut{}, err
	}
	return nil, stepOut{
		Workflow:    wf.Name,
		Step:        step.Name,
		Iteration:   sr.Iteration,
		Input:       sr.Input,
		Feedback:    sr.Feedback,
		Outcomes:    outcomes,
		Finished:    sr.Finished(),
		Outcome:     sr.Outcome,
		Deliverable: sr.Deliverable,
	}, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
