package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/usage"
	"github.com/jwstover/tend/internal/workflow"
)

// The run inspection surface (tend task #327): a task's workflow runs,
// each run's step runs, and what a step run's stream-json log says it
// cost. Read-only, and registered for every session, because "why did
// that run take four passes" and "where did the tokens go" are ordinary
// task work that used to need direct SQLite and log-file access. The
// text columns (prompt, input, feedback, deliverable) are reported by
// length, since a run's worth of them would flood the context; the
// full text is opt-in per call.

// runOut is one workflow run as list_workflow_runs shows it.
type runOut struct {
	ID         int64  `json:"id"`
	WorkflowID int64  `json:"workflow_id"`
	Workflow   string `json:"workflow"`
	TaskID     int64  `json:"task_id"`
	State      string `json:"state"`
	Cwd        string `json:"cwd"`
	// Error is why the run failed, or why a retried run is paused; ""
	// otherwise.
	Error     string  `json:"error,omitempty"`
	StartedAt string  `json:"started_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
	// DurationSeconds is started to ended, or to now for a run still
	// going.
	DurationSeconds int64 `json:"duration_seconds"`
	// CurrentStep is the step run the runner is on (or parked at), nil
	// before the first step starts.
	CurrentStep *currentStepOut `json:"current_step,omitempty"`
}

// currentStepOut names the step run a run is currently at.
type currentStepOut struct {
	StepRunID int64  `json:"step_run_id"`
	Step      string `json:"step"`
	Iteration int64  `json:"iteration"`
	Finished  bool   `json:"finished"`
}

// runsOut wraps the list in an object (see subtasksOut).
type runsOut struct {
	Runs []runOut `json:"runs"`
}

// stepRunOut is one step run inside a run, as get_workflow_run lists
// them. The text columns are lengths by default; Input, Feedback and
// Deliverable carry the text only when asked for.
type stepRunOut struct {
	ID                int64   `json:"step_run_id"`
	StepID            int64   `json:"step_id"`
	Step              string  `json:"step"`
	Kind              string  `json:"kind"`
	Iteration         int64   `json:"iteration"`
	Finished          bool    `json:"finished"`
	Outcome           string  `json:"outcome,omitempty"`
	Model             string  `json:"model,omitempty"`
	PermissionMode    string  `json:"permission_mode,omitempty"`
	SessionExternalID string  `json:"session_external_id,omitempty"`
	LogPath           string  `json:"log_path,omitempty"`
	StartedAt         string  `json:"started_at"`
	EndedAt           *string `json:"ended_at,omitempty"`
	DurationSeconds   int64   `json:"duration_seconds"`
	PromptLen         int     `json:"prompt_len"`
	InputLen          int     `json:"input_len"`
	FeedbackLen       int     `json:"feedback_len"`
	DeliverableLen    int     `json:"deliverable_len"`
	Input             string  `json:"input,omitempty"`
	Feedback          string  `json:"feedback,omitempty"`
	Deliverable       string  `json:"deliverable,omitempty"`
}

// stepTotalOut is one step's share of a run: how many times it ran and
// how long those runs took together -- the per-step aggregate every
// "where did the time go" question starts with.
type stepTotalOut struct {
	StepID       int64  `json:"step_id"`
	Step         string `json:"step"`
	Runs         int    `json:"runs"`
	TotalSeconds int64  `json:"total_seconds"`
}

// runDetailOut is get_workflow_run's answer: the run, its step runs in
// the order they started, and the per-step totals.
type runDetailOut struct {
	runOut
	StepRuns []stepRunOut   `json:"step_runs"`
	BySteps  []stepTotalOut `json:"by_step"`
}

// usageOut is get_step_run_usage's answer: usage.StreamUsage for one step
// run's log, plus which step run and log it came from.
type usageOut struct {
	StepRunID int64  `json:"step_run_id"`
	Step      string `json:"step"`
	Iteration int64  `json:"iteration"`
	LogPath   string `json:"log_path"`
	// Results is how many result events the log holds: one for a step
	// that ran straight through, one more per resume (crash resume,
	// finish_step nudge, retry).
	Results             int     `json:"results"`
	Turns               int     `json:"turns"`
	DurationSeconds     int64   `json:"duration_seconds"`
	APIDurationSeconds  int64   `json:"api_duration_seconds"`
	CostUSD             float64 `json:"cost_usd"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	PermissionDenials   int     `json:"permission_denials"`
	Subtype             string  `json:"subtype,omitempty"`
	IsError             bool    `json:"is_error"`
	SubagentsSpawned    int     `json:"subagents_spawned"`
	// SubagentsByType counts spawned sub-agents by agent type.
	SubagentsByType map[string]int `json:"subagents_by_type,omitempty"`
	// Models is the per-model breakdown, keyed by model id.
	Models map[string]modelUsageOut `json:"models,omitempty"`
	// ToolCalls is the top-level agent's tool calls, most-called first;
	// SubagentToolCalls is every sub-agent's together.
	ToolCalls         []toolUsageOut `json:"tool_calls"`
	SubagentToolCalls []toolUsageOut `json:"subagent_tool_calls"`
}

