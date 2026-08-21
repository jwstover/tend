package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

func TestStatusForEvent(t *testing.T) {
	cases := []struct {
		event string
		want  task.SessionStatus
	}{
		{"SessionStart", task.SessionStarting},
		{"Stop", task.SessionIdle},
		{"Notification", task.SessionBlocked},
		{"SessionEnd", task.SessionEnded},
	}
	for _, c := range cases {
		got, ok := StatusForEvent(c.event)
		if !ok {
			t.Errorf("StatusForEvent(%q) not recognized", c.event)
			continue
		}
		if got != c.want {
			t.Errorf("StatusForEvent(%q) = %q, want %q", c.event, got, c.want)
		}
	}
}

// Claude Code fires many hooks tend doesn't subscribe to. An unrecognized
// one must be reported as such rather than silently writing a status.
func TestStatusForEventRejectsUnsubscribed(t *testing.T) {
	for _, event := range []string{"PreToolUse", "PostToolUse", "UserPromptSubmit", ""} {
		if _, ok := StatusForEvent(event); ok {
			t.Errorf("StatusForEvent(%q) reported subscribed", event)
		}
	}
}

// No hook can report "working" — Claude Code fires nothing mid-tool-call,
// which is the entire reason the capture-pane poller has to exist. If
// this ever starts failing, that design can be reconsidered.
func TestNoHookReportsWorking(t *testing.T) {
	for event, st := range hookEvents {
		if st == task.SessionWorking {
			t.Errorf("event %q maps to working; no hook can know that", event)
		}
	}
}

// The payload shape here was captured from a real `claude` run, not
// taken from docs. session_id is the field that makes correlation a
// direct lookup against agent_sessions.
func TestParseHookPayload(t *testing.T) {
	raw := `{"session_id":"6169c9d7-0fed-45cf-bc14-2b1ba4299b3b",` +
		`"transcript_path":"/home/u/.claude/projects/-tmp-x/6169c9d7.jsonl",` +
		`"cwd":"/tmp/x","prompt_id":"2223aaf7","permission_mode":"default",` +
		`"hook_event_name":"Stop","stop_hook_active":false,` +
		`"last_assistant_message":"hi","background_tasks":[],"session_crons":[]}`
	p, err := ParseHookPayload(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	if p.SessionID != "6169c9d7-0fed-45cf-bc14-2b1ba4299b3b" {
		t.Errorf("SessionID = %q", p.SessionID)
	}
	if p.HookEventName != "Stop" {
		t.Errorf("HookEventName = %q, want Stop", p.HookEventName)
	}
	if p.Cwd != "/tmp/x" {
		t.Errorf("Cwd = %q, want /tmp/x", p.Cwd)
	}
}

func TestParseHookPayloadRejectsGarbage(t *testing.T) {
	if _, err := ParseHookPayload(strings.NewReader("not json")); err == nil {
		t.Fatal("ParseHookPayload accepted non-JSON")
	}
}

func TestBuildHookSettingsWiresEveryEvent(t *testing.T) {
	b, err := buildHookSettings("/usr/local/bin/tend", "/data/tend.db")
	if err != nil {
		t.Fatalf("buildHookSettings: %v", err)
	}
	var got hookSettings
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshaling generated settings: %v", err)
	}
	if len(got.Hooks) != len(hookEvents) {
		t.Fatalf("wired %d events, want %d", len(got.Hooks), len(hookEvents))
	}
	for event := range hookEvents {
		matchers, ok := got.Hooks[event]
		if !ok || len(matchers) != 1 || len(matchers[0].Hooks) != 1 {
			t.Fatalf("event %q not wired to exactly one command hook: %+v", event, matchers)
		}
		h := matchers[0].Hooks[0]
		if h.Type != "command" {
			t.Errorf("event %q hook type = %q, want command", event, h.Type)
		}
		if h.Timeout != hookTimeout {
			t.Errorf("event %q timeout = %d, want %d — Stop runs between turns and must fail fast",
				event, h.Timeout, hookTimeout)
		}
		want := "agent-hook " + event
		if !strings.Contains(h.Command, want) {
			t.Errorf("event %q command = %q, want it to contain %q", event, h.Command, want)
		}
		if !strings.Contains(h.Command, "--db '/data/tend.db'") {
			t.Errorf("event %q command = %q, want the explicit --db; the hook inherits claude's "+
				"environment, not tend's", event, h.Command)
		}
	}
}

// Both the binary path and the user-supplied --db path can contain
// spaces, and Claude Code runs hook commands through a shell.
func TestBuildHookSettingsQuotesPaths(t *testing.T) {
	b, err := buildHookSettings("/opt/my tools/tend", "/home/u/My Data/tend.db")
	if err != nil {
		t.Fatalf("buildHookSettings: %v", err)
	}
	var got hookSettings
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cmd := got.Hooks["Stop"][0].Hooks[0].Command
	if !strings.HasPrefix(cmd, "'/opt/my tools/tend' agent-hook Stop ") {
		t.Errorf("command = %q, want the binary path single-quoted", cmd)
	}
	if !strings.Contains(cmd, "'/home/u/My Data/tend.db'") {
		t.Errorf("command = %q, want the db path single-quoted", cmd)
	}
}

func TestShellQuoteEscapesEmbeddedQuote(t *testing.T) {
	got := shellQuote(`/tmp/it's here/tend.db`)
	want := `'/tmp/it'\''s here/tend.db'`
	if got != want {
		t.Errorf("shellQuote = %s, want %s", got, want)
	}
}
