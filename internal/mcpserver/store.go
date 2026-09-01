// Package mcpserver exposes a task's read/write surface to a launched
// Claude Code session over the Model Context Protocol, so a session bound
// to a task can turn "here's what I'm doing" into real tend rows instead
// of an ad-hoc scratch markdown file. It is the third consumer of Store,
// alongside tui and cli.
package mcpserver

import (
	"context"

	"github.com/jwstover/tend/internal/task"
)

// Store is the slice of the persistence layer the MCP tool surface
// needs — the same "accept interfaces, return structs" convention as
// cli.Store (internal/cli/root.go), kept as its own interface because
// `tend mcp` needs a different method set than the rest of the command
// tree.
type Store interface {
	GetTask(ctx context.Context, id int64) (task.Task, error)
	ListChildren(ctx context.Context, parentID int64) ([]task.Task, error)
	AddTaskWithBody(ctx context.Context, title, body string) (task.Task, error)
	AddChild(ctx context.Context, parentID int64, title string) (task.Task, error)
	SetBody(ctx context.Context, id int64, body string) error
	SetState(ctx context.Context, id int64, st task.State) error
	SetTags(ctx context.Context, taskID int64, tags []string) error
	TagsForTask(ctx context.Context, taskID int64) ([]string, error)
	SetProject(ctx context.Context, taskID, projectID int64) error
	GetProject(ctx context.Context, id int64) (task.Project, error)
	ProjectByName(ctx context.Context, name string) (task.Project, error)
	ListProjects(ctx context.Context) ([]task.Project, error)
	SetPriority(ctx context.Context, id int64, p *int64) error
	SetDue(ctx context.Context, id int64, due *string) error
	AddLogEntry(ctx context.Context, taskID *int64, body string) (task.LogEntry, error)
	Close() error
}
