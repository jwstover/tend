package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/vcs"
)

// detailTab is which tab of the detail pane is showing.
type detailTab int

const (
	tabTask detailTab = iota
	tabDiff
)

// diffScrolloff is how many rows the view keeps between the cursor and the
// top or bottom edge, like vim's scrolloff.
const diffScrolloff = 3

// diffState is what the Diff tab has to say before it has rows.
type diffState int

const (
	diffIdle    diffState = iota // nothing loaded for this task yet
	diffReady                    // rows built
	diffNoLink                   // the task links no branch
	diffMissing                  // the selected link's branch is gone
	diffNoVCS                    // no git, or not a repository
	diffFailed                   // some other failure; notice holds it
)

// diffSource is what the load needs from the store.
type diffSource interface {
	TaskBranches(ctx context.Context, taskID int64) ([]task.BranchLink, error)
}

// diffLoadedMsg is a finished load. taskID and links let the loop drop a
// result for a task that is no longer selected.
type diffLoadedMsg struct {
	taskID      int64
	want        int64 // the link id asked for; a result for a choice since changed is dropped
	links       []task.BranchLink
	link        task.BranchLink // the one the diff is of; zero when none
	state       diffState
	notice      string
	committed   vcs.Diff
	uncommitted vcs.Diff
}

// loadDiffCmd reads the task's links off the loop and, for the selected one
// (want, else the newest), asks git whether the branch exists and for both
// diffs. It never fails: every outcome is a state on the message.
func loadDiffCmd(ctx context.Context, src diffSource, v VCS, taskID, want int64) tea.Cmd {
	return func() tea.Msg {
		msg := diffLoadedMsg{taskID: taskID, want: want}
		links, err := src.TaskBranches(ctx, taskID)
		if err != nil {
			msg.state, msg.notice = diffFailed, err.Error()
			return msg
		}
		msg.links = links
		if len(links) == 0 {
			msg.state = diffNoLink
			return msg
		}
		msg.link = links[len(links)-1] // oldest first, so newest last
		for _, l := range links {
			if l.ID == want {
				msg.link = l
			}
		}
		if !v.BranchExists(ctx, msg.link.RepoPath, msg.link.Branch) {
			msg.state = diffMissing
			return msg
		}
		if msg.committed, err = v.CommittedDiff(ctx, msg.link.RepoPath, msg.link.Branch); err == nil {
			msg.uncommitted, err = v.UncommittedDiff(ctx, msg.link.RepoPath, msg.link.Branch)
		}
		switch {
		case err == nil:
			msg.state = diffReady
		case errors.Is(err, vcs.ErrBranchNotFound):
			msg.state = diffMissing
		case errors.Is(err, vcs.ErrNoGit), errors.Is(err, vcs.ErrNotARepo), errors.Is(err, vcs.ErrNoBut):
			msg.state, msg.notice = diffNoVCS, vcsFlash(msg.link.RepoPath, err).text
		default:
			msg.state, msg.notice = diffFailed, err.Error()
		}
		return msg
	}
}

// diffPane is the Diff tab: the selected task's linked branch as rows with
// a line cursor. It holds no app state, so the run view can embed it too.
type diffPane struct {
	taskID   int64
	links    []task.BranchLink
	sel      int64 // selected link id; 0 = the newest
	state    diffState
	notice   string
	loading  bool // a load is in flight
	rows     []Row
	folded   map[string]bool // per file path; folding itself is #802
	cursor   int
	offset   int
	added    int
	removed  int
	width    int
	height   int
	selLabel string
}

// Reset points the pane at another task, dropping everything of the last.
func (p *diffPane) Reset(taskID int64) {
	*p = diffPane{taskID: taskID, width: p.width, height: p.height}
}

// SetSize sets the area View fills, and re-clamps the scroll to it.
func (p *diffPane) SetSize(w, h int) {
	p.width, p.height = w, h
	p.ensureVisible()
}

