// Package mcpserver exposes a task's read/write surface to a launched
// Claude Code session over the Model Context Protocol, so a session bound
// to a task can turn "here's what I'm doing" into real tend rows instead
// of an ad-hoc scratch markdown file. It is the third consumer of Store,
// alongside tui and cli.
package mcpserver

import (
	"context"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/workflow"
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
	AppendBody(ctx context.Context, id int64, text string) error
	SetState(ctx context.Context, id int64, st task.State) error
	SetTags(ctx context.Context, taskID int64, tags []string) error
	TagsForTask(ctx context.Context, taskID int64) ([]string, error)
	SetProject(ctx context.Context, taskID, projectID int64) error
	SetParent(ctx context.Context, taskID int64, parentID *int64) error
	GetProject(ctx context.Context, id int64) (task.Project, error)
	ProjectByName(ctx context.Context, name string) (task.Project, error)
	ListProjects(ctx context.Context) ([]task.Project, error)
	SetPriority(ctx context.Context, id int64, p *int64) error
	SetDue(ctx context.Context, id int64, due *string) error

	// Task dependencies: "task waits on blocker". Blockers and Blocking
	// are read on every task response so an agent sees what is holding a
	// task up (and what it holds up) without a second call; the writes
	// back set_task_dependencies and its add/remove siblings. The store
	// owns the rules (no self-dependency, no cycle).
	Blockers(ctx context.Context, taskID int64) ([]task.Task, error)
	Blocking(ctx context.Context, taskID int64) ([]task.Task, error)
	SetDependencies(ctx context.Context, taskID int64, dependsOn []int64) error
	AddDependency(ctx context.Context, taskID, dependsOnID int64) error
	RemoveDependency(ctx context.Context, taskID, dependsOnID int64) error

	// The workflow step tools (steps.go), used only when the session is
	// bound to a step run. Edges are read live, the same as the runner
	// does, so a step's allowed outcomes are whatever is authored now.
	GetStepRun(ctx context.Context, id int64) (workflow.StepRun, error)
	GetStep(ctx context.Context, id int64) (workflow.Step, error)
	GetWorkflow(ctx context.Context, id int64) (workflow.Workflow, error)
	OutgoingEdges(ctx context.Context, stepID int64) ([]workflow.Edge, error)
	FinishStepRun(ctx context.Context, id int64, outcome, deliverable string) error

	// The workflow authoring tools (workflows.go), available to every
	// session: the same writes the TUI's workflows view makes, so an
	// agent can draft a workflow the user then refines there.
	ListWorkflows(ctx context.Context) ([]workflow.Workflow, error)
	WorkflowByName(ctx context.Context, name string) (workflow.Workflow, error)
	CreateWorkflow(ctx context.Context, name, description string) (workflow.Workflow, error)
	RenameWorkflow(ctx context.Context, id int64, name string) error
	SetWorkflowDescription(ctx context.Context, id int64, description string) error
	ListSteps(ctx context.Context, workflowID int64) ([]workflow.Step, error)
	AddStep(ctx context.Context, workflowID int64, name string, kind workflow.StepKind) (workflow.Step, error)
	UpdateStep(ctx context.Context, st workflow.Step) error
	SetStepPrompt(ctx context.Context, id int64, prompt string) error
	ReorderSteps(ctx context.Context, workflowID int64, ids []int64) error
	DeleteStep(ctx context.Context, id int64) error
	ListEdges(ctx context.Context, workflowID int64) ([]workflow.Edge, error)
	SetEdge(ctx context.Context, fromStepID int64, outcome string, toStepID int64, maxIterations *int64) (workflow.Edge, error)
	DeleteEdge(ctx context.Context, id int64) error

	Close() error
}
