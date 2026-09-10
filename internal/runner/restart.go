package runner

import (
	"context"
	"fmt"

	"github.com/jwstover/tend/internal/workflow"
)

// RestartStore is the slice of Store that RestartStep needs, in the
// LaunchStore idiom: the TUI's takeover picker calls it and should not
// have to carry the whole runner interface to do so.
type RestartStore interface {
	GetStepRun(ctx context.Context, id int64) (workflow.StepRun, error)
	SetStepRunSession(ctx context.Context, id int64, externalID string) error
}

// RestartStep marks the unfinished step run stepRunID as owned by no
// attempt, so the run's next runner starts the step over. It is how the
// TUI's takeover picker (tend task #184) asks for "rerun this step
// headlessly": the user paused the run, took the current step's claude
// session over interactively, and now wants the runner to try the step
// again from its recorded prompt rather than continue the session they
// were just in.
//
// The mark is the step run's session id being set to "". Nothing else is
// touched: the step run keeps its row, iteration, prompt, input and
// feedback, so a rerun is the same iteration, not a new one -- the
// iteration count stays honest and max_iterations is not spent on a
// human intervention. The session that ran the step so far stays as
// history on the task (its row is already 'ended') and the step's log is
// not truncated; the new attempt appends below it.
//
// The other half is resumeStep: when Resume (Run with takeover) re-enters
// the run at current_step_run_id and finds an unfinished agent step run
// with no session id, it mints a fresh one, writes the new session row
// and execs the step's recorded prompt as a first turn, before it would
// otherwise look at the log or try to continue the old session.
//
// A finished step run is refused with an error wrapping
// workflow.ErrStepRunFinished: it already has an outcome, and a run of
// the step after that is a new iteration, which only the workflow's edges
// can create.
func RestartStep(ctx context.Context, s RestartStore, stepRunID int64) error {
	sr, err := s.GetStepRun(ctx, stepRunID)
	if err != nil {
		return err
	}
	if sr.Finished() {
		return fmt.Errorf("step run %d: %w: its outcome %q stands; a rerun would be a new iteration", stepRunID, workflow.ErrStepRunFinished, sr.Outcome)
	}
	return s.SetStepRunSession(ctx, stepRunID, "")
}
