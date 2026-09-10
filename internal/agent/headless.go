package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// HeadlessCmd builds the headless equivalent of LaunchCmdWith: the same
// session pinned to sessionID in cwd, but run as `claude -p` so it takes
// the prompt, works until done, and exits without ever owning a
// terminal. This is how a workflow runner executes an agent step.
//
//	claude -p --session-id <id> --output-format stream-json --verbose
//	       [--mcp-config <path>] [--settings <path>]
//	       [--model <m>] [--permission-mode <mode>]
//	       [--append-system-prompt <text>] <prompt>
//
// Everything below was verified against the installed CLI (claude
// 2.1.267) before this was written; the findings are logged on tend task
// #178:
//
//   - `--output-format stream-json` is refused in print mode without
//     `--verbose`, so both are always set. Stdout is then one JSON object
//     per line, ending in a `result` event that carries the assistant's
//     final text (see ParseStream).
//   - A `-p` session with `--session-id` is written to the same on-disk
//     transcript an interactive one is, and `claude --resume <id>` picks it
//     up with its context intact — which is what lets a running step be
//     taken over interactively. That holds whether the process finished
//     or was killed: the transcript is appended turn by turn, so
//     cancelling ctx (which sends SIGTERM, then SIGKILL after
//     headlessKillDelay) leaves a session that is still resumable.
//   - With no --permission-mode, a `-p` run does not prompt: any tool
//     call that would need approval is denied, and the run still ends
//     with subtype "success" and is_error false. The denials are only
//     visible in the result event's permission_denials list, which
//     ParseStream surfaces as HeadlessResult.PermissionDenials. Hooks
//     (--settings) and MCP servers (--mcp-config) both work in print mode
//     exactly as they do interactively.
//   - `--append-system-prompt <text>` adds to the default system prompt
//     (`--system-prompt` would replace it); it is accepted on a `--resume`
//     turn as well. This is how the runner states the finish_step
//     contract to every step (tend task #197).
//
// opts.Prompt is required: `-p` with no positional prompt reads stdin,
// which a headless step never has. Options come before the prompt for
// the same reason as LaunchCmdWith.
//
// Like the rest of the package this only builds the command. Use
// RunHeadless to run it with its stdout tee'd to a step log, or wire
// Stdout yourself and read the stream with ParseStream.
func HeadlessCmd(ctx context.Context, cwd, sessionID, mcpConfigPath, settingsPath string, opts LaunchOpts) *exec.Cmd {
	return headlessCmd(ctx, cwd, []string{"--session-id", sessionID}, mcpConfigPath, settingsPath, opts)
}

// HeadlessResumeCmd is HeadlessCmd for a session that already exists: the
// same print-mode invocation with `--resume <id>` in place of
// `--session-id <id>`, so opts.Prompt lands as the next turn of that
// session, with its context intact. This is how a workflow runner picks a
// step back up after it (or the host) died mid-step. Verified against
// claude 2.1.267: a `-p --resume` turn keeps the original session id (the
// result event reports it; `--fork-session` is what would mint a new one)
// and answers from the earlier turns.
//
// A session id with no transcript on disk -- a step killed before claude
// wrote its first turn -- makes claude exit non-zero with no result
// event; the caller falls back to starting the step over.
func HeadlessResumeCmd(ctx context.Context, cwd, sessionID, mcpConfigPath, settingsPath string, opts LaunchOpts) *exec.Cmd {
	return headlessCmd(ctx, cwd, []string{"--resume", sessionID}, mcpConfigPath, settingsPath, opts)
}

// headlessCmd is the shared body of HeadlessCmd and HeadlessResumeCmd:
// sessionArgs is the one flag pair that differs.
func headlessCmd(ctx context.Context, cwd string, sessionArgs []string, mcpConfigPath, settingsPath string, opts LaunchOpts) *exec.Cmd {
	args := append([]string{"-p"}, sessionArgs...)
	args = append(args, "--output-format", "stream-json", "--verbose")
	if mcpConfigPath != "" {
		args = append(args, "--mcp-config", mcpConfigPath)
	}
	if settingsPath != "" {
		args = append(args, "--settings", settingsPath)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}
	if opts.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystemPrompt)
	}
	args = append(args, opts.Prompt)

	c := exec.CommandContext(ctx, binary, args...)
	c.Dir = cwd
	// The step gets its own process group so cancelling reaches everything
	// claude spawned — MCP servers, a Bash tool's shell — and not just
	// claude: a child that inherited stdout would otherwise hold the pipe
	// open past claude's death and stall RunHeadless until WaitDelay.
	// SIGTERM rather than exec's default SIGKILL gives claude the chance to
	// flush its transcript and fire its SessionEnd hook.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGTERM) }
	c.WaitDelay = headlessKillDelay
	return c
}

