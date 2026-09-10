// Package runner drives one workflow run from step to step: it is the
// process behind the hidden `tend workflow run <run-id>`, hosted in its
// own tmux session (agent.RunnerSessionName) and gone when the run
// reaches a terminal state. A process per run, never a daemon.
//
// The loop, from tend task #179:
//
//  1. Load the run and claim it (Store.ClaimRun). If current_step_run_id
//     points at an unfinished step run, that step is picked back up
//     (crash recovery) before anything else. An unfinished step run with
//     no session id is one nobody owns -- RestartStep cleared it after a
//     takeover -- and is started over under a fresh session instead.
//  2. Otherwise pick the next step from the previous step run's outcome
//     via the workflow's edges, read live -- there is no snapshot. No
//     edge for the outcome means the run is done.
//  3. An edge's max_iterations bounds how often its target may run;
//     exceeding it fails the run with a message saying so.
//  4. Agent steps: create the step run, render the prompt with the task,
//     cwd, Input, Feedback, Iteration and allowed Outcomes, create the
//     session row, and exec claude headlessly (agent.HeadlessCmd via the
//     Exec seam) with a runner-built system prompt block
//     (workflow.StepSystemPrompt) stating the finish_step contract. On
//     exit the outcome and deliverable written by finish_step are read
//     back. A step that never called it is taken as "done" with the
//     stream's final text as its deliverable only when done is its sole
//     outcome; otherwise its session is nudged once to hand off
//     (workflow.NudgePrompt), and if it still does not, the run fails
//     naming the step and the outcomes it routes.
//  5. Gate steps: create the step run, set the run waiting_review, and
//     poll the step run until the TUI or CLI records a decision.
//  6. paused and cancelled, written by the TUI or CLI, are honoured by
//     polling the run's state between steps and while a step runs.
//
// Everything that shells out goes through Exec, so the transition logic
// is tested against a fake without claude on the machine.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

// Store is the slice of the persistence layer the runner needs -- the
// same "accept interfaces, return structs" convention as cli.Store and
// mcpserver.Store, satisfied by *store.Store.
type Store interface {
	GetRun(ctx context.Context, id int64) (workflow.Run, error)
	ClaimRun(ctx context.Context, id int64) (bool, error)
	SetRunState(ctx context.Context, id int64, st workflow.RunState) error
	FailRun(ctx context.Context, id int64, reason string) error
	SetRunTmuxSession(ctx context.Context, id int64, name string) error

	GetWorkflow(ctx context.Context, id int64) (workflow.Workflow, error)
	ListSteps(ctx context.Context, workflowID int64) ([]workflow.Step, error)
	GetStep(ctx context.Context, id int64) (workflow.Step, error)
	OutgoingEdges(ctx context.Context, stepID int64) ([]workflow.Edge, error)
	GetTask(ctx context.Context, id int64) (task.Task, error)

	CreateStepRun(ctx context.Context, sr workflow.StepRun) (workflow.StepRun, error)
	GetStepRun(ctx context.Context, id int64) (workflow.StepRun, error)
	ListStepRunsForRun(ctx context.Context, runID int64) ([]workflow.StepRun, error)
	FinishStepRun(ctx context.Context, id int64, outcome, deliverable string) error
	SetStepRunLogPath(ctx context.Context, id int64, path string) error
	SetStepRunSession(ctx context.Context, id int64, externalID string) error

	CreateStepRunSession(ctx context.Context, stepRunID, taskID int64, externalID, cwd, label, tmuxSession string) (task.Session, error)
	SetSessionStatus(ctx context.Context, externalID string, status task.SessionStatus) error
	Close() error
}

// StepExec is one attempt at an agent step: everything Exec needs to
// build and run the claude process. Resume means the step run's session
// already exists and Prompt is the next turn of it (agent.HeadlessResumeCmd)
// rather than the first (agent.HeadlessCmd).
type StepExec struct {
	Run     workflow.Run
	StepRun workflow.StepRun
	TaskID  int64
	Prompt  string
	Resume  bool
}

// Exec is the seam between the transition logic and the claude process.
// Check reports whether steps can run at all (claude on $PATH); Run
// executes one attempt with stdout tee'd to req.StepRun.LogPath and
// returns the parsed result, the way agent.RunHeadless does. Run must
// honour ctx: cancelling it is how a pause or cancel stops a step.
type Exec interface {
	Check() error
	Run(ctx context.Context, req StepExec) (agent.HeadlessResult, error)
}

