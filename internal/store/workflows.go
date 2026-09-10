package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jwstover/tend/internal/store/gen"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// ---- workflows -----------------------------------------------------------

// CreateWorkflow adds a workflow definition. Names are unique
// case-insensitively; a collision surfaces as the driver's constraint
// error, the same as CreateProject.
func (s *Store) CreateWorkflow(ctx context.Context, name, description string) (workflow.Workflow, error) {
	n, err := workflow.NormalizeName(name)
	if err != nil {
		return workflow.Workflow{}, err
	}
	row, err := s.q.CreateWorkflow(ctx, gen.CreateWorkflowParams{Name: n, Description: description})
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("creating workflow %q: %w", n, err)
	}
	return workflowToDomain(row, 0)
}

// GetWorkflow loads one workflow by id.
func (s *Store) GetWorkflow(ctx context.Context, id int64) (workflow.Workflow, error) {
	row, err := s.q.GetWorkflow(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Workflow{}, fmt.Errorf("workflow %d: %w", id, workflow.ErrWorkflowNotFound)
	}
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("loading workflow %d: %w", id, err)
	}
	return workflowToDomain(row, 0)
}

// WorkflowByName resolves a workflow by name, case-insensitively. Names
// resolve, never create: `tend workflow start <name>` matches on
// workflow.ErrWorkflowNotFound to report a typo.
func (s *Store) WorkflowByName(ctx context.Context, name string) (workflow.Workflow, error) {
	n, err := workflow.NormalizeName(name)
	if err != nil {
		return workflow.Workflow{}, err
	}
	row, err := s.q.GetWorkflowByName(ctx, n)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Workflow{}, fmt.Errorf("workflow %q: %w", n, workflow.ErrWorkflowNotFound)
	}
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("loading workflow %q: %w", n, err)
	}
	return workflowToDomain(row, 0)
}