// headlessKillDelay is how long a cancelled headless step gets to exit on
// SIGTERM before exec escalates to SIGKILL and stops waiting on its
// output pipes.
const headlessKillDelay = 5 * time.Second

// HeadlessResult is what a headless step leaves behind: the fields of the
// stream's final `result` event the runner needs. Text is the assistant's
// last message — the fallback deliverable when a step never calls
// finish_step. PermissionDenials counts tool calls claude refused for
// want of a permission mode; a step that "succeeded" with denials almost
// certainly did not do its job, which is why it is surfaced separately
// from IsError (which is false in that case).
type HeadlessResult struct {
	// Found reports whether a result event was seen at all. It is false
	// for a stream cut short by a crash, a kill, or a stub that never
	// finished, in which case the other fields are zero.
	Found             bool
	Text              string
	Subtype           string
	IsError           bool
	SessionID         string
	NumTurns          int
	CostUSD           float64
	PermissionDenials int
}

// streamEvent is the subset of a stream-json line ParseStream reads. Every
// other event type (system, assistant, user, rate_limit_event, ...) and
// every other field is ignored on purpose so the stream can grow without
// breaking the parser.
type streamEvent struct {
	Type              string            `json:"type"`
	Subtype           string            `json:"subtype"`
	IsError           bool              `json:"is_error"`
	Result            string            `json:"result"`
	SessionID         string            `json:"session_id"`
	NumTurns          int               `json:"num_turns"`
	TotalCostUSD      float64           `json:"total_cost_usd"`
	PermissionDenials []json.RawMessage `json:"permission_denials"`
}

// ParseStream reads a stream-json log — live from a pipe or after the
// fact from the file RunHeadless wrote — and returns the final `result`
// event's fields. Lines that aren't JSON objects, or are events of some
// other type, are skipped rather than failing the parse: a partial last
// line from a killed process must not hide a result that came before it.
// Only a read error is returned as an error; a stream with no result
// event is a HeadlessResult with Found false.
func ParseStream(r io.Reader) (HeadlessResult, error) {
	var res HeadlessResult
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if ev, ok := parseResultLine(sc.Bytes()); ok {
			res = ev
		}
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("reading stream-json: %w", err)
	}
	return res, nil
}

// parseResultLine decodes one stream-json line, reporting true only for a
// `result` event.
func parseResultLine(line []byte) (HeadlessResult, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return HeadlessResult{}, false
	}
	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil || ev.Type != "result" {
		return HeadlessResult{}, false
	}
	return HeadlessResult{
		Found:             true,
		Text:              ev.Result,
		Subtype:           ev.Subtype,
		IsError:           ev.IsError,
		SessionID:         ev.SessionID,
		NumTurns:          ev.NumTurns,
		CostUSD:           ev.TotalCostUSD,
		PermissionDenials: len(ev.PermissionDenials),
	}, true
}

// resultWriter is the in-memory half of RunHeadless's tee: an io.Writer
// that reassembles the stream into lines as it arrives and keeps the
// latest result event, so the result is known the moment the process
// exits without re-reading the log file.
type resultWriter struct {
	buf bytes.Buffer
	res HeadlessResult
}

func (w *resultWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			break
		}
		line := make([]byte, i)
		copy(line, w.buf.Bytes()[:i])
		w.buf.Next(i + 1)
		if ev, ok := parseResultLine(line); ok {
			w.res = ev
		}
	}
	return len(p), nil
}

// flush parses whatever trailing bytes never got a newline.
func (w *resultWriter) flush() {
	if ev, ok := parseResultLine(w.buf.Bytes()); ok {
		w.res = ev
	}
	w.buf.Reset()
}

// stderrLimit bounds how much of claude's stderr RunHeadless keeps for the
// error message; the log file has stdout, and stderr in print mode is
// short (a usage error, a crash trace) when it is anything at all.
const stderrLimit = 8 * 1024

