package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/usage"
)

// usageLine is one assistant line, the smallest shape ParseLine accepts.
func usageLine(id string, out int64, at time.Time) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":"s1","cwd":"/tmp/p","message":{"id":%q,"model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":%d}}}`+"\n",
		at.UTC().Format(time.RFC3339), id, out)
}

// writeTranscript writes lines to <root>/<project>/<session>.jsonl,
// creating the project directory, the way claude lays the tree out.
func writeTranscript(t *testing.T, root, project, session string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, session+".jsonl")
	var content string
	for _, l := range lines {
		content += l
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestPollUsageScansThenSkipsUnchangedTree(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeTranscript(t, root, "proj-a", "sess1", usageLine("m1", 10, now.Add(-time.Hour)))

	msg, fp, changed := pollUsage(root, nil)
	if !changed {
		t.Fatalf("first poll: want changed")
	}
	if msg.err != nil {
		t.Fatalf("first poll: unexpected error: %v", msg.err)
	}
	if got, want := msg.summary.AllTime.Output, int64(10); got != want {
		t.Errorf("AllTime.Output = %d, want %d", got, want)
	}

	msg2, _, changed2 := pollUsage(root, &fp)
	if changed2 {
		t.Errorf("second poll on unchanged tree: want !changed, got %+v", msg2)
	}
	if msg2.entries != nil || msg2.err != nil || msg2.skipped != 0 || !msg2.summary.AllTime.IsZero() {
		t.Errorf("second poll on unchanged tree: want zero usageMsg, got %+v", msg2)
	}
}

func TestPollUsageRescansWhenATranscriptGrows(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	path := writeTranscript(t, root, "proj-a", "sess1", usageLine("m1", 10, now.Add(-time.Hour)))

	_, fp, changed := pollUsage(root, nil)
	if !changed {
		t.Fatalf("first poll: want changed")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString(usageLine("m2", 20, now.Add(-30*time.Minute))); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	msg, _, changed2 := pollUsage(root, &fp)
	if !changed2 {
		t.Fatalf("poll after growth: want changed")
	}
	if got, want := msg.summary.AllTime.Output, int64(30); got != want {
		t.Errorf("AllTime.Output = %d, want %d", got, want)
	}
}

func TestPollUsageRescansWhenASessionAppears(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeTranscript(t, root, "proj-a", "sess1", usageLine("m1", 10, now.Add(-time.Hour)))

	_, fp, changed := pollUsage(root, nil)
	if !changed {
		t.Fatalf("first poll: want changed")
	}

	writeTranscript(t, root, "proj-b", "sess2", usageLine("m2", 5, now.Add(-time.Hour)))

	_, _, changed2 := pollUsage(root, &fp)
	if !changed2 {
		t.Fatalf("poll after a new session appears: want changed")
	}
}

func TestPollUsageMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-ran-claude")

	msg, fp, changed := pollUsage(root, nil)
	if !changed {
		t.Fatalf("first poll on missing root: want changed")
	}
	if msg.err != nil {
		t.Errorf("first poll on missing root: unexpected error: %v", msg.err)
	}
	if !msg.summary.AllTime.IsZero() {
		t.Errorf("first poll on missing root: want empty summary, got %+v", msg.summary)
	}

	_, _, changed2 := pollUsage(root, &fp)
	if changed2 {
		t.Errorf("second poll on still-missing root: want !changed")
	}
}

func TestPollUsageCountsSkippedFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file regardless of its mode")
	}
	root := t.TempDir()
	now := time.Now()
	unreadable := writeTranscript(t, root, "proj-a", "sess1", usageLine("m1", 10, now.Add(-time.Hour)))
	writeTranscript(t, root, "proj-b", "sess2", usageLine("m2", 20, now.Add(-time.Hour)))

	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	msg, _, changed := pollUsage(root, nil)
	if !changed {
		t.Fatalf("poll: want changed")
	}
	if msg.skipped != 1 {
		t.Errorf("skipped = %d, want 1", msg.skipped)
	}
	if got, want := msg.summary.AllTime.Output, int64(20); got != want {
		t.Errorf("AllTime.Output = %d, want %d (readable file's tokens only)", got, want)
	}
}

func TestRunUsagePollerScansImmediatelyThenOnChange(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	path := writeTranscript(t, root, "proj-a", "sess1", usageLine("m1", 10, now.Add(-time.Hour)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var got []tea.Msg
	sent := make(chan struct{}, 8)
	send := func(m tea.Msg) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
		sent <- struct{}{}
	}

	go runUsagePoller(ctx, root, 20*time.Millisecond, send)

	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("first scan never arrived")
	}
	mu.Lock()
	first, ok := got[0].(usageMsg)
	mu.Unlock()
	if !ok || first.err != nil || first.summary.AllTime.Output != 10 {
		t.Fatalf("first message = %#v, want the initial scan", got[0])
	}

	select {
	case <-sent:
		t.Fatal("unexpected second message on an unchanged tree")
	case <-time.After(150 * time.Millisecond):
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString(usageLine("m2", 5, now.Add(-30*time.Minute))); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("second scan after the tree changed never arrived")
	}
	mu.Lock()
	second, ok := got[len(got)-1].(usageMsg)
	mu.Unlock()
	if !ok || second.summary.AllTime.Output != 15 {
		t.Fatalf("second message = %#v, want the grown total", got[len(got)-1])
	}
}

func TestApplyUsage(t *testing.T) {
	m, _ := newTestApp(t)
	entries := []usage.Entry{{Tokens: usage.Tokens{Output: 1}}}
	m = drive(t, m, usageMsg{summary: usage.Summary{AllTime: usage.Tokens{Output: 1}}, entries: entries, skipped: 2})

	a := m.(app)
	if !a.usageLoaded {
		t.Fatal("usageLoaded = false, want true")
	}
	if a.usage.AllTime.Output != 1 {
		t.Errorf("usage.AllTime.Output = %d, want 1", a.usage.AllTime.Output)
	}
	if a.usageSkipped != 2 {
		t.Errorf("usageSkipped = %d, want 2", a.usageSkipped)
	}
	if len(a.usageEntries) != 1 {
		t.Errorf("usageEntries = %+v, want 1 entry", a.usageEntries)
	}

	a = a.applyUsage(usageMsg{err: errors.New("boom")})
	if !a.usageLoaded {
		t.Error("usageLoaded after a failed scan = false, want true (previous reading kept)")
	}
	if a.usage.AllTime.Output != 1 {
		t.Errorf("usage after a failed scan = %+v, want the previous reading kept", a.usage)
	}

	var fresh app
	fresh = fresh.applyUsage(usageMsg{err: errors.New("boom")})
	if fresh.usageLoaded {
		t.Error("usageLoaded on a fresh app after a failed scan = true, want false")
	}
}
