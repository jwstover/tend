package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHeadlessCmdArgv(t *testing.T) {
	c := HeadlessCmd(context.Background(), "/tmp/work", "abc-123", "/tmp/mcp.json", "/tmp/hooks.json",
		LaunchOpts{Prompt: "Fix the flaky test", Model: "opus", PermissionMode: "acceptEdits"})
	want := []string{
		binary, "-p", "--session-id", "abc-123", "--output-format", "stream-json", "--verbose",
		"--mcp-config", "/tmp/mcp.json", "--settings", "/tmp/hooks.json",
		"--model", "opus", "--permission-mode", "acceptEdits",
		"Fix the flaky test",
	}
	if got := c.Args; !equalArgs(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
	if c.Dir != "/tmp/work" {
		t.Errorf("Dir = %q, want /tmp/work", c.Dir)
	}
	if c.Cancel == nil {
		t.Error("Cancel should be set so a cancelled ctx sends SIGTERM rather than SIGKILL")
	}
	if c.WaitDelay != headlessKillDelay {
		t.Errorf("WaitDelay = %v, want %v", c.WaitDelay, headlessKillDelay)
	}
}

// Unset options add nothing, and stream-json always brings --verbose with
// it because print mode refuses one without the other.
func TestHeadlessCmdMinimalArgv(t *testing.T) {
	c := HeadlessCmd(context.Background(), "/tmp/work", "abc-123", "", "", LaunchOpts{Prompt: "go"})
	want := []string{binary, "-p", "--session-id", "abc-123", "--output-format", "stream-json", "--verbose", "go"}
	if got := c.Args; !equalArgs(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
}

// streamFixture is a trimmed copy of a real `claude -p --output-format
// stream-json --verbose` run (claude 2.1.267): the event types in the
// order they arrived, with the result event's bulk removed.
const streamFixture = `{"type":"system","subtype":"status","status":null,"session_id":"8c9ba8fe-daf9-4ac7-b691-36bf60b52168","uuid":"c1"}
{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"},"uuid":"c2","session_id":"8c9ba8fe-daf9-4ac7-b691-36bf60b52168"}
{"type":"system","subtype":"init","cwd":"/tmp/work","session_id":"8c9ba8fe-daf9-4ac7-b691-36bf60b52168","model":"claude-haiku-4-5-20251001","permissionMode":"default"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"..."}]},"session_id":"8c9ba8fe-daf9-4ac7-b691-36bf60b52168"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"pong"}]},"session_id":"8c9ba8fe-daf9-4ac7-b691-36bf60b52168"}
{"type":"result","subtype":"success","is_error":false,"duration_ms":1793,"num_turns":1,"result":"pong","session_id":"8c9ba8fe-daf9-4ac7-b691-36bf60b52168","total_cost_usd":0.02241385,"permission_denials":[],"uuid":"b7"}
`

func TestParseStreamResult(t *testing.T) {
	got, err := ParseStream(strings.NewReader(streamFixture))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	want := HeadlessResult{
		Found:     true,
		Text:      "pong",
		Subtype:   "success",
		SessionID: "8c9ba8fe-daf9-4ac7-b691-36bf60b52168",
		NumTurns:  1,
		CostUSD:   0.02241385,
	}
	if got != want {
		t.Errorf("ParseStream = %+v, want %+v", got, want)
	}
	text, err := got.ResultOrError()
	if err != nil || text != "pong" {
		t.Errorf("ResultOrError = (%q, %v), want (pong, nil)", text, err)
	}
}

// A step with no permission mode has its tool calls denied but still ends
// "success" — the denials list is the only signal, so it must be counted.
func TestParseStreamCountsPermissionDenials(t *testing.T) {
	line := `{"type":"result","subtype":"success","is_error":false,"result":"Permission is pending.",` +
		`"permission_denials":[{"tool_name":"Write","tool_use_id":"toolu_1","tool_input":{}},{"tool_name":"Bash","tool_use_id":"toolu_2","tool_input":{}}]}`
	got, err := ParseStream(strings.NewReader(line + "\n"))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if got.PermissionDenials != 2 {
		t.Errorf("PermissionDenials = %d, want 2", got.PermissionDenials)
	}
	if got.IsError {
		t.Error("IsError should be false: claude reports a denied run as success")
	}
}

func TestParseStreamErrorResult(t *testing.T) {
	line := `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"ran out of turns","num_turns":10}`
	got, err := ParseStream(strings.NewReader(line))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if !got.Found || !got.IsError || got.Subtype != "error_max_turns" {
		t.Errorf("ParseStream = %+v, want an error result", got)
	}
	text, err := got.ResultOrError()
	if err == nil || text != "ran out of turns" {
		t.Errorf("ResultOrError = (%q, %v), want the text and an error", text, err)
	}
}

// Garbage, blank lines, and a truncated final line — what a killed
// process leaves behind — must not hide a result that came before them,
// and a stream with no result is Found false, not an error.
func TestParseStreamTolerates(t *testing.T) {
	stream := "\n" + streamFixture + "not json at all\n" + `{"type":"result","subtype":"succ`
	got, err := ParseStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if !got.Found || got.Text != "pong" {
		t.Errorf("ParseStream = %+v, want the earlier pong result", got)
	}

	none, err := ParseStream(strings.NewReader(`{"type":"system","subtype":"init"}` + "\nplain text\n"))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if none.Found {
		t.Errorf("Found = true for a stream with no result event: %+v", none)
	}
	if _, err := none.ResultOrError(); !errors.Is(err, ErrNoResult) {
		t.Errorf("ResultOrError err = %v, want ErrNoResult", err)
	}
}

// installStubClaude puts a shell script named `claude` first on $PATH for
// the rest of the test. The script records its argv, one per line, to
// $STUB_ARGV, then runs body. HeadlessCmd resolves the binary via $PATH at
// construction, so this must run before the command is built.
func installStubClaude(t *testing.T, body string) (argvPath string) {
	t.Helper()
	dir := t.TempDir()
	argvPath = filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$STUB_ARGV\"\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing stub claude: %v", err)
	}
	t.Setenv("STUB_ARGV", argvPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvPath
}

// The end-to-end shape against a stub: the step's stdout lands in the log
// file byte for byte, the result is parsed from the live tee, and the
// binary saw the argv HeadlessCmd built.
func TestRunHeadlessTeesAndParses(t *testing.T) {
	argvPath := installStubClaude(t, `cat <<'EOF'
`+strings.TrimSuffix(streamFixture, "\n")+`
EOF
echo "some warning" >&2`)
	logPath := filepath.Join(t.TempDir(), "runs", "1", "2.jsonl")

	c := HeadlessCmd(context.Background(), t.TempDir(), "abc-123", "", "", LaunchOpts{Prompt: "ping", Model: "haiku"})
	res, err := RunHeadless(c, logPath)
	if err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if !res.Found || res.Text != "pong" || res.SessionID != "8c9ba8fe-daf9-4ac7-b691-36bf60b52168" {
		t.Errorf("result = %+v, want the fixture's pong result", res)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log: %v", err)
	}
	if string(logged) != streamFixture {
		t.Errorf("log file = %q, want the stub's stdout verbatim", logged)
	}

	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("reading argv: %v", err)
	}
	want := "-p\n--session-id\nabc-123\n--output-format\nstream-json\n--verbose\n--model\nhaiku\nping\n"
	if string(argv) != want {
		t.Errorf("stub saw argv %q, want %q", argv, want)
	}
}