// ListWorkflows returns every workflow, by name, with its step count.
func (s *Store) ListWorkflows(ctx context.Context) ([]workflow.Workflow, error) {
	rows, err := s.q.ListWorkflows(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing workflows: %w", err)
	}
	out := make([]workflow.Workflow, 0, len(rows))
	for _, r := range rows {
		w, err := workflowToDomain(gen.Workflow{
			ID: r.ID, Name: r.Name, Description: r.Description, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		}, r.StepCount)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// RenameWorkflow changes a workflow's name.
func (s *Store) RenameWorkflow(ctx context.Context, id int64, name string) error {
	n, err := workflow.NormalizeName(name)
	if err != nil {
		return err
	}
	if err := s.q.RenameWorkflow(ctx, gen.RenameWorkflowParams{Name: n, ID: id}); err != nil {
		return fmt.Errorf("renaming workflow %d: %w", id, err)
	}
	return nil
}

// SetWorkflowDescription replaces a workflow's description.
func (s *Store) SetWorkflowDescription(ctx context.Context, id int64, description string) error {
	if err := s.q.SetWorkflowDescription(ctx, gen.SetWorkflowDescriptionParams{Description: description, ID: id}); err != nil {
		return fmt.Errorf("setting workflow %d description: %w", id, err)
	}
	return nil
}

// DeleteWorkflow removes a workflow, its steps and edges, and the history
// of its finished runs. It is refused (workflow.ErrInUse, naming the run)
// while any run of it is in a non-terminal state: the runner reads the
// definition live at each step start, so pulling it out from under a live
// run would strand the run. The check and the delete share a transaction
// so a run created in between cannot slip through.
func (s *Store) DeleteWorkflow(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(q *gen.Queries) error {
		active, err := q.ListActiveRunIDsForWorkflow(ctx, id)
		if err != nil {
			return fmt.Errorf("checking runs of workflow %d: %w", id, err)
		}
		if len(active) > 0 {
			return workflow.InUseError(fmt.Sprintf("workflow %d", id), active[0])
		}
		if err := q.DeleteWorkflow(ctx, id); err != nil {
			return fmt.Errorf("deleting workflow %d: %w", id, err)
		}
		return nil
	})
}

// DuplicateWorkflow copies a workflow under a new name: steps with their
// prompts and settings, and edges re-pointed at the copied steps. The
// authoring TUI's "duplicate" is the intended caller; a run history is
// not copied because it belongs to the original.
func (s *Store) DuplicateWorkflow(ctx context.Context, id int64, newName string) (workflow.Workflow, error) {
	n, err := workflow.NormalizeName(newName)
	if err != nil {
		return workflow.Workflow{}, err
	}
	var out workflow.Workflow
	err = s.inTx(ctx, func(q *gen.Queries) error {
		src, err := q.GetWorkflow(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workflow %d: %w", id, workflow.ErrWorkflowNotFound)
		}
		if err != nil {
			return fmt.Errorf("loading workflow %d: %w", id, err)
		}
		dst, err := q.CreateWorkflow(ctx, gen.CreateWorkflowParams{Name: n, Description: src.Description})
		if err != nil {
			return fmt.Errorf("creating workflow %q: %w", n, err)
		}

		steps, err := q.ListSteps(ctx, id)
		if err != nil {
			return fmt.Errorf("listing steps of workflow %d: %w", id, err)
		}
		// Old step id -> new step id, for re-pointing the edges.
		idMap := make(map[int64]int64, len(steps))
		for _, st := range steps {
			copied, err := q.CreateStepFull(ctx, gen.CreateStepFullParams{
				WorkflowID: dst.ID, Name: st.Name, Kind: st.Kind, PromptMd: st.PromptMd,
				Model: st.Model, PermissionMode: st.PermissionMode, SortOrder: st.SortOrder,
			})
			if err != nil {
				return fmt.Errorf("copying step %d: %w", st.ID, err)
			}
			idMap[st.ID] = copied.ID
		}

		edges, err := q.ListEdgesForWorkflow(ctx, id)
		if err != nil {
			return fmt.Errorf("listing edges of workflow %d: %w", id, err)
		}
		for _, e := range edges {
			if _, err := q.UpsertEdge(ctx, gen.UpsertEdgeParams{
				FromStepID: idMap[e.FromStepID], Outcome: e.Outcome,
				ToStepID: idMap[e.ToStepID], MaxIterations: e.MaxIterations,
			}); err != nil {
				return fmt.Errorf("copying edge %d: %w", e.ID, err)
			}
		}

		out, err = workflowToDomain(dst, int64(len(steps)))
		return err
	})
	if err != nil {
		return workflow.Workflow{}, err
	}
	return out, nil
}

// ---- steps ---------------------------------------------------------------

// AddStep appends a step to a workflow. It sorts after every existing
// step; prompt, model and permission mode start empty and are set through
// the SetStep* setters (or UpdateStep, for the whole row at once).
func (s *Store) AddStep(ctx context.Context, workflowID int64, name string, kind workflow.StepKind) (workflow.Step, error) {
	n, err := workflow.NormalizeName(name)
	if err != nil {
		return workflow.Step{}, err
	}
	if !kind.Valid() {
		return workflow.Step{}, fmt.Errorf("unknown step kind %q", kind)
	}
	row, err := s.q.CreateStep(ctx, gen.CreateStepParams{WorkflowID: workflowID, Name: n, Kind: string(kind)})
	if err != nil {
		return workflow.Step{}, fmt.Errorf("adding step %q to workflow %d: %w", n, workflowID, err)
	}
	return stepToDomain(row)
}

// GetStep loads one step by id.
func (s *Store) GetStep(ctx context.Context, id int64) (workflow.Step, error) {
	row, err := s.q.GetStep(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Step{}, fmt.Errorf("step %d: %w", id, workflow.ErrStepNotFound)
	}
	if err != nil {
		return workflow.Step{}, fmt.Errorf("loading step %d: %w", id, err)
	}
	return stepToDomain(row)
}

// ListSteps returns a workflow's steps in authoring order.
func (s *Store) ListSteps(ctx context.Context, workflowID int64) ([]workflow.Step, error) {
	rows, err := s.q.ListSteps(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("listing steps of workflow %d: %w", workflowID, err)
	}
	out := make([]workflow.Step, 0, len(rows))
	for _, r := range rows {
		st, err := stepToDomain(r)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// UpdateStep writes a step's editable attributes -- name, kind, prompt,
// model, permission mode -- from st, keyed by st.ID. Workflow membership
// and sort order are not editable this way (see ReorderSteps).
func (s *Store) UpdateStep(ctx context.Context, st workflow.Step) error {
	n, err := workflow.NormalizeName(st.Name)
	if err != nil {
		return err
	}
	if !st.Kind.Valid() {
		return fmt.Errorf("unknown step kind %q", st.Kind)
	}
	if err := s.q.UpdateStep(ctx, gen.UpdateStepParams{
		Name: n, Kind: string(st.Kind), PromptMd: st.PromptMD,
		Model: st.Model, PermissionMode: st.PermissionMode, ID: st.ID,
	}); err != nil {
		return fmt.Errorf("updating step %d: %w", st.ID, err)
	}
	return nil
}

// SetStepPrompt replaces a step's prompt template -- the $EDITOR
// round-trip's write, shaped like SetBody so the same TUI idiom applies.
func (s *Store) SetStepPrompt(ctx context.Context, id int64, prompt string) error {
	if err := s.q.SetStepPrompt(ctx, gen.SetStepPromptParams{PromptMd: prompt, ID: id}); err != nil {
		return fmt.Errorf("setting step %d prompt: %w", id, err)
	}
	return nil
}

// SetStepKind, SetStepModel and SetStepPermissionMode each write one
// attribute and leave the rest of the row alone. The TUI edits one
// attribute per keypress, and writing only that column means a caller
// holding a stale copy of the step can never clobber a write that landed
// in between (UpdateStep would, since it rewrites every editable column).

// SetStepKind changes a step's kind.
func (s *Store) SetStepKind(ctx context.Context, id int64, kind workflow.StepKind) error {
	if !kind.Valid() {
		return fmt.Errorf("unknown step kind %q", kind)
	}
	if err := s.q.SetStepKind(ctx, gen.SetStepKindParams{Kind: string(kind), ID: id}); err != nil {
		return fmt.Errorf("setting step %d kind: %w", id, err)
	}
	return nil
}

// SetStepModel changes a step's model; "" means inherit.
func (s *Store) SetStepModel(ctx context.Context, id int64, model string) error {
	if err := s.q.SetStepModel(ctx, gen.SetStepModelParams{Model: model, ID: id}); err != nil {
		return fmt.Errorf("setting step %d model: %w", id, err)
	}
	return nil
}

// SetStepPermissionMode changes a step's permission mode; "" means inherit.
func (s *Store) SetStepPermissionMode(ctx context.Context, id int64, mode string) error {
	if err := s.q.SetStepPermissionMode(ctx, gen.SetStepPermissionModeParams{PermissionMode: mode, ID: id}); err != nil {
		return fmt.Errorf("setting step %d permission mode: %w", id, err)
	}
	return nil
}

// ReorderSteps rewrites a workflow's authoring order to match ids, which
// must be exactly the workflow's steps. A partial or foreign list is
// refused rather than partially applied.
func (s *Store) ReorderSteps(ctx context.Context, workflowID int64, ids []int64) error {
	return s.inTx(ctx, func(q *gen.Queries) error {
		current, err := q.ListSteps(ctx, workflowID)
		if err != nil {
			return fmt.Errorf("listing steps of workflow %d: %w", workflowID, err)
		}
		if len(current) != len(ids) {
			return fmt.Errorf("reordering workflow %d: got %d ids for %d steps", workflowID, len(ids), len(current))
		}
		known := make(map[int64]bool, len(current))
		for _, st := range current {
			known[st.ID] = true
		}
		for i, id := range ids {
			if !known[id] {
				return fmt.Errorf("reordering workflow %d: step %d is not in it", workflowID, id)
			}
			delete(known, id) // catches a duplicate id in the list
			if err := q.SetStepSortOrder(ctx, gen.SetStepSortOrderParams{
				SortOrder: int64(i), ID: id, WorkflowID: workflowID,
			}); err != nil {
				return fmt.Errorf("ordering step %d: %w", id, err)
			}
		}
		return nil
	})
}

// DeleteStep removes a step and every edge touching it. Refused
// (workflow.ErrInUse, naming the run) if the step has run within a run
// that is still in a non-terminal state -- the runner may loop back to
// it, and a step run pointing at a missing step would be nonsense. The
// check and the delete share a transaction.
func (s *Store) DeleteStep(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(q *gen.Queries) error {
		active, err := q.ListActiveRunIDsForStep(ctx, id)
		if err != nil {
			return fmt.Errorf("checking runs of step %d: %w", id, err)
		}
		if len(active) > 0 {
			return workflow.InUseError(fmt.Sprintf("step %d", id), active[0])
		}
		if err := q.DeleteStep(ctx, id); err != nil {
			return fmt.Errorf("deleting step %d: %w", id, err)
		}
		return nil
	})
}

// ---- edges ---------------------------------------------------------------

// SetEdge routes outcome from one step to another, creating the edge or
// re-pointing the existing one for that outcome. The outcome is
// normalized (trimmed, lower-cased); both steps must belong to the same
// workflow. maxIterations nil means unbounded.
func (s *Store) SetEdge(ctx context.Context, fromStepID int64, outcome string, toStepID int64, maxIterations *int64) (workflow.Edge, error) {
	o, err := workflow.NormalizeOutcome(outcome)
	if err != nil {
		return workflow.Edge{}, err
	}
	if maxIterations != nil && *maxIterations < 1 {
		return workflow.Edge{}, fmt.Errorf("max iterations %d must be at least 1", *maxIterations)
	}
	from, err := s.GetStep(ctx, fromStepID)
	if err != nil {
		return workflow.Edge{}, err
	}
	to, err := s.GetStep(ctx, toStepID)
	if err != nil {
		return workflow.Edge{}, err
	}
	if from.WorkflowID != to.WorkflowID {
		return workflow.Edge{}, fmt.Errorf("edge %d -> %d: %w", fromStepID, toStepID, workflow.ErrCrossWorkflowEdge)
	}
	row, err := s.q.UpsertEdge(ctx, gen.UpsertEdgeParams{
		FromStepID: fromStepID, Outcome: o, ToStepID: toStepID, MaxIterations: toNullInt64(maxIterations),
	})
	if err != nil {
		return workflow.Edge{}, fmt.Errorf("setting edge %d -%s-> %d: %w", fromStepID, o, toStepID, err)
	}
	return edgeToDomain(row), nil
}

// ListEdges returns every edge of a workflow, grouped by source step in
// authoring order -- the graph preview's and the validator's input.
func (s *Store) ListEdges(ctx context.Context, workflowID int64) ([]workflow.Edge, error) {
	rows, err := s.q.ListEdgesForWorkflow(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("listing edges of workflow %d: %w", workflowID, err)
	}
	out := make([]workflow.Edge, 0, len(rows))
	for _, r := range rows {
		out = append(out, edgeToDomain(gen.WorkflowEdge{
			ID: r.ID, FromStepID: r.FromStepID, Outcome: r.Outcome, ToStepID: r.ToStepID, MaxIterations: r.MaxIterations,
		}))
	}
	return out, nil
}

// OutgoingEdges returns the edges leaving one step, by outcome -- which
// is to say the step's allowed outcomes and where each one leads. Empty
// means any outcome ends the run.
func (s *Store) OutgoingEdges(ctx context.Context, stepID int64) ([]workflow.Edge, error) {
	rows, err := s.q.ListEdgesFromStep(ctx, stepID)
	if err != nil {
		return nil, fmt.Errorf("listing edges from step %d: %w", stepID, err)
	}
	out := make([]workflow.Edge, 0, len(rows))
	for _, r := range rows {
		out = append(out, edgeToDomain(r))
	}
	return out, nil
}

// DeleteEdge removes one edge.
func (s *Store) DeleteEdge(ctx context.Context, id int64) error {
	if err := s.q.DeleteEdge(ctx, id); err != nil {
		return fmt.Errorf("deleting edge %d: %w", id, err)
	}
	return nil
}

// ---- runs ----------------------------------------------------------------

// CreateRun binds a workflow to a task in the pending state, ready for a
// runner to claim (ClaimRun) or for the interactive POC to start
// (SetRunState).
func (s *Store) CreateRun(ctx context.Context, workflowID, taskID int64, cwd string) (workflow.Run, error) {
	row, err := s.q.CreateRun(ctx, gen.CreateRunParams{WorkflowID: workflowID, TaskID: taskID, Cwd: cwd})
	if err != nil {
		return workflow.Run{}, fmt.Errorf("creating run of workflow %d for task %d: %w", workflowID, taskID, err)
	}
	return runToDomain(row)
}

// GetRun loads one run by id.
func (s *Store) GetRun(ctx context.Context, id int64) (workflow.Run, error) {
	row, err := s.q.GetRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Run{}, fmt.Errorf("run %d: %w", id, workflow.ErrRunNotFound)
	}
	if err != nil {
		return workflow.Run{}, fmt.Errorf("loading run %d: %w", id, err)
	}
	return runToDomain(row)
}

// ListRunsForTask returns a task's runs, newest first -- the detail
// pane's WORKFLOWS section.
func (s *Store) ListRunsForTask(ctx context.Context, taskID int64) ([]workflow.Run, error) {
	rows, err := s.q.ListRunsForTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing runs for task %d: %w", taskID, err)
	}
	return runsToDomain(rows)
}