// Runner drives runs. Poll is how often it re-reads the run's state
// while a step is executing or a gate is waiting; Log receives a line per
// transition (the tmux pane and runner.log in production, a buffer in
// tests). Both are optional: the zero value polls every two seconds and
// logs nowhere.
type Runner struct {
	Store Store
	Exec  Exec
	Poll  time.Duration
	Log   io.Writer
}

// DefaultPoll is how often the runner checks for a pause, cancel or gate
// decision when Runner.Poll is zero. A human writes those, so a couple of
// seconds is instant enough and keeps a waiting gate cheap.
const DefaultPoll = 2 * time.Second

// ResumePrompt is the turn sent to a step's existing session when the
// runner picks the step back up after dying mid-step. Deliberately short:
// the session already holds the real prompt and everything the agent did
// with it.
const ResumePrompt = "The previous attempt at this step was interrupted before it finished. " +
	"Review where you left off and complete the step as originally instructed."

// ErrAlreadyRunning is returned by Run when the run is already claimed
// and takeover was not requested: a second runner must not drive a run.
var ErrAlreadyRunning = errors.New("run is already being driven; use `tend workflow resume` if its runner died")

// ErrRunFailed is what Run returns after failing a run, so the process
// exits non-zero; the reason is on the run (workflow.Run.Error) and in
// the wrapping message.
var ErrRunFailed = errors.New("run failed")

// errInterrupted is the internal signal that a step stopped because the
// run was paused or cancelled from outside; Run then exits cleanly.
var errInterrupted = errors.New("run interrupted")

// Run drives runID until it ends, is paused or cancelled, or ctx is done.
// With takeover false only a pending or paused run can be entered
// (Store.ClaimRun, a compare-and-swap, so two runners racing for one run
// see exactly one winner). With takeover true a run left running or
// waiting_review by a runner that died is entered as it stands; the
// caller (`tend workflow resume`) is responsible for having checked that
// the old runner really is gone.
//
// A run that ends in failure returns an error wrapping ErrRunFailed after
// the reason has been recorded on the row. ctx being cancelled -- the
// runner itself being killed -- returns ctx.Err() with the run left
// exactly as it was, so a resume picks it up.
func (r *Runner) Run(ctx context.Context, runID int64, takeover bool) error {
	run, err := r.claim(ctx, runID, takeover)
	if err != nil {
		return err
	}
	wf, err := r.Store.GetWorkflow(ctx, run.WorkflowID)
	if err != nil {
		return r.fail(ctx, run, err.Error())
	}
	tk, err := r.Store.GetTask(ctx, run.TaskID)
	if err != nil {
		return r.fail(ctx, run, err.Error())
	}
	if err := r.Exec.Check(); err != nil {
		return r.fail(ctx, run, err.Error())
	}
	r.logf("run %d: %s on #%d (%s) in %s", run.ID, wf.Name, tk.ID, tk.Title, run.Cwd)

	// A step in flight from a previous runner is finished first.
	var last *workflow.StepRun
	if run.CurrentStepRunID != nil {
		sr, err := r.Store.GetStepRun(ctx, *run.CurrentStepRunID)
		if err != nil {
			return r.fail(ctx, run, err.Error())
		}
		if !sr.Finished() {
			sr, err = r.resumeStep(ctx, run, tk, sr)
			if err != nil {
				return r.stopped(ctx, run, err)
			}
		}
		last = &sr
	}

	for {
		if err := r.checkRun(ctx, run.ID); err != nil {
			return r.stopped(ctx, run, err)
		}
		next, input, feedback, err := r.nextStep(ctx, run, wf, last)
		if err != nil {
			return r.stopped(ctx, run, err)
		}
		if next == nil {
			if err := r.Store.SetRunState(ctx, run.ID, workflow.RunDone); err != nil {
				return err
			}
			r.logf("run %d: done", run.ID)
			return nil
		}
		sr, err := r.startStep(ctx, run, wf, tk, *next, input, feedback)
		if err != nil {
			return r.stopped(ctx, run, err)
		}
		last = &sr
	}
}