type modelUsageOut struct {
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CostUSD             float64 `json:"cost_usd"`
}

// toolUsageOut is one tool's tally: calls, and the bytes of text its
// results put back into the context.
type toolUsageOut struct {
	Name        string `json:"name"`
	Calls       int    `json:"calls"`
	ResultBytes int64  `json:"result_bytes"`
}

// registerRunTools wires the run inspection tools onto srv. taskID is
// the session's bound task, the default for list_workflow_runs.
func registerRunTools(srv *mcp.Server, store Store, taskID int64) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_workflow_runs",
		Description: "List a task's workflow runs, newest first: run id, workflow, state, cwd, " +
			"when it started and ended, and the step run it is currently at. Defaults to the " +
			"current task. Use get_workflow_run for a run's step runs.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		TaskID *int64 `json:"task_id,omitempty" jsonschema:"task whose runs to list; defaults to the current task"`
	}) (*mcp.CallToolResult, runsOut, error) {
		id := taskID
		if in.TaskID != nil {
			id = *in.TaskID
		}
		// Checked first so an unknown task reads as "not found" rather
		// than an empty list.
		if _, err := store.GetTask(ctx, id); err != nil {
			return nil, runsOut{}, err
		}
		runs, err := store.ListRunsForTask(ctx, id)
		if err != nil {
			return nil, runsOut{}, err
		}
		out := runsOut{Runs: make([]runOut, 0, len(runs))}
		for _, r := range runs {
			ro, err := toRunOut(ctx, store, r)
			if err != nil {
				return nil, runsOut{}, err
			}
			out.Runs = append(out.Runs, ro)
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_workflow_run",
		Description: "Get a workflow run in full: the run, its step runs in the order they " +
			"started (step, iteration, outcome, timing, the lengths of the prompt, input, " +
			"feedback and deliverable, the session id and log path), and per-step totals " +
			"(how many times each step ran, for how long). The texts themselves are left " +
			"out unless asked for: include_deliverables adds each step run's deliverable, " +
			"include_inputs its input and feedback. Use get_step_run_usage for what a step " +
			"run cost.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		RunID               int64 `json:"run_id" jsonschema:"the run id, from list_workflow_runs"`
		IncludeDeliverables bool  `json:"include_deliverables,omitempty" jsonschema:"include each step run's full deliverable text"`
		IncludeInputs       bool  `json:"include_inputs,omitempty" jsonschema:"include each step run's full input and feedback text"`
	}) (*mcp.CallToolResult, runDetailOut, error) {
		run, err := store.GetRun(ctx, in.RunID)
		if err != nil {
			return nil, runDetailOut{}, err
		}
		ro, err := toRunOut(ctx, store, run)
		if err != nil {
			return nil, runDetailOut{}, err
		}
		srs, err := store.ListStepRunsForRun(ctx, in.RunID)
		if err != nil {
			return nil, runDetailOut{}, err
		}
		out := runDetailOut{runOut: ro, StepRuns: make([]stepRunOut, 0, len(srs)), BySteps: []stepTotalOut{}}
		steps := map[int64]workflow.Step{}
		totals := map[int64]*stepTotalOut{}
		for _, sr := range srs {
			st, ok := steps[sr.StepID]
			if !ok {
				st, err = stepOrGhost(ctx, store, sr.StepID)
				if err != nil {
					return nil, runDetailOut{}, err
				}
				steps[sr.StepID] = st
			}
			row := toStepRunOut(sr, st)
			if in.IncludeDeliverables {
				row.Deliverable = sr.Deliverable
			}
			if in.IncludeInputs {
				row.Input, row.Feedback = sr.Input, sr.Feedback
			}
			out.StepRuns = append(out.StepRuns, row)

			tot, ok := totals[sr.StepID]
			if !ok {
				tot = &stepTotalOut{StepID: st.ID, Step: st.Name}
				totals[sr.StepID] = tot
				out.BySteps = append(out.BySteps, stepTotalOut{})
			}
			tot.Runs++
			tot.TotalSeconds += row.DurationSeconds
		}
		// by_step in first-run order, the order the run met the steps.
		i := 0
		for _, sr := range srs {
			tot := totals[sr.StepID]
			if tot == nil {
				continue
			}
			out.BySteps[i] = *tot
			totals[sr.StepID] = nil
			i++
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_step_run_usage",
		Description: "What a step run cost, tallied from its stream-json log: total cost, " +
			"tokens (input, output, cache creation, cache read), turns, wall and API " +
			"duration, permission denials, sub-agents spawned, how many result records the " +
			"log holds (one per process that ran the step: more than one means the step was " +
			"resumed -- a crash resume, a finish_step nudge or a retry), and tool calls by " +
			"tool name with the bytes each tool's results put back into the context, for the " +
			"top-level agent and for its sub-agents. Counters are summed across resumes; " +
			"cost and the per-model breakdown are the session totals from the last result. " +
			"A gate has no log.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		StepRunID int64 `json:"step_run_id" jsonschema:"the step run id, from get_workflow_run"`
	}) (*mcp.CallToolResult, usageOut, error) {
		sr, err := store.GetStepRun(ctx, in.StepRunID)
		if err != nil {
			return nil, usageOut{}, err
		}
		st, err := stepOrGhost(ctx, store, sr.StepID)
		if err != nil {
			return nil, usageOut{}, err
		}
		if sr.LogPath == "" {
			if st.Kind == workflow.StepGate {
				return nil, usageOut{}, fmt.Errorf("step run %d is the gate %q: a gate runs no claude session and has no log", sr.ID, st.Name)
			}
			return nil, usageOut{}, fmt.Errorf("step run %d (%s) has no log: the step never started a claude session", sr.ID, st.Name)
		}
		f, err := os.Open(sr.LogPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, usageOut{}, fmt.Errorf("step run %d (%s): log %s is gone", sr.ID, st.Name, sr.LogPath)
			}
			return nil, usageOut{}, fmt.Errorf("step run %d (%s): opening log: %w", sr.ID, st.Name, err)
		}
		defer f.Close()
		u, err := usage.ParseStepLog(f)
		if err != nil {
			return nil, usageOut{}, fmt.Errorf("step run %d (%s): %w", sr.ID, st.Name, err)
		}
		return nil, toUsageOut(sr, st, u), nil
	})
}