// ListActiveRuns returns every run not yet in a terminal state, newest
// first -- what `tend workflow status` and the TUI's poller look at.
func (s *Store) ListActiveRuns(ctx context.Context) ([]workflow.Run, error) {
	rows, err := s.q.ListActiveRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing active runs: %w", err)
	}
	return runsToDomain(rows)
}

// SetRunState moves a run to a new state. Entering a terminal state
// stamps ended_at. A run already in a terminal state cannot move again
// (workflow.ErrRunEnded): terminal is final, and a new attempt is a new
// run.
func (s *Store) SetRunState(ctx context.Context, id int64, st workflow.RunState) error {
	if !st.Valid() {
		return fmt.Errorf("unknown run state %q", st)
	}
	var ended sql.NullString
	if st.Terminal() {
		ended = sql.NullString{String: time.Now().UTC().Format(sqliteTimeLayout), Valid: true}
	}
	n, err := s.q.SetRunState(ctx, gen.SetRunStateParams{State: string(st), EndedAt: ended, ID: id})
	if err != nil {
		return fmt.Errorf("setting run %d state to %s: %w", id, st, err)
	}
	if n == 0 {
		// Either the run is gone or it has ended; tell them apart.
		if _, err := s.GetRun(ctx, id); err != nil {
			return err
		}
		return fmt.Errorf("run %d: %w", id, workflow.ErrRunEnded)
	}
	return nil
}

