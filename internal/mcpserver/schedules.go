package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/workflow"
)

// The schedule authoring surface: an agent session can draft a schedule
// (a workflow fired on a task by a cron expression) the way it can draft
// a workflow. Registered for every session. There is deliberately no
// run-now tool: the user fires runs from the CLI or TUI.

// scheduleOut is one schedule as the tools show it.
type scheduleOut struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	WorkflowID int64  `json:"workflow_id"`
	Workflow   string `json:"workflow"`
	TaskID     int64  `json:"task_id"`
	Cron       string `json:"cron"`
	Cwd        string `json:"cwd"`
	Enabled    bool   `json:"enabled"`
	// NextRunAt is RFC3339 in local time; "" while the schedule is paused.
	NextRunAt string   `json:"next_run_at"`
	LastRunID *int64   `json:"last_run_id"`
	Warnings  []string `json:"warnings,omitempty"`
}

// schedulesOut wraps the list in an object (see subtasksOut).
type schedulesOut struct {
	Schedules []scheduleOut `json:"schedules"`
}

type deletedScheduleOut struct {
	Deleted int64 `json:"deleted"`
}

// toScheduleOut renders a schedule, naming its workflow and, when
// withWarnings, flagging risky steps.
func toScheduleOut(ctx context.Context, store Store, sc workflow.Schedule, withWarnings bool) (scheduleOut, error) {
	out := scheduleOut{
		ID: sc.ID, Name: sc.Name, WorkflowID: sc.WorkflowID, TaskID: sc.TaskID,
		Cron: sc.CronExpr, Cwd: sc.Cwd, Enabled: sc.Enabled, LastRunID: sc.LastRunID,
	}
	if sc.Enabled && !sc.NextRunAt.IsZero() {
		out.NextRunAt = sc.NextRunAt.Local().Format(time.RFC3339)
	}
	wf, err := store.GetWorkflow(ctx, sc.WorkflowID)
	if err != nil {
		return scheduleOut{}, err
	}
	out.Workflow = wf.Name
	if withWarnings {
		steps, err := store.ListSteps(ctx, sc.WorkflowID)
		if err != nil {
			return scheduleOut{}, err
		}
		out.Warnings = workflow.ScheduleWarnings(steps)
	}
	return out, nil
}

// scheduleWorkflow finds a workflow by case-insensitive name, then by id.
func scheduleWorkflow(ctx context.Context, store Store, ref string) (workflow.Workflow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return workflow.Workflow{}, errors.New("workflow is required: an id or a name")
	}
	wf, err := store.WorkflowByName(ctx, ref)
	if err == nil {
		return wf, nil
	}
	if !errors.Is(err, workflow.ErrWorkflowNotFound) {
		return workflow.Workflow{}, err
	}
	if id, perr := strconv.ParseInt(ref, 10, 64); perr == nil {
		return store.GetWorkflow(ctx, id)
	}
	return workflow.Workflow{}, fmt.Errorf("workflow %q: %w", ref, err)
}

const scheduleWatcherAdvice = "A schedule fires its workflow on the task on a 5-field cron " +
	"expression (minute hour day-of-month month day-of-week, local time); each run lands in " +
	"the task's WORKFLOWS section. A scheduled run is unattended, so a watcher should use a " +
	"cheap model and a non-bypass permission mode."

