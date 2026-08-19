package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/task"
)

// loadSessionsForPicker fetches a task's sessions fresh (not from the
// detail-pane cache, which may be stale or never populated if the detail
// pane hasn't shown this task yet) and opens the picker once loaded.
func (a app) loadSessionsForPicker(t task.Task) tea.Cmd {
	return func() tea.Msg {
		sessions, err := a.store.ListSessionsForTask(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		return sessionsForPickerMsg{taskID: t.ID, label: t.Title, sessions: sessions}
	}
}

// openSessionPicker arms the picker with a task's sessions. With none yet,
// there's nothing to choose between, so it skips straight to the cwd
// prompt for a new session — the same skip-the-picker-when-there's-only-
// one-option move resolveURLs makes for a single link.
func (a *app) openSessionPicker(msg sessionsForPickerMsg) tea.Cmd {
	a.sessionsCache[msg.taskID] = msg.sessions
	if len(msg.sessions) == 0 {
		return a.openSessionCwdPrompt(msg.taskID, msg.label, a.defaultCwd(msg.sessions))
	}
	a.sessionPickerOpen = true
	a.sessionPickerTaskID = msg.taskID
	a.sessionPickerLabel = msg.label
	a.sessionPickerSessions = msg.sessions
	a.sessionPickerSel = 0
	return nil
}

func (a *app) closeSessionPicker() {
	a.sessionPickerOpen = false
	a.sessionPickerSessions = nil
	a.sessionPickerSel = 0
}

// defaultCwd suggests where to launch a new session: the most recently
// active existing session's directory (ListSessionsForTask orders newest
// first), else the directory tend itself was started from.
func (a app) defaultCwd(sessions []task.Session) string {
	if len(sessions) > 0 {
		return sessions[0].Cwd
	}
	return a.startCwd
}

// openSessionCwdPrompt collects the working directory for a brand-new
// session, prefilled with the best guess so enter alone accepts it.
func (a *app) openSessionCwdPrompt(taskID int64, label, defaultCwd string) tea.Cmd {
	a.sessionLabel = label
	cmd := a.openPrompt(promptSessionCwd, fmt.Sprintf("cwd for new session on #%d: ", taskID), taskID)
	a.prompt.SetValue(defaultCwd)
	a.prompt.CursorEnd()
	return cmd
}

// handleSessionPickerKey owns the keyboard while the picker is open. Row 0
// is always "+ new session"; rows 1..N are the task's sessions, newest
// first. ↑/↓ move, ⏎ acts on the highlight, a digit 1-9 jumps straight to
// that session, esc dismisses.
func (a app) handleSessionPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		a.closeSessionPicker()
		return a, nil
	case "enter":
		return a.chooseSessionPickerRow(a.sessionPickerSel)
	case "up", "ctrl+p":
		if a.sessionPickerSel > 0 {
			a.sessionPickerSel--
		}
		return a, nil
	case "down", "ctrl+n":
		if a.sessionPickerSel < len(a.sessionPickerSessions) {
			a.sessionPickerSel++
		}
		return a, nil
	}
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		if row := int(msg.Text[0]-'1') + 1; row <= len(a.sessionPickerSessions) {
			return a.chooseSessionPickerRow(row)
		}
	}
	return a, nil
}

// chooseSessionPickerRow acts on a picker row: 0 launches a new session,
// anything else resumes the session at that index.
func (a app) chooseSessionPickerRow(row int) (tea.Model, tea.Cmd) {
	taskID, label, sessions := a.sessionPickerTaskID, a.sessionPickerLabel, a.sessionPickerSessions
	a.closeSessionPicker()
	if row == 0 {
		return a, a.openSessionCwdPrompt(taskID, label, a.defaultCwd(sessions))
	}
	if i := row - 1; i >= 0 && i < len(sessions) {
		return a, resumeSessionCmd(sessions[i], a.dbPath)
	}
	return a, nil
}

// wrapInTmux rewrites a direct claude command to run inside a named tmux
// session on tend's own server, so detaching backgrounds it instead of
// ending it (docs/agent-sessions-plan.md §8.1). Returns the unmodified
// command with an empty name when tmux isn't installed or its config
// can't be written — launch and resume then behave exactly as they did
// before §8.1, just without backgrounding. tmux is a capability here,
// never a requirement.
func wrapInTmux(c *exec.Cmd, externalID string) (wrapped *exec.Cmd, name, confPath string) {
	if !agent.TmuxInstalled() {
		return c, "", ""
	}
	confPath, err := agent.WriteConfig()
	if err != nil {
		return c, "", ""
	}
	name = agent.SessionName(externalID)
	return agent.WrapTmux(c, name, confPath), name, confPath
}

