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
