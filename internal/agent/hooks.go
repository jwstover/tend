package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/jwstover/tend/internal/task"
)

// HookPayload is the subset of Claude Code's hook JSON tend reads from
// stdin. The full payload carries more per-event (Stop adds
// last_assistant_message and stop_hook_active, SessionEnd adds reason,
// SessionStart adds source); everything tend needs to find the row is in
// these fields, and the rest is deliberately ignored so a payload that
// grows new keys stays readable.
//
// SessionID is the claude --session-id UUID — the same value tend pinned
// at launch and stored as agent_sessions.external_id, which is what
// makes correlation a lookup rather than a join. Verified against the
// real CLI (docs/agent-sessions-plan.md §8.2), not assumed from docs.
type HookPayload struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Cwd           string `json:"cwd"`
}

// ParseHookPayload decodes one hook payload from r.
func ParseHookPayload(r io.Reader) (HookPayload, error) {
	var p HookPayload
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return HookPayload{}, fmt.Errorf("decoding hook payload: %w", err)
	}
	return p, nil
}

// hookEvents maps the Claude Code hook events tend subscribes to onto the
// status each implies. These four are every event that says something
// about a session's state without tend having to look at the screen:
//
//   - SessionStart — claude is up. Nothing is known about what it's doing
//     yet, hence "starting" rather than idle.
//   - Stop — a turn finished and claude is sitting at the input prompt.
//   - Notification — claude is waiting on the user, typically a
//     permission prompt.
//   - SessionEnd — the session is over. Note this also fires on /clear,
//     where the process keeps running, so it is never treated as the
//     authority on liveness; `tmux has-session` is (see HasSession).
//
// There is deliberately no "working" entry: Claude Code fires no hook
// mid-tool-call, which is the whole reason §8.3's capture-pane poller
// exists.
var hookEvents = map[string]task.SessionStatus{
	"SessionStart": task.SessionStarting,
	"Stop":         task.SessionIdle,
	"Notification": task.SessionBlocked,
	"SessionEnd":   task.SessionEnded,
}

// StatusForEvent maps a hook event name to the session status it implies,
// reporting false for an event tend doesn't subscribe to.
func StatusForEvent(event string) (task.SessionStatus, bool) {
	st, ok := hookEvents[event]
	return st, ok
}

// hookSettings mirrors the slice of Claude Code's settings schema tend
// generates: one matcher-less entry per event, each running a single
// command hook. It is written to a file passed as --settings, which
// loads *additional* settings rather than replacing the user's own —
// confirmed against the real CLI, and the reason this is safe to inject
// at all.
type hookSettings struct {
	Hooks map[string][]hookMatcher `json:"hooks"`
}

type hookMatcher struct {
	Hooks []hookCommand `json:"hooks"`
}

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// hookTimeout bounds each hook invocation, in seconds. Stop runs
// synchronously between turns, so a tend that can't reach its database —
// a locked file, a disk hiccup — must fail fast rather than sit in the
// user's way. Losing a status update costs nothing; stalling a session
// is the only real harm this feature can do.
const hookTimeout = 5

// WriteHookSettings writes a per-session --settings file wiring `tend
// agent-hook` to the events in hookEvents, so a launched or resumed
// session reports its own status back into the same sqlite database tend
// is reading (docs/agent-sessions-plan.md §8.2). No socket server and no
// daemon: tend already owns a WAL-mode database every instance can write
// to.
//
// dbPath is passed explicitly rather than left to resolveDBPath's
// defaults because the hook subprocess inherits claude's environment,
// not tend's, so a tend launched with --db or a different TEND_DB would
// otherwise have its sessions reporting into the wrong database.
//
// Returns "" with a no-op cleanup when `tend` isn't resolvable on $PATH,
// exactly as WriteMCPConfig does: status reporting is a convenience on
// top of launch/resume, never a reason for either to fail.
func WriteHookSettings(dbPath string) (path string, cleanup func(), err error) {
	noop := func() {}

	tendPath, lookErr := exec.LookPath("tend")
	if lookErr != nil {
		return "", noop, nil
	}

	b, err := buildHookSettings(tendPath, dbPath)
	if err != nil {
		return "", noop, err
	}

	f, err := os.CreateTemp("", "tend-hooks-*.json")
	if err != nil {
		return "", noop, fmt.Errorf("creating hook settings temp file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		os.Remove(f.Name())
		return "", noop, fmt.Errorf("writing hook settings: %w", err)
	}

	name := f.Name()
	return name, func() { os.Remove(name) }, nil
}

// buildHookSettings renders the settings JSON wiring every event in
// hookEvents to `tend agent-hook`. Split out from WriteHookSettings so
// the generated shape is testable without a `tend` on $PATH or a temp
// file on disk.
func buildHookSettings(tendPath, dbPath string) ([]byte, error) {
	matchers := make(map[string][]hookMatcher, len(hookEvents))
	for event := range hookEvents {
		cmd := fmt.Sprintf("%s agent-hook %s --db %s",
			shellQuote(tendPath), event, shellQuote(dbPath))
		matchers[event] = []hookMatcher{{
			Hooks: []hookCommand{{Type: "command", Command: cmd, Timeout: hookTimeout}},
		}}
	}
	b, err := json.Marshal(hookSettings{Hooks: matchers})
	if err != nil {
		return nil, fmt.Errorf("marshaling hook settings: %w", err)
	}
	return b, nil
}

// shellQuote wraps s in single quotes for the shell Claude Code runs hook
// commands through. Both the tend binary path and the database path can
// contain spaces, and the database path is user-supplied via --db or
// TEND_DB, so neither can be interpolated bare.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