// launchSessionCmd pins a fresh session id, suspends the TUI, and hands
// the terminal to claude — wrapped in tmux where available, so tmux's
// detach chord backgrounds the session rather than ending it. The store
// row is written only once the process returns cleanly (see
// sessionFinishedMsg), mirroring editBodyCmd's don't-save-on-error
// handling for $EDITOR. dbPath wires the session's task-bound MCP tools
// (docs/agent-sessions-plan.md §9.2); a config write failure just means
// no MCP tools this session, not a failure to launch, so it's swallowed
// rather than surfaced as errCmd.
//
// The MCP config is only cleaned up when the session is really over. A
// backgrounded session still has a live claude process holding that
// config, and deleting it out from under a running session would break
// its tools on any reconnect — leaking a small temp file is the cheaper
// mistake.
func launchSessionCmd(taskID int64, cwd, label, dbPath string) tea.Cmd {
	if err := agent.CheckInstalled(); err != nil {
		return errCmd(err)
	}
	id, err := agent.NewSessionID()
	if err != nil {
		return errCmd(err)
	}
	mcpPath, mcpCleanup, _ := agent.WriteMCPConfig(taskID, dbPath)
	hooksPath, hooksCleanup, _ := agent.WriteHookSettings(dbPath)
	c, tmuxName, confPath := wrapInTmux(agent.LaunchCmd(cwd, id, label, mcpPath, hooksPath), id)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		bg := err == nil && agent.HasSession(tmuxName, confPath)
		if !bg {
			mcpCleanup()
			hooksCleanup()
		}
		return sessionFinishedMsg{
			taskID:       taskID,
			externalID:   id,
			cwd:          cwd,
			label:        label,
			tmuxSession:  tmuxName,
			backgrounded: bg,
			err:          err,
		}
	})
}

// resumeSessionCmd reopens an existing session in its stored directory.
// It snapshots the transcript's current line count before handing off
// the terminal — recorded on the resulting msg as `since` — so a later
// recap can scope itself to only what's appended after this point (see
// agent.TranscriptExcerptSince). A read error here just means an
// unscoped recap later, not a failure to resume, so it's swallowed.
// dbPath is as in launchSessionCmd.
//
// It takes one of two paths. If the session's tmux session is still
// alive — it was backgrounded, possibly by a different tend process on
// this host — it attaches to the running claude rather than starting a
// second one against the same session id. This is the cross-instance
// reconnect case, and it needs no IPC beyond what tend already shares:
// the sqlite DB and the tmux socket. Otherwise (never backgrounded, or
// the server died with the host) it falls back to launching `claude
// --resume` fresh, wrapped in tmux so this time it can be backgrounded.
func resumeSessionCmd(sess task.Session, dbPath string) tea.Cmd {
	if err := agent.CheckInstalled(); err != nil {
		return errCmd(err)
	}
	since, _ := agent.TranscriptLineCount(sess.Cwd, sess.ExternalID)

	// A pre-§8.1 row has no stored name; derive the one it would have
	// had, so an old session becomes attachable from its first resume.
	name := sess.TmuxSession
	if name == "" {
		name = agent.SessionName(sess.ExternalID)
	}

	var c *exec.Cmd
	var confPath string
	var cleanup = func() {}
	if agent.TmuxInstalled() {
		confPath, _ = agent.WriteConfig()
	}
	if agent.HasSession(name, confPath) {
		// Already running: just take the terminal back. No new claude
		// process, so neither an MCP config nor a hook settings file is
		// needed — the live one is already wired with its own.
		c = agent.AttachCmd(name, confPath)
	} else {
		mcpPath, mcpCleanup, _ := agent.WriteMCPConfig(sess.TaskID, dbPath)
		hooksPath, hooksCleanup, _ := agent.WriteHookSettings(dbPath)
		cleanup = func() { mcpCleanup(); hooksCleanup() }
		c, name, confPath = wrapInTmux(
			agent.ResumeCmd(sess.Cwd, sess.ExternalID, mcpPath, hooksPath), sess.ExternalID)
	}

	return tea.ExecProcess(c, func(err error) tea.Msg {
		bg := err == nil && agent.HasSession(name, confPath)
		if !bg {
			cleanup()
		}
		return sessionResumedMsg{
			sessionRowID: sess.ID,
			taskID:       sess.TaskID,
			cwd:          sess.Cwd,
			externalID:   sess.ExternalID,
			since:        since,
			backgrounded: bg,
			err:          err,
		}
	})
}

// recapNotePrefix marks a log entry as an auto-generated session recap,
// not hand-typed, so the LOG section reads sensibly next to manual
// notes — a plain-text convention, not a schema change (see
// docs/agent-sessions-plan.md §5).
const recapNotePrefix = "[Claude] Session recap — "