// SelectedLink is the link the diff is of, if any.
func (p diffPane) SelectedLink() (task.BranchLink, bool) {
	for _, l := range p.links {
		if l.ID == p.sel {
			return l, true
		}
	}
	if len(p.links) > 0 {
		return p.links[len(p.links)-1], true
	}
	return task.BranchLink{}, false
}

// Summary is the `+N −M` for the tab bar, empty until loaded.
func (p diffPane) Summary() string {
	if p.state != diffReady {
		return ""
	}
	return fmt.Sprintf("+%d −%d", p.added, p.removed)
}

// Apply takes a finished load. A result for another task is dropped
// (false). The cursor is kept by Anchor, never by row index, and keeps its
// place on screen, so a background reload does not move what is being read.
func (p *diffPane) Apply(m diffLoadedMsg) bool {
	if m.taskID != p.taskID || m.want != p.sel {
		return false
	}
	p.loading = false
	same := p.state == diffReady && m.state == diffReady && m.link.ID == p.sel
	anchor, rel := AnchorAt(p.rows, p.cursor), p.cursor-p.offset
	p.links, p.state, p.notice = m.links, m.state, m.notice
	p.sel = m.link.ID
	p.selLabel = m.link.Branch
	p.rows, p.added, p.removed = nil, 0, 0
	if m.state == diffReady {
		p.rows = BuildRows(m.committed, m.uncommitted, p.folded)
		for _, d := range []vcs.Diff{m.committed, m.uncommitted} {
			for _, f := range d.Files {
				p.added += f.Added
				p.removed += f.Removed
			}
		}
	}
	if same {
		p.cursor = FindRow(p.rows, anchor)
		p.offset = p.cursor - rel
	} else {
		p.cursor, p.offset = 0, 0
	}
	p.ensureVisible()
	return true
}

// HandleKey moves the cursor. handled reports whether the key was the
// pane's; reload that the selected link changed and the diff must be
// loaded again.
func (p *diffPane) HandleKey(k string) (handled, reload bool) {
	switch k {
	case "j", "down":
		p.move(1)
	case "k", "up":
		p.move(-1)
	case "ctrl+d", "pgdown":
		p.move(max(p.height/2, 1))
	case "ctrl+u", "pgup":
		p.move(-max(p.height/2, 1))
	case "g", "home":
		p.cursor = 0
		p.ensureVisible()
	case "G", "end":
		p.cursor = len(p.rows) - 1
		p.ensureVisible()
	case "B":
		if len(p.links) > 1 {
			p.cycleLink()
			return true, true
		}
	case "l":
		// Unfolding arrives with #802. Swallowed so it never reaches the
		// list's expand.
	default:
		return false, false
	}
	return true, false
}

// cycleLink selects the next link, wrapping.
func (p *diffPane) cycleLink() {
	for i, l := range p.links {
		if l.ID == p.sel {
			p.sel = p.links[(i+1)%len(p.links)].ID
			return
		}
	}
	p.sel = p.links[0].ID
}

func (p *diffPane) move(n int) {
	if len(p.rows) == 0 {
		return
	}
	p.cursor = min(max(p.cursor+n, 0), len(p.rows)-1)
	p.ensureVisible()
}

// bodyHeight is the rows available under the selector line.
func (p diffPane) bodyHeight() int {
	h := p.height
	if len(p.links) > 1 {
		h--
	}
	return max(h, 1)
}

// ensureVisible scrolls just enough to keep the cursor diffScrolloff rows
// from either edge, and never past the last row.
func (p *diffPane) ensureVisible() {
	h := p.bodyHeight()
	margin := min(diffScrolloff, (h-1)/2)
	if p.cursor < p.offset+margin {
		p.offset = p.cursor - margin
	}
	if p.cursor > p.offset+h-1-margin {
		p.offset = p.cursor - (h - 1 - margin)
	}
	p.offset = min(p.offset, max(len(p.rows)-h, 0))
	p.offset = max(p.offset, 0)
}