// claim takes the run for this runner, per Run's contract.
func (r *Runner) claim(ctx context.Context, runID int64, takeover bool) (workflow.Run, error) {
	ok, err := r.Store.ClaimRun(ctx, runID)
	if err != nil {
		return workflow.Run{}, err
	}
	run, err := r.Store.GetRun(ctx, runID)
	if err != nil {
		return workflow.Run{}, err
	}
	if ok {
		return run, nil
	}
	if run.State.Terminal() {
		return workflow.Run{}, fmt.Errorf("run %d: %w", runID, workflow.ErrRunEnded)
	}
	if !takeover {
		return workflow.Run{}, fmt.Errorf("run %d: %w", runID, ErrAlreadyRunning)
	}
	r.logf("run %d: taking over a run left %s", run.ID, run.State)
	return run, nil
}

// nextStep resolves what runs after last: the workflow's first step in
// authoring order when nothing has run yet, else the target of the edge
// keyed on last's outcome. nil with no error means the run is done.
//
// What last hands forward is its deliverable, or its own input when the
// deliverable is empty -- a gate that approves passes along what it was
// reviewing. A forward edge (to a step later in authoring order) makes
// that the target's Input, whether or not the target has run before: a
// gate re-entered after a reject cycle reviews the new deliverable, not
// the one it already rejected. A loop-back edge (to an earlier step, or
// the step itself) is the reject-style case: the target keeps the Input
// it had and gets the hand-off as Feedback instead. Either way
// max_iterations bounds how often the target runs.
func (r *Runner) nextStep(ctx context.Context, run workflow.Run, wf workflow.Workflow, last *workflow.StepRun) (*workflow.Step, string, string, error) {
	if last == nil {
		steps, err := r.Store.ListSteps(ctx, wf.ID)
		if err != nil {
			return nil, "", "", err
		}
		if len(steps) == 0 {
			return nil, "", "", fmt.Errorf("workflow %q has no steps", wf.Name)
		}
		return &steps[0], "", "", nil
	}

	edges, err := r.Store.OutgoingEdges(ctx, last.StepID)
	if err != nil {
		return nil, "", "", err
	}
	var edge *workflow.Edge
	for i := range edges {
		if edges[i].Outcome == last.Outcome {
			edge = &edges[i]
			break
		}
	}
	if edge == nil {
		if len(edges) > 0 {
			r.logf("run %d: outcome %q has no edge (the step routes %s); ending the run",
				run.ID, last.Outcome, strings.Join(outcomesOf(edges), ", "))
		}
		return nil, "", "", nil
	}
	next, err := r.Store.GetStep(ctx, edge.ToStepID)
	if err != nil {
		return nil, "", "", err
	}

	prior, err := r.priorRuns(ctx, run.ID, next.ID)
	if err != nil {
		return nil, "", "", err
	}
	if edge.MaxIterations != nil && int64(len(prior))+1 > *edge.MaxIterations {
		return nil, "", "", fmt.Errorf("step %q would run for the %s time via %q; the edge allows %d",
			next.Name, ordinal(len(prior)+1), last.Outcome, *edge.MaxIterations)
	}

	from, err := r.Store.GetStep(ctx, last.StepID)
	if err != nil {
		return nil, "", "", err
	}
	carry := last.Deliverable
	if carry == "" {
		carry = last.Input
	}
	if len(prior) > 0 && next.SortOrder <= from.SortOrder {
		return &next, prior[len(prior)-1].Input, carry, nil
	}
	return &next, carry, "", nil
}

