// Package agent owns the one I/O edge for launching and resuming Claude
// Code sessions: building the exec.Cmd the TUI hands to tea.ExecProcess.
// It never runs a command itself — the caller owns the terminal handoff —
// so this package stays trivially testable.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// RecapPrompt builds the standup-style recap prompt asked of a resumed
// session, suitable for a task's log entry — the headless follow-up
// docs/agent-sessions-plan.md §5 fires after a session's terminal
// handoff returns. excerpt, when non-empty, is a rendering of just the
// conversational turns since the session was last resumed (see
// TranscriptExcerptSince): it scopes the recap to that new work while
// the `--resume` call still gives the model the rest of the session for
// background, so a small, constrained resume doesn't lose the context of
// what the task actually is.
func RecapPrompt(excerpt string) string {
	base := "In 2-3 short sentences (plain text, no markdown headers or bullet " +
		"lists), give a standup-style update on this session for the task you were working on: " +
		"what got done, what's next, and whether anything is blocked or needs a decision. Stay " +
		"high-level — no file names, function names, or other implementation detail. If there's " +
		"nothing blocking, don't mention blockers at all. Respond with only that update — no " +
		"preamble like \"Here's a summary\"."
	if excerpt == "" {
		return base
	}
	return base + "\n\nScope the recap to only the turns below — the new work since you were " +
		"last resumed. Use the rest of the session only as background context to understand " +
		"them; don't re-report anything from before this excerpt.\n\n" + excerpt
}

// RecapTimeout bounds the headless follow-up: generous, since a real
// claude -p summarization can take a while, but bounded so a hung or
// unresponsive call degrades quietly instead of lingering forever.
const RecapTimeout = 90 * time.Second

// RecapCmd builds the headless follow-up run after a session ends:
// `claude -p <prompt> --resume <externalID>`, print mode — unlike
// Launch/ResumeCmd this is never handed to tea.ExecProcess; the caller
// runs it directly (Output) off the update loop, so it takes ctx to
// bound and cancel the process.
func RecapCmd(ctx context.Context, cwd, externalID, prompt string) *exec.Cmd {
	c := exec.CommandContext(ctx, binary, "-p", prompt, "--resume", externalID)
	c.Dir = cwd
	return c
}

// transcriptPath locates a session's local Claude Code transcript on
// disk: one JSONL file per session under
// ~/.claude/projects/<cwd, with "/" and "." replaced by "-">/<externalID>.jsonl.
// This is an on-disk convention of the claude CLI itself, not a stable
// public API, so TranscriptLineCount/TranscriptExcerptSince degrade to
// "nothing found" rather than erroring if it ever changes — a Claude
// Code update can only make recaps fall back to unscoped, never break
// them outright.
func transcriptPath(cwd, externalID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sanitized := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
	return filepath.Join(home, ".claude", "projects", sanitized, externalID+".jsonl"), nil
}

// TranscriptLineCount returns how many lines are currently in a
// session's local transcript file — the marker to record at resume time
// so a later recap can identify what's new (see TranscriptExcerptSince).
// A transcript that doesn't exist yet (a brand-new session) is 0 lines,
// not an error.
func TranscriptLineCount(cwd, externalID string) (int, error) {
	path, err := transcriptPath(cwd, externalID)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		n++
	}
	return n, sc.Err()
}

// transcriptEntry is the subset of a Claude Code transcript JSONL line
// TranscriptExcerptSince needs: whether a line is real conversation (not
// a meta slash-command or subagent sidechain) and its role/content.
type transcriptEntry struct {
	IsMeta      bool `json:"isMeta"`
	IsSidechain bool `json:"isSidechain"`
	Message     *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// TranscriptExcerptSince renders the user/assistant text turns appended
// to a session's local transcript after line `since` as plain
// "User: ...\nAssistant: ..." text — the delta a resumed session's recap
// should be scoped to (see RecapPrompt). Tool calls, thinking blocks,
// and tool results are dropped; they're noise for a 2-3 sentence standup
// update. Returns "" rather than an error if the transcript can't be
// found or read, so a missing/moved file just falls back to an unscoped
// recap instead of failing it.
func TranscriptExcerptSince(cwd, externalID string, since int) (string, error) {
	path, err := transcriptPath(cwd, externalID)
	if err != nil {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer f.Close()

	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		if line <= since {
			continue
		}
		var e transcriptEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.Message == nil {
			continue
		}
		if e.IsMeta || e.IsSidechain {
			continue
		}
		text, ok := renderTranscriptTurn(e.Message.Role, e.Message.Content)
		if !ok {
			continue
		}
		label := "User"
		if e.Message.Role == "assistant" {
			label = "Assistant"
		}
		fmt.Fprintf(&b, "%s: %s\n", label, text)
	}
	return strings.TrimSpace(b.String()), nil
}

// renderTranscriptTurn extracts the plain-text content of one transcript
// message. A user turn's content is a bare JSON string in the
// transcript, unless it's a slash-command or tool-result wrapper
// (skipped — not something a human said). An assistant turn's content
// is a block array; only "text" blocks count, not "thinking" or
// "tool_use".
func renderTranscriptTurn(role string, raw json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if role != "user" || strings.HasPrefix(s, "<command-") || strings.HasPrefix(s, "<local-command") {
			return "", false
		}
		return s, true
	}

	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false
	}
	var texts []string
	for _, blk := range blocks {
		if blk.Type == "text" && strings.TrimSpace(blk.Text) != "" {
			texts = append(texts, blk.Text)
		}
	}
	if len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, "\n"), true
}
