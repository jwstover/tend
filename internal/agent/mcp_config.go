package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// mcpConfig mirrors Claude Code's --mcp-config file shape: one entry
// under mcpServers, invoking `tend mcp` as a stdio subprocess bound to
// this session's task.
type mcpConfig struct {
	MCPServers map[string]mcpServerConfig `json:"mcpServers"`
}

type mcpServerConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// WriteMCPConfig writes a per-session --mcp-config temp file wiring
// `tend mcp` as a stdio MCP server bound to taskID, so a launched or
// resumed session can read/write that task directly instead of an
// ad-hoc scratch markdown file. stepRunID, when non-zero, also binds the
// server to a workflow step run so the session gets the step tools
// (get_workflow_step, finish_step); an ordinary session passes 0 and
// sees the task tools only. Returns "" with a no-op cleanup if `tend`
// isn't resolvable on $PATH — degrades quietly, the same way
// CheckInstalled treats a missing `claude` binary, since MCP tools are
// a convenience on top of launch/resume, not something either should
// fail over.
func WriteMCPConfig(taskID, stepRunID int64, dbPath string) (path string, cleanup func(), err error) {
	noop := func() {}

	tendPath, lookErr := exec.LookPath("tend")
	if lookErr != nil {
		return "", noop, nil
	}

	args := []string{"mcp", "--task-id", strconv.FormatInt(taskID, 10)}
	if stepRunID != 0 {
		args = append(args, "--step-run-id", strconv.FormatInt(stepRunID, 10))
	}
	args = append(args, "--db", dbPath)
	cfg := mcpConfig{MCPServers: map[string]mcpServerConfig{
		"tend": {Command: tendPath, Args: args},
	}}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", noop, fmt.Errorf("marshaling mcp config: %w", err)
	}

	f, err := os.CreateTemp("", "tend-mcp-*.json")
	if err != nil {
		return "", noop, fmt.Errorf("creating mcp config temp file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		os.Remove(f.Name())
		return "", noop, fmt.Errorf("writing mcp config: %w", err)
	}

	name := f.Name()
	return name, func() { os.Remove(name) }, nil
}
