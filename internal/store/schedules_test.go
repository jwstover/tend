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

// dueSchedule makes a daily 03:00 schedule and returns it with a "now" two
// days past its first fire.
func dueSchedule(t *testing.T, s *Store) (workflow.Schedule, time.Time) {
	t.Helper()
	wf, taskID := scheduleFixture(t, s)
	created := time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)
	sc, err := s.CreateSchedule(context.Background(), workflow.Schedule{
		Name: "daily", WorkflowID: wf.ID, TaskID: taskID, Cwd: "/tmp", CronExpr: "0 3 * * *",
	}, created)
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	return sc, time.Date(2026, 3, 4, 3, 5, 0, 0, time.Local)
}

func TestClaimScheduleOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sc, now := dueSchedule(t, s)

	if early, err := s.ClaimSchedule(ctx, sc.ID, sc.NextRunAt.Add(-time.Minute)); err != nil || early.Claimed || early.Skipped != "" {
		t.Fatalf("early claim = %+v, %v, want zero", early, err)
	}
	due, err := s.DueSchedules(ctx, now)
	if err != nil || len(due) != 1 || due[0].ID != sc.ID {
		t.Fatalf("DueSchedules = %+v, %v", due, err)
	}

	first, err := s.ClaimSchedule(ctx, sc.ID, now)
	if err != nil || !first.Claimed || first.Run == nil || first.Run.State != workflow.RunPending {
		t.Fatalf("first claim = %+v, %v", first, err)
	}
	second, err := s.ClaimSchedule(ctx, sc.ID, now)
	if err != nil || second.Claimed || second.Skipped != "" {
		t.Errorf("second claim = %+v, %v, want zero", second, err)
	}
	// Overdue by three intervals: one run, next fire after now.
	got, _ := s.GetSchedule(ctx, sc.ID)
	if !got.NextRunAt.After(now) || got.LastRunID == nil || *got.LastRunID != first.Run.ID {
		t.Errorf("schedule after claim = %+v", got)
	}
	if want := time.Date(2026, 3, 5, 3, 0, 0, 0, time.Local); !got.NextRunAt.Equal(want) {
		t.Errorf("NextRunAt = %v, want %v", got.NextRunAt, want)
	}
	if due, _ := s.DueSchedules(ctx, now); len(due) != 0 {
		t.Errorf("DueSchedules after claim = %+v", due)
	}
}

func TestClaimScheduleConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tend.db")
	ctx := context.Background()
	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	sc, now := dueSchedule(t, s1)

	claims := make(chan workflow.ScheduleClaim, 2)
	errs := make(chan error, 2)
	for _, st := range []*Store{s1, s2} {
		go func() {
			c, err := st.ClaimSchedule(ctx, sc.ID, now)
			claims <- c
			errs <- err
		}()
	}
	won := 0
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("ClaimSchedule: %v", err)
		}
		if c := <-claims; c.Claimed {
			won++
		}
	}
	if won != 1 {
		t.Errorf("%d claims won, want 1", won)
	}
	runs, err := s1.ListRunsForTask(ctx, sc.TaskID)
	if err != nil || len(runs) != 1 {
		t.Errorf("runs = %d, %v, want 1", len(runs), err)
	}
}

func TestClaimScheduleSkipsWhileRunLive(t *testing.T) {
	for _, live := range []workflow.RunState{workflow.RunPending, workflow.RunRunning, workflow.RunWaitingReview, workflow.RunPaused} {
		t.Run(string(live), func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			sc, now := dueSchedule(t, s)
			first, err := s.ClaimSchedule(ctx, sc.ID, now)
			if err != nil || !first.Claimed {
				t.Fatalf("claim = %+v, %v", first, err)
			}
			if live != workflow.RunPending {
				if err := s.SetRunState(ctx, first.Run.ID, live); err != nil {
					t.Fatalf("SetRunState: %v", err)
				}
			}
			next := now.Add(24 * time.Hour)
			skipped, err := s.ClaimSchedule(ctx, sc.ID, next)
			if err != nil || skipped.Claimed || skipped.Skipped == "" {
				t.Fatalf("claim while live = %+v, %v, want skipped", skipped, err)
			}
			got, _ := s.GetSchedule(ctx, sc.ID)
			if !got.NextRunAt.After(next) {
				t.Errorf("NextRunAt = %v, want advanced past %v", got.NextRunAt, next)
			}
			if err := s.SetRunState(ctx, first.Run.ID, workflow.RunCancelled); err != nil {
				t.Fatalf("SetRunState: %v", err)
			}
			again, err := s.ClaimSchedule(ctx, sc.ID, next.Add(24*time.Hour))
			if err != nil || !again.Claimed || again.Run.ID == first.Run.ID {
				t.Errorf("claim after terminal = %+v, %v, want a new run", again, err)
			}
		})
	}
}

func TestStartScheduleNowAndRunContext(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sc, _ := dueSchedule(t, s)

	if _, ok, err := s.ScheduleRunContext(ctx, mustRun(t, s, sc.WorkflowID).ID); err != nil || ok {
		t.Errorf("unscheduled run context ok = %v, %v, want false", ok, err)
	}

	r1, err := s.StartScheduleNow(ctx, sc.ID)
	if err != nil || r1.State != workflow.RunPending {
		t.Fatalf("StartScheduleNow = %+v, %v", r1, err)
	}
	if _, err := s.StartScheduleNow(ctx, sc.ID); !errors.Is(err, workflow.ErrScheduleBusy) {
		t.Errorf("second start err = %v, want ErrScheduleBusy", err)
	}
	if got, _ := s.GetSchedule(ctx, sc.ID); !got.NextRunAt.Equal(sc.NextRunAt) {
		t.Errorf("run-now moved next_run_at to %v", got.NextRunAt)
	}

	// Give r1 a finished step with a deliverable, end it, then run again.
	step := mustStep(t, s, sc.WorkflowID, "work")
	sr, err := s.CreateStepRun(ctx, workflow.StepRun{RunID: r1.ID, StepID: step.ID})
	if err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}
	if err := s.FinishStepRun(ctx, sr.ID, "done", "all quiet"); err != nil {
		t.Fatalf("FinishStepRun: %v", err)
	}
	if err := s.SetRunState(ctx, r1.ID, workflow.RunDone); err != nil {
		t.Fatalf("SetRunState: %v", err)
	}
	if err := s.SetScheduleEnabled(ctx, sc.ID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	r2, err := s.StartScheduleNow(ctx, sc.ID) // works while paused
	if err != nil {
		t.Fatalf("StartScheduleNow on paused schedule: %v", err)
	}

	rc, ok, err := s.ScheduleRunContext(ctx, r2.ID)
	if err != nil || !ok {
		t.Fatalf("ScheduleRunContext = %v, %v", ok, err)
	}
	if rc.Schedule.ID != sc.ID || rc.Previous == nil || rc.Previous.ID != r1.ID || rc.PreviousDeliverable != "all quiet" {
		t.Errorf("context = %+v", rc)
	}
	first, _, _ := s.ScheduleRunContext(ctx, r1.ID)
	if first.Previous != nil || first.PreviousDeliverable != "" {
		t.Errorf("first run context = %+v, want no previous", first)
	}
}
