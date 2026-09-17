package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// realAssistantLine is one `assistant` line as claude 2.1.x writes it to a
// session transcript, its content elided. Every field the parser ignores
// is kept, so a test notices if an unknown field ever breaks the read.
const realAssistantLine = `{"parentUuid":"9f0c1e1a-0000-4000-8000-000000000001","isSidechain":false,"userType":"external","cwd":"/home/u/code/tend","sessionId":"a86b46d0-fb97-4b65-90ff-fdeb39b72072","version":"2.1.272","gitBranch":"main","type":"assistant","uuid":"3f2a0c88-0000-4000-8000-000000000002","timestamp":"2026-09-15T17:54:27.704Z","requestId":"req_011CF0000000000000000001","message":{"id":"msg_011Cf5esHrrKnGPRqHymesiv","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"ok"}],"stop_reason":"tool_use","usage":{"input_tokens":10,"cache_creation_input_tokens":8629,"cache_read_input_tokens":13708,"output_tokens":531,"output_tokens_details":{"thinking_tokens":447},"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":8629,"ephemeral_5m_input_tokens":0},"iterations":null,"speed":"standard"}}}`

// sidechainLine is a sub-agent message: isSidechain true, a distinct
// message id, smaller numbers.
const sidechainLine = `{"parentUuid":"9f0c1e1a-0000-4000-8000-000000000003","isSidechain":true,"userType":"external","cwd":"/home/u/code/tend","sessionId":"a86b46d0-fb97-4b65-90ff-fdeb39b72072","version":"2.1.272","gitBranch":"main","type":"assistant","uuid":"3f2a0c88-0000-4000-8000-000000000004","timestamp":"2026-09-15T17:55:00.000Z","requestId":"req_011CF0000000000000000002","message":{"id":"msg_011Cf5esHrrKnGPRqHymesiw","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"sub"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":100,"output_tokens":5,"output_tokens_details":{"thinking_tokens":0},"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},"iterations":null,"speed":"standard"}}}`

