package tui

import (
	"fmt"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/vcs"
)

// diffToolLookPath resolves a preset's binary; tests replace it.
var diffToolLookPath = exec.LookPath

// diffToolReadyMsg carries the built command back to the loop, which hands
// the terminal to it. note is a flash to show first (a fallback, several
// links).
type diffToolReadyMsg struct {
	cmd  *exec.Cmd
	note flash
}

// diffToolFinishedMsg is the handoff returning.
type diffToolFinishedMsg struct{ err error }

// openDiffToolCmd gathers the task's links, the diff_tool setting and the
// base off the loop, and builds the command; it runs nothing. External
// tools see refs only, so this shows committed work.
func (a app) openDiffToolCmd(t task.Task) tea.Cmd {
	return func() tea.Msg {
		links, err := a.store.TaskBranches(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		if len(links) == 0 {
			return statusMsg(flash{text: "no branch linked (L to link one)"})
		}
		// TODO(owner): #797 passes the Diff tab's selected link.
		link := links[len(links)-1] // TaskBranches is oldest first
		note := ""
		if len(links) > 1 {
			note = fmt.Sprintf("%d branches linked; using %s", len(links), link.Branch)
		}
		value, _, err := a.store.Setting(a.ctx, "diff_tool")
		if err != nil {
			return errMsg{err}
		}
		base, err := a.vcs.Base(a.ctx, link.RepoPath, link.Branch)
		if err != nil {
			return statusMsg(vcsFlash(link.RepoPath, err))
		}
		res, err := vcs.DiffToolCmd(value, link.RepoPath, base, link.Branch, diffToolLookPath)
		if err != nil {
			return statusMsg(flash{text: "diff tool: " + err.Error(), isErr: true})
		}
		if res.FellBack {
			note = fmt.Sprintf("%s not found; using %s", res.Missing, res.Used)
		}
		return diffToolReadyMsg{cmd: res.Cmd, note: flash{text: note}}
	}
}

func (a app) runDiffTool(msg diffToolReadyMsg) (tea.Model, tea.Cmd) {
	if msg.note.text != "" {
		a.status = msg.note
	}
	return a, tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
		return diffToolFinishedMsg{err}
	})
}
