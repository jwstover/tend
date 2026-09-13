package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/workflow"
)

// LaunchStore is the slice of Store that hosting a runner needs: the
// TUI's `w` chord and `tend workflow resume` both go through Launch and
// neither wants the whole runner interface for it.
type LaunchStore interface {
	GetRun(ctx context.Context, id int64) (workflow.Run, error)
	SetRunTmuxSession(ctx context.Context, id int64, name string) error
}

// ErrNoTmux is returned by Launch when tmux is not on $PATH. Unlike an
// interactive session, which falls back to running claude in the
// caller's terminal, a runner has no terminal to fall back to: tmux is
// what hosts it.
var ErrNoTmux = errors.New("tmux: not found on $PATH — a workflow runner needs tmux to run in")

// ErrRunnerAlive is returned by Resume when the run's runner is still
// running: `tmux has-session` says its session exists, so there is
// nothing to resume and starting a second runner would be exactly the
// double-drive the claim guards against.
var ErrRunnerAlive = errors.New("its runner is still running")

// Launch starts the runner for runID in a detached tmux session named
// agent.RunnerSessionName(runID) on tend's own tmux server, records that
// name on the run, and returns it. It returns as soon as tmux has the
// session; the runner claims the run from inside it (see Run). takeover
// is passed through to the runner for re-entering a run whose previous
// runner died. A tmux session of that name already existing fails the
// launch: that is the "two runners on one run" guard, so the error is
// surfaced rather than smoothed over.
func Launch(ctx context.Context, s LaunchStore, runID int64, dbPath string, takeover bool) (string, error) {
	if !agent.TmuxInstalled() {
		return "", ErrNoTmux
	}
	confPath, err := agent.WriteConfig()
	if err != nil {
		return "", err
	}
	inner, err := agent.RunnerCmd(runID, dbPath, takeover)
	if err != nil {
		return "", err
	}
	name := agent.RunnerSessionName(runID)
	if out, err := agent.StartDetached(inner, name, confPath).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("starting runner for run %d in tmux: %s", runID, msg)
	}
	if err := s.SetRunTmuxSession(ctx, runID, name); err != nil {
		return name, err
	}
	return name, nil
}

// RetryStore is the slice of Store that Retry needs: hosting the runner
// (LaunchStore), the failed-to-paused transition, and the step run
// reads/writes RestartStep makes for a fresh start.
type RetryStore interface {
	LaunchStore
	RestartStore
	RetryRun(ctx context.Context, id int64) error
}

// Retry re-enters a failed run at the step that failed. It takes the run
// back to paused (Store.RetryRun; the failure stays on the row for the
// runner to read) and launches a runner with takeover, exactly as Resume
// does for a paused run: the runner picks the failed step back up on its
// existing session, telling it what went wrong (workflow.RetryPrompt).
// With fresh true the step is started over instead -- RestartStep clears
// the step run's session id, so the runner runs the step's recorded
// prompt under a new session, same step run and iteration. Either way
// the step run first takes the model and permission mode its step has
// now, so a step fixed after the failure runs fixed. A failure that left
// no unfinished step (an edge's max_iterations exceeded, claude not on
// $PATH) is simply re-evaluated from where the run stood.
//
// A run in any state but failed is refused with workflow.ErrRunNotFailed
// (a paused run is resumed, not retried), and a runner somehow still
// alive with ErrRunnerAlive, so it can be run on a hunch without harm.
func Retry(ctx context.Context, s RetryStore, runID int64, dbPath string, fresh bool) (string, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	if run.State != workflow.RunFailed {
		return "", fmt.Errorf("run %d is %s: %w", runID, run.State, workflow.ErrRunNotFailed)
	}
	if !agent.TmuxInstalled() {
		return "", ErrNoTmux
	}
	confPath, err := agent.WriteConfig()
	if err != nil {
		return "", err
	}
	if agent.HasSession(agent.RunnerSessionName(runID), confPath) {
		return "", fmt.Errorf("run %d: %w (tmux session %s)", runID, ErrRunnerAlive, agent.RunnerSessionName(runID))
	}
	if fresh && run.CurrentStepRunID != nil {
		sr, err := s.GetStepRun(ctx, *run.CurrentStepRunID)
		if err != nil {
			return "", err
		}
		if !sr.Finished() {
			if err := RestartStep(ctx, s, sr.ID); err != nil {
				return "", err
			}
		}
	}
	if err := s.RetryRun(ctx, runID); err != nil {
		return "", err
	}
	return Launch(ctx, s, runID, dbPath, true)
}

// Resume re-enters a run whose runner died -- the host rebooted, the
// tmux server was killed, the runner crashed -- or was paused, by
// launching a fresh runner with takeover. It refuses a run that has
// ended (workflow.ErrRunEnded) or whose runner is still alive
// (ErrRunnerAlive), so it can be run on a hunch without doing harm.
func Resume(ctx context.Context, s LaunchStore, runID int64, dbPath string) (string, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	if run.State.Terminal() {
		return "", fmt.Errorf("run %d is %s: %w", runID, run.State, workflow.ErrRunEnded)
	}
	if !agent.TmuxInstalled() {
		return "", ErrNoTmux
	}
	confPath, err := agent.WriteConfig()
	if err != nil {
		return "", err
	}
	if agent.HasSession(agent.RunnerSessionName(runID), confPath) {
		return "", fmt.Errorf("run %d: %w (tmux session %s)", runID, ErrRunnerAlive, agent.RunnerSessionName(runID))
	}
	return Launch(ctx, s, runID, dbPath, true)
}