// syntheticLine is what claude really writes for a message it generated
// without calling the API: model "<synthetic>", a uuid-shaped message id,
// and an all-zero usage block.
const syntheticLine = `{"parentUuid":"9f0c1e1a-0000-4000-8000-000000000005","isSidechain":false,"userType":"external","cwd":"/home/u/code/tend","sessionId":"a86b46d0-fb97-4b65-90ff-fdeb39b72072","version":"2.1.272","gitBranch":"main","type":"assistant","uuid":"3f2a0c88-0000-4000-8000-000000000006","timestamp":"2026-09-15T17:56:00.000Z","message":{"id":"3f2a0c88-0000-4000-8000-000000000006","type":"message","role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"[Request interrupted]"}],"stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

// zeroUsageLine is a real model and id whose usage block is all zeros.
const zeroUsageLine = `{"parentUuid":"9f0c1e1a-0000-4000-8000-000000000007","isSidechain":false,"cwd":"/home/u/code/tend","sessionId":"a86b46d0-fb97-4b65-90ff-fdeb39b72072","type":"assistant","uuid":"3f2a0c88-0000-4000-8000-000000000008","timestamp":"2026-09-15T17:57:00.000Z","message":{"id":"msg_011Cf5esHrrKnGPRqHymesix","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

// noUsageLine carries an id and a model but no usage block at all.
const noUsageLine = `{"type":"assistant","message":{"id":"msg_011Cf5esHrrKnGPRqHymesiy","model":"claude-haiku-4-5-20251001"}}`

// userLine is a user turn: never counted.
const userLine = `{"type":"user","timestamp":"2026-09-15T17:53:00.000Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`

// stepLogLine is the stream-json shape a workflow step writes: no
// timestamp, cwd or sessionId.
const stepLogLine = `{"type":"assistant","message":{"id":"msg_step1","model":"claude-opus-5","usage":{"input_tokens":5,"output_tokens":7}}}`

// badTimestampLine is realAssistantLine with an unparseable clock.
const badTimestampLine = `{"parentUuid":"9f0c1e1a-0000-4000-8000-000000000001","isSidechain":false,"userType":"external","cwd":"/home/u/code/tend","sessionId":"a86b46d0-fb97-4b65-90ff-fdeb39b72072","version":"2.1.272","gitBranch":"main","type":"assistant","uuid":"3f2a0c88-0000-4000-8000-000000000002","timestamp":"not a time","requestId":"req_011CF0000000000000000001","message":{"id":"msg_011Cf5esHrrKnGPRqHymesiv","type":"message","role":"assistant","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"ok"}],"stop_reason":"tool_use","usage":{"input_tokens":10,"cache_creation_input_tokens":8629,"cache_read_input_tokens":13708,"output_tokens":531,"output_tokens_details":{"thinking_tokens":447},"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":8629,"ephemeral_5m_input_tokens":0},"iterations":null,"speed":"standard"}}}`

// entryEq compares everything but At, which is checked with At.Equal:
// two time.Time values can be the same instant and still differ under ==.
func entryEq(a, b Entry) bool {
	a.At, b.At = time.Time{}, time.Time{}
	return a == b
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Entry
		ok   bool
	}{
		{
			name: "real assistant line",
			line: realAssistantLine,
			want: Entry{
				Tokens: Tokens{
					Input:         10,
					Output:        531,
					CacheCreation: 8629,
					CacheRead:     13708,
					Ephemeral1h:   8629,
					Ephemeral5m:   0,
				},
				Model:     "claude-haiku-4-5-20251001",
				SessionID: "a86b46d0-fb97-4b65-90ff-fdeb39b72072",
				MessageID: "msg_011Cf5esHrrKnGPRqHymesiv",
				Cwd:       "/home/u/code/tend",
				Sidechain: false,
				At:        time.Date(2026, 9, 15, 17, 54, 27, 704_000_000, time.UTC),
			},
			ok: true,
		},
		{
			name: "sidechain line",
			line: sidechainLine,
			want: Entry{
				Tokens: Tokens{
					Input:     2,
					Output:    5,
					CacheRead: 100,
				},
				Model:     "claude-haiku-4-5-20251001",
				SessionID: "a86b46d0-fb97-4b65-90ff-fdeb39b72072",
				MessageID: "msg_011Cf5esHrrKnGPRqHymesiw",
				Cwd:       "/home/u/code/tend",
				Sidechain: true,
				At:        time.Date(2026, 9, 15, 17, 55, 0, 0, time.UTC),
			},
			ok: true,
		},
		{
			name: "step log line has no timestamp",
			line: stepLogLine,
			want: Entry{
				Tokens:    Tokens{Input: 5, Output: 7},
				Model:     "claude-opus-5",
				MessageID: "msg_step1",
			},
			ok: true,
		},
		{
			name: "bad timestamp keeps the tokens",
			line: badTimestampLine,
			want: Entry{
				Tokens: Tokens{
					Input:         10,
					Output:        531,
					CacheCreation: 8629,
					CacheRead:     13708,
					Ephemeral1h:   8629,
				},
				Model:     "claude-haiku-4-5-20251001",
				SessionID: "a86b46d0-fb97-4b65-90ff-fdeb39b72072",
				MessageID: "msg_011Cf5esHrrKnGPRqHymesiv",
				Cwd:       "/home/u/code/tend",
			},
			ok: true,
		},
		{name: "synthetic model", line: syntheticLine, ok: false},
		{name: "no usage block", line: noUsageLine, ok: false},
		{name: "all-zero usage", line: zeroUsageLine, ok: false},
		{name: "user line", line: userLine, ok: false},
		{name: "empty", line: "", ok: false},
		{name: "spaces", line: "   ", ok: false},
		{name: "tab newline", line: "\t\n", ok: false},
		{name: "not json", line: "hello", ok: false},
		{name: "malformed json", line: `{"type":"assistant"`, ok: false},
		{name: "partial trailing line", line: realAssistantLine[:len(realAssistantLine)*6/10], ok: false},
		{
			name: "padded with whitespace",
			line: "  " + realAssistantLine + "\r\n",
			want: Entry{
				Tokens: Tokens{
					Input:         10,
					Output:        531,
					CacheCreation: 8629,
					CacheRead:     13708,
					Ephemeral1h:   8629,
				},
				Model:     "claude-haiku-4-5-20251001",
				SessionID: "a86b46d0-fb97-4b65-90ff-fdeb39b72072",
				MessageID: "msg_011Cf5esHrrKnGPRqHymesiv",
				Cwd:       "/home/u/code/tend",
				At:        time.Date(2026, 9, 15, 17, 54, 27, 704_000_000, time.UTC),
			},
			ok: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseLine([]byte(tt.line))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (entry %+v)", ok, tt.ok, got)
			}
			if !tt.ok {
				return
			}
			if !entryEq(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			if tt.want.At.IsZero() {
				if !got.At.IsZero() {
					t.Errorf("At = %v, want zero", got.At)
				}
			} else if !got.At.Equal(tt.want.At) {
				t.Errorf("At = %v, want %v", got.At, tt.want.At)
			}
		})
	}
}

