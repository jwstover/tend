// Package workflow is the domain layer for agent workflows: reusable
// multi-step agent procedures a task can run (tend task #171). Types and
// rules only, zero I/O -- the same contract as internal/task, and a
// sibling of it rather than a tenant because a workflow has its own
// vocabulary (Step, Edge, Run) that would collide with or crowd the task
// package's.
//
// A Workflow is a set of Steps joined by Edges keyed on outcome. A Run
// binds a Workflow to a task; each StepRun is one execution of one Step
// within a Run, and for an agent step that is exactly one Claude Code
// session.
package workflow

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrEmptyName is returned when a workflow or step name is blank.
var ErrEmptyName = errors.New("name is empty")

// ErrEmptyOutcome is returned when an edge outcome or a step's finishing
// outcome normalizes to nothing.
var ErrEmptyOutcome = errors.New("outcome is empty")

// ErrWorkflowNotFound is returned when a workflow id or name does not
// resolve. Callers that take a name from the user match on this to report
// a typo rather than creating a workflow behind their back.
var ErrWorkflowNotFound = errors.New("workflow not found")

// ErrStepNotFound is returned when a step id does not resolve.
var ErrStepNotFound = errors.New("step not found")

// ErrRunNotFound is returned when a run id does not resolve.
var ErrRunNotFound = errors.New("run not found")

// ErrStepRunNotFound is returned when a step run id does not resolve.
var ErrStepRunNotFound = errors.New("step run not found")

// ErrInUse is returned when a workflow or step cannot be deleted because
// a run in a non-terminal state still references it. The wrapping error
// names the run so the message is actionable.
var ErrInUse = errors.New("referenced by an active run")

// ErrCrossWorkflowEdge is returned when an edge would join steps of two
// different workflows.
var ErrCrossWorkflowEdge = errors.New("edge joins steps of different workflows")

// ErrRunEnded is returned when a state change is attempted on a run that
// has already reached a terminal state. Terminal is final: a new attempt
// is a new run.
var ErrRunEnded = errors.New("run has already ended")

// ErrStepRunFinished is returned when a step run is finished a second
// time. finish_step is a one-shot handoff; the first outcome stands.
var ErrStepRunFinished = errors.New("step run already finished")

// OutcomeDone is the conventional "it went fine, move on" outcome: what a
// linear workflow's edges are keyed on, and the fallback a headless step
// reports when it exits without calling finish_step.
const OutcomeDone = "done"

// StepKind distinguishes a step the runner executes as a headless claude
// session from one where it stops and waits for a human.
type StepKind string

const (
	// StepAgent runs prompt_md as a headless Claude Code session.
	StepAgent StepKind = "agent"
	// StepGate pauses the run for a manual decision; no claude process
	// is launched. Its outcomes come from its edges like any other step.
	StepGate StepKind = "gate"
)

// Valid reports whether k is a known step kind.
func (k StepKind) Valid() bool {
	return k == StepAgent || k == StepGate
}

// RunState is where a run is in its life. Unlike task.SessionStatus this
// is a workflow the runner moves rows through, not a cache of something
// observed from outside, so the schema pins the set with a CHECK.
type RunState string

const (
	// RunPending is a run that has been created but whose runner has not
	// claimed it yet.
	RunPending RunState = "pending"
	// RunRunning means the runner owns the run and a step is executing.
	RunRunning RunState = "running"
	// RunWaitingReview means the run is sitting at a gate step.
	RunWaitingReview RunState = "waiting_review"
	// RunPaused means the user stopped the runner, typically to take over
	// the current step interactively.
	RunPaused RunState = "paused"
	// RunDone means the last step's outcome had no edge to follow.
	RunDone RunState = "done"
	// RunFailed means the runner gave up: a step could not launch, or an
	// edge's max_iterations was exceeded.
	RunFailed RunState = "failed"
	// RunCancelled means the user ended the run deliberately.
	RunCancelled RunState = "cancelled"
)

// Valid reports whether s is a known run state.
func (s RunState) Valid() bool {
	switch s {
	case RunPending, RunRunning, RunWaitingReview, RunPaused, RunDone, RunFailed, RunCancelled:
		return true
	}
	return false
}