// ClaimRun atomically takes a pending or paused run to running, reporting
// whether this caller is the one that got it. Two runners started against
// the same run see exactly one true -- the guard against a run being
// driven twice, in the ClaimSessionRecap mould. false is not an error:
// the run is already running, or has ended.
func (s *Store) ClaimRun(ctx context.Context, id int64) (bool, error) {
	n, err := s.q.ClaimRun(ctx, id)
	if err != nil {
		return false, fmt.Errorf("claiming run %d: %w", id, err)
	}
	return n > 0, nil
}

// SetRunTmuxSession records the name of the tmux session hosting the
// run's runner, "" when it has none.
func (s *Store) SetRunTmuxSession(ctx context.Context, id int64, name string) error {
	if err := s.q.SetRunTmuxSession(ctx, gen.SetRunTmuxSessionParams{TmuxSession: name, ID: id}); err != nil {
		return fmt.Errorf("setting run %d tmux session: %w", id, err)
	}
	return nil
}

// ---- step runs -----------------------------------------------------------

// CreateStepRun records the start of one step within a run and points the
// run at it as its current step, both in one transaction. The iteration
// is derived (one more than the times this step has already run within
// the run), so sr.Iteration is ignored; sr.RunID, StepID,
// SessionExternalID, PromptRendered, Model, PermissionMode and Input are
// taken as given. Outcome, Deliverable, LogPath and EndedAt start empty.
func (s *Store) CreateStepRun(ctx context.Context, sr workflow.StepRun) (workflow.StepRun, error) {
	var out workflow.StepRun
	err := s.inTx(ctx, func(q *gen.Queries) error {
		row, err := q.CreateStepRun(ctx, gen.CreateStepRunParams{
			RunID: sr.RunID, StepID: sr.StepID, SessionExternalID: sr.SessionExternalID,
			PromptRendered: sr.PromptRendered, Model: sr.Model, PermissionMode: sr.PermissionMode, Input: sr.Input,
		})
		if err != nil {
			return fmt.Errorf("creating step run of step %d in run %d: %w", sr.StepID, sr.RunID, err)
		}
		if err := q.SetRunCurrentStepRun(ctx, gen.SetRunCurrentStepRunParams{
			CurrentStepRunID: sql.NullInt64{Int64: row.ID, Valid: true}, ID: sr.RunID,
		}); err != nil {
			return fmt.Errorf("pointing run %d at step run %d: %w", sr.RunID, row.ID, err)
		}
		out, err = stepRunToDomain(row)
		return err
	})
	if err != nil {
		return workflow.StepRun{}, err
	}
	return out, nil
}

