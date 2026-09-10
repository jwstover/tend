package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/jwstover/tend/internal/mcpserver"
)

// MCPStoreFactory opens the mcpserver.Store slice of the persistence
// layer at the given database path — kept separate from StoreFactory
// because `tend mcp` needs a different method set than the rest of the
// command tree (internal/mcpserver.Store, not cli.Store).
type MCPStoreFactory func(ctx context.Context, dbPath string) (mcpserver.Store, error)

// newMcpCmd wires the hidden `tend mcp` command: one process per Claude
// Code session, spawned by `claude` itself via --mcp-config (see
// internal/agent.WriteMCPConfig), serving the task-scoped MCP tool
// surface over stdio until stdin closes. A workflow step's session is
// spawned with --step-run-id as well, which adds the step tools
// (get_workflow_step, finish_step) for that one step run. Hidden because
// it's internal plumbing a launched session's --mcp-config points at,
// not something a user is meant to run by hand.
func newMcpCmd(open func(ctx context.Context) (mcpserver.Store, error)) *cobra.Command {
	var taskID, stepRunID int64
	cmd := &cobra.Command{
		Use:    "mcp",
		Short:  "Run tend's MCP server, bound to one task",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := open(cmd.Context())
			if err != nil {
				return err
			}
			defer s.Close()
			return mcpserver.New(s, taskID, stepRunID).Run(cmd.Context())
		},
	}
	cmd.Flags().Int64Var(&taskID, "task-id", 0, "task this session is bound to")
	cmd.Flags().Int64Var(&stepRunID, "step-run-id", 0, "workflow step run this session is executing, if any")
	_ = cmd.MarkFlagRequired("task-id")
	return cmd
}
