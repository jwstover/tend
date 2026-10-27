package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/workflow"
)

// ScheduleStore is the slice of Store that firing schedules needs. Tick is
// the one function every trigger calls (launchd via `tend schedule tick`,
// the TUI's backup ticker), so it declares only what it uses.
type ScheduleStore interface {
	DueSchedules(ctx context.Context, now time.Time) ([]workflow.Schedule, error)
	ClaimSchedule(ctx context.Context, id int64, now time.Time) (workflow.ScheduleClaim, error)
	FailRun(ctx context.Context, id int64, reason string) error
}

// LaunchFunc starts the runner for a claimed run and returns its tmux
// session. It is a parameter, never Launch itself, so callers and tests can
// stub it; production passes a closure over Launch.
type LaunchFunc func(ctx context.Context, runID int64) (tmuxSession string, err error)

// TickGuard is asked once per tick that has due schedules; a non-empty
// reason skips them all, unclaimed, so they stay due for the next tick.
type TickGuard func(ctx context.Context) (skip string)

// TickResult reports what a tick did with each due schedule it claimed or
// skipped. A schedule another tick claimed first appears nowhere.
type TickResult struct {
	Started []TickStarted
	Skipped []TickSkipped
	Failed  []TickFailed
}

// TickStarted is a schedule whose run was created and whose runner started.
type TickStarted struct {
	Schedule    workflow.Schedule
	RunID       int64
	TmuxSession string
}

// TickSkipped is a due schedule that started no run, and why.
type TickSkipped struct {
	Schedule workflow.Schedule
	Reason   string
}

// TickFailed is a due schedule whose claim or launch errored. RunID is 0
// when the claim itself failed.
type TickFailed struct {
	Schedule workflow.Schedule
	RunID    int64
	Err      error
}

// Tick fires every schedule due at now: claim it, launch a runner for the
// run the claim created, and move on. It returns an error only when the due
// list cannot be read. A run whose runner cannot be launched is failed so
// no pending run is left looking live, as `tend workflow start` does.
func Tick(ctx context.Context, s ScheduleStore, now time.Time, launch LaunchFunc, guard TickGuard) (TickResult, error) {
	var res TickResult
	due, err := s.DueSchedules(ctx, now)
	if err != nil {
		return res, fmt.Errorf("listing due schedules: %w", err)
	}
	if len(due) == 0 {
		return res, nil
	}
	if guard != nil {
		if reason := guard(ctx); reason != "" {
			for _, sc := range due {
				res.Skipped = append(res.Skipped, TickSkipped{Schedule: sc, Reason: reason})
			}
			return res, nil
		}
	}
	for _, sc := range due {
		claim, err := s.ClaimSchedule(ctx, sc.ID, now)
		if err != nil {
			res.Failed = append(res.Failed, TickFailed{Schedule: sc, Err: fmt.Errorf("claiming schedule %d: %w", sc.ID, err)})
			continue
		}
		switch {
		case claim.Skipped != "":
			res.Skipped = append(res.Skipped, TickSkipped{Schedule: sc, Reason: claim.Skipped})
		case claim.Claimed && claim.Run != nil:
			session, err := launch(ctx, claim.Run.ID)
			if err != nil {
				if ferr := s.FailRun(ctx, claim.Run.ID, err.Error()); ferr != nil {
					err = fmt.Errorf("%w (and failing the run: %v)", err, ferr)
				}
				res.Failed = append(res.Failed, TickFailed{Schedule: sc, RunID: claim.Run.ID, Err: err})
				continue
			}
			res.Started = append(res.Started, TickStarted{Schedule: sc, RunID: claim.Run.ID, TmuxSession: session})
		}
	}
	return res, nil
}

// QuotaSkipPercent is the five-hour quota usage at or above which a tick
// leaves its due schedules for a later one.
const QuotaSkipPercent = 80

// quotaTimeout bounds the `claude -p /usage` call a guard makes.
const quotaTimeout = 20 * time.Second

// fetchQuota is the quota reading, a seam so tests need no claude binary.
var fetchQuota = agent.FetchQuota

// QuotaGuard is the production TickGuard: unattended runs must not eat the
// subscription, so it skips the tick once the five-hour window is at or past
// QuotaSkipPercent. Any failure to read the quota (no claude, an API-key
// login, a timeout) proceeds, since that must never stop schedules.
func QuotaGuard(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	q, err := fetchQuota(ctx)
	if err != nil || q.Session == nil || q.Session.Percent < QuotaSkipPercent {
		return ""
	}
	reason := fmt.Sprintf("5h quota at %d%%", q.Session.Percent)
	if q.Session.Resets != "" {
		reason += fmt.Sprintf(" (resets %s)", q.Session.Resets)
	}
	return reason
}
