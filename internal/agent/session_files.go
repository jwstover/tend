package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// SessionFiles are the files a session bound to a task starts with, as
// absolute paths: what LaunchCmd/ResumeCmd pass as --mcp-config,
// --settings and --append-system-prompt-file. SystemPromptFile is "" when
// there is no brief.
type SessionFiles struct {
	MCPConfig        string `json:"mcp_config"`
	Settings         string `json:"settings"`
	SystemPromptFile string `json:"system_prompt_file,omitempty"`
}

// WriteSessionFiles writes a session's files into dir (created if
// missing) for a session another program starts, and so whose lifetime
// tend does not see: unlike WriteMCPConfig and friends these are not
// temp files with a cleanup, they stay until the caller removes dir.
// tendPath is the tend binary the MCP server and hooks run; dbPath is
// passed to both for the reason WriteHookSettings gives.
func WriteSessionFiles(dir, tendPath string, taskID int64, dbPath, brief string) (SessionFiles, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return SessionFiles{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return SessionFiles{}, fmt.Errorf("creating session dir: %w", err)
	}
	mcp, err := buildMCPConfig(tendPath, taskID, 0, dbPath)
	if err != nil {
		return SessionFiles{}, err
	}
	hooks, err := buildHookSettings(tendPath, dbPath)
	if err != nil {
		return SessionFiles{}, err
	}
	files := SessionFiles{
		MCPConfig: filepath.Join(dir, "mcp.json"),
		Settings:  filepath.Join(dir, "settings.json"),
	}
	writes := map[string][]byte{files.MCPConfig: mcp, files.Settings: hooks}
	if brief != "" {
		files.SystemPromptFile = filepath.Join(dir, "brief.md")
		writes[files.SystemPromptFile] = []byte(brief)
	}
	for path, b := range writes {
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return SessionFiles{}, fmt.Errorf("writing %s: %w", filepath.Base(path), err)
		}
	}
	return files, nil
}
