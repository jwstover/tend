package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jwstover/tend/internal/task"
)

func hookOpener(f *fakeStore) func(context.Context) (Store, error) {
	return func(context.Context) (Store, error) { return f, nil }
}

func TestRunAgentHookRecordsStatus(t *testing.T) {
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
		f := &fakeStore{}
		payload := `{"session_id":"ext-1","hook_event_name":"` + c.event + `","cwd":"/tmp/work"}`
		if err := runAgentHook(context.Background(), hookOpener(f), c.event, strings.NewReader(payload)); err != nil {
			t.Fatalf("runAgentHook(%s): %v", c.event, err)
		}
		if got := f.statuses["ext-1"]; got != c.want {
			t.Errorf("%s recorded %q, want %q", c.event, got, c.want)
		}
	}
}

// The event comes from argv, not the payload — tend generates both sides
// of this contract, and argv is the explicit statement of which hook
// fired.
func TestRunAgentHookUsesArgvEventNotPayload(t *testing.T) {
	f := &fakeStore{}
	payload := `{"session_id":"ext-1","hook_event_name":"Notification"}`
	if err := runAgentHook(context.Background(), hookOpener(f), "Stop", strings.NewReader(payload)); err != nil {
		t.Fatalf("runAgentHook: %v", err)
	}
	if got := f.statuses["ext-1"]; got != task.SessionIdle {
		t.Errorf("recorded %q, want %q from the argv event", got, task.SessionIdle)
	}
}

func TestRunAgentHookRejectsUnsubscribedEvent(t *testing.T) {
	f := &fakeStore{}
	err := runAgentHook(context.Background(), hookOpener(f), "PreToolUse",
		strings.NewReader(`{"session_id":"ext-1"}`))
	if err == nil {
		t.Fatal("runAgentHook accepted an unsubscribed event")
	}
	if len(f.statuses) != 0 {
		t.Errorf("statuses = %v, want no write for an unsubscribed event", f.statuses)
	}
}

// An unrecognized event or a malformed payload must cost no file I/O:
// the store is only opened once there is something worth writing.
func TestRunAgentHookDoesNotOpenStoreOnBadInput(t *testing.T) {
	opened := false
	open := func(context.Context) (Store, error) {
		opened = true
		return &fakeStore{}, nil
	}
	if err := runAgentHook(context.Background(), open, "Stop", strings.NewReader("not json")); err == nil {
		t.Fatal("runAgentHook accepted non-JSON")
	}
	if err := runAgentHook(context.Background(), open, "Stop", strings.NewReader(`{"cwd":"/tmp"}`)); err == nil {
		t.Fatal("runAgentHook accepted a payload with no session_id")
	}
	if opened {
		t.Error("store opened for input that could never produce a write")
	}
}

// A hook must never wedge a session. Whatever runAgentHook reports, the
// command itself succeeds — the failure goes to stderr and nowhere else.
func TestAgentHookCommandAlwaysSucceeds(t *testing.T) {
	f := &fakeStore{statusErr: errors.New("database is locked")}
	cmd := newAgentHookCmd(hookOpener(f))
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	cmd.SetOut(&strings.Builder{})
	cmd.SetIn(strings.NewReader(`{"session_id":"ext-1","hook_event_name":"Stop"}`))
	cmd.SetArgs([]string{"Stop"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent-hook returned %v; a failing hook must not disrupt the session", err)
	}
	if !strings.Contains(stderr.String(), "database is locked") {
		t.Errorf("stderr = %q, want the underlying failure reported for debugging", stderr.String())
	}
}

func TestAgentHookCommandIsHidden(t *testing.T) {
	if !newAgentHookCmd(hookOpener(&fakeStore{})).Hidden {
		t.Error("agent-hook is not hidden; it's plumbing, not a user-facing command")
	}
}