// startStep creates and runs one step run of step, returning it finished.
func (r *Runner) startStep(ctx context.Context, run workflow.Run, wf workflow.Workflow, tk task.Task, step workflow.Step, input, feedback string) (workflow.StepRun, error) {
	prior, err := r.priorRuns(ctx, run.ID, step.ID)
	if err != nil {
		return workflow.StepRun{}, err
	}
	edges, err := r.Store.OutgoingEdges(ctx, step.ID)
	if err != nil {
		return workflow.StepRun{}, err
	}
	data := workflow.PromptData{
		Task:      workflow.PromptTask{ID: tk.ID, Title: tk.Title, Body: tk.BodyMD},
		Cwd:       run.Cwd,
		Input:     input,
		Feedback:  feedback,
		Iteration: int64(len(prior)) + 1,
		Outcomes:  outcomesOf(edges),
	}
	// A gate's prompt is optional reviewer text; an agent's is the whole
	// step, so it renders even when empty (an empty prompt is claude's
	// problem to report, not a reason to skip the step silently).
	var prompt string
	if step.Kind != workflow.StepGate || step.PromptMD != "" {
		prompt, err = workflow.RenderPrompt(step.PromptMD, data)
		if err != nil {
			return workflow.StepRun{}, fmt.Errorf("step %q prompt: %w", step.Name, err)
		}
	}

	sr := workflow.StepRun{
		RunID: run.ID, StepID: step.ID, PromptRendered: prompt,
		Model: step.Model, PermissionMode: step.PermissionMode, Input: input, Feedback: feedback,
	}
	if step.Kind == workflow.StepGate {
		sr, err = r.Store.CreateStepRun(ctx, sr)
		if err != nil {
			return workflow.StepRun{}, err
		}
		return r.waitGate(ctx, run, step, sr)
	}

	// The hand-off contract goes in the system prompt, not prompt_md, so
	// no author has to remember it; recorded on the row like the prompt.
	sr.SystemPrompt = workflow.StepSystemPrompt(workflow.HandoffContext{
		Workflow: wf.Name, Step: step.Name, Iteration: data.Iteration, Outcomes: data.Outcomes,
	})
	sr.SessionExternalID, err = agent.NewSessionID()
	if err != nil {
		return workflow.StepRun{}, err
	}
	sr, err = r.Store.CreateStepRun(ctx, sr)
	if err != nil {
		return workflow.StepRun{}, err
	}
	sr.LogPath, err = agent.StepLogPath(run.ID, sr.ID)
	if err != nil {
		return workflow.StepRun{}, err
	}
	// Stored before the step starts so a log viewer can find the file
	// while the step is still writing it.
	if err := r.Store.SetStepRunLogPath(ctx, sr.ID, sr.LogPath); err != nil {
		return workflow.StepRun{}, err
	}
	if err := r.createSession(ctx, run, tk, step, sr); err != nil {
		return workflow.StepRun{}, err
	}
	r.logf("run %d: step %q (iteration %d) as session %s", run.ID, step.Name, sr.Iteration, sr.SessionExternalID)
	return r.execStep(ctx, run, tk, step, sr, prompt, false, false)
}

// resumeStep picks up a step run a previous runner left unfinished.
// A gate just goes back to waiting. An agent step run with no session id
// belongs to no attempt at all -- RestartStep cleared it, the other half
// of "rerun this step headlessly" after a takeover -- and is started over
// under a fresh session id before the log or the old session is even
// consulted. Otherwise a step whose log already holds a result event
// finished on its own after the runner died and is settled from that;
// failing that its session is continued (ResumePrompt), and if that
// session cannot be continued -- killed before claude ever wrote it --
// the step is likewise started over. Either way the restart is on the
// same step run, so the iteration count stays honest.
func (r *Runner) resumeStep(ctx context.Context, run workflow.Run, tk task.Task, sr workflow.StepRun) (workflow.StepRun, error) {
	step, err := r.Store.GetStep(ctx, sr.StepID)
	if err != nil {
		return workflow.StepRun{}, err
	}
	r.logf("run %d: resuming step %q (iteration %d)", run.ID, step.Name, sr.Iteration)
	if step.Kind == workflow.StepGate {
		return r.waitGate(ctx, run, step, sr)
	}

	if sr.SessionExternalID == "" {
		r.logf("run %d: step %q has no session; starting it over", run.ID, step.Name)
		return r.startOver(ctx, run, tk, step, sr)
	}

	if res, ok := loggedResult(sr.LogPath); ok {
		r.logf("run %d: step %q had already finished; recording its result", run.ID, step.Name)
		return r.settle(ctx, run, tk, step, sr, res, nil, false)
	}

	fin, err := r.execStep(ctx, run, tk, step, sr, ResumePrompt, true, false)
	if !errors.Is(err, errNoResult) {
		return fin, err
	}
	r.logf("run %d: step %q could not be resumed (%v); starting it over", run.ID, step.Name, err)
	return r.startOver(ctx, run, tk, step, sr)
}