// A run that already emitted a result and then dies still hands the
// result back, with stderr in the error for the run view.
func TestRunHeadlessNonZeroExitKeepsResult(t *testing.T) {
	installStubClaude(t, `echo '{"type":"result","subtype":"success","result":"partial"}'
echo "boom" >&2
exit 3`)
	c := HeadlessCmd(context.Background(), t.TempDir(), "abc-123", "", "", LaunchOpts{Prompt: "go"})
	res, err := RunHeadless(c, filepath.Join(t.TempDir(), "log.jsonl"))
	if err == nil {
		t.Fatal("RunHeadless: want an error for exit 3")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Errorf("err = %v, want to wrap the exit status 3", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want it to carry stderr", err)
	}
	if !res.Found || res.Text != "partial" {
		t.Errorf("result = %+v, want the result emitted before the crash", res)
	}
}

// A step that never finishes is stopped by cancelling ctx: the tee has
// already written what streamed so far, the process is gone promptly, and
// the error says it was killed rather than that it failed. The stub's
// `sleep` is a child of the stub shell holding the stdout pipe — the
// shape of a Bash tool call — so a prompt return also proves the whole
// process group was signalled, not just the top process.
func TestRunHeadlessCancelKillsAndKeepsPartialLog(t *testing.T) {
	installStubClaude(t, `echo '{"type":"system","subtype":"init","session_id":"s1"}'
sleep 30`)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := HeadlessCmd(ctx, t.TempDir(), "s1", "", "", LaunchOpts{Prompt: "go"})

	// Cancel once the first line has been tee'd, proving the file is
	// written as the stream arrives rather than at exit.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if b, err := os.ReadFile(logPath); err == nil && strings.Contains(string(b), "init") {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()

	start := time.Now()
	res, err := RunHeadless(c, logPath)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("RunHeadless took %v after cancel; want well under the %v WaitDelay, so the child holding stdout was killed too", elapsed, headlessKillDelay)
	}
	if !errors.Is(err, ErrKilled) {
		t.Errorf("err = %v, want ErrKilled", err)
	}
	if res.Found {
		t.Errorf("result = %+v, want none for a stream that never finished", res)
	}
	logged, readErr := os.ReadFile(logPath)
	if readErr != nil || !strings.Contains(string(logged), `"subtype":"init"`) {
		t.Errorf("log = %q (%v), want the line streamed before the cancel", logged, readErr)
	}
}