// GetStepRun loads one step run by id.
func (s *Store) GetStepRun(ctx context.Context, id int64) (workflow.StepRun, error) {
	row, err := s.q.GetStepRun(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.StepRun{}, fmt.Errorf("step run %d: %w", id, workflow.ErrStepRunNotFound)
	}
	if err != nil {
		return workflow.StepRun{}, fmt.Errorf("loading step run %d: %w", id, err)
	}
	return stepRunToDomain(row)
}

// ListStepRunsForRun returns a run's step runs in the order they started.
func (s *Store) ListStepRunsForRun(ctx context.Context, runID int64) ([]workflow.StepRun, error) {
	rows, err := s.q.ListStepRunsForRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("listing step runs of run %d: %w", runID, err)
	}
	out := make([]workflow.StepRun, 0, len(rows))
	for _, r := range rows {
		sr, err := stepRunToDomain(r)
		if err != nil {
			return nil, err
		}
		out = append(out, sr)
	}
	return out, nil
}

// FinishStepRun records a step's handoff: its outcome (normalized) and
// deliverable, and stamps ended_at. It is one-shot -- a second call
// returns workflow.ErrStepRunFinished and the first outcome stands, so a
// finish_step from the agent and the runner's exit-time fallback cannot
// overwrite each other.
func (s *Store) FinishStepRun(ctx context.Context, id int64, outcome, deliverable string) error {
	o, err := workflow.NormalizeOutcome(outcome)
	if err != nil {
		return err
	}
	n, err := s.q.FinishStepRun(ctx, gen.FinishStepRunParams{Outcome: o, Deliverable: deliverable, ID: id})
	if err != nil {
		return fmt.Errorf("finishing step run %d: %w", id, err)
	}
	if n == 0 {
		if _, err := s.GetStepRun(ctx, id); err != nil {
			return err
		}
		return fmt.Errorf("step run %d: %w", id, workflow.ErrStepRunFinished)
	}
	return nil
}

