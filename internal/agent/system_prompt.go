package agent

import (
	"fmt"
	"os"
)

// WriteSystemPrompt writes a system prompt block to a per-session temp
// file for claude's --append-system-prompt-file, and returns the path
// with a cleanup that removes it. It exists because the block cannot ride
// on the command line: claude runs inside tmux (WrapTmux), and tmux ships
// the whole `new-session -- claude ...` argv from its client to its
// server in one message capped at 16KB, past which it prints "command too
// long" and exits 1 before claude starts. A task brief
// (task.SessionSystemPrompt) carrying a body of a few tens of KB — routine
// for a task that has logged a few sessions' worth of notes — blew that
// cap on every launch. A file path is a few dozen bytes however long the
// block is, so the argv stays small and the tmux cap stops mattering.
//
// An empty text writes nothing and returns "" with a no-op cleanup, so a
// caller can pass the result straight through as LaunchOpts's
// AppendSystemPromptFile: "" adds no flag. On error the path is "" and
// the cleanup a no-op; like WriteMCPConfig, the caller treats a failure
// as "no brief this session", not as a failure to launch.
//
// The file has the same lifetime as the MCP config: claude reads it at
// startup, but the caller only removes it once the session has really
// ended rather than been backgrounded, so a still-running claude never
// has a file it was given deleted underneath it.
func WriteSystemPrompt(text string) (path string, cleanup func(), err error) {
	noop := func() {}
	if text == "" {
		return "", noop, nil
	}
	f, err := os.CreateTemp("", "tend-system-prompt-*.md")
	if err != nil {
		return "", noop, fmt.Errorf("creating system prompt temp file: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		os.Remove(f.Name())
		return "", noop, fmt.Errorf("writing system prompt: %w", err)
	}
	name := f.Name()
	return name, func() { os.Remove(name) }, nil
}