// Resuming a step run appends to its existing log rather than truncating
// the earlier attempt's stream.
func TestRunHeadlessAppendsToExistingLog(t *testing.T) {
	installStubClaude(t, `echo '{"type":"result","subtype":"success","result":"second"}'`)
	logPath := filepath.Join(t.TempDir(), "log.jsonl")
	if err := os.WriteFile(logPath, []byte("first attempt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := HeadlessCmd(context.Background(), t.TempDir(), "s1", "", "", LaunchOpts{Prompt: "go"})
	if _, err := RunHeadless(c, logPath); err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	logged, _ := os.ReadFile(logPath)
	if !strings.HasPrefix(string(logged), "first attempt\n") || !strings.Contains(string(logged), "second") {
		t.Errorf("log = %q, want the first attempt kept and the second appended", logged)
	}
}

func TestStepLogPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	got, err := StepLogPath(7, 42)
	if err != nil {
		t.Fatalf("StepLogPath: %v", err)
	}
	if want := filepath.Join("/data", "tend", "runs", "7", "42.jsonl"); got != want {
		t.Errorf("StepLogPath = %q, want %q", got, want)
	}

	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/u")
	got, err = StepLogPath(7, 42)
	if err != nil {
		t.Fatalf("StepLogPath: %v", err)
	}
	if want := filepath.Join("/home/u", ".local", "share", "tend", "runs", "7", "42.jsonl"); got != want {
		t.Errorf("StepLogPath = %q, want %q", got, want)
	}
}

// TestRunHeadlessRealClaude runs one real headless turn against the
// installed CLI. It costs a (haiku) API call, so it is skipped under
// -short and wherever claude isn't on $PATH — CI included.
func TestRunHeadlessRealClaude(t *testing.T) {
	if testing.Short() {
		t.Skip("real claude run skipped in -short mode")
	}
	if _, err := exec.LookPath(binary); err != nil {
		t.Skip("claude not on PATH")
	}
	id, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	// claude keys its transcript directory by the resolved cwd, and on
	// macOS t.TempDir() lives under the /var -> /private/var symlink.
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "runs", "1", "1.jsonl")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := HeadlessCmd(ctx, cwd, id, "", "", LaunchOpts{
		Prompt: "Reply with exactly the word pong and nothing else.",
		Model:  "haiku",
	})
	res, err := RunHeadless(c, logPath)
	if err != nil {
		t.Fatalf("RunHeadless: %v", err)
	}
	if !res.Found || res.IsError {
		t.Fatalf("result = %+v, want a successful result event", res)
	}
	if !strings.Contains(strings.ToLower(res.Text), "pong") {
		t.Errorf("Text = %q, want it to contain pong", res.Text)
	}
	if res.SessionID != id {
		t.Errorf("SessionID = %q, want the pinned %q", res.SessionID, id)
	}
	// The log on disk parses to the same result the live tee produced.
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("opening log: %v", err)
	}
	defer f.Close()
	fromLog, err := ParseStream(f)
	if err != nil {
		t.Fatalf("ParseStream(log): %v", err)
	}
	if fromLog != res {
		t.Errorf("log parses to %+v, live tee gave %+v", fromLog, res)
	}
	// And the session is on disk where --resume will find it.
	if n, _ := TranscriptLineCount(cwd, id); n == 0 {
		t.Errorf("no transcript for %s under %s; the -p session would not be resumable", id, cwd)
	}
}
