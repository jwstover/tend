package store

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"

	"github.com/jwstover/tend/internal/workflow"
)

func scheduleFixture(t *testing.T, s *Store) (workflow.Workflow, int64) {
	t.Helper()
	ctx := context.Background()
	wf, err := s.CreateWorkflow(ctx, "nightly", "")
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	return wf, mustAdd(t, s, "home").ID
}

func TestCreateSchedule(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wf, taskID := scheduleFixture(t, s)
	now := time.Date(2026, 3, 1, 10, 30, 0, 0, time.Local)

	got, err := s.CreateSchedule(ctx, workflow.Schedule{
		Name: " Nightly ", WorkflowID: wf.ID, TaskID: taskID, Cwd: "/tmp", CronExpr: "  0  3 * * * ",
	}, now)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	want := time.Date(2026, 3, 2, 3, 0, 0, 0, time.Local)
	if got.Name != "Nightly" || got.CronExpr != "0 3 * * *" || !got.Enabled || got.Overlap != workflow.OverlapSkip || !got.NextRunAt.Equal(want.UTC()) {
		t.Errorf("got %+v, want name Nightly, cron %q, enabled, skip, next %v", got, "0 3 * * *", want)
	}

	byName, err := s.ScheduleByName(ctx, "NIGHTLY")
	if err != nil || byName.ID != got.ID {
		t.Errorf("ScheduleByName = %+v, %v", byName, err)
	}
	if _, err := s.ScheduleByName(ctx, "nope"); !errors.Is(err, workflow.ErrScheduleNotFound) {
		t.Errorf("miss err = %v, want ErrScheduleNotFound", err)
	}

	for _, tc := range []struct {
		name, cron string
		want       error
	}{
		{"  ", "0 3 * * *", workflow.ErrEmptyName},
		{"bad", "nonsense", workflow.ErrInvalidCron},
		{"nightly", "0 4 * * *", nil}, // duplicate, any case
	} {
		_, err := s.CreateSchedule(ctx, workflow.Schedule{Name: tc.name, WorkflowID: wf.ID, TaskID: taskID, CronExpr: tc.cron}, now)
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("CreateSchedule(%q, %q) err = %v, want %v", tc.name, tc.cron, err, tc.want)
		}
	}
	if list, err := s.ListSchedules(ctx); err != nil || len(list) != 1 {
		t.Errorf("ListSchedules = %v, %v; want only the one valid schedule", list, err)
	}
}

func TestUpdateScheduleRecomputesNext(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wf, taskID := scheduleFixture(t, s)
	t0 := time.Date(2026, 3, 1, 10, 30, 0, 0, time.Local)
	t1 := t0.Add(48 * time.Hour)
	sc, err := s.CreateSchedule(ctx, workflow.Schedule{Name: "a", WorkflowID: wf.ID, TaskID: taskID, CronExpr: "0 3 * * *"}, t0)
	if err != nil {
		t.Fatal(err)
	}

	// A rename keeps next_run_at even though now moved.
	sc.Name, sc.Cwd, sc.Enabled = "b", "/x", true
	got, err := s.UpdateSchedule(ctx, sc, t1)
	if err != nil || got.Name != "b" || got.Cwd != "/x" || !got.NextRunAt.Equal(sc.NextRunAt) {
		t.Fatalf("rename: %+v, %v; want next kept at %v", got, err, sc.NextRunAt)
	}

	// A cron change recomputes from now.
	got.CronExpr = "0 5 * * *"
	got, err = s.UpdateSchedule(ctx, got, t1)
	if want := time.Date(2026, 3, 4, 5, 0, 0, 0, time.Local); err != nil || !got.NextRunAt.Equal(want) || got.CronExpr != "0 5 * * *" {
		t.Fatalf("cron change: %+v, %v; want next %v", got, err, want)
	}

	// Disabling keeps next; enabling again via SetScheduleEnabled recomputes.
	if err := s.SetScheduleEnabled(ctx, sc.ID, false, t1); err != nil {
		t.Fatal(err)
	}
	off, _ := s.GetSchedule(ctx, sc.ID)
	if off.Enabled || !off.NextRunAt.Equal(got.NextRunAt) {
		t.Errorf("disable: %+v, want disabled with next kept", off)
	}
	t2 := t1.Add(72 * time.Hour)
	if err := s.SetScheduleEnabled(ctx, sc.ID, true, t2); err != nil {
		t.Fatal(err)
	}
	on, _ := s.GetSchedule(ctx, sc.ID)
	if want := time.Date(2026, 3, 7, 5, 0, 0, 0, time.Local); !on.Enabled || !on.NextRunAt.Equal(want) {
		t.Errorf("resume: %+v, want next %v", on, want)
	}

	// Re-enabling through UpdateSchedule recomputes too.
	if err := s.SetScheduleEnabled(ctx, sc.ID, false, t2); err != nil {
		t.Fatal(err)
	}
	on.Enabled = true
	t3 := t2.Add(24 * time.Hour)
	on, err = s.UpdateSchedule(ctx, on, t3)
	if want := time.Date(2026, 3, 8, 5, 0, 0, 0, time.Local); err != nil || !on.NextRunAt.Equal(want) {
		t.Errorf("update re-enable: %+v, %v; want next %v", on, err, want)
	}
}

func TestScheduleCascadesAndDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	wf, taskID := scheduleFixture(t, s)
	mk := func(name string, wfID, tID int64) workflow.Schedule {
		sc, err := s.CreateSchedule(ctx, workflow.Schedule{Name: name, WorkflowID: wfID, TaskID: tID, CronExpr: "* * * * *"}, now)
		if err != nil {
			t.Fatal(err)
		}
		return sc
	}
	mk("by-wf", wf.ID, taskID)
	wf2, _ := s.CreateWorkflow(ctx, "other", "")
	other := mustAdd(t, s, "other home")
	mk("by-task", wf2.ID, other.ID)
	gone := mk("gone", wf2.ID, taskID)

	if err := s.DeleteSchedule(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSchedule(ctx, gone.ID); !errors.Is(err, workflow.ErrScheduleNotFound) {
		t.Errorf("second delete err = %v, want ErrScheduleNotFound", err)
	}
	if err := s.DeleteWorkflow(ctx, wf.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTask(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListSchedules(ctx); err != nil || len(list) != 0 {
		t.Errorf("ListSchedules = %v, %v; want none after cascades", list, err)
	}
}

func TestSchedulesMigrationUpDown(t *testing.T) {
	db := openAt(t, filepath.Join(t.TempDir(), "tend.db"), 18)
	defer db.Close()
	has := func(q string) int {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	const table = `SELECT count(*) FROM sqlite_master WHERE name = 'schedules'`
	const col = `SELECT count(*) FROM pragma_table_info('workflow_runs') WHERE name = 'schedule_id'`
	if has(table) != 1 || has(col) != 1 {
		t.Fatal("version 18 lacks schedules or workflow_runs.schedule_id")
	}
	sub, _ := fs.Sub(migrationsFS, "migrations")
	provider, err := goose.NewProvider(database.DialectSQLite3, db, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(context.Background(), 17); err != nil {
		t.Fatalf("down: %v", err)
	}
	if has(table) != 0 || has(col) != 0 {
		t.Error("down left schedules or schedule_id behind")
	}
}
