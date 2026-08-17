// Package agent owns the one I/O edge for launching and resuming Claude
// Code sessions: building the exec.Cmd the TUI hands to tea.ExecProcess.
// It never runs a command itself — the caller owns the terminal handoff —
// so this package stays trivially testable.
package agent

import (
	"errors"
	"os/exec"
)

// binary is the Claude Code executable name, resolved via $PATH.
const binary = "claude"

// ErrNotFound is returned by CheckInstalled when the claude binary isn't
// on $PATH.
var ErrNotFound = errors.New("claude: not found on $PATH — install Claude Code to launch sessions")

// CheckInstalled resolves the binary so callers can surface ErrNotFound
// as a clear status-line message instead of exec's cryptic "executable
// file not found in $PATH". Call it before LaunchCmd/ResumeCmd's Cmd is
// handed to tea.ExecProcess.
func CheckInstalled() error {
	if _, err := exec.LookPath(binary); err != nil {
		return ErrNotFound
	}
	return nil
}

// LaunchCmd builds the command for a brand-new session pinned to
// sessionID (see NewSessionID), running in cwd. label sets the display
// name shown in Claude Code's own /resume picker and terminal title.
func LaunchCmd(cwd, sessionID, label string) *exec.Cmd {
	c := exec.Command(binary, "--session-id", sessionID, "-n", label)
	c.Dir = cwd
	return c
}

// ResumeCmd builds the command to reopen an existing session by its
// external id, in the directory it was stored against.
func ResumeCmd(cwd, externalID string) *exec.Cmd {
	c := exec.Command(binary, "--resume", externalID)
	c.Dir = cwd
	return c
}