// startOver runs an unfinished agent step run again from its recorded
// prompt under a brand-new session id, on the same row. The previous
// session row (if any) is left as history and the log is not truncated:
// agent.RunHeadless appends, so the earlier attempt stays readable above
// the new one in the log viewer.
func (r *Runner) startOver(ctx context.Context, run workflow.Run, tk task.Task, step workflow.Step, sr workflow.StepRun) (workflow.StepRun, error) {
	var err error
	sr.SessionExternalID, err = agent.NewSessionID()
	if err != nil {
		return workflow.StepRun{}, err
	}
	if err := r.Store.SetStepRunSession(ctx, sr.ID, sr.SessionExternalID); err != nil {
		return workflow.StepRun{}, err
	}
	if err := r.createSession(ctx, run, tk, step, sr); err != nil {
		return workflow.StepRun{}, err
	}
	return r.execStep(ctx, run, tk, step, sr, sr.PromptRendered, false, false)
}

// createSession writes the agent_sessions row for a step's headless
// session ahead of the process, as launchSessionCmd does, so the hooks
// claude fires from its first turn land on a row. tmux_session is "" on
// purpose: claude runs inside the runner's tmux session, whose pane is
// the runner's output, not claude's chrome, so it is neither attachable
// as a session nor something the capture-pane poller should classify.
func (r *Runner) createSession(ctx context.Context, run workflow.Run, tk task.Task, step workflow.Step, sr workflow.StepRun) error {
	_, err := r.Store.CreateStepRunSession(ctx, sr.ID, tk.ID, sr.SessionExternalID, run.Cwd,
		step.Name+" — "+tk.Title, "")
	return err
}

// errNoResult marks an attempt that produced no result event at all --
// the process failed before claude got going -- as distinct from a step
// that ran and reported an error.
var errNoResult = errors.New("attempt produced no result")

// execStep runs one attempt of an agent step and settles the step run
// from what it left behind. While claude runs, the run's state is polled
// so a pause or cancel written by the TUI stops the process: cancelling
// ctx SIGTERMs it, and its session stays resumable either way. nudged
// marks the attempt as the one follow-up turn settle sends when a step
// exits without handing off, so settle does not send another.
func (r *Runner) execStep(ctx context.Context, run workflow.Run, tk task.Task, step workflow.Step, sr workflow.StepRun, prompt string, resume, nudged bool) (workflow.StepRun, error) {
	stepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	interrupted := make(chan workflow.RunState, 1)
	go r.watchRun(stepCtx, run.ID, func(st workflow.RunState) {
		interrupted <- st
		cancel()
	})

	// The step's session has no tmux pane of its own, so the TUI's
	// capture-pane poller never classifies it as working; without this the
	// row would read 'starting' for the whole life of the step. Best
	// effort, like the 'ended' written after the process for the same
	// reason: a hook (Stop, SessionEnd) landing later overwrites it.
	_ = r.Store.SetSessionStatus(ctx, sr.SessionExternalID, task.SessionWorking)
	res, runErr := r.Exec.Run(stepCtx, StepExec{Run: run, StepRun: sr, TaskID: tk.ID, Prompt: prompt, Resume: resume})
	cancel()
	// Best effort: the SessionEnd hook normally did this already, but a
	// killed step may not have got that far.
	_ = r.Store.SetSessionStatus(ctx, sr.SessionExternalID, task.SessionEnded)

	select {
	case st := <-interrupted:
		r.logf("run %d: step %q stopped: run %s", run.ID, step.Name, st)
		return workflow.StepRun{}, errInterrupted
	default:
	}
	if ctx.Err() != nil {
		return workflow.StepRun{}, ctx.Err()
	}
	return r.settle(ctx, run, tk, step, sr, res, runErr, nudged)
}

