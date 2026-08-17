package tui

import (
	"fmt"
	"path/filepath"
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
		sess := sessions[i]
		return a, resumeSessionCmd(sess.ID, sess.Cwd, sess.ExternalID)
	}
	return a, nil
}

// launchSessionCmd pins a fresh session id, suspends the TUI, and hands
// the terminal to claude. The store row is written only once the process
// returns cleanly (see sessionFinishedMsg), mirroring editBodyCmd's
// don't-save-on-error handling for $EDITOR.
func launchSessionCmd(taskID int64, cwd, label string) tea.Cmd {
	if err := agent.CheckInstalled(); err != nil {
		return errCmd(err)
	}
	id, err := agent.NewSessionID()
	if err != nil {
		return errCmd(err)
	}
	c := agent.LaunchCmd(cwd, id, label)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return sessionFinishedMsg{taskID: taskID, externalID: id, cwd: cwd, label: label, err: err}
	})
}

// resumeSessionCmd reopens an existing session in its stored directory.
func resumeSessionCmd(sessionRowID int64, cwd, externalID string) tea.Cmd {
	if err := agent.CheckInstalled(); err != nil {
		return errCmd(err)
	}
	c := agent.ResumeCmd(cwd, externalID)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return sessionResumedMsg{sessionRowID: sessionRowID, err: err}
	})
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
				s.Link.Render(filepath.Base(sess.Cwd)) + "  " + s.Muted.Render(age)
		} else {
			content = "  " + s.Muted.Render(num) + s.Dimmed.Render(filepath.Base(sess.Cwd)) +
				"  " + s.Muted.Render(age)
		}
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}
