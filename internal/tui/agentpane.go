package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/task"
)

// The agents view's pane preview: for a live interactive session, the
// right pane shows what its tmux pane shows right now (capture-pane -e),
// refreshed every panePreviewInterval while the view is up. `v` flips the
// pane back to the session's task detail; a session with no pane to
// capture (ended, or its tmux session gone) shows the task detail anyway.
//
// The preview is read-only and cropped, never resized: the tmux window is
// the size of the client that last attached, and resizing it to fit the
// column would make claude reflow its UI under the user's next attach.

// panePreviewInterval is how often the selected session's pane is
// re-captured. The status poller's 4s is fine for a glyph and too slow
// for something meant to look live.
const panePreviewInterval = time.Second

// paneTickMsg asks for a fresh capture of the selected session's pane;
// panePreviewMsg carries one back, tagged with the session it is of.
type (
	paneTickMsg    struct{}
	panePreviewMsg struct {
		sessionID int64
		text      string
	}
)

// agentPane is the last capture of the selected interactive session's
// pane. captured says a capture for sessionID has landed (text "" then
// means there was no pane to read); loading guards against stacking
// captures when tmux is slower than the tick.
type agentPane struct {
	sessionID int64
	text      string
	captured  bool
	loading   bool
}

// capturePaneStyled reads a session's pane with its colours. A seam for
// the same reason capturePane is one: tests drive the view without tmux.
var capturePaneStyled = func(sess task.Session) (string, error) {
	name := sess.TmuxSession
	if name == "" {
		name = agent.SessionName(sess.ExternalID)
	}
	confPath, err := agent.WriteConfig()
	if err != nil {
		return "", err
	}
	return agent.CapturePaneStyled(name, confPath)
}

// runPanePreviewTicker sends paneTickMsg every interval until ctx is done.
// A goroutine like the other pollers rather than a tea.Tick chain, so
// there is only ever one ticker however often the view is entered; Update
// ignores the tick unless the agents view has a preview to refresh.
func runPanePreviewTicker(ctx context.Context, interval time.Duration, send func(tea.Msg)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send(paneTickMsg{})
		}
	}
}

// previewable reports whether the row is an interactive session that may
// still have a pane: ended sessions and workflow steps have none.
func (r agentRow) previewable() bool {
	return !r.headless() && r.sess.Status != task.SessionEnded
}

// previewing reports whether the pane should show the selected session's
// pane rather than its task: the preview is on, the row can have one, and
// the last capture of it did not come back empty.
func (a app) previewing() (agentRow, bool) {
	row, ok := a.selectedAgent()
	if !ok || a.av.hidePreview || !row.previewable() {
		return row, false
	}
	p := a.av.pane
	if p.sessionID == row.sess.ID && p.captured && p.text == "" {
		return row, false
	}
	return row, true
}

// previewActive is previewing without the row.
func (a app) previewActive() bool {
	_, ok := a.previewing()
	return ok
}

// previewToggleHint names what `v` switches an interactive session's pane to.
func (a app) previewToggleHint() string {
	if a.av.hidePreview {
		return "pane preview"
	}
	return "task detail"
}

// capturePreview re-captures the selected session's pane when the view
// is previewing one and no capture is already in flight.
func (a *app) capturePreview() tea.Cmd {
	if a.mode != modeAgents {
		return nil
	}
	row, ok := a.selectedAgent()
	if !ok || a.av.hidePreview || !row.previewable() {
		return nil
	}
	if a.av.pane.sessionID != row.sess.ID {
		a.av.pane = agentPane{sessionID: row.sess.ID}
	}
	if a.av.pane.loading {
		return nil
	}
	a.av.pane.loading = true
	sess := row.sess.Session
	return func() tea.Msg {
		text, err := capturePaneStyled(sess)
		if err != nil {
			text = ""
		}
		return panePreviewMsg{sessionID: sess.ID, text: text}
	}
}

// applyPanePreview installs a capture if the cursor is still on the
// session it is of, and redraws only when something changed.
func (a *app) applyPanePreview(msg panePreviewMsg) {
	if a.av.pane.sessionID != msg.sessionID {
		return
	}
	text := strings.TrimRight(msg.text, " \n")
	changed := !a.av.pane.captured || a.av.pane.text != text
	a.av.pane.loading = false
	a.av.pane.captured = true
	a.av.pane.text = text
	if changed && a.mode == modeAgents {
		a.renderAgentPane()
	}
}

// panePreviewLines crops a capture to the pane's width: every line cut to
// fit without breaking an escape sequence, and closed with a reset so a
// colour left open at the cut never bleeds into the column beside it.
func panePreviewLines(text string, width int) []string {
	raw := strings.Split(text, "\n")
	out := make([]string, len(raw))
	w := max(width-1, 1)
	for i, l := range raw {
		out[i] = " " + ansi.Truncate(strings.TrimRight(l, " "), w, "") + "\x1b[0m"
	}
	return out
}