// View renders the pane to width × height.
func (p diffPane) View(st Styles) string {
	var lines []string
	if len(p.links) > 1 {
		lines = append(lines, p.selector(st))
	}
	switch p.state {
	case diffReady:
		out := RenderRows(p.rows, p.offset, p.bodyHeight(), p.width, p.cursor,
			DiffRenderOpts{Styles: st, Highlight: true})
		lines = append(lines, out)
	default:
		lines = append(lines, st.Muted.Render(ansi.Truncate(p.message(), p.width, st.Glyphs.Ellipsis)))
	}
	return strings.Join(lines, "\n")
}

func (p diffPane) message() string {
	switch p.state {
	case diffNoLink:
		return "  no branch linked (L to link one)"
	case diffMissing:
		b := p.selLabel
		return fmt.Sprintf("  branch %s is missing (L to re-link)", b)
	case diffNoVCS, diffFailed:
		return "  " + p.notice
	}
	return "  loading…"
}

// selector is the line above several links: the branches, the selected one
// marked, and the key that cycles them.
func (p diffPane) selector(st Styles) string {
	var parts []string
	for _, l := range p.links {
		name := l.Branch
		if l.ID == p.sel {
			parts = append(parts, st.Accent.Render("● "+name))
		} else {
			parts = append(parts, st.Muted.Render("○ "+name))
		}
	}
	line := strings.Join(parts, "  ") + st.Muted.Render("  (B)")
	return ansi.Truncate(line, p.width, st.Glyphs.Ellipsis)
}

// tabBar renders `Task | Diff +N −M` for the top of the detail pane.
func tabBar(st Styles, active detailTab, summary string, width int) string {
	on, off := st.Accent.Bold(true).Underline(true), st.Muted
	task, diff := off.Render("Task"), off.Render("Diff")
	if active == tabTask {
		task = on.Render("Task")
	} else {
		diff = on.Render("Diff")
	}
	if summary != "" {
		diff += " " + st.Muted.Render(summary)
	}
	return ansi.Truncate(" "+task+st.Muted.Render("  |  ")+diff, width, st.Glyphs.Ellipsis)
}

// loaded reports whether the pane has had a result (or one is on its way).
func (p diffPane) loaded() bool { return p.state != diffIdle || p.loading }

// diffLoad starts a load for the task the Diff tab is showing, unless the
// tab is not showing or one is already in flight.
func (a *app) diffLoad() tea.Cmd {
	if a.detailTab != tabDiff || !a.showDetail || a.mode != modeList || a.dp.loading {
		return nil
	}
	n, ok := a.selectedNode()
	if !ok {
		return nil
	}
	if n.t.ID != a.dp.taskID {
		a.dp.Reset(n.t.ID)
	}
	a.dp.loading = true
	return loadDiffCmd(a.ctx, a.store, a.vcs, n.t.ID, a.dp.sel)
}

// detailKeysActive reports whether the detail pane owns the keyboard: it is
// focused, or it has replaced the list outright.
func (a app) detailKeysActive() bool {
	if a.mode != modeList || !a.showDetail {
		return false
	}
	_, _, _, full := a.paneWidths()
	return a.focus == paneDetail || full
}

// handleDetailTabKey is the detail pane's guard, run before the global key
// switch. tab/shift+tab switch tabs on either one; the rest are the Diff
// tab's own, and anything else is not handled.
func (a *app) handleDetailTabKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if key.Matches(msg, a.keys.DetailTab) {
		next := tabDiff
		if a.detailTab == tabDiff {
			next = tabTask
		}
		a.detailTab = next
		return *a, a.syncDetail(true), true
	}
	if a.detailTab != tabDiff {
		return *a, nil, false
	}
	handled, reload := a.dp.HandleKey(msg.String())
	if !handled {
		return *a, nil, false
	}
	if reload {
		a.dp.loading = false // the choice changed: a load in flight is for the old one
		return *a, a.diffLoad(), true
	}
	return *a, nil, true
}

// detailPaneView is the tab bar over the active tab.
func (a app) detailPaneView() string {
	body := a.detail.View()
	if a.detailTab == tabDiff {
		body = a.dp.View(a.styles)
	}
	return tabBar(a.styles, a.detailTab, a.dp.Summary(), a.detail.Width()) + "\n" + body
}
