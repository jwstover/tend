package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jwstover/tend/internal/version"
)

// Server binds tend's MCP tool surface to one task — the load-bearing
// piece that lets "update the current task" resolve without the model
// guessing an id.
type Server struct {
	store  Store
	taskID int64
}

// New builds a Server whose tools default to (and, for get_current_task,
// are pinned to) taskID.
func New(store Store, taskID int64) *Server {
	return &Server{store: store, taskID: taskID}
}

// Run serves the tool surface over stdio until the client disconnects
// (stdin closing) or ctx is cancelled — the natural lifetime of a
// `tend mcp` process spawned by claude for the duration of one session.
func (s *Server) Run(ctx context.Context) error {
	srv := mcp.NewServer(&mcp.Implementation{Name: "tend", Version: version.String()}, nil)
	registerTools(srv, s.store, s.taskID)
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}