// FinishRunAtStep finishes a step run (FinishStepRun) and, in the same
// transaction, marks its run done -- the interactive POC's handoff, where
// there is no runner to route the outcome onward and a one-step run ends
// with its step. Unlike FinishStepRun it is idempotent: a step run that
// has already finished is left alone and nil is returned, because every
// path that observes a workflow session ending (the terminal handoff
// returning, a re-attach exiting, the recap drain finding it dead) calls
// this, and only the first of them should decide anything. A run that
// has already ended is likewise left as it is.
func (s *Store) FinishRunAtStep(ctx context.Context, stepRunID int64, outcome, deliverable string) error {
	o, err := workflow.NormalizeOutcome(outcome)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *gen.Queries) error {
		sr, err := q.GetStepRun(ctx, stepRunID)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("step run %d: %w", stepRunID, workflow.ErrStepRunNotFound)
		}
		if err != nil {
			return fmt.Errorf("loading step run %d: %w", stepRunID, err)
		}
		n, err := q.FinishStepRun(ctx, gen.FinishStepRunParams{Outcome: o, Deliverable: deliverable, ID: stepRunID})
		if err != nil {
			return fmt.Errorf("finishing step run %d: %w", stepRunID, err)
		}
		if n == 0 {
			return nil // already finished; the first outcome stands
		}
		ended := sql.NullString{String: time.Now().UTC().Format(sqliteTimeLayout), Valid: true}
		if _, err := q.SetRunState(ctx, gen.SetRunStateParams{
			State: string(workflow.RunDone), EndedAt: ended, ID: sr.RunID,
		}); err != nil {
			return fmt.Errorf("ending run %d: %w", sr.RunID, err)
		}
		return nil
	})
}