// RunHeadless runs a HeadlessCmd with its stdout tee'd, line by line as it
// streams, into logPath — created along with its parent directories,
// appended to if it already exists so a resumed step run keeps one log —
// and returns the parsed result once the process exits. The tee is live
// so a log viewer can tail the file while the step runs.
//
// The result is returned alongside a non-nil error whenever one was
// parsed: a step that exits non-zero after emitting a result event (an
// API error mid-run, a max-turns stop) still has a final text worth
// recording, and a cancelled step has whatever it managed to say. Callers
// should check Found before trusting it. A step ended by ctx comes back as
// an error wrapping ErrKilled; the caller holds the ctx and can tell a
// cancellation from a timeout by its own ctx.Err().
func RunHeadless(c *exec.Cmd, logPath string) (HeadlessResult, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return HeadlessResult{}, fmt.Errorf("creating step log dir: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return HeadlessResult{}, fmt.Errorf("opening step log: %w", err)
	}
	defer f.Close()

	rw := &resultWriter{}
	c.Stdout = io.MultiWriter(f, rw)
	var stderr limitedBuffer
	c.Stderr = &stderr

	runErr := c.Run()
	rw.flush()
	if runErr == nil {
		return rw.res, nil
	}
	if killed(c) {
		return rw.res, fmt.Errorf("headless step: %w", ErrKilled)
	}
	msg := strings.TrimSpace(stderr.String())
	if msg != "" {
		return rw.res, fmt.Errorf("headless step: %w: %s", runErr, msg)
	}
	return rw.res, fmt.Errorf("headless step: %w", runErr)
}

// ErrKilled is the error RunHeadless wraps when the step's process was
// ended by the SIGTERM/SIGKILL that cancelling its ctx sends, rather than
// exiting on its own. exec folds both outcomes into an ExitError, and it
// never exposes the ctx, so the exit signal is what distinguishes them.
var ErrKilled = errors.New("process killed (session remains resumable)")

// killed reports whether c's process died from a termination signal.
func killed(c *exec.Cmd) bool {
	if c.ProcessState == nil {
		return false
	}
	ws, ok := c.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return false
	}
	sig := ws.Signal()
	return sig == syscall.SIGTERM || sig == syscall.SIGKILL
}

// limitedBuffer keeps the first stderrLimit bytes written to it and drops
// the rest, so a runaway stderr can't grow the runner's memory.
type limitedBuffer struct {
	bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := stderrLimit - b.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.Buffer.Write(p)
	}
	return len(p), nil
}

// StepLogPath is where a step run's stream-json log lives:
// ${XDG_DATA_HOME:-~/.local/share}/tend/runs/<run-id>/<step-run-id>.jsonl,
// beside the database in tend's data directory (the same resolution as
// the default --db path, minus TEND_DB, which names a file, not a
// directory). The database stores this path on the step run
// (workflow_step_runs.log_path); the log's content never goes in SQLite.
func StepLogPath(runID, stepRunID int64) (string, error) {
	dir, err := runDir(runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, strconv.FormatInt(stepRunID, 10)+".jsonl"), nil
}

// RunnerLogPath is where a run's runner writes its own progress log --
// runner.log beside the step logs in the run's directory -- so what the
// runner said survives the tmux session it said it in.
func RunnerLogPath(runID int64) (string, error) {
	dir, err := runDir(runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runner.log"), nil
}

// runDir is ${XDG_DATA_HOME:-~/.local/share}/tend/runs/<run-id>.
func runDir(runID int64) (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locating run log dir: %w", err)
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "tend", "runs", strconv.FormatInt(runID, 10)), nil
}

// ErrNoResult is returned by ResultOrError when a finished headless step
// produced no result event — the stream was cut short — so a runner can
// tell "the agent said nothing" from "the agent said an empty string".
var ErrNoResult = errors.New("headless step produced no result event")

// ResultOrError turns a HeadlessResult into the runner's fallback
// deliverable: the final text, or an error if there was no result event
// or claude flagged the run as an error. Permission denials are not an
// error here — the text explains what was refused and the runner decides
// how loud to be — but they are the reason callers should look at
// PermissionDenials before treating a "success" as one.
func (r HeadlessResult) ResultOrError() (string, error) {
	if !r.Found {
		return "", ErrNoResult
	}
	if r.IsError {
		return r.Text, fmt.Errorf("headless step ended with %s: %s", r.Subtype, r.Text)
	}
	return r.Text, nil
}