func registerScheduleTools(srv *mcp.Server, store Store, taskID int64) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_schedules",
		Description: "List every schedule by name: its workflow, task, cron, cwd, whether it is enabled, and when it next fires. " + scheduleWatcherAdvice,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, schedulesOut, error) {
		list, err := store.ListSchedules(ctx)
		if err != nil {
			return nil, schedulesOut{}, err
		}
		out := schedulesOut{Schedules: make([]scheduleOut, 0, len(list))}
		for _, sc := range list {
			so, err := toScheduleOut(ctx, store, sc, false)
			if err != nil {
				return nil, schedulesOut{}, err
			}
			out.Schedules = append(out.Schedules, so)
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_schedule",
		Description: "Create an enabled schedule that runs a workflow on a task on a cron. " +
			scheduleWatcherAdvice + " task_id defaults to the current task; cwd defaults to the " +
			"task's project directory. The result carries next_run_at and a warning for each step " +
			"that runs with bypassPermissions.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Name     string  `json:"name" jsonschema:"unique schedule name"`
		Workflow string  `json:"workflow" jsonschema:"workflow id or (case-insensitive) name"`
		Cron     string  `json:"cron" jsonschema:"5-field cron expression, local time, e.g. '0 9 * * 1-5'"`
		TaskID   *int64  `json:"task_id,omitempty" jsonschema:"task the runs belong to; defaults to the current task"`
		Cwd      *string `json:"cwd,omitempty" jsonschema:"working directory for runs; defaults to the task's project directory"`
	}) (*mcp.CallToolResult, scheduleOut, error) {
		wf, err := scheduleWorkflow(ctx, store, in.Workflow)
		if err != nil {
			return nil, scheduleOut{}, err
		}
		id := taskID
		if in.TaskID != nil {
			id = *in.TaskID
		}
		t, err := store.GetTask(ctx, id)
		if err != nil {
			return nil, scheduleOut{}, err
		}
		cwd := ""
		if in.Cwd != nil {
			cwd = strings.TrimSpace(*in.Cwd)
		}
		if cwd == "" {
			p, err := store.GetProject(ctx, t.ProjectID)
			if err != nil {
				return nil, scheduleOut{}, err
			}
			cwd = p.Cwd
		}
		if cwd == "" {
			return nil, scheduleOut{}, errors.New("cwd is required: the task's project has no default directory")
		}
		sc, err := store.CreateSchedule(ctx, workflow.Schedule{
			Name: in.Name, WorkflowID: wf.ID, TaskID: id, Cwd: cwd, CronExpr: in.Cron,
		}, time.Now())
		if err != nil {
			return nil, scheduleOut{}, err
		}
		out, err := toScheduleOut(ctx, store, sc, true)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "update_schedule",
		Description: "Change a schedule's name, cron, cwd or enabled flag; only the fields given change. " +
			"enabled:false pauses it, enabled:true resumes it from now (no catch-up run). " + scheduleWatcherAdvice,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ID      int64   `json:"id" jsonschema:"schedule id"`
		Name    *string `json:"name,omitempty" jsonschema:"new name"`
		Cron    *string `json:"cron,omitempty" jsonschema:"new 5-field cron expression"`
		Cwd     *string `json:"cwd,omitempty" jsonschema:"new working directory"`
		Enabled *bool   `json:"enabled,omitempty" jsonschema:"false pauses, true resumes"`
	}) (*mcp.CallToolResult, scheduleOut, error) {
		sc, err := store.GetSchedule(ctx, in.ID)
		if err != nil {
			return nil, scheduleOut{}, err
		}
		if in.Name != nil {
			sc.Name = *in.Name
		}
		if in.Cron != nil {
			sc.CronExpr = *in.Cron
		}
		if in.Cwd != nil {
			sc.Cwd = strings.TrimSpace(*in.Cwd)
			if sc.Cwd == "" {
				return nil, scheduleOut{}, errors.New("cwd must not be empty")
			}
		}
		if in.Enabled != nil {
			sc.Enabled = *in.Enabled
		}
		sc, err = store.UpdateSchedule(ctx, sc, time.Now())
		if err != nil {
			return nil, scheduleOut{}, err
		}
		out, err := toScheduleOut(ctx, store, sc, true)
		return nil, out, err
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_schedule",
		Description: "Delete a schedule. Runs it already started are kept.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ID int64 `json:"id" jsonschema:"schedule id"`
	}) (*mcp.CallToolResult, deletedScheduleOut, error) {
		if err := store.DeleteSchedule(ctx, in.ID); err != nil {
			return nil, deletedScheduleOut{}, err
		}
		return nil, deletedScheduleOut{Deleted: in.ID}, nil
	})
}