// SetStepRunLogPath records where a step's stream-json log was written.
// Separate from CreateStepRun because the path is derived from the step
// run's own id.
func (s *Store) SetStepRunLogPath(ctx context.Context, id int64, path string) error {
	if err := s.q.SetStepRunLogPath(ctx, gen.SetStepRunLogPathParams{LogPath: path, ID: id}); err != nil {
		return fmt.Errorf("setting step run %d log path: %w", id, err)
	}
	return nil
}

// SetStepRunSession records the claude session id that ran (or is
// rerunning) a step, for a step run created before its session id was
// known.
func (s *Store) SetStepRunSession(ctx context.Context, id int64, externalID string) error {
	if err := s.q.SetStepRunSession(ctx, gen.SetStepRunSessionParams{SessionExternalID: externalID, ID: id}); err != nil {
		return fmt.Errorf("setting step run %d session: %w", id, err)
	}
	return nil
}

// CreateStepRunSession is CreateSession for a session launched by a
// workflow step: identical — written at launch, ahead of the handoff, with
// status 'starting' — plus the back-pointer that lets the SESSIONS section
// say which step the session belonged to.
func (s *Store) CreateStepRunSession(ctx context.Context, stepRunID, taskID int64, externalID, cwd, label, tmuxSession string) (task.Session, error) {
	row, err := s.q.CreateSession(ctx, gen.CreateSessionParams{
		TaskID: taskID, ExternalID: externalID, Cwd: cwd, Label: label, TmuxSession: tmuxSession,
		WorkflowStepRunID: sql.NullInt64{Int64: stepRunID, Valid: true},
	})
	if err != nil {
		return task.Session{}, fmt.Errorf("inserting session for step run %d: %w", stepRunID, err)
	}
	return sessionToDomain(row)
}

