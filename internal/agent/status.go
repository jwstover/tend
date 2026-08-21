package agent

import (
	"strings"

	"github.com/jwstover/tend/internal/task"
)

// workingChrome is the literal substrings that only appear in a captured
// pane while Claude Code is actively generating or running a tool —
// harvested from a real `claude` 2.1.237 session driven inside a scratch
// tmux session (docs/agent-sessions-plan.md §8.3), not assumed.
//
// The obvious first idea — matching the rotating spinner verb ("Nucleat‑
// ing…", "Churning…", "Cooking…", …) — was dropped after probing: the
// verb and glyph both vary per turn from a large, undocumented set, so a
// literal table would constantly go stale. The bottom status bar's hint
// text does not: it reads "… · esc to interrupt · …" for the whole
// interruptible window (generating or running a tool) and drops the
// clause entirely the instant Claude Code returns to the input prompt.
// One stable substring beats an open-ended list of spinner text.
var workingChrome = []string{
	"esc to interrupt",
}

// ClassifyPane reports what a captured tmux pane implies about a Claude
// Code session's status. It only ever returns task.SessionWorking (chrome
// recognized) or task.SessionUnknown (nothing recognized) — hooks already
// own idle/blocked/ended authoritatively (§8.2), so the poller that calls
// this only ever acts on the working case and must never let a
// misclassified pane downgrade a status a hook already set with more
// authority.
//
// Exported (the design doc's own sketch, §8.3, wrote this lowercase as
// `classifyPane`) because the poller that drives it lives in
// internal/tui, a sibling package — AGENTS.md §6's layering has tui
// depend on agent already (WriteConfig, HasSession, CapturePane), never
// the reverse, so this has to be a normal exported symbol to be callable
// from there at all.
func ClassifyPane(text string) task.SessionStatus {
	for _, chrome := range workingChrome {
		if strings.Contains(text, chrome) {
			return task.SessionWorking
		}
	}
	return task.SessionUnknown
}
