package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/task"
)

// taskOut is a task rendered for a tool response: the fields an agent
// needs to see, JSON-tagged for the MCP wire format rather than reusing
// task.Task's Go-facing shape directly.
type taskOut struct {
	ID        int64    `json:"id"`
	Title     string   `json:"title"`
	BodyMD    string   `json:"body_md"`
	State     string   `json:"state"`
	ParentID  *int64   `json:"parent_id,omitempty"`
	ProjectID int64    `json:"project_id"`
	Tags      []string `json:"tags,omitempty"`
	Priority  *int64   `json:"priority,omitempty"`
	Due       *string  `json:"due,omitempty"`
}

func toTaskOut(t task.Task, tags []string) taskOut {
	return taskOut{
		ID:        t.ID,
		Title:     t.Title,
		BodyMD:    t.BodyMD,
		State:     string(t.State),
		ParentID:  t.ParentID,
		ProjectID: t.ProjectID,
		Tags:      tags,
		Priority:  t.Priority,
		Due:       t.Due,
	}
}

// projectOut is a project rendered for a tool response.
type projectOut struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Tasks    int64  `json:"live_task_count"`
	Archived bool   `json:"archived,omitempty"`
}

// projectsOut wraps the project list in an object, for the same reason
// subtasksOut does: MCP's outputSchema describes an object, so a bare
// slice generates a top-level array schema that clients reject.
type projectsOut struct {
	Projects []projectOut `json:"projects"`
}

func toProjectOut(p task.Project) projectOut {
	return projectOut{ID: p.ID, Name: p.Name, Tasks: p.LiveCount, Archived: p.Archived()}
}

// logOut is an added log entry rendered for a tool response.
type logOut struct {
	ID     int64  `json:"id"`
	TaskID *int64 `json:"task_id,omitempty"`
	Body   string `json:"body"`
}

// subtasksOut wraps the sub-task list in an object: MCP's outputSchema
// describes the structuredContent object, so a bare slice generates a
// top-level array schema that clients reject.
type subtasksOut struct {
	Tasks []taskOut `json:"tasks"`
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
	}) (*mcp.CallToolResult, subtasksOut, error) {
		children, err := store.ListChildren(ctx, resolveID(in.TaskID, boundTaskID))
		if err != nil {
			return nil, subtasksOut{}, err
		}
		out := make([]taskOut, len(children))
		for i, c := range children {
			// Per-child rather than one batch map: a task's sub-tasks
			// number in the handful, and this is not a hot path the way
			// the TUI list is.
			tags, err := store.TagsForTask(ctx, c.ID)
			if err != nil {
				return nil, subtasksOut{}, err
			}
			out[i] = toTaskOut(c, tags)
		}
		return nil, subtasksOut{Tasks: out}, nil
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
		return nil, toTaskOut(t, nil), nil
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
		return nil, toTaskOut(t, nil), nil
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

	// The old set_task_project lives on here rather than as a project
	// tool: what it actually set was a free-text label, and labels are
	// tags now (docs/projects-plan.md §0). Moving a task between projects
	// is a separate tool, added with the rest of the project surface in
	// Phase 3.
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_current_project",
		Description: "Get the project the session's bound task belongs to. Every task belongs " +
			"to exactly one project.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		TaskID *int64 `json:"task_id,omitempty" jsonschema:"task id; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, projectOut, error) {
		t, err := store.GetTask(ctx, resolveID(in.TaskID, boundTaskID))
		if err != nil {
			return nil, projectOut{}, err
		}
		p, err := store.GetProject(ctx, t.ProjectID)
		if err != nil {
			return nil, projectOut{}, err
		}
		return nil, toProjectOut(p), nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_projects",
		Description: "List every project with its live task count.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, projectsOut, error) {
		projects, err := store.ListProjects(ctx)
		if err != nil {
			return nil, projectsOut{}, err
		}
		out := make([]projectOut, len(projects))
		for i, p := range projects {
			out[i] = toProjectOut(p)
		}
		return nil, projectsOut{Projects: out}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_task_project",
		Description: "Move a task, and its whole sub-tree, into a project named by " +
			"`project`. The project must already exist -- list_projects shows the " +
			"names. Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Project string `json:"project" jsonschema:"name of an existing project"`
		TaskID  *int64 `json:"task_id,omitempty" jsonschema:"task id to move; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		// Resolved by name, never created: an agent guessing at a project
		// name should get an error it can act on, not a new project.
		p, err := store.ProjectByName(ctx, in.Project)
		if err != nil {
			return nil, taskOut{}, err
		}
		if err := store.SetProject(ctx, id, p.ID); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_task_tags",
		Description: "Replace a task's tags with the given list; send an empty list to clear " +
			"them all. Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Tags   []string `json:"tags" jsonschema:"the complete tag list for the task; empty clears every tag"`
		TaskID *int64   `json:"task_id,omitempty" jsonschema:"task id to update; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		if err := store.SetTags(ctx, id, task.ParseTags(strings.Join(in.Tags, " "))); err != nil {
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
	tags, err := store.TagsForTask(ctx, id)
	if err != nil {
		return nil, taskOut{}, err
	}
	return nil, toTaskOut(t, tags), nil
}
