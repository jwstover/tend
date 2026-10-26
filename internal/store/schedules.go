package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jwstover/tend/internal/store/gen"
	"github.com/jwstover/tend/internal/workflow"
)

// ---- schedules -----------------------------------------------------------

// nextRunAt is the first cron time after now, in the local zone the
// expression is written in, as the UTC column text.
func nextRunAt(c workflow.Cron, now time.Time) (string, error) {
	next, err := c.Next(now.In(time.Local))
	if err != nil {
		return "", err
	}
	return next.UTC().Format(sqliteTimeLayout), nil
}

// CreateSchedule adds an enabled schedule. The cron expression is parsed
// here so an invalid one never lands, and stored normalized; next_run_at is
// the first match after now. Names are unique case-insensitively.
func (s *Store) CreateSchedule(ctx context.Context, sc workflow.Schedule, now time.Time) (workflow.Schedule, error) {
	name, err := workflow.NormalizeName(sc.Name)
	if err != nil {
		return workflow.Schedule{}, err
	}
	c, err := workflow.ParseCron(sc.CronExpr)
	if err != nil {
		return workflow.Schedule{}, err
	}
	next, err := nextRunAt(c, now)
	if err != nil {
		return workflow.Schedule{}, err
	}
	row, err := s.q.CreateSchedule(ctx, gen.CreateScheduleParams{
		Name: name, WorkflowID: sc.WorkflowID, TaskID: sc.TaskID,
		Cwd: sc.Cwd, CronExpr: c.String(), NextRunAt: next,
	})
	if err != nil {
		return workflow.Schedule{}, fmt.Errorf("creating schedule %q: %w", name, err)
	}
	return scheduleToDomain(row)
}

// GetSchedule loads one schedule by id.
func (s *Store) GetSchedule(ctx context.Context, id int64) (workflow.Schedule, error) {
	row, err := s.q.GetSchedule(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Schedule{}, fmt.Errorf("schedule %d: %w", id, workflow.ErrScheduleNotFound)
	}
	if err != nil {
		return workflow.Schedule{}, fmt.Errorf("loading schedule %d: %w", id, err)
	}
	return scheduleToDomain(row)
}

// ScheduleByName resolves a schedule by name, case-insensitively.
func (s *Store) ScheduleByName(ctx context.Context, name string) (workflow.Schedule, error) {
	n, err := workflow.NormalizeName(name)
	if err != nil {
		return workflow.Schedule{}, err
	}
	row, err := s.q.GetScheduleByName(ctx, n)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.Schedule{}, fmt.Errorf("schedule %q: %w", n, workflow.ErrScheduleNotFound)
	}
	if err != nil {
		return workflow.Schedule{}, fmt.Errorf("loading schedule %q: %w", n, err)
	}
	return scheduleToDomain(row)
}

