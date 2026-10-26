package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
)

func seedScheduleStore(t *testing.T) *fakeStore {
	t.Helper()
	s := newFakeStore(task.Task{ID: 1, Title: "bound", ProjectID: task.DefaultProjectID})
	s.projects[0].Cwd = "/repo"
	s.workflows[1] = workflow.Workflow{ID: 1, Name: "Watch CI"}
	s.steps[10] = workflow.Step{ID: 10, WorkflowID: 1, Name: "watch", Kind: workflow.StepAgent, PermissionMode: "bypassPermissions"}
	return s
}

func TestOrdinarySessionListsScheduleTools(t *testing.T) {
	cs := dial(t, seedScheduleStore(t), 1)
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tl := range res.Tools {
		have[tl.Name] = true
	}
	for _, n := range []string{"list_schedules", "create_schedule", "update_schedule", "delete_schedule"} {
		if !have[n] {
			t.Errorf("tool %s not listed", n)
		}
	}
}

func TestScheduleLifecycle(t *testing.T) {
	cs := dial(t, seedScheduleStore(t), 1)

	got := callTool[scheduleOut](t, cs, "create_schedule", map[string]any{
		"name": "ci watch", "workflow": "watch ci", "cron": "*/15 * * * *",
	})
	if got.Workflow != "Watch CI" || got.TaskID != 1 || got.Cwd != "/repo" || !got.Enabled {
		t.Errorf("created = %+v, want workflow Watch CI, task 1, cwd /repo, enabled", got)
	}
	next, err := time.Parse(time.RFC3339, got.NextRunAt)
	if err != nil || !next.After(time.Now()) {
		t.Errorf("next_run_at = %q (%v), want a future RFC3339 time", got.NextRunAt, err)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "bypassPermissions") {
		t.Errorf("warnings = %v, want one bypassPermissions warning", got.Warnings)
	}

	// Pausing changes only enabled.
	up := callTool[scheduleOut](t, cs, "update_schedule", map[string]any{"id": got.ID, "enabled": false})
	if up.Enabled || up.NextRunAt != "" || up.Cron != got.Cron || up.Name != "ci watch" || up.Cwd != "/repo" {
		t.Errorf("paused = %+v, want only enabled changed and no next_run_at", up)
	}
	up = callTool[scheduleOut](t, cs, "update_schedule", map[string]any{"id": got.ID, "cron": "0 9 * * *", "name": "morning"})
	if up.Cron != "0 9 * * *" || up.Name != "morning" || up.Enabled {
		t.Errorf("updated = %+v, want new cron and name, still paused", up)
	}

	list := callTool[schedulesOut](t, cs, "list_schedules", nil)
	if len(list.Schedules) != 1 || list.Schedules[0].Name != "morning" {
		t.Errorf("list = %+v, want the one renamed schedule", list.Schedules)
	}

	del := callTool[deletedScheduleOut](t, cs, "delete_schedule", map[string]any{"id": got.ID})
	if del.Deleted != got.ID {
		t.Errorf("deleted = %d, want %d", del.Deleted, got.ID)
	}
	list = callTool[schedulesOut](t, cs, "list_schedules", nil)
	if len(list.Schedules) != 0 {
		t.Errorf("list after delete = %+v, want empty", list.Schedules)
	}
}

func TestCreateScheduleErrors(t *testing.T) {
	s := seedScheduleStore(t)
	cs := dial(t, s, 1)
	if msg := callToolErr(t, cs, "create_schedule", map[string]any{"name": "a", "workflow": "watch ci", "cron": "nope"}); !strings.Contains(msg, "invalid cron") {
		t.Errorf("bad cron error = %q", msg)
	}
	if msg := callToolErr(t, cs, "create_schedule", map[string]any{"name": "a", "workflow": "missing", "cron": "* * * * *"}); !strings.Contains(msg, "not found") {
		t.Errorf("unknown workflow error = %q", msg)
	}
	s.projects[0].Cwd = ""
	if msg := callToolErr(t, cs, "create_schedule", map[string]any{"name": "a", "workflow": "1", "cron": "* * * * *"}); !strings.Contains(msg, "cwd is required") {
		t.Errorf("no cwd error = %q", msg)
	}
	// An explicit cwd and workflow id work without a project default.
	got := callTool[scheduleOut](t, cs, "create_schedule", map[string]any{"name": "a", "workflow": "1", "cron": "* * * * *", "cwd": "/x"})
	if got.Cwd != "/x" || got.WorkflowID != 1 {
		t.Errorf("created = %+v", got)
	}
}