// toRunOut renders a run with its workflow's name and current step
// resolved.
func toRunOut(ctx context.Context, store Store, r workflow.Run) (runOut, error) {
	out := runOut{
		ID: r.ID, WorkflowID: r.WorkflowID, TaskID: r.TaskID,
		State: string(r.State), Cwd: r.Cwd, Error: r.Error,
		StartedAt:       fmtTime(r.StartedAt),
		EndedAt:         fmtTimePtr(r.EndedAt),
		DurationSeconds: durationSeconds(r.StartedAt, r.EndedAt),
	}
	wf, err := store.GetWorkflow(ctx, r.WorkflowID)
	switch {
	case err == nil:
		out.Workflow = wf.Name
	case errors.Is(err, workflow.ErrWorkflowNotFound):
		// A finished run cascades with its workflow, so this is a race
		// with a delete, not a state worth failing the listing over.
		out.Workflow = fmt.Sprintf("(deleted workflow %d)", r.WorkflowID)
	default:
		return runOut{}, err
	}
	if r.CurrentStepRunID != nil {
		sr, err := store.GetStepRun(ctx, *r.CurrentStepRunID)
		if err != nil {
			return runOut{}, err
		}
		st, err := stepOrGhost(ctx, store, sr.StepID)
		if err != nil {
			return runOut{}, err
		}
		out.CurrentStep = &currentStepOut{
			StepRunID: sr.ID, Step: st.Name, Iteration: sr.Iteration, Finished: sr.Finished(),
		}
	}
	return out, nil
}

