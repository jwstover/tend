package mcpserver

import (
	"context"
	"fmt"
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
	// DependsOn is what this task waits on; Blocks is the reverse edge,
	// the tasks waiting on this one. IsBlocked is derived rather than
	// left to the agent: true while any depends_on task is not done, so
	// "can I start this?" needs no second look at each blocker's state.
	DependsOn []depOut `json:"depends_on,omitempty"`
	Blocks    []depOut `json:"blocks,omitempty"`
	IsBlocked bool     `json:"is_blocked"`
}

// depOut is the far end of a dependency edge: enough to name the task
// and see whether it is done, without nesting a whole taskOut.
type depOut struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

func toTaskOut(t task.Task, tags []string, blockers, blocking []task.Task) taskOut {
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
		DependsOn: toDepOuts(blockers),
		Blocks:    toDepOuts(blocking),
		IsBlocked: len(task.OpenBlockers(blockers)) > 0,
	}
}

func toDepOuts(ts []task.Task) []depOut {
	if len(ts) == 0 {
		return nil
	}
	out := make([]depOut, len(ts))
	for i, t := range ts {
		out[i] = depOut{ID: t.ID, Title: t.Title, State: string(t.State)}
	}
	return out
}

// projectOut is a project rendered for a tool response.
type projectOut struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Tasks    int64  `json:"live_task_count"`
	Cwd      string `json:"cwd,omitempty"` // default working directory for new sessions, if set
	Archived bool   `json:"archived,omitempty"`
}

// projectsOut wraps the project list in an object, for the same reason
// subtasksOut does: MCP's outputSchema describes an object, so a bare
// slice generates a top-level array schema that clients reject.
type projectsOut struct {
	Projects []projectOut `json:"projects"`
}

func toProjectOut(p task.Project) projectOut {
	return projectOut{ID: p.ID, Name: p.Name, Tasks: p.LiveCount, Cwd: p.Cwd, Archived: p.Archived()}
}

// subtasksOut wraps the sub-task list in an object: MCP's outputSchema
// describes the structuredContent object, so a bare slice generates a
// top-level array schema that clients reject.
type subtasksOut struct {
	Tasks []taskOut `json:"tasks"`
}

