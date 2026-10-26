package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/runner"
	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/workflow"
)

// mustSchedule creates a workflow, a task and a schedule over them.
func mustSchedule(t *testing.T, s *store.Store, name string) workflow.Schedule {
	t.Helper()
	ctx := context.Background()
	wf, err := s.CreateWorkflow(ctx, "wf-"+name, "")
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	tk, err := s.AddTask(ctx, "task for "+name)
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	sc, err := s.CreateSchedule(ctx, workflow.Schedule{Name: name, WorkflowID: wf.ID, TaskID: tk.ID,
		Cwd: t.TempDir(), CronExpr: "*/15 * * * *", Overlap: workflow.OverlapSkip}, time.Now())
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	return sc
}

func TestSchedulesViewListsTogglesAndLeaves(t *testing.T) {
	m, s := newTestApp(t)
	sc := mustSchedule(t, s, "nightly")

	m = drive(t, m, keyPress('X'))
	a := m.(app)
	if a.mode != modeSchedules || len(a.sv.rows) != 1 {
		t.Fatalf("mode %v rows %d; want the schedules view with one row", a.mode, len(a.sv.rows))
	}
	view := ansi.Strip(a.View().Content)
	for _, want := range []string{"nightly", "wf-nightly", "task for nightly", "*/15 * * * *", "in "} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}

	m = drive(t, m, keyPress('p'))
	got, _ := s.GetSchedule(context.Background(), sc.ID)
	if got.Enabled {
		t.Fatal("p did not pause the schedule")
	}
	if view := ansi.Strip(m.(app).View().Content); !strings.Contains(view, "paused") {
		t.Errorf("next-run column does not say paused:\n%s", view)
	}
	m = drive(t, m, keyPress('p'))
	if got, _ := s.GetSchedule(context.Background(), sc.ID); !got.Enabled {
		t.Fatal("second p did not resume")
	}

	m = drive(t, m, keyPress('X'))
	if m.(app).mode != modeList {
		t.Fatalf("X did not return to the list, mode %v", m.(app).mode)
	}
}

func TestSchedulesViewOpensLastRunAndReturns(t *testing.T) {
	m, s := newTestApp(t)
	sc := mustSchedule(t, s, "weekly")
	run, err := s.StartScheduleNow(context.Background(), sc.ID)
	if err != nil {
		t.Fatalf("StartScheduleNow: %v", err)
	}

	m = drive(t, m, keyPress('X'))
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	a := m.(app)
	if a.mode != modeRun || a.rv.runID != run.ID {
		t.Fatalf("mode %v run %d; want run view on %d", a.mode, a.rv.runID, run.ID)
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.(app).mode != modeSchedules {
		t.Fatalf("leaving the run view landed in mode %v, want schedules", m.(app).mode)
	}
}

func TestSchedulesViewRunNowBusyFlashes(t *testing.T) {
	m, s := newTestApp(t)
	sc := mustSchedule(t, s, "busy")
	if _, err := s.StartScheduleNow(context.Background(), sc.ID); err != nil {
		t.Fatalf("StartScheduleNow: %v", err)
	}
	m = drive(t, m, keyPress('X'))
	m = drive(t, m, keyPress('r'))
	if txt := m.(app).status.text; !strings.Contains(txt, "still live") {
		t.Errorf("status = %q, want the busy flash", txt)
	}
}

func TestScheduleTickerFiresDueSchedules(t *testing.T) {
	_, s := newTestApp(t)
	sc := mustSchedule(t, s, "due")
	// Make it due: next_run_at is in the past once resumed from a past now.
	if err := s.SetScheduleEnabled(context.Background(), sc.ID, true, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatalf("SetScheduleEnabled: %v", err)
	}

	var launched atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	launch := runner.LaunchFunc(func(_ context.Context, runID int64) (string, error) {
		launched.Store(runID)
		return "tend-wf-test", nil
	})
	go runScheduleTicker(ctx, s, launch, 20*time.Millisecond)

	deadline := time.Now().Add(3 * time.Second)
	for launched.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if launched.Load() == 0 {
		t.Fatal("ticker never launched the due schedule")
	}
}