// stepOrGhost loads a step run's step, standing in a placeholder for one
// that has since been deleted: the store refuses to delete a step a live
// run has executed, but a finished run's steps are fair game, and its
// history should still read.
func stepOrGhost(ctx context.Context, store Store, stepID int64) (workflow.Step, error) {
	st, err := store.GetStep(ctx, stepID)
	if errors.Is(err, workflow.ErrStepNotFound) {
		return workflow.Step{ID: stepID, Name: fmt.Sprintf("(deleted step %d)", stepID), Kind: workflow.StepAgent}, nil
	}
	if err != nil {
		return workflow.Step{}, err
	}
	return st, nil
}

func toStepRunOut(sr workflow.StepRun, st workflow.Step) stepRunOut {
	return stepRunOut{
		ID: sr.ID, StepID: sr.StepID, Step: st.Name, Kind: string(st.Kind),
		Iteration: sr.Iteration, Finished: sr.Finished(), Outcome: sr.Outcome,
		Model: sr.Model, PermissionMode: sr.PermissionMode,
		SessionExternalID: sr.SessionExternalID, LogPath: sr.LogPath,
		StartedAt:       fmtTime(sr.StartedAt),
		EndedAt:         fmtTimePtr(sr.EndedAt),
		DurationSeconds: durationSeconds(sr.StartedAt, sr.EndedAt),
		PromptLen:       len(sr.PromptRendered),
		InputLen:        len(sr.Input),
		FeedbackLen:     len(sr.Feedback),
		DeliverableLen:  len(sr.Deliverable),
	}
}

func toUsageOut(sr workflow.StepRun, st workflow.Step, u usage.StreamUsage) usageOut {
	out := usageOut{
		StepRunID: sr.ID, Step: st.Name, Iteration: sr.Iteration, LogPath: sr.LogPath,
		Results:             u.Results,
		Turns:               u.Turns,
		DurationSeconds:     u.DurationMS / 1000,
		APIDurationSeconds:  u.APIDurationMS / 1000,
		CostUSD:             u.CostUSD,
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		CacheCreationTokens: u.CacheCreationTokens,
		CacheReadTokens:     u.CacheReadTokens,
		PermissionDenials:   u.PermissionDenials,
		Subtype:             u.Subtype,
		IsError:             u.IsError,
		SubagentsSpawned:    u.SubagentsSpawned,
		SubagentsByType:     u.SubagentsByType,
		ToolCalls:           toToolUsageOuts(u.ToolCalls),
		SubagentToolCalls:   toToolUsageOuts(u.SubagentToolCalls),
	}
	if len(u.Models) > 0 {
		out.Models = make(map[string]modelUsageOut, len(u.Models))
		for name, m := range u.Models {
			out.Models[name] = modelUsageOut{
				InputTokens: m.InputTokens, OutputTokens: m.OutputTokens,
				CacheReadTokens: m.CacheReadTokens, CacheCreationTokens: m.CacheCreationTokens,
				CostUSD: m.CostUSD,
			}
		}
	}
	return out
}

func toToolUsageOuts(ts []usage.ToolUsage) []toolUsageOut {
	out := make([]toolUsageOut, len(ts))
	for i, t := range ts {
		out[i] = toolUsageOut{Name: t.Name, Calls: t.Calls, ResultBytes: t.ResultBytes}
	}
	return out
}

// fmtTime renders a timestamp for the wire, "" for the zero value.
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := fmtTime(*t)
	return &s
}

// durationSeconds is how long something has run: start to end, or to
// now while it is still going; 0 for a start never recorded.
func durationSeconds(start time.Time, end *time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	stop := time.Now()
	if end != nil {
		stop = *end
	}
	d := stop.Sub(start)
	if d < 0 {
		return 0
	}
	return int64(d / time.Second)
}