// ListSchedules returns every schedule ordered by name.
func (s *Store) ListSchedules(ctx context.Context) ([]workflow.Schedule, error) {
	rows, err := s.q.ListSchedules(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing schedules: %w", err)
	}
	out := make([]workflow.Schedule, 0, len(rows))
	for _, r := range rows {
		sc, err := scheduleToDomain(r)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

// UpdateSchedule writes the name, cwd, cron expression and enabled flag.
// next_run_at is recomputed from now when the cron changed or the schedule
// is being re-enabled, and kept otherwise.
func (s *Store) UpdateSchedule(ctx context.Context, sc workflow.Schedule, now time.Time) (workflow.Schedule, error) {
	name, err := workflow.NormalizeName(sc.Name)
	if err != nil {
		return workflow.Schedule{}, err
	}
	c, err := workflow.ParseCron(sc.CronExpr)
	if err != nil {
		return workflow.Schedule{}, err
	}
	old, err := s.GetSchedule(ctx, sc.ID)
	if err != nil {
		return workflow.Schedule{}, err
	}
	next := old.NextRunAt.UTC().Format(sqliteTimeLayout)
	if c.String() != old.CronExpr || (sc.Enabled && !old.Enabled) {
		if next, err = nextRunAt(c, now); err != nil {
			return workflow.Schedule{}, err
		}
	}
	row, err := s.q.UpdateSchedule(ctx, gen.UpdateScheduleParams{
		Name: name, Cwd: sc.Cwd, CronExpr: c.String(), Enabled: boolInt(sc.Enabled),
		NextRunAt: next, ID: sc.ID,
	})
	if err != nil {
		return workflow.Schedule{}, fmt.Errorf("updating schedule %d: %w", sc.ID, err)
	}
	return scheduleToDomain(row)
}

// SetScheduleEnabled pauses or resumes a schedule. Resuming recomputes
// next_run_at from now, so a paused stretch never fires a catch-up run.
func (s *Store) SetScheduleEnabled(ctx context.Context, id int64, enabled bool, now time.Time) error {
	sc, err := s.GetSchedule(ctx, id)
	if err != nil {
		return err
	}
	next := sc.NextRunAt.UTC().Format(sqliteTimeLayout)
	if enabled {
		c, err := workflow.ParseCron(sc.CronExpr)
		if err != nil {
			return err
		}
		if next, err = nextRunAt(c, now); err != nil {
			return err
		}
	}
	if _, err := s.q.SetScheduleEnabled(ctx, gen.SetScheduleEnabledParams{Enabled: boolInt(enabled), NextRunAt: next, ID: id}); err != nil {
		return fmt.Errorf("setting schedule %d enabled=%t: %w", id, enabled, err)
	}
	return nil
}

// DeleteSchedule removes a schedule. The runs it created stay; their
// schedule_id becomes NULL.
func (s *Store) DeleteSchedule(ctx context.Context, id int64) error {
	n, err := s.q.DeleteSchedule(ctx, id)
	if err != nil {
		return fmt.Errorf("deleting schedule %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("schedule %d: %w", id, workflow.ErrScheduleNotFound)
	}
	return nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func scheduleToDomain(r gen.Schedule) (workflow.Schedule, error) {
	next, err := parseTime(r.NextRunAt)
	if err != nil {
		return workflow.Schedule{}, fmt.Errorf("schedule %d next_run_at: %w", r.ID, err)
	}
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return workflow.Schedule{}, fmt.Errorf("schedule %d created_at: %w", r.ID, err)
	}
	updated, err := parseTime(r.UpdatedAt)
	if err != nil {
		return workflow.Schedule{}, fmt.Errorf("schedule %d updated_at: %w", r.ID, err)
	}
	var last *int64
	if r.LastRunID.Valid {
		last = &r.LastRunID.Int64
	}
	return workflow.Schedule{
		ID: r.ID, Name: r.Name, WorkflowID: r.WorkflowID, TaskID: r.TaskID,
		Cwd: r.Cwd, CronExpr: r.CronExpr, Enabled: r.Enabled != 0,
		Overlap: workflow.OverlapPolicy(r.OverlapPolicy), LastRunID: last,
		NextRunAt: next, CreatedAt: created, UpdatedAt: updated,
	}, nil
}

// DueSchedules lists the enabled schedules whose next_run_at is at or
// before now, oldest first.
func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]workflow.Schedule, error) {
	rows, err := s.q.DueSchedules(ctx, now.UTC().Format(sqliteTimeLayout))
	if err != nil {
		return nil, fmt.Errorf("listing due schedules: %w", err)
	}
	out := make([]workflow.Schedule, 0, len(rows))
	for _, r := range rows {
		sc, err := scheduleToDomain(r)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

// liveLastRun returns the schedule's last run when it is not terminal.
func liveLastRun(ctx context.Context, q *gen.Queries, id int64) (*workflow.Run, error) {
	row, err := q.GetScheduleLastRunForTx(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading last run of schedule %d: %w", id, err)
	}
	run, err := runToDomain(row)
	if err != nil {
		return nil, err
	}
	if run.State.Terminal() {
		return nil, nil
	}
	return &run, nil
}

func startScheduledRun(ctx context.Context, q *gen.Queries, sc workflow.Schedule) (workflow.Run, error) {
	row, err := q.CreateScheduledRun(ctx, gen.CreateScheduledRunParams{
		WorkflowID: sc.WorkflowID, TaskID: sc.TaskID, Cwd: sc.Cwd,
		ScheduleID: sql.NullInt64{Int64: sc.ID, Valid: true},
	})
	if err != nil {
		return workflow.Run{}, fmt.Errorf("creating run for schedule %d: %w", sc.ID, err)
	}
	if err := q.SetScheduleLastRun(ctx, gen.SetScheduleLastRunParams{
		LastRunID: sql.NullInt64{Int64: row.ID, Valid: true}, ID: sc.ID,
	}); err != nil {
		return workflow.Run{}, fmt.Errorf("recording last run of schedule %d: %w", sc.ID, err)
	}
	return runToDomain(row)
}

// ClaimSchedule turns a due schedule into a pending run, at most once per
// due time: next_run_at is advanced by a compare-and-swap that is the first
// write of the transaction, so a racing tick matches no row and gets the
// zero claim. The next time is computed from now, so intervals missed while
// asleep merge into this one run. When the previous run is still live the
// tick is skipped (overlap policy skip) but next_run_at still advances.
func (s *Store) ClaimSchedule(ctx context.Context, id int64, now time.Time) (workflow.ScheduleClaim, error) {
	sc, err := s.GetSchedule(ctx, id)
	if err != nil {
		return workflow.ScheduleClaim{}, err
	}
	if !sc.Enabled || sc.NextRunAt.After(now) {
		return workflow.ScheduleClaim{}, nil
	}
	c, err := workflow.ParseCron(sc.CronExpr)
	if err != nil {
		return workflow.ScheduleClaim{}, err
	}
	next, err := nextRunAt(c, now)
	if err != nil {
		return workflow.ScheduleClaim{}, err
	}
	var claim workflow.ScheduleClaim
	err = s.inTx(ctx, func(q *gen.Queries) error {
		n, err := q.AdvanceSchedule(ctx, gen.AdvanceScheduleParams{
			NextRunAt: next, ID: id, NextRunAt_2: sc.NextRunAt.UTC().Format(sqliteTimeLayout),
		})
		if err != nil {
			return fmt.Errorf("advancing schedule %d: %w", id, err)
		}
		if n == 0 {
			return nil
		}
		live, err := liveLastRun(ctx, q, id)
		if err != nil {
			return err
		}
		if live != nil {
			claim.Skipped = fmt.Sprintf("run %d is still %s", live.ID, live.State)
			return nil
		}
		run, err := startScheduledRun(ctx, q, sc)
		if err != nil {
			return err
		}
		claim.Claimed, claim.Run = true, &run
		return nil
	})
	if err != nil {
		return workflow.ScheduleClaim{}, err
	}
	return claim, nil
}

// StartScheduleNow is run-now: it starts a pending run of the schedule
// regardless of next_run_at or whether the schedule is enabled, refusing
// with ErrScheduleBusy while its last run is live.
func (s *Store) StartScheduleNow(ctx context.Context, id int64) (workflow.Run, error) {
	sc, err := s.GetSchedule(ctx, id)
	if err != nil {
		return workflow.Run{}, err
	}
	var run workflow.Run
	err = s.inTx(ctx, func(q *gen.Queries) error {
		live, err := liveLastRun(ctx, q, id)
		if err != nil {
			return err
		}
		if live != nil {
			return fmt.Errorf("schedule %q: run %d is %s: %w", sc.Name, live.ID, live.State, workflow.ErrScheduleBusy)
		}
		run, err = startScheduledRun(ctx, q, sc)
		return err
	})
	return run, err
}

// ScheduleRunContext describes the schedule behind a run and the run
// before it; ok is false for a run no schedule started.
func (s *Store) ScheduleRunContext(ctx context.Context, runID int64) (workflow.ScheduleRunContext, bool, error) {
	sid, err := s.q.GetRunScheduleID(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.ScheduleRunContext{}, false, fmt.Errorf("run %d: %w", runID, workflow.ErrRunNotFound)
	}
	if err != nil {
		return workflow.ScheduleRunContext{}, false, fmt.Errorf("loading run %d: %w", runID, err)
	}
	if !sid.Valid {
		return workflow.ScheduleRunContext{}, false, nil
	}
	sc, err := s.GetSchedule(ctx, sid.Int64)
	if err != nil {
		return workflow.ScheduleRunContext{}, false, err
	}
	out := workflow.ScheduleRunContext{Schedule: sc}
	row, err := s.q.GetPreviousScheduledRun(ctx, gen.GetPreviousScheduledRunParams{
		ScheduleID: sid, ID: runID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return out, true, nil
	}
	if err != nil {
		return workflow.ScheduleRunContext{}, false, fmt.Errorf("loading run before %d: %w", runID, err)
	}
	prev, err := runToDomain(row)
	if err != nil {
		return workflow.ScheduleRunContext{}, false, err
	}
	out.Previous = &prev
	d, err := s.q.GetLastFinishedStepDeliverable(ctx, prev.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return workflow.ScheduleRunContext{}, false, fmt.Errorf("loading deliverable of run %d: %w", prev.ID, err)
	}
	out.PreviousDeliverable = d
	return out, true, nil
}
