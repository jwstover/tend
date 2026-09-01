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
// ad-hoc scratch markdown file. Returns "" with a no-op cleanup if
// `tend` isn't resolvable on $PATH — degrades quietly, the same way
// CheckInstalled treats a missing `claude` binary, since MCP tools are
// a convenience on top of launch/resume, not something either should
// fail over.
func WriteMCPConfig(taskID int64, dbPath string) (path string, cleanup func(), err error) {
	noop := func() {}

	tendPath, lookErr := exec.LookPath("tend")
	if lookErr != nil {
		return "", noop, nil
	}

	cfg := mcpConfig{MCPServers: map[string]mcpServerConfig{
		"tend": {
			Command: tendPath,
			Args:    []string{"mcp", "--task-id", strconv.FormatInt(taskID, 10), "--db", dbPath},
		},
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
