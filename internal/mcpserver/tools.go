package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/task"
)

// taskOut is a task rendered for a tool response: the fields an agent
// needs to see, JSON-tagged for the MCP wire format rather than reusing
// task.Task's Go-facing shape directly.
type taskOut struct {
	ID       int64   `json:"id"`
	Title    string  `json:"title"`
	BodyMD   string  `json:"body_md"`
	State    string  `json:"state"`
	ParentID *int64  `json:"parent_id,omitempty"`
	Project  *string `json:"project,omitempty"`
	Priority *int64  `json:"priority,omitempty"`
	Due      *string `json:"due,omitempty"`
}

func toTaskOut(t task.Task) taskOut {
	return taskOut{
		ID:       t.ID,
		Title:    t.Title,
		BodyMD:   t.BodyMD,
		State:    string(t.State),
		ParentID: t.ParentID,
		Project:  t.Project,
		Priority: t.Priority,
		Due:      t.Due,
	}
}

// logOut is an added log entry rendered for a tool response.
type logOut struct {
	ID     int64  `json:"id"`
	TaskID *int64 `json:"task_id,omitempty"`
	Body   string `json:"body"`
}

// registerTools wires the nine tools of docs/agent-sessions-plan.md §9.3
// onto srv. Every mutating tool accepts an explicit task_id override —
// the bound task is a convenience default, not a hard sandbox (tend is
// single-user/local, so the risk being managed is an agent editing the
// wrong task from a guessed id, not an isolation boundary).
func registerTools(srv *mcp.Server, store Store, boundTaskID int64) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_current_task",
		Description: "Get the task this session is bound to.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, taskOut, error) {
		return fetchTask(ctx, store, boundTaskID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_task",
		Description: "Get any task by id, to inspect it before making changes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		TaskID int64 `json:"task_id" jsonschema:"the task id to look up"`
	}) (*mcp.CallToolResult, taskOut, error) {
		return fetchTask(ctx, store, in.TaskID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_subtasks",
		Description: "List the sub-tasks of a task; defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		TaskID *int64 `json:"task_id,omitempty" jsonschema:"parent task id; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, []taskOut, error) {
		children, err := store.ListChildren(ctx, resolveID(in.TaskID, boundTaskID))
		if err != nil {
			return nil, nil, err
		}
		out := make([]taskOut, len(children))
		for i, c := range children {
			out[i] = toTaskOut(c)
		}
		return nil, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_task",
		Description: "Create a new top-level task — NOT scoped to the bound task. Use for a " +
			"genuinely separate work item; use create_subtask for phases of the current task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Title  string `json:"title" jsonschema:"the task title"`
		BodyMD string `json:"body_md,omitempty" jsonschema:"optional markdown body"`
	}) (*mcp.CallToolResult, taskOut, error) {
		t, err := store.AddTaskWithBody(ctx, in.Title, in.BodyMD)
		if err != nil {
			return nil, taskOut{}, err
		}
		return nil, toTaskOut(t), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_subtask",
		Description: "Create a sub-task; parent defaults to the bound task — the way to split " +
			"the current task's work into phases.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Title    string `json:"title" jsonschema:"the sub-task title"`
		ParentID *int64 `json:"parent_id,omitempty" jsonschema:"parent task id; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		t, err := store.AddChild(ctx, resolveID(in.ParentID, boundTaskID), in.Title)
		if err != nil {
			return nil, taskOut{}, err
		}
		return nil, toTaskOut(t), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_task_body",
		Description: "Replace a task's markdown body; defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		BodyMD string `json:"body_md" jsonschema:"the new markdown body, replacing the existing one"`
		TaskID *int64 `json:"task_id,omitempty" jsonschema:"task id to update; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		if err := store.SetBody(ctx, id, in.BodyMD); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_task_state",
		Description: "Move a task to a new workflow state: todo, doing, blocked, done, or " +
			"someday. Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		State  string `json:"state" jsonschema:"one of: todo, doing, blocked, done, someday"`
		TaskID *int64 `json:"task_id,omitempty" jsonschema:"task id to update; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		if err := store.SetState(ctx, id, task.State(in.State)); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "set_task_project",
		Description: "Set a task's project; omit or send empty to clear. Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Project string `json:"project,omitempty" jsonschema:"project name; omit or empty to clear"`
		TaskID  *int64 `json:"task_id,omitempty" jsonschema:"task id to update; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		var p *string
		if in.Project != "" {
			p = &in.Project
		}
		if err := store.SetProject(ctx, id, p); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_task_priority",
		Description: "Set a task's priority, 1 (highest) through 4 (lowest); omit to clear. " +
			"Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Priority *int64 `json:"priority,omitempty" jsonschema:"1 (highest) through 4 (lowest); omit to clear"`
		TaskID   *int64 `json:"task_id,omitempty" jsonschema:"task id to update; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		if err := store.SetPriority(ctx, id, in.Priority); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "set_task_due",
		Description: "Set a task's due date (YYYY-MM-DD); omit or send empty to clear. Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Due    string `json:"due,omitempty" jsonschema:"ISO 8601 date YYYY-MM-DD; omit or empty to clear"`
		TaskID *int64 `json:"task_id,omitempty" jsonschema:"task id to update; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		var d *string
		if in.Due != "" {
			d = &in.Due
		}
		if err := store.SetDue(ctx, id, d); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "add_log_entry",
		Description: "Add a standup-style note to a task's log — the direct replacement for a " +
			"scratch doc's running notes. Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Body   string `json:"body" jsonschema:"the note text"`
		TaskID *int64 `json:"task_id,omitempty" jsonschema:"task id to attach the note to; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, logOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		entry, err := store.AddLogEntry(ctx, &id, in.Body)
		if err != nil {
			return nil, logOut{}, err
		}
		return nil, logOut{ID: entry.ID, TaskID: entry.TaskID, Body: entry.Body}, nil
	})
}

// resolveID returns override when the caller supplied one, else def —
// the "defaults to the bound task, but every mutating tool still
// accepts an explicit override" rule from §9.3.
func resolveID(override *int64, def int64) int64 {
	if override != nil {
		return *override
	}
	return def
}

// fetchTask loads and renders a task, the common tail of every tool
// that reports a task's post-mutation state.
func fetchTask(ctx context.Context, store Store, id int64) (*mcp.CallToolResult, taskOut, error) {
	t, err := store.GetTask(ctx, id)
	if err != nil {
		return nil, taskOut{}, err
	}
	return nil, toTaskOut(t), nil
}