// settle records how an attempt ended. An outcome already on the step
// run -- the agent called finish_step -- stands, whatever the process
// did afterwards. Otherwise the run fails, with a message that says
// which, when the stream never reached a result, claude reported an
// error, or tool calls were denied for want of a permission mode.
// Denials are checked even on a "success" result: a step that could not
// use its tools almost certainly did not do its job.
//
// A step that ran fine but never called finish_step is settled from the
// stream's final text as a "done" deliverable only when done is the one
// outcome it can have (workflow.FallbackAllowed). A step that routes
// anything else is nudged: its session gets one more turn
// (workflow.NudgePrompt, over the same crash-resume path) to hand off,
// on the same step run and without counting as an iteration. If that
// turn also ends without finish_step the run fails, naming the step and
// the outcomes it was supposed to return, rather than routing on a
// guessed "done" -- the same reasoning as the denials rule.
func (r *Runner) settle(ctx context.Context, run workflow.Run, tk task.Task, step workflow.Step, sr workflow.StepRun, res agent.HeadlessResult, runErr error, nudged bool) (workflow.StepRun, error) {
	fin, err := r.Store.GetStepRun(ctx, sr.ID)
	if err != nil {
		return workflow.StepRun{}, err
	}
	if fin.Finished() {
		if res.PermissionDenials > 0 {
			r.logf("run %d: step %q finished with %s but %d tool call(s) were denied; set a permission mode on the step",
				run.ID, step.Name, fin.Outcome, res.PermissionDenials)
		} else {
			r.logf("run %d: step %q finished: %s", run.ID, step.Name, fin.Outcome)
		}
		return fin, nil
	}

	if !res.Found {
		if runErr == nil {
			runErr = agent.ErrNoResult
		}
		return workflow.StepRun{}, fmt.Errorf("step %q: %w: %w", step.Name, errNoResult, runErr)
	}
	text, err := res.ResultOrError()
	if err != nil {
		return workflow.StepRun{}, fmt.Errorf("step %q: %w", step.Name, err)
	}
	if res.PermissionDenials > 0 {
		return workflow.StepRun{}, fmt.Errorf("step %q: %d tool call(s) were denied; the step needs a permission mode it does not have",
			step.Name, res.PermissionDenials)
	}
	if runErr != nil {
		r.logf("run %d: step %q exited with an error after its result (%v); keeping the result", run.ID, step.Name, runErr)
	}

	edges, err := r.Store.OutgoingEdges(ctx, step.ID)
	if err != nil {
		return workflow.StepRun{}, err
	}
	outcomes := outcomesOf(edges)
	if !workflow.FallbackAllowed(outcomes) {
		routes := strings.Join(outcomes, ", ")
		if nudged {
			return workflow.StepRun{}, fmt.Errorf("step %q exited without calling finish_step, even after being asked to; it routes %s and the runner will not guess which",
				step.Name, routes)
		}
		r.logf("run %d: step %q exited without finish_step; it routes %s, so its session is asked once to hand off", run.ID, step.Name, routes)
		fin, err := r.execStep(ctx, run, tk, step, sr, workflow.NudgePrompt(step.Name, outcomes), true, true)
		if errors.Is(err, errNoResult) {
			// Not errNoResult to the caller: resumeStep would read that as
			// "the session cannot be continued" and start the step over.
			return workflow.StepRun{}, fmt.Errorf("step %q: its session could not be continued to hand off: %v", step.Name, err)
		}
		return fin, err
	}

	if err := r.Store.FinishStepRun(ctx, sr.ID, workflow.OutcomeDone, text); err != nil {
		return workflow.StepRun{}, err
	}
	r.logf("run %d: step %q finished without finish_step; recorded as %s", run.ID, step.Name, workflow.OutcomeDone)
	return r.Store.GetStepRun(ctx, sr.ID)
}

// waitGate parks the run at a gate step until someone records a decision
// on its step run (Store.FinishStepRun via the TUI or CLI), then takes the
// run back to running. A pause or cancel while waiting stops the wait.
func (r *Runner) waitGate(ctx context.Context, run workflow.Run, step workflow.Step, sr workflow.StepRun) (workflow.StepRun, error) {
	if err := r.Store.SetRunState(ctx, run.ID, workflow.RunWaitingReview); err != nil {
		return workflow.StepRun{}, err
	}
	r.logf("run %d: gate %q is waiting for review", run.ID, step.Name)
	ticker := time.NewTicker(r.poll())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return workflow.StepRun{}, ctx.Err()
		case <-ticker.C:
		}
		fin, err := r.Store.GetStepRun(ctx, sr.ID)
		if err != nil {
			return workflow.StepRun{}, err
		}
		if fin.Finished() {
			if err := r.Store.SetRunState(ctx, run.ID, workflow.RunRunning); err != nil {
				return workflow.StepRun{}, err
			}
			r.logf("run %d: gate %q decided: %s", run.ID, step.Name, fin.Outcome)
			return fin, nil
		}
		cur, err := r.Store.GetRun(ctx, run.ID)
		if err != nil {
			return workflow.StepRun{}, err
		}
		if cur.State != workflow.RunWaitingReview {
			r.logf("run %d: gate %q stopped waiting: run %s", run.ID, step.Name, cur.State)
			return workflow.StepRun{}, errInterrupted
		}
	}
}