// ---- row conversion ------------------------------------------------------

func workflowToDomain(row gen.Workflow, stepCount int64) (workflow.Workflow, error) {
	created, err := parseTime(row.CreatedAt)
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("workflow %d created_at: %w", row.ID, err)
	}
	updated, err := parseTime(row.UpdatedAt)
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("workflow %d updated_at: %w", row.ID, err)
	}
	return workflow.Workflow{
		ID: row.ID, Name: row.Name, Description: row.Description,
		CreatedAt: created, UpdatedAt: updated, StepCount: stepCount,
	}, nil
}

func stepToDomain(row gen.WorkflowStep) (workflow.Step, error) {
	created, err := parseTime(row.CreatedAt)
	if err != nil {
		return workflow.Step{}, fmt.Errorf("step %d created_at: %w", row.ID, err)
	}
	updated, err := parseTime(row.UpdatedAt)
	if err != nil {
		return workflow.Step{}, fmt.Errorf("step %d updated_at: %w", row.ID, err)
	}
	return workflow.Step{
		ID: row.ID, WorkflowID: row.WorkflowID, Name: row.Name, Kind: workflow.StepKind(row.Kind),
		PromptMD: row.PromptMd, Model: row.Model, PermissionMode: row.PermissionMode,
		SortOrder: row.SortOrder, CreatedAt: created, UpdatedAt: updated,
	}, nil
}

func edgeToDomain(row gen.WorkflowEdge) workflow.Edge {
	return workflow.Edge{
		ID: row.ID, FromStepID: row.FromStepID, Outcome: row.Outcome,
		ToStepID: row.ToStepID, MaxIterations: nullInt64(row.MaxIterations),
	}
}

func runsToDomain(rows []gen.WorkflowRun) ([]workflow.Run, error) {
	out := make([]workflow.Run, 0, len(rows))
	for _, r := range rows {
		run, err := runToDomain(r)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

func runToDomain(row gen.WorkflowRun) (workflow.Run, error) {
	started, err := parseTime(row.StartedAt)
	if err != nil {
		return workflow.Run{}, fmt.Errorf("run %d started_at: %w", row.ID, err)
	}
	ended, err := parseNullTime(row.EndedAt)
	if err != nil {
		return workflow.Run{}, fmt.Errorf("run %d ended_at: %w", row.ID, err)
	}
	return workflow.Run{
		ID: row.ID, WorkflowID: row.WorkflowID, TaskID: row.TaskID, Cwd: row.Cwd,
		State: workflow.RunState(row.State), CurrentStepRunID: nullInt64(row.CurrentStepRunID),
		TmuxSession: row.TmuxSession, StartedAt: started, EndedAt: ended,
	}, nil
}

func stepRunToDomain(row gen.WorkflowStepRun) (workflow.StepRun, error) {
	started, err := parseTime(row.StartedAt)
	if err != nil {
		return workflow.StepRun{}, fmt.Errorf("step run %d started_at: %w", row.ID, err)
	}
	ended, err := parseNullTime(row.EndedAt)
	if err != nil {
		return workflow.StepRun{}, fmt.Errorf("step run %d ended_at: %w", row.ID, err)
	}
	return workflow.StepRun{
		ID: row.ID, RunID: row.RunID, StepID: row.StepID, Iteration: row.Iteration,
		SessionExternalID: row.SessionExternalID, PromptRendered: row.PromptRendered,
		Model: row.Model, PermissionMode: row.PermissionMode, Input: row.Input,
		Outcome: row.Outcome, Deliverable: row.Deliverable, LogPath: row.LogPath,
		StartedAt: started, EndedAt: ended,
	}, nil
}

// parseNullTime converts a nullable sqliteTimeLayout column: nil for NULL.
func parseNullTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid {
		return nil, nil
	}
	t, err := parseTime(v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