// Terminal reports whether s ends a run. A run in a terminal state is
// history: its workflow and steps may be deleted, and its state can no
// longer change.
func (s RunState) Terminal() bool {
	return s == RunDone || s == RunFailed || s == RunCancelled
}

// Workflow is a named, reusable procedure: an ordered set of steps plus
// the edges joining them. Deliberately not bound to a project or task;
// binding happens per Run.
type Workflow struct {
	ID          int64
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// StepCount is the number of steps in the workflow. Zero unless the
	// value came from a listing that counts them.
	StepCount int64
}

// Step is one node of a workflow. PromptMD is a Go text/template rendered
// per run by RenderPrompt; Model and PermissionMode are
// forwarded to claude as-is, "" meaning "inherit the default". SortOrder
// is authoring order only: execution order is defined by the Edges.
type Step struct {
	ID             int64
	WorkflowID     int64
	Name           string
	Kind           StepKind
	PromptMD       string
	Model          string
	PermissionMode string
	SortOrder      int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Edge routes a step's outcome to the next step. The edges leaving a step
// define its allowed outcomes; an outcome with no edge ends the run.
// MaxIterations bounds how many times ToStep may run within one run when
// reached over this edge, nil meaning unbounded -- the guard on a
// review-reject loop.
type Edge struct {
	ID            int64
	FromStepID    int64
	Outcome       string
	ToStepID      int64
	MaxIterations *int64
}

// Run is one execution of a workflow against a task. CurrentStepRunID is
// the step run the runner is on (or paused at), nil before the first
// step starts. TmuxSession names the runner's tmux session, "" when none
// has been started. EndedAt is set exactly when State becomes terminal.
// Error is why the run failed -- the runner's message, kept on the row
// because the runner's own output dies with its tmux session -- and ""
// unless State is RunFailed.
type Run struct {
	ID               int64
	WorkflowID       int64
	TaskID           int64
	Cwd              string
	State            RunState
	CurrentStepRunID *int64
	TmuxSession      string
	Error            string
	StartedAt        time.Time
	EndedAt          *time.Time
}

// StepRun is one execution of one step within a run. The definition is
// read live, so PromptRendered, Model and PermissionMode record what
// actually ran. Input is the previous step's deliverable; Feedback is the
// hand-off of the step that routed here over a loop-back edge (a
// reject-style outcome), "" when the step was reached going forward.
// Outcome and Deliverable are what this step handed off, empty until
// Finished.
// SessionExternalID is the claude --session-id of the agent session that
// ran the step, "" for a gate. LogPath is the step's stream-json log
// file, "" if none was written.
type StepRun struct {
	ID                int64
	RunID             int64
	StepID            int64
	Iteration         int64
	SessionExternalID string
	PromptRendered    string
	Model             string
	PermissionMode    string
	Input             string
	Feedback          string
	Outcome           string
	Deliverable       string
	LogPath           string
	StartedAt         time.Time
	EndedAt           *time.Time
}

// Finished reports whether the step run has handed off an outcome.
func (r StepRun) Finished() bool { return r.EndedAt != nil }

// NormalizeName trims surrounding whitespace from a workflow or step name
// and rejects blanks. Case is preserved as typed; the schema's NOCASE
// collation is what makes "Ship" and "ship" the same workflow.
func NormalizeName(s string) (string, error) {
	n := strings.TrimSpace(s)
	if n == "" {
		return "", ErrEmptyName
	}
	return n, nil
}

// NormalizeOutcome canonicalizes an outcome name: trimmed and lower-cased,
// so the "Approve" an agent reports matches the "approve" an edge was
// authored with. Rejects anything that normalizes to nothing.
func NormalizeOutcome(s string) (string, error) {
	o := strings.ToLower(strings.TrimSpace(s))
	if o == "" {
		return "", ErrEmptyOutcome
	}
	return o, nil
}

// InUseError builds the error DeleteWorkflow/DeleteStep return when a
// live run stands in the way, naming the run so the user can go deal
// with it. Matches ErrInUse under errors.Is.
func InUseError(what string, runID int64) error {
	return fmt.Errorf("%s is %w %d", what, ErrInUse, runID)
}
