package agent

import (
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// Fixtures below are trimmed captures from a real `claude` 2.1.237 pane
// (docs/agent-sessions-plan.md §8.3), harvested by driving a session
// inside a scratch tmux server and running `tmux capture-pane -p` at each
// phase of a multi-step tool call — not invented.

// The bottom status bar while a turn is actively generating or running a
// tool: the auto-mode hint gains an "esc to interrupt" clause it doesn't
// have at idle.
const paneWorkingSpinner = `● I'll run the first command with its sleep.

  Ran 1 shell command

✢ Nucleating…
                                                                        You've used 92% of your session limit
──────────────────────────────────────────────────────────────────────────── probe session ─
❯
────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents`

// A different turn, mid running-a-shell-command, with a different
// spinner verb — confirming the match isn't tied to one specific word.
const paneWorkingRunningTool = `● Running 1 shell command…

* Churning…

──────────────────────────────────────────────────────────────────────────── probe session ─
❯
────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents`

// Back at the input prompt once the turn finishes: the same status bar,
// minus the interrupt clause.
const paneIdleAtPrompt = `✻ Cooked for 25s

──────────────────────────────────────────────────────────────────────────── probe session ─
❯
────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents`

// The welcome screen shown before a first prompt is ever sent — also
// carries no interrupt clause.
const paneWelcome = `╭─── Claude Code v2.1.237 ────────────────────────────────────────────────────╮
│                 Welcome back Jake!                                          │
╰────────────────────────────────────────────────────────────────────────────╯

❯ Try "how does <filepath> work?"
──────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents`

func TestClassifyPane(t *testing.T) {
	cases := []struct {
		name string
		pane string
		want task.SessionStatus
	}{
		{"spinner mid-turn", paneWorkingSpinner, task.SessionWorking},
		{"running a tool", paneWorkingRunningTool, task.SessionWorking},
		{"idle at prompt after a turn", paneIdleAtPrompt, task.SessionUnknown},
		{"welcome screen, nothing sent yet", paneWelcome, task.SessionUnknown},
		{"empty pane", "", task.SessionUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyPane(c.pane); got != c.want {
				t.Errorf("ClassifyPane(%q) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// classifyPane must never report idle/blocked/ended itself — hooks own
// those authoritatively (§8.2), and the poller that calls this only ever
// acts on a SessionWorking result. Anything classifyPane can't positively
// identify as working has to fall back to unknown, not a guess at one of
// the hook-owned states.
func TestClassifyPaneNeverReportsHookOwnedStatuses(t *testing.T) {
	for _, st := range []task.SessionStatus{task.SessionIdle, task.SessionBlocked, task.SessionEnded, task.SessionStarting} {
		if got := ClassifyPane(paneIdleAtPrompt); got == st {
			t.Fatalf("ClassifyPane returned hook-owned status %q", st)
		}
	}
}
