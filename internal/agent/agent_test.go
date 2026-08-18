package agent

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewSessionID(t *testing.T) {
	seen := make(map[string]bool)
	for range 100 {
		id, err := NewSessionID()
		if err != nil {
			t.Fatalf("NewSessionID: %v", err)
		}
		if !uuidV4Re.MatchString(id) {
			t.Fatalf("NewSessionID = %q, not a v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("NewSessionID produced a duplicate: %q", id)
		}
		seen[id] = true
	}
}

func TestLaunchCmd(t *testing.T) {
	c := LaunchCmd("/tmp/work", "abc-123", "fix the bug", "")
	if c.Dir != "/tmp/work" {
		t.Errorf("Dir = %q, want /tmp/work", c.Dir)
	}
	want := []string{binary, "--session-id", "abc-123", "-n", "fix the bug"}
	if got := c.Args; !equalArgs(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
}

func TestLaunchCmdWithMCPConfig(t *testing.T) {
	c := LaunchCmd("/tmp/work", "abc-123", "fix the bug", "/tmp/mcp.json")
	want := []string{binary, "--session-id", "abc-123", "-n", "fix the bug", "--mcp-config", "/tmp/mcp.json"}
	if got := c.Args; !equalArgs(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
}

func TestResumeCmd(t *testing.T) {
	c := ResumeCmd("/tmp/work", "abc-123", "")
	if c.Dir != "/tmp/work" {
		t.Errorf("Dir = %q, want /tmp/work", c.Dir)
	}
	want := []string{binary, "--resume", "abc-123"}
	if got := c.Args; !equalArgs(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
}

func TestResumeCmdWithMCPConfig(t *testing.T) {
	c := ResumeCmd("/tmp/work", "abc-123", "/tmp/mcp.json")
	want := []string{binary, "--resume", "abc-123", "--mcp-config", "/tmp/mcp.json"}
	if got := c.Args; !equalArgs(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
}

func TestRecapCmd(t *testing.T) {
	c := RecapCmd(context.Background(), "/tmp/work", "abc-123", "summarize it")
	if c.Dir != "/tmp/work" {
		t.Errorf("Dir = %q, want /tmp/work", c.Dir)
	}
	want := []string{binary, "-p", "summarize it", "--resume", "abc-123"}
	if got := c.Args; !equalArgs(got, want) {
		t.Errorf("Args = %v, want %v", got, want)
	}
}

func TestParseRecapResponseWellFormed(t *testing.T) {
	raw := "LABEL: fixed the flaky test\nRECAP: Tracked down a race in the scheduler and stabilized it. Ready for review."
	label, recap := ParseRecapResponse(raw)
	if label != "fixed the flaky test" {
		t.Errorf("label = %q, want %q", label, "fixed the flaky test")
	}
	want := "Tracked down a race in the scheduler and stabilized it. Ready for review."
	if recap != want {
		t.Errorf("recap = %q, want %q", recap, want)
	}
}

func TestParseRecapResponseIgnoresSurroundingWhitespace(t *testing.T) {
	raw := "\n\n  LABEL:   fixed the flaky test  \nRECAP:   did the thing  \n\n"
	label, recap := ParseRecapResponse(raw)
	if label != "fixed the flaky test" {
		t.Errorf("label = %q, want %q", label, "fixed the flaky test")
	}
	if recap != "did the thing" {
		t.Errorf("recap = %q, want %q", recap, "did the thing")
	}
}

func TestParseRecapResponseFallsBackWithoutMarkers(t *testing.T) {
	label, recap := ParseRecapResponse("just a plain summary with no markers")
	if label != "" {
		t.Errorf("label = %q, want empty", label)
	}
	if recap != "just a plain summary with no markers" {
		t.Errorf("recap = %q, want the whole trimmed response", recap)
	}
}

func TestParseRecapResponseFallsBackOnEmptyRecap(t *testing.T) {
	label, recap := ParseRecapResponse("LABEL: fixed the flaky test\nRECAP:")
	if label != "" {
		t.Errorf("label = %q, want empty when RECAP has no content", label)
	}
	if recap != "LABEL: fixed the flaky test\nRECAP:" {
		t.Errorf("recap = %q, want the whole trimmed response as a fallback", recap)
	}
}

func TestParseRecapResponseFallsBackOnMarkersOutOfOrder(t *testing.T) {
	label, recap := ParseRecapResponse("RECAP: did the thing\nLABEL: fixed the flaky test")
	if label != "" {
		t.Errorf("label = %q, want empty when markers are out of order", label)
	}
	if recap != "RECAP: did the thing\nLABEL: fixed the flaky test" {
		t.Errorf("recap = %q, want the whole trimmed response as a fallback", recap)
	}
}

func TestRecapPromptScoping(t *testing.T) {
	base := RecapPrompt("")
	if strings.Contains(base, "Scope the recap") {
		t.Error("unscoped RecapPrompt should not mention scoping")
	}
	scoped := RecapPrompt("User: do X\nAssistant: did X")
	if !strings.Contains(scoped, "Scope the recap") {
		t.Error("scoped RecapPrompt should instruct the model to scope to the excerpt")
	}
	if !strings.Contains(scoped, "User: do X\nAssistant: did X") {
		t.Error("scoped RecapPrompt should include the excerpt verbatim")
	}
	if !strings.HasPrefix(scoped, base) {
		t.Error("scoped RecapPrompt should extend the base prompt, not replace it")
	}
}

// writeTranscript points $HOME at a temp dir and writes lines to the
// transcript file transcriptPath would resolve for (cwd, externalID),
// so TranscriptLineCount/TranscriptExcerptSince exercise the real path
// derivation instead of a stand-in.
func writeTranscript(t *testing.T, cwd, externalID string, lines []string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path, err := transcriptPath(cwd, externalID)
	if err != nil {
		t.Fatalf("transcriptPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestTranscriptLineCountMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	n, err := TranscriptLineCount("/tmp/work", "no-such-session")
	if err != nil {
		t.Fatalf("TranscriptLineCount: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0 for a session with no transcript yet", n)
	}
}

func TestTranscriptLineCountCounts(t *testing.T) {
	writeTranscript(t, "/tmp/work", "sess-1", []string{`{"type":"mode"}`, `{"type":"mode"}`, `{"type":"mode"}`})
	n, err := TranscriptLineCount("/tmp/work", "sess-1")
	if err != nil {
		t.Fatalf("TranscriptLineCount: %v", err)
	}
	if n != 3 {
		t.Errorf("n = %d, want 3", n)
	}
}

func TestTranscriptExcerptSinceScopesToNewTurns(t *testing.T) {
	oldUser := `{"type":"user","message":{"role":"user","content":"old question"}}`
	oldAsst := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"old answer"}]}}`
	newUser := `{"type":"user","message":{"role":"user","content":"new question"}}`
	newAsst := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"new answer"}]}}`
	toolUseOnly := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read"}]}}`
	toolResult := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"file contents"}]}}`
	meta := `{"type":"user","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>...</local-command-caveat>"}}`
	sidechain := `{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent chatter"}]}}`
	command := `{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"}}`
	noMessage := `{"type":"mode"}`

	lines := []string{oldUser, oldAsst}
	writeTranscript(t, "/tmp/work", "sess-2", lines)
	since := len(lines)

	// Append the rest after the marker was taken, mirroring a session
	// being resumed (marker = line count then) and continuing.
	path, err := transcriptPath("/tmp/work", "sess-2")
	if err != nil {
		t.Fatalf("transcriptPath: %v", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	for _, l := range []string{newUser, toolUseOnly, toolResult, meta, sidechain, command, noMessage, newAsst} {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("WriteString: %v", err)
		}
	}
	f.Close()

	got, err := TranscriptExcerptSince("/tmp/work", "sess-2", since)
	if err != nil {
		t.Fatalf("TranscriptExcerptSince: %v", err)
	}
	want := "User: new question\nAssistant: new answer"
	if got != want {
		t.Errorf("excerpt = %q, want %q", got, want)
	}
}

func TestTranscriptExcerptSinceMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := TranscriptExcerptSince("/tmp/work", "no-such-session", 0)
	if err != nil {
		t.Fatalf("TranscriptExcerptSince: %v", err)
	}
	if got != "" {
		t.Errorf("excerpt = %q, want empty for a missing transcript", got)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
