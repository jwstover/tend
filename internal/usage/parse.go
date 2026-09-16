package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// syntheticModel is the model claude records for messages it generated
// without calling the API -- an interrupt notice, an error stand-in. They
// carry an all-zero usage block and must not be counted as traffic.
const syntheticModel = "<synthetic>"

// line is the subset of a transcript or stream-json line that usage
// reads. Every other field is ignored on purpose so the format can grow
// without breaking the parser, the way agent.streamEvent does.
type line struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	SessionID   string `json:"sessionId"`
	Cwd         string `json:"cwd"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreation            *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseLine reads one line of a transcript or a step's stream-json log,
// reporting an Entry only for an assistant message that actually reached
// the model. Everything else -- user turns, tool results, system events,
// synthetic messages, blank or non-JSON lines, and a partial final line
// from a session still being written -- reports false rather than an
// error, because a log being read while it is written must not fail the
// read.
func ParseLine(b []byte) (Entry, bool) {
	b = trimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return Entry{}, false
	}
	var l line
	if err := json.Unmarshal(b, &l); err != nil {
		return Entry{}, false
	}
	if l.Type != "assistant" || l.Message.Usage == nil || l.Message.Model == syntheticModel {
		return Entry{}, false
	}
	u := l.Message.Usage
	e := Entry{
		Tokens: Tokens{
			Input:         u.InputTokens,
			Output:        u.OutputTokens,
			CacheCreation: u.CacheCreationInputTokens,
			CacheRead:     u.CacheReadInputTokens,
		},
		Model:     l.Message.Model,
		SessionID: l.SessionID,
		MessageID: l.Message.ID,
		Cwd:       l.Cwd,
		Sidechain: l.IsSidechain,
	}
	if u.CacheCreation != nil {
		e.Ephemeral5m = u.CacheCreation.Ephemeral5m
		e.Ephemeral1h = u.CacheCreation.Ephemeral1h
	}
	// A step's stream-json log carries no timestamp; the step run's own
	// StartedAt is the time in that case, so a zero At is left for the
	// caller to fill rather than guessed at here.
	if l.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, l.Timestamp); err == nil {
			e.At = t
		}
	}
	if e.IsZero() {
		return Entry{}, false
	}
	return e, true
}

// Parse reads every assistant message in r. Only a read error is an
// error: an unparseable line is skipped, so a truncated or still-growing
// log yields everything written so far.
func Parse(r io.Reader) ([]Entry, error) {
	var out []Entry
	sc := bufio.NewScanner(r)
	// Transcript lines carry whole tool results and can be very large; the
	// same 16MB ceiling agent.ParseStream uses.
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if e, ok := ParseLine(sc.Bytes()); ok {
			out = append(out, e)
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("reading usage log: %w", err)
	}
	return out, nil
}

// ParseFile reads every assistant message in the log at path. A missing
// file is not an error -- a step that died before claude wrote anything
// leaves none -- and reports no entries.
func ParseFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening usage log %s: %w", path, err)
	}
	defer f.Close()
	return Parse(f)
}

// Dedup drops entries whose message id has already been seen, keeping the
// first. Claude appends a resumed or forked session's replayed history to
// the new transcript, so the same assistant message appears in more than
// one file; counting it once per appearance would overstate usage by the
// size of every replay. Entries with no message id (a step log that
// omitted it) are always kept, since there is nothing to match them on.
func Dedup(entries []Entry) []Entry {
	seen := make(map[string]struct{}, len(entries))
	out := entries[:0:0]
	for _, e := range entries {
		if e.MessageID != "" {
			if _, dup := seen[e.MessageID]; dup {
				continue
			}
			seen[e.MessageID] = struct{}{}
		}
		out = append(out, e)
	}
	return out
}

// TranscriptRoot is ~/.claude/projects, where claude writes one directory
// per project and one .jsonl per session inside it. CLAUDE_CONFIG_DIR
// moves claude's whole config directory, so it is honoured here too.
func TranscriptRoot() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "projects"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating claude transcripts: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// ScanTranscripts reads every session transcript under root and returns
// their entries deduplicated, oldest first. A root that does not exist
// reports no entries and no error: a machine that has never run claude
// interactively is not a failure, it is an empty total.
//
// A file or directory that cannot be read is skipped rather than failing
// the scan -- one unreadable session must not blank the whole usage view
// -- and the count of skipped files and directories is returned so a
// caller can say so.
func ScanTranscripts(root string) (entries []Entry, skipped int, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// The root itself -- WalkDir reports its lstat failure with a nil
			// DirEntry. Returned so a missing tree is answered by the
			// os.IsNotExist check below, rather than counted as one skipped
			// file.
			if d == nil {
				return err
			}
			skipped++
			// An unreadable directory is skipped along with its contents.
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		got, perr := ParseFile(path)
		if perr != nil {
			skipped++
			return nil
		}
		entries = append(entries, got...)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, skipped, fmt.Errorf("scanning claude transcripts: %w", err)
	}
	entries = Dedup(entries)
	sortByTime(entries)
	return entries, skipped, nil
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 {
		c := b[len(b)-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}