// registerTools wires tend's MCP tool surface onto srv. Every mutating
// tool accepts an explicit task_id override —
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
			_, o, err := fetchTask(ctx, store, c.ID)
			if err != nil {
				return nil, subtasksOut{}, err
			}
			out[i] = o
		}
		return nil, subtasksOut{Tasks: out}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_task",
		Description: "Create a new top-level task — NOT scoped to the bound task. Use for a " +
			"genuinely separate work item; use create_subtask for phases of the current task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Title     string  `json:"title" jsonschema:"the task title"`
		BodyMD    string  `json:"body_md,omitempty" jsonschema:"optional markdown body"`
		DependsOn []int64 `json:"depends_on,omitempty" jsonschema:"ids of tasks this one must wait for; each must be done before this task can be worked on"`
	}) (*mcp.CallToolResult, taskOut, error) {
		t, err := store.AddTaskWithBody(ctx, in.Title, in.BodyMD)
		if err != nil {
			return nil, taskOut{}, err
		}
		if err := setInitialDependencies(ctx, store, t.ID, in.DependsOn); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, t.ID)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_subtask",
		Description: "Create a sub-task; parent defaults to the bound task — the way to split " +
			"the current task's work into phases.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Title     string  `json:"title" jsonschema:"the sub-task title"`
		ParentID  *int64  `json:"parent_id,omitempty" jsonschema:"parent task id; defaults to the session's bound task"`
		DependsOn []int64 `json:"depends_on,omitempty" jsonschema:"ids of tasks this sub-task must wait for (often earlier phases); each must be done before it can be worked on"`
	}) (*mcp.CallToolResult, taskOut, error) {
		t, err := store.AddChild(ctx, resolveID(in.ParentID, boundTaskID), in.Title)
		if err != nil {
			return nil, taskOut{}, err
		}
		if err := setInitialDependencies(ctx, store, t.ID, in.DependsOn); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, t.ID)
	})

	// A rename is not a state change: the store trims and refuses a blank
	// title (task.NormalizeTitle, the same rule capture and the TUI's `R`
	// apply) and writes nothing to task_events.
	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_task_title",
		Description: "Rename a task. The title is trimmed; an empty or whitespace-only title " +
			"is refused. Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Title  string `json:"title" jsonschema:"the new title; leading and trailing whitespace is trimmed"`
		TaskID *int64 `json:"task_id,omitempty" jsonschema:"task id to rename; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		if err := store.SetTitle(ctx, id, in.Title); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})

	// The only free-form writes an agent gets. There is deliberately no
	// add_log_entry tool: log entries are the user's standup notes, so an
	// agent that wants to leave a record on a task edits its body instead.
	mcp.AddTool(srv, &mcp.Tool{
		Name: "update_task_body",
		Description: "Replace a task's markdown body; defaults to the bound task. This is the " +
			"place to record progress, links, or a summary of the work — log entries are " +
			"reserved for the user. To add to the body without rewriting it, use " +
			"append_task_body instead.",
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

	// The incremental sibling of update_task_body: most agent updates are
	// "add a PR link" or "note what changed", and re-sending the whole
	// body for those risks clobbering edits made since it was last read.
	mcp.AddTool(srv, &mcp.Tool{
		Name: "append_task_body",
		Description: "Append markdown to the end of a task's body as a new paragraph, keeping " +
			"what is already there; defaults to the bound task. Prefer this over " +
			"update_task_body for adding a link, a progress note, or a summary.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Text   string `json:"text" jsonschema:"markdown to append; a blank line is inserted before it when the body is non-empty"`
		TaskID *int64 `json:"task_id,omitempty" jsonschema:"task id to update; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		if err := store.AppendBody(ctx, id, in.Text); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_task_state",
		Description: "Move a task to a new workflow state: todo, doing, review, blocked, done, or " +
			"someday. Use review once the work is handed off and waiting on someone else " +
			"(a PR out for review). Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		State  string `json:"state" jsonschema:"one of: todo, doing, review, blocked, done, someday"`
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

	// parent_id is a plain int64 with 0 meaning "top level", not a *int64:
	// over MCP a pointer cannot tell an explicit null from an omitted
	// field, and "promote to the top level" has to be a deliberate request,
	// not the accident of forgetting the argument.
	mcp.AddTool(srv, &mcp.Tool{
		Name: "move_task",
		Description: "Move a task, and its whole sub-tree, under another task as a sub-task, " +
			"or to the top level by sending parent_id 0. A task cannot be moved under " +
			"itself or under one of its own sub-tasks. If the new parent is in a " +
			"different project, the sub-tree moves into that project too. Defaults to " +
			"the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ParentID int64  `json:"parent_id" jsonschema:"id of the task to become the parent; 0 moves the task to the top level"`
		TaskID   *int64 `json:"task_id,omitempty" jsonschema:"task id to move; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		var parent *int64
		if in.ParentID != 0 {
			parent = &in.ParentID
		}
		if err := store.SetParent(ctx, id, parent); err != nil {
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

	// Dependencies: "this task waits on that one". The wholesale form
	// mirrors set_task_tags, the single-edge forms mirror the relationship
	// append_task_body has to update_task_body -- most agent edits are
	// "this also needs #12 first", and re-sending the list for that risks
	// dropping an edge the user added since it was last read.
	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_task_dependencies",
		Description: "Replace the list of tasks a task waits on (its blockers): each must be done " +
			"before the task can be worked on. Send an empty list to clear every dependency. " +
			"A task cannot depend on itself or on a task that already waits on it, directly or " +
			"indirectly. Defaults to the bound task. Dependencies do not change the task's " +
			"state; is_blocked on the returned task says whether any blocker is still open.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		DependsOn []int64 `json:"depends_on" jsonschema:"the complete list of task ids this task waits on; empty clears every dependency"`
		TaskID    *int64  `json:"task_id,omitempty" jsonschema:"task id to update; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		if err := store.SetDependencies(ctx, id, in.DependsOn); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "add_task_dependency",
		Description: "Record that a task waits on one more task (depends_on must be done before " +
			"it can be worked on), keeping its other dependencies. Prefer this over " +
			"set_task_dependencies for adding a single blocker. Refused for a self-dependency " +
			"or one that would form a cycle. Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		DependsOn int64  `json:"depends_on" jsonschema:"id of the task that must be done first"`
		TaskID    *int64 `json:"task_id,omitempty" jsonschema:"task id to update; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		if err := store.AddDependency(ctx, id, in.DependsOn); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "remove_task_dependency",
		Description: "Forget that a task waits on depends_on, keeping its other dependencies. " +
			"Removing a dependency that is not there is not an error. Defaults to the bound task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		DependsOn int64  `json:"depends_on" jsonschema:"id of the task it no longer waits on"`
		TaskID    *int64 `json:"task_id,omitempty" jsonschema:"task id to update; defaults to the session's bound task"`
	}) (*mcp.CallToolResult, taskOut, error) {
		id := resolveID(in.TaskID, boundTaskID)
		if err := store.RemoveDependency(ctx, id, in.DependsOn); err != nil {
			return nil, taskOut{}, err
		}
		return fetchTask(ctx, store, id)
	})
}

// setInitialDependencies applies a create tool's optional depends_on
// list to the task it just made. The task row is already written when a
// bad id is refused, so the error says so: the agent should fix the
// dependencies with set_task_dependencies, not create the task again.
func setInitialDependencies(ctx context.Context, store Store, id int64, dependsOn []int64) error {
	if len(dependsOn) == 0 {
		return nil
	}
	if err := store.SetDependencies(ctx, id, dependsOn); err != nil {
		return fmt.Errorf("task %d was created, but setting its dependencies failed: %w", id, err)
	}
	return nil
}

// resolveID returns override when the caller supplied one, else def —
// the "defaults to the bound task, but every mutating tool still
// accepts an explicit override" rule every tool follows.
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
	blockers, err := store.Blockers(ctx, id)
	if err != nil {
		return nil, taskOut{}, err
	}
	blocking, err := store.Blocking(ctx, id)
	if err != nil {
		return nil, taskOut{}, err
	}
	return nil, toTaskOut(t, tags, blockers, blocking), nil
}
