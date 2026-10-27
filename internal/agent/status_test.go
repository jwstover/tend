package agent

import (
	"testing"

	"github.com/jwstover/tend/internal/task"
)

// Fixtures below are trimmed captures from a real `claude` 2.1.237 pane,
// harvested by driving a session inside a scratch tmux server and
// running `tmux capture-pane -p` at each phase of a multi-step tool
// call — not invented.

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

// An AskUserQuestion multi-choice prompt open and waiting on the user:
// the normal auto-mode status bar is replaced entirely by its own nav
// hint. Harvested from a real `claude` 2.1.238 pane captured mid-prompt
// during this bug's own investigation (docs/agent-sessions-plan.md §8.3).
const paneBlockedQuestion = `  1. Rewrite AGENTS.md as current-state truth (Recommended)
     Replace AGENTS.md wholesale: update the description, tech stack...
  2. Keep AGENTS.md's core, add a 'v2 scope' section
  3. Something else / let me describe it

Enter to select · Tab/Arrow keys to navigate · Esc to cancel`

// Claude Code 2.1.291 mid-turn, captured from a live tend session: the
// status bar no longer says "esc to interrupt", so the spinner line above
// the input box is the only working signal on screen.
const paneWorking291 = `  ⎿  $ go test ./internal/agent/

✶ Cerebrating… (1m 7s · ↓ 3.2k tokens)

──────────────────────────────────────────────────────────────── Clearer agent working icon ─
❯
────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents`

// The same build mid-turn after a thinking pause, which adds a clause.
const paneWorking291Thought = `✳ Cerebrating… (49s · ↓ 3.2k tokens · thought for 7s)

──────────────────────────────────────────────────────────────── Clearer agent working icon ─
❯
────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents`

// The same build back at the prompt: past tense, no ellipsis. The
// transcript above it quotes a spinner line mid-sentence, which must not
// count because it does not start the line.
const paneIdle291 = `⏺ The pane read "✢ Cerebrating… (19s · ↓ 738 tokens)" while it worked.

✻ Churned for 1m 20s · done 11:26 AM
※ recap: We're deleting the old PDP quote sync. (disable recaps in /config)
──────────────────────────────────────────────── Cleanup MR: delete the PDP quote sync path ─
❯
────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents`

func TestClassifyPane(t *testing.T) {
	cases := []struct {
		name string
		pane string
		want task.SessionStatus
	}{
		{"spinner mid-turn", paneWorkingSpinner, task.SessionWorking},
		{"running a tool", paneWorkingRunningTool, task.SessionWorking},
		{"2.1.291 spinner mid-turn", paneWorking291, task.SessionWorking},
		{"2.1.291 spinner after thinking", paneWorking291Thought, task.SessionWorking},
		{"2.1.291 idle at prompt", paneIdle291, task.SessionUnknown},
		{"idle at prompt after a turn", paneIdleAtPrompt, task.SessionUnknown},
		{"welcome screen, nothing sent yet", paneWelcome, task.SessionUnknown},
		{"AskUserQuestion prompt open", paneBlockedQuestion, task.SessionBlocked},
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

// classifyPane must never report idle/ended/starting itself — hooks own
// those authoritatively, and the poller that calls this only ever acts
// on SessionWorking and SessionBlocked results (see pollSessions in
// internal/tui). Anything it can't positively identify as one of those
// two has to fall back to unknown, not a guess at one of the hook-owned
// states.
func TestClassifyPaneNeverReportsHookOwnedStatuses(t *testing.T) {
	for _, st := range []task.SessionStatus{task.SessionIdle, task.SessionEnded, task.SessionStarting} {
		if got := ClassifyPane(paneIdleAtPrompt); got == st {
			t.Fatalf("ClassifyPane returned hook-owned status %q", st)
		}
	}
}