func TestParse(t *testing.T) {
	lines := []string{
		realAssistantLine,
		sidechainLine,
		syntheticLine,
		zeroUsageLine,
		noUsageLine,
		userLine,
		stepLogLine,
		badTimestampLine,
	}
	input := strings.Join(lines, "\n") + "\n" + realAssistantLine[:len(realAssistantLine)/3]

	got, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantIDs := []string{
		"msg_011Cf5esHrrKnGPRqHymesiv", // realAssistantLine
		"msg_011Cf5esHrrKnGPRqHymesiw", // sidechainLine
		"msg_step1",                    // stepLogLine
		"msg_011Cf5esHrrKnGPRqHymesiv", // badTimestampLine, same id, different message
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("Parse returned %d entries, want %d: %+v", len(got), len(wantIDs), got)
	}
	for i, id := range wantIDs {
		if got[i].MessageID != id {
			t.Errorf("entry %d MessageID = %q, want %q", i, got[i].MessageID, id)
		}
	}
}

func TestParseHandlesLinesOverBufioDefault(t *testing.T) {
	big := strings.Repeat("x", 200_000)
	line := `{"type":"assistant","timestamp":"2026-09-15T17:54:27.704Z","sessionId":"s1","cwd":"/tmp/p","message":{"id":"msg_big","model":"claude-opus-5","content":[{"type":"text","text":"` + big + `"}],"usage":{"input_tokens":1,"output_tokens":2}}}`

	got, err := Parse(strings.NewReader(line))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 1 || got[0].MessageID != "msg_big" {
		t.Fatalf("Parse of an oversized line = %+v, want one entry msg_big", got)
	}
}

func TestParseFileMissingIsNotAnError(t *testing.T) {
	entries, err := ParseFile(filepath.Join(t.TempDir(), "nope.jsonl"))
	if entries != nil || err != nil {
		t.Errorf("ParseFile(missing) = %v, %v, want nil, nil", entries, err)
	}
}

func TestDedupKeepsFirstOfAReplayedMessage(t *testing.T) {
	a := Entry{MessageID: "m1", Tokens: Tokens{Output: 10}}
	b := Entry{MessageID: "m2", Tokens: Tokens{Output: 20}}
	aReplay := Entry{MessageID: "m1", Tokens: Tokens{Output: 10}}

	got := Dedup([]Entry{a, b, aReplay})
	if len(got) != 2 {
		t.Fatalf("Dedup returned %d entries, want 2: %+v", len(got), got)
	}
	if got[0].MessageID != "m1" || got[1].MessageID != "m2" {
		t.Errorf("Dedup order = %+v, want [m1, m2]", got)
	}
	if sum := Sum(got).Output; sum != 30 {
		t.Errorf("Sum(Dedup(...)).Output = %d, want 30", sum)
	}
}

func TestDedupKeepsEveryEntryWithNoMessageID(t *testing.T) {
	entries := []Entry{{Tokens: Tokens{Output: 1}}, {Tokens: Tokens{Output: 2}}, {Tokens: Tokens{Output: 3}}}
	got := Dedup(entries)
	if len(got) != 3 {
		t.Errorf("Dedup of id-less entries = %d, want 3", len(got))
	}
}

func TestDedupNil(t *testing.T) {
	if got := Dedup(nil); len(got) != 0 {
		t.Errorf("Dedup(nil) = %+v, want empty", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// laterAssistantLine is a second, distinct message in root/proj-b/sess2.jsonl.
const laterAssistantLine = `{"type":"assistant","timestamp":"2026-09-15T18:00:00.000Z","sessionId":"sess2","cwd":"/home/u/code/proj-b","message":{"id":"msg_later","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

// earliestLine is the oldest timestamped entry in the scan tree.
const earliestLine = `{"type":"assistant","timestamp":"2026-09-15T10:00:00.000Z","sessionId":"sess3","cwd":"/home/u/code/proj-c","message":{"id":"msg_earliest","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":1,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

func TestScanTranscripts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "proj-a", "sess1.jsonl"),
		strings.Join([]string{realAssistantLine, userLine, syntheticLine}, "\n")+"\n")
	writeFile(t, filepath.Join(root, "proj-a", "notes.md"), "not a transcript\n")
	writeFile(t, filepath.Join(root, "proj-b", "sess2.jsonl"),
		strings.Join([]string{realAssistantLine, laterAssistantLine}, "\n")+"\n")
	writeFile(t, filepath.Join(root, "proj-b", "nested", "sess3.jsonl"), earliestLine+"\n")
	writeFile(t, filepath.Join(root, "proj-c", "empty.jsonl"), "")

	entries, skipped, err := ScanTranscripts(root)
	if err != nil {
		t.Fatalf("ScanTranscripts: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}

	distinct := Dedup([]Entry{
		mustParse(t, realAssistantLine),
		mustParse(t, laterAssistantLine),
		mustParse(t, earliestLine),
	})
	want := Sum(distinct)
	if got := Sum(entries); got != want {
		t.Errorf("Sum(entries) = %+v, want %+v (replay must count once)", got, want)
	}

	for i := 1; i < len(entries); i++ {
		if entries[i].At.Before(entries[i-1].At) {
			t.Fatalf("entries not sorted oldest first: %+v", entries)
		}
	}
	if entries[0].MessageID != "msg_earliest" {
		t.Errorf("first entry = %q, want msg_earliest", entries[0].MessageID)
	}
}

func mustParse(t *testing.T, line string) Entry {
	t.Helper()
	e, ok := ParseLine([]byte(line))
	if !ok {
		t.Fatalf("ParseLine failed to parse fixture: %s", line)
	}
	return e
}

func TestScanTranscriptsSkipsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 file regardless of its mode")
	}
	root := t.TempDir()
	unreadable := filepath.Join(root, "proj-a", "sess1.jsonl")
	writeFile(t, unreadable, realAssistantLine+"\n")
	writeFile(t, filepath.Join(root, "proj-b", "sess2.jsonl"), laterAssistantLine+"\n")

	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })

	entries, skipped, err := ScanTranscripts(root)
	if err != nil {
		t.Fatalf("ScanTranscripts: %v", err)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(entries) != 1 || entries[0].MessageID != "msg_later" {
		t.Errorf("entries = %+v, want just the readable file's entry", entries)
	}
}

func TestScanTranscriptsMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-ran-claude")
	entries, skipped, err := ScanTranscripts(root)
	if entries != nil || skipped != 0 || err != nil {
		t.Errorf("ScanTranscripts(missing root) = %v, %d, %v, want nil, 0, nil", entries, skipped, err)
	}
}

func TestTranscriptRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	got, err := TranscriptRoot()
	if err != nil {
		t.Fatalf("TranscriptRoot: %v", err)
	}
	if want := filepath.Join(dir, "projects"); got != want {
		t.Errorf("TranscriptRoot() = %q, want %q", got, want)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", "")
	got, err = TranscriptRoot()
	if err != nil {
		t.Fatalf("TranscriptRoot: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join(".claude", "projects")) {
		t.Errorf("TranscriptRoot() = %q, want a suffix of .claude/projects", got)
	}
}

func TestFingerprintTranscripts(t *testing.T) {
	root := t.TempDir()
	transcript := filepath.Join(root, "proj-a", "sess1.jsonl")
	writeFile(t, transcript, realAssistantLine+"\n")
	writeFile(t, filepath.Join(root, "proj-a", "notes.md"), "ignored\n")

	fp1, err := FingerprintTranscripts(root)
	if err != nil {
		t.Fatalf("FingerprintTranscripts: %v", err)
	}
	fp2, err := FingerprintTranscripts(root)
	if err != nil {
		t.Fatalf("FingerprintTranscripts: %v", err)
	}
	if !fp1.Equal(fp2) {
		t.Errorf("unchanged tree fingerprinted differently: %+v vs %+v", fp1, fp2)
	}

	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString(laterAssistantLine + "\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fp3, err := FingerprintTranscripts(root)
	if err != nil {
		t.Fatalf("FingerprintTranscripts: %v", err)
	}
	if fp3.Equal(fp1) {
		t.Errorf("appending to a transcript did not change the fingerprint: %+v", fp3)
	}

	writeFile(t, filepath.Join(root, "proj-d", "sess4.jsonl"), earliestLine+"\n")
	fp4, err := FingerprintTranscripts(root)
	if err != nil {
		t.Fatalf("FingerprintTranscripts: %v", err)
	}
	if fp4.Equal(fp3) {
		t.Errorf("a new session file did not change the fingerprint: %+v", fp4)
	}

	writeFile(t, filepath.Join(root, "proj-d", "more-notes.md"), "still ignored\n")
	fp5, err := FingerprintTranscripts(root)
	if err != nil {
		t.Fatalf("FingerprintTranscripts: %v", err)
	}
	if !fp5.Equal(fp4) {
		t.Errorf("a non-.jsonl file changed the fingerprint: %+v vs %+v", fp5, fp4)
	}

	missing := filepath.Join(t.TempDir(), "never-ran-claude")
	fpMissing, err := FingerprintTranscripts(missing)
	if err != nil {
		t.Fatalf("FingerprintTranscripts(missing): %v", err)
	}
	if !fpMissing.Equal(Fingerprint{}) {
		t.Errorf("FingerprintTranscripts(missing) = %+v, want zero", fpMissing)
	}
	if !(Fingerprint{}).Equal(Fingerprint{}) {
		t.Error("Fingerprint{}.Equal(Fingerprint{}) = false, want true")
	}
}
