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
// mcpConfigPath, when non-empty (see WriteMCPConfig), wires the
// session's task-bound MCP tools via --mcp-config. settingsPath, when
// non-empty (see WriteHookSettings), wires the status-reporting hooks
// via --settings, which *adds* to the user's own settings rather than
// replacing them.
//
// Both paths are separate flags rather than one file because they are
// separate Claude Code mechanisms, and because each degrades on its own:
// a session with no MCP tools still reports status, and one with no
// hooks still gets its tools.
func LaunchCmd(cwd, sessionID, label, mcpConfigPath, settingsPath string) *exec.Cmd {
	args := []string{"--session-id", sessionID, "-n", label}
	if mcpConfigPath != "" {
		args = append(args, "--mcp-config", mcpConfigPath)
	}
	if settingsPath != "" {
		args = append(args, "--settings", settingsPath)
	}
	c := exec.Command(binary, args...)
	c.Dir = cwd
	return c
}

// ResumeCmd builds the command to reopen an existing session by its
// external id, in the directory it was stored against. mcpConfigPath and
// settingsPath are as in LaunchCmd — resuming is "continue the work,"
// not just "reread the transcript," so tools and status reporting should
// both be there either time.
func ResumeCmd(cwd, externalID, mcpConfigPath, settingsPath string) *exec.Cmd {
	args := []string{"--resume", externalID}
	if mcpConfigPath != "" {
		args = append(args, "--mcp-config", mcpConfigPath)
	}
	if settingsPath != "" {
		args = append(args, "--settings", settingsPath)
	}
	c := exec.Command(binary, args...)
	c.Dir = cwd
	return c
}

// RecapPrompt builds the combined label+recap prompt asked of a
// finished/resumed session — the headless follow-up
// docs/agent-sessions-plan.md §5 fires after a session's terminal
// handoff returns. The recap half is a standup-style update suitable
// for a task's log entry; the label half is a short, distinguishing
// description of what the session actually did, stored as
// agent_sessions.label (see ParseRecapResponse) — the auto-naming fix
// for §4's known issue, where every session on a task otherwise renders
// the same static task-title snapshot. excerpt, when non-empty, is a
// rendering of just the conversational turns since the session was last
// resumed (see TranscriptExcerptSince): it scopes the recap to that new
// work while the `--resume` call still gives the model the rest of the
// session for background, so a small, constrained resume doesn't lose
// the context of what the task actually is.
func RecapPrompt(excerpt string) string {
	base := "Respond in exactly this two-line format and nothing else — no preamble, no " +
		"markdown, no extra lines:\n\n" +
		"LABEL: <a short label, 3-6 words, naming what this session actually worked on — " +
		"specific enough to tell it apart from another session on the same task>\n" +
		"RECAP: <in 2-3 short sentences, plain text, a standup-style update on this session " +
		"for the task you were working on: what got done, what's next, and whether anything " +
		"is blocked or needs a decision. Stay high-level — no file names, function names, or " +
		"other implementation detail. If there's nothing blocking, don't mention blockers at " +
		"all>"
	if excerpt == "" {
		return base
	}
	return base + "\n\nScope the recap to only the turns below — the new work since you were " +
		"last resumed. Use the rest of the session only as background context to understand " +
		"them; don't re-report anything from before this excerpt.\n\n" + excerpt
}

// ParseRecapResponse splits a RecapPrompt reply into its label and recap
// halves. A real model reply won't always land in the exact two-line
// shape asked for, so this degrades gracefully rather than erroring: if
// both markers aren't found in order, or the RECAP half comes back
// empty, the whole trimmed response is returned as the recap with no
// label — exactly the pre-auto-naming behavior, so a malformed reply
// still logs a usable recap, it just skips renaming the session.
func ParseRecapResponse(raw string) (label, recap string) {
	raw = strings.TrimSpace(raw)
	li := strings.Index(raw, "LABEL:")
	ri := strings.Index(raw, "RECAP:")
	if li == -1 || ri == -1 || ri <= li {
		return "", raw
	}
	label = strings.TrimSpace(raw[li+len("LABEL:") : ri])
	recap = strings.TrimSpace(raw[ri+len("RECAP:"):])
	if recap == "" {
		return "", raw
	}
	return label, recap
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
