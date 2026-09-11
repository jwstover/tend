package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/version"
)

// Server binds tend's MCP tool surface to one task — the load-bearing
// piece that lets "update the current task" resolve without the model
// guessing an id — and, for a workflow step's session, to the step run
// it is executing, so finish_step lands on the right row.
type Server struct {
	store     Store
	taskID    int64
	stepRunID int64
}

// New builds a Server whose tools default to (and, for get_current_task,
// are pinned to) taskID. Every session also gets the workflow authoring
// tools (workflows.go), which are bound to nothing. stepRunID is the
// workflow step run the session is executing; non-zero adds
// get_workflow_step and finish_step bound to it, zero (an ordinary
// session) leaves the tool set at that.
func New(store Store, taskID, stepRunID int64) *Server {
	return &Server{store: store, taskID: taskID, stepRunID: stepRunID}
}

// Run serves the tool surface over stdio until the client disconnects
// (stdin closing) or ctx is cancelled — the natural lifetime of a
// `tend mcp` process spawned by claude for the duration of one session.
func (s *Server) Run(ctx context.Context) error {
	srv := mcp.NewServer(&mcp.Implementation{Name: "tend", Version: version.String()}, nil)
	registerTools(srv, s.store, s.taskID)
	registerWorkflowTools(srv, s.store)
	if s.stepRunID != 0 {
		registerStepTools(srv, s.store, s.stepRunID)
	}
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}