// watchRun polls the run's state until ctx is done, calling stop once if
// the run leaves running -- paused or cancelled by the user, or ended by
// something else entirely. Store errors are skipped: a missed tick is
// caught by the next one.
func (r *Runner) watchRun(ctx context.Context, runID int64, stop func(workflow.RunState)) {
	ticker := time.NewTicker(r.poll())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cur, err := r.Store.GetRun(ctx, runID)
		if err != nil {
			continue
		}
		if cur.State != workflow.RunRunning {
			stop(cur.State)
			return
		}
	}
}

// checkRun refuses to start another step once the run has left running.
func (r *Runner) checkRun(ctx context.Context, runID int64) error {
	cur, err := r.Store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if cur.State != workflow.RunRunning {
		r.logf("run %d: %s; not starting another step", runID, cur.State)
		return errInterrupted
	}
	return nil
}

// stopped turns an error out of the loop into Run's result: an
// interrupt (paused/cancelled) and the runner's own ctx ending both
// leave the run as it is; anything else fails it.
func (r *Runner) stopped(ctx context.Context, run workflow.Run, err error) error {
	switch {
	case errors.Is(err, errInterrupted):
		return nil
	case ctx.Err() != nil:
		r.logf("run %d: runner interrupted; `tend workflow resume %d` picks the run back up", run.ID, run.ID)
		return ctx.Err()
	default:
		return r.fail(ctx, run, err.Error())
	}
}

// fail records reason on the run, logs it, and returns the ErrRunFailed
// the process exits with. A run that has meanwhile ended some other way
// (cancelled from the TUI as the step failed) keeps its own ending.
func (r *Runner) fail(ctx context.Context, run workflow.Run, reason string) error {
	r.logf("run %d: failed: %s", run.ID, reason)
	if err := r.Store.FailRun(ctx, run.ID, reason); err != nil && !errors.Is(err, workflow.ErrRunEnded) {
		return fmt.Errorf("%w: %s (and recording that: %v)", ErrRunFailed, reason, err)
	}
	return fmt.Errorf("run %d: %w: %s", run.ID, ErrRunFailed, reason)
}

// priorRuns is the step runs of stepID within the run so far, oldest
// first -- the iteration count and, for a loop-back, the input to keep.
func (r *Runner) priorRuns(ctx context.Context, runID, stepID int64) ([]workflow.StepRun, error) {
	all, err := r.Store.ListStepRunsForRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	var out []workflow.StepRun
	for _, sr := range all {
		if sr.StepID == stepID {
			out = append(out, sr)
		}
	}
	return out, nil
}

func (r *Runner) poll() time.Duration {
	if r.Poll > 0 {
		return r.Poll
	}
	return DefaultPoll
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log == nil {
		return
	}
	fmt.Fprintf(r.Log, time.Now().Format("15:04:05")+" "+format+"\n", args...)
}

// loggedResult reads the result event out of a step's existing log, for
// a step whose runner died after claude finished but before the outcome
// was recorded. A missing or unreadable log is simply no result.
func loggedResult(logPath string) (agent.HeadlessResult, bool) {
	if logPath == "" {
		return agent.HeadlessResult{}, false
	}
	f, err := os.Open(logPath)
	if err != nil {
		return agent.HeadlessResult{}, false
	}
	defer f.Close()
	res, err := agent.ParseStream(f)
	if err != nil || !res.Found {
		return agent.HeadlessResult{}, false
	}
	return res, true
}

func outcomesOf(edges []workflow.Edge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.Outcome)
	}
	return out
}

// ordinal renders 1 as "1st", 2 as "2nd", and so on, for the
// max_iterations message.
func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