// runRecap executes the headless recap call and returns its trimmed
// output. A package-level var, not a direct call to agent.RecapCmd, so
// tests can swap in a stub that never shells out to the real claude
// binary — recapSessionCmd fires from the same Update path
// sessions_test.go already drives with drive(), and this repo's own dev
// environment has claude installed, so a hard-coded exec call here would
// make `go test` intermittently invoke a real CLI call. excerpt is
// forwarded straight to agent.RecapPrompt to scope a resumed session's
// recap (see recapSessionCmd); empty means unscoped.
var runRecap = func(ctx context.Context, cwd, externalID, excerpt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, agent.RecapTimeout)
	defer cancel()
	out, err := agent.RecapCmd(ctx, cwd, externalID, agent.RecapPrompt(excerpt)).Output()
	return strings.TrimSpace(string(out)), err
}

// recapSessionCmd fires the headless claude -p follow-up after a
// session's tea.ExecProcess handoff returns, and stores a successful
// result as a log entry on the task via Store.AddLogEntry — the fix for
// "I took a break and lost the thread" (docs/agent-sessions-plan.md §5).
// It runs fully async and off the update loop, exactly like
// captureTask's Jira lookup, and swallows any failure — claude
// erroring, timing out, or returning nothing — by wrapping a bare
// recapDoneMsg{} rather than an error flash: losing an automatic recap
// isn't something the user needs to act on, unlike a genuine store
// failure. Every path returns a recapDoneMsg (never a literal nil), so
// Update's recapDoneMsg case can reliably decrement pendingRecaps and
// warn before quitting drops a call still in flight (see handleKey's
// quitPending chord).
//
// The same call also drives auto-naming (§5): agent.ParseRecapResponse
// splits the reply into the recap body and a short label describing what
// the session actually did, and a successfully-parsed label is persisted
// via Store.UpdateSessionLabel, replacing the static task-title snapshot
// CreateSession wrote at launch. Keyed by externalID rather than a row
// id since that's what's in hand for both a freshly launched session
// (whose row id this func never sees — sessionFinishedMsg's CreateSession
// call is a sibling in the same tea.Batch, not a dependency of this one)
// and a resumed one alike. A response that doesn't parse into a label
// just skips the rename — the recap itself still logs normally.
//
// since is nil for a freshly launched session (the whole transcript is
// new, so the recap is naturally unscoped) and non-nil for a resumed
// one — the transcript line count at the moment it was resumed
// (sessionResumedMsg.since), used to pull just the new turns via
// agent.TranscriptExcerptSince and scope the recap to them.
func (a app) recapSessionCmd(taskID int64, cwd, externalID string, since *int) tea.Cmd {
	return func() tea.Msg {
		var excerpt string
		if since != nil {
			excerpt, _ = agent.TranscriptExcerptSince(cwd, externalID, *since)
		}
		raw, err := runRecap(a.ctx, cwd, externalID, excerpt)
		if err != nil || raw == "" {
			return recapDoneMsg{}
		}
		label, body := agent.ParseRecapResponse(raw)
		if body == "" {
			return recapDoneMsg{}
		}
		if _, err := a.store.AddLogEntry(a.ctx, &taskID, recapNotePrefix+body); err != nil {
			return recapDoneMsg{inner: errMsg{err}}
		}
		if label != "" {
			if err := a.store.UpdateSessionLabel(a.ctx, externalID, label); err != nil {
				return recapDoneMsg{inner: errMsg{err}}
			}
		}
		return recapDoneMsg{inner: refreshMsg{status: flash{kind: flashEdit, text: "session recap logged"}}}
	}
}

// sessionPickerView renders the chooser box: numbered session rows below
// an always-present "+ new session" row, the selected one marked with the
// selection bar — same layout as urlPickerView.
func (a app) sessionPickerView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)

	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	lines = append(lines, row(s.Accent.Bold(true).Render("⚡ ")+
		s.Title.Render("claude sessions — ")+s.Muted.Render("⏎ or type a number")))
	lines = append(lines, "  "+cb.Render(g.TeeRight+hbar+g.TeeLeft))

	newRowStyle := func(selected bool) string {
		label := s.Accent.Render("+ new session")
		if selected {
			return s.SelBar.Render(g.SelBar+" ") + "  " + label
		}
		return "    " + s.Dimmed.Render("+ new session")
	}
	lines = append(lines, row(newRowStyle(a.sessionPickerSel == 0)))

	now := time.Now()
	for i, sess := range a.sessionPickerSessions {
		num := fmt.Sprintf("%d ", i+1)
		age := relTime(sess.LastActiveAt, now)
		var content string
		if i+1 == a.sessionPickerSel {
			content = s.SelBar.Render(g.SelBar+" ") + s.Accent.Render(num) +
				s.Link.Render(sess.Label) + "  " + s.Muted.Render(age)
		} else {
			content = "  " + s.Muted.Render(num) + s.Dimmed.Render(sess.Label) +
				"  " + s.Muted.Render(age)
		}
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}
