package agent

import (
	"regexp"
	"strings"

	"github.com/jwstover/tend/internal/task"
)

// workingChrome is the literal substrings that only appear in a captured
// pane while Claude Code is actively generating or running a tool —
// harvested from a real `claude` 2.1.237 session driven inside a scratch
// tmux session, not assumed.
//
// The obvious first idea — matching the rotating spinner verb ("Nucleat‑
// ing…", "Churning…", "Cooking…", …) — was dropped after probing: the
// verb and glyph both vary per turn from a large, undocumented set, so a
// literal table would constantly go stale. The bottom status bar's hint
// text does not: it reads "… · esc to interrupt · …" for the whole
// interruptible window (generating or running a tool) and drops the
// clause entirely the instant Claude Code returns to the input prompt.
// One stable substring beats an open-ended list of spinner text.
//
// Claude Code 2.1.291 dropped that clause: the status bar now reads the
// same while working as at idle. What still tells them apart is the
// spinner line above the input box, which while a turn runs is a glyph,
// a one-word verb with an ellipsis, then the elapsed time in parentheses
// ("✢ Cerebrating… (1m 7s · ↓ 3.2k tokens)"), and once it ends becomes
// past tense with no ellipsis ("✻ Churned for 1m 20s · done 11:26 AM").
// workingSpinner matches that shape rather than any verb or glyph, for
// the same reason the verbs were never listed: both rotate. The literal
// stays for older builds.
var workingChrome = []string{
	"esc to interrupt",
}

// workingSpinner is the shape of the mid-turn spinner line, anchored to
// the start of a line so the same characters inside transcript text do
// not count: one glyph, a space, a single word ending in "…", then " ("
// and a digit for the elapsed time.
var workingSpinner = regexp.MustCompile(`(?m)^\S \p{L}+… \(\d`)

// blockedChrome is literal substrings that only appear while an
// interactive prompt — a permission request or an AskUserQuestion-style
// menu — is open, waiting on the user. Harvested the same way
// workingChrome was, by directly capturing a real `claude` 2.1.238 pane
// mid-prompt: an AskUserQuestion menu replaces the normal auto-mode
// status bar entirely with its own nav hint, so this is a positive
// signal distinct from "not working" rather than an inference from
// absence.
//
// Not exhaustive. This is the AskUserQuestion multi-choice prompt's own
// footer; a plain yes/no tool-permission prompt may render different
// chrome not yet captured here. Extend this list the same way
// workingChrome gets extended, once observed — never by inference.
var blockedChrome = []string{
	"Tab/Arrow keys to navigate",
}

// ClassifyPane reports what a captured tmux pane implies about a Claude
// Code session's status: task.SessionWorking when interruptible-turn
// chrome is recognized, task.SessionBlocked when an interactive prompt's
// chrome is recognized, or task.SessionUnknown when neither is. Hooks
// still own idle/starting/ended authoritatively. The poller that calls
// this must never let a misclassified pane downgrade a status a hook
// already set with more authority, and — the reason task.SessionBlocked
// is a real return value here rather than folded into "unknown" — must
// never let an unrecognized prompt variant read as plain idle and get
// silently cleared.
//
// Exported, not lowercase, because the poller that drives it lives in
// internal/tui, a sibling package — AGENTS.md's dependency layering has
// tui depend on agent already (WriteConfig, HasSession, CapturePane),
// never the reverse, so this has to be a normal exported symbol to be
// callable from there at all.
func ClassifyPane(text string) task.SessionStatus {
	for _, chrome := range workingChrome {
		if strings.Contains(text, chrome) {
			return task.SessionWorking
		}
	}
	if workingSpinner.MatchString(text) {
		return task.SessionWorking
	}
	for _, chrome := range blockedChrome {
		if strings.Contains(text, chrome) {
			return task.SessionBlocked
		}
	}
	return task.SessionUnknown
}
