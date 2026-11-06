package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/vcs"
)

// The branch link picker is `L` (and the palette's "Link branch…"): choose
// which branches carry the selected task's work. Like the dependency picker
// it is a multi-select that stays open across toggles. Links are always
// explicit: a branch named for the task sorts first as a hint, and is never
// linked on its own.

// modalLinkRepo collects another repo path for the picker (ctrl+o). It is
// declared here, past the modalKind values in modal.go, so this file is the
// only place that knows about it.
const modalLinkRepo modalKind = 100

// linkRow is one branch under a repo heading.
type linkRow struct {
	repo    string
	name    string
	commits int
	dirty   bool // GitButler: uncommitted changes assigned to the branch
	linked  bool
	linkID  int64 // the task_branches row, when linked
	missing bool  // linked, but the repo no longer has the branch
}

// linkPicker is the picker's state, one field on app.
type linkPicker struct {
	open      bool
	taskID    int64
	task      task.Task
	label     string
	query     string
	sel       int
	rows      []linkRow
	extra     []string // repos added with ctrl+o during this visit
	relinkID  int64    // non-zero: the next branch picked replaces this missing link
	relinkRow linkRow
}

// linkPickerLoadedMsg carries the rows for a task, ready to show.
type linkPickerLoadedMsg struct {
	task   task.Task
	rows   []linkRow
	reopen bool // reload of an open picker: keep query and cursor
}

// linkToggledMsg reports a store-confirmed link or unlink.
type linkToggledMsg struct {
	taskID int64
	repo   string
	branch string
	link   task.BranchLink // set when linked
	added  bool
}

// linkRelinkedMsg reports a confirmed re-link; the picker reloads.
type linkRelinkedMsg struct {
	task   task.Task
	branch string
}

// vcsFlash turns a vcs failure into the quiet status line text.
func vcsFlash(repo string, err error) flash {
	switch {
	case errors.Is(err, vcs.ErrNoGit):
		return flash{text: "git is not installed"}
	case errors.Is(err, vcs.ErrNotARepo):
		return flash{text: fmt.Sprintf("%s is not a git repository", repo)}
	case errors.Is(err, vcs.ErrNoBut):
		return flash{text: "but (GitButler) is not installed"}
	}
	return flash{text: fmt.Sprintf("branches in %s: %v", repo, err)}
}

// loadLinkPicker gathers the rows off the loop: the task's links, the repos
// worth listing (their repos, the default cwd, any added by hand), and each
// repo's branches.
func (a app) loadLinkPicker(t task.Task, extra []string, reopen bool) tea.Cmd {
	return func() tea.Msg {
		links, err := a.store.TaskBranches(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		sessions, err := a.store.ListSessionsForTask(a.ctx, t.ID)
		if err != nil {
			return errMsg{err}
		}
		var repos []string
		addRepo := func(r string) {
			if r = strings.TrimSpace(r); r != "" {
				r = filepath.Clean(r)
				if !slices.Contains(repos, r) {
					repos = append(repos, r)
				}
			}
		}
		for _, l := range links {
			addRepo(l.RepoPath)
		}
		addRepo(a.defaultCwd(sessions, t.ProjectID))
		for _, r := range extra {
			addRepo(r)
		}
		if len(repos) == 0 {
			return statusMsg(flash{text: "no repo to list branches from — set a project cwd, or press ctrl+o"})
		}

		var rows []linkRow
		var firstErr flash
		failed := 0
		for _, repo := range repos {
			rr, ferr := a.linkRowsFor(t, repo, links)
			if ferr != nil {
				failed++
				if failed == 1 {
					firstErr = vcsFlash(repo, ferr)
				}
			}
			rows = append(rows, rr...)
		}
		if len(rows) == 0 && failed > 0 {
			return statusMsg(firstErr)
		}
		return linkPickerLoadedMsg{task: t, rows: rows, reopen: reopen}
	}
}

// linkRowsFor lists one repo's rows: the task's links first (missing ones
// marked), then a branch named for the task, then the rest as vcs gave
// them. A listing error still yields the repo's link rows, flagged missing.
func (a app) linkRowsFor(t task.Task, repo string, links []task.BranchLink) ([]linkRow, error) {
	branches, err := a.vcs.Branches(a.ctx, repo)
	byName := make(map[string]vcs.Branch, len(branches))
	for _, b := range branches {
		byName[b.Name] = b
	}
	var linked, hinted, rest []linkRow
	seen := map[string]bool{}
	for _, l := range links {
		if filepath.Clean(l.RepoPath) != repo {
			continue
		}
		seen[l.Branch] = true
		r := linkRow{repo: repo, name: l.Branch, linked: true, linkID: l.ID}
		if b, ok := byName[l.Branch]; ok {
			r.commits, r.dirty = b.Commits, b.Uncommitted
		} else if err != nil || !a.vcs.BranchExists(a.ctx, repo, l.Branch) {
			r.missing = true
		}
		linked = append(linked, r)
	}
	hints := []string{fmt.Sprintf("task-%d", t.ID), fmt.Sprintf("sub-%d", t.ID)}
	for _, b := range branches {
		if seen[b.Name] {
			continue
		}
		r := linkRow{repo: repo, name: b.Name, commits: b.Commits, dirty: b.Uncommitted}
		if hasTaskHint(b.Name, hints) {
			hinted = append(hinted, r)
		} else {
			rest = append(rest, r)
		}
	}
	return slices.Concat(linked, hinted, rest), err
}

// hasTaskHint reports whether name contains one of the task's markers as a
// whole number: task-79 must not match task-790.
func hasTaskHint(name string, hints []string) bool {
	for _, h := range hints {
		for i := 0; ; {
			j := strings.Index(name[i:], h)
			if j < 0 {
				break
			}
			end := i + j + len(h)
			if end >= len(name) || name[end] < '0' || name[end] > '9' {
				return true
			}
			i = end
		}
	}
	return false
}

func (a *app) openLinkPicker(msg linkPickerLoadedMsg) {
	if msg.reopen && a.lp.open && a.lp.taskID == msg.task.ID {
		a.lp.rows = msg.rows
		a.lp.sel = min(a.lp.sel, max(len(a.linkPickerMatches())-1, 0))
		return
	}
	a.lp = linkPicker{open: true, taskID: msg.task.ID, task: msg.task, label: msg.task.Title, rows: msg.rows}
}

func (a *app) closeLinkPicker() { a.lp = linkPicker{} }

// linkPickerMatches narrows the rows with the fuzzy filter, substring hits
// first, matching the workflow picker.
func (a app) linkPickerMatches() []linkRow {
	return fuzzyFilter(a.lp.query, a.lp.rows, func(r linkRow) string { return r.name })
}

func (a app) openLinkPickerFor(t task.Task) tea.Cmd { return a.loadLinkPicker(t, nil, false) }

// handleLinkPickerKey owns the keyboard while the picker is open.
func (a app) handleLinkPickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := a.linkPickerMatches()
	act := func(r linkRow) (tea.Model, tea.Cmd) {
		if a.lp.relinkID != 0 {
			return a.relinkTo(r)
		}
		if r.missing {
			a.lp.relinkID, a.lp.relinkRow = r.linkID, r
			a.status = flash{text: fmt.Sprintf("pick the branch to re-link %s to (esc cancels)", r.name)}
			return a, nil
		}
		return a, a.toggleLink(a.lp.taskID, r)
	}

	switch msg.String() {
	case "esc":
		if a.lp.relinkID != 0 {
			a.lp.relinkID = 0
			return a, nil
		}
		a.closeLinkPicker()
		return a, nil
	case "enter":
		if sel := a.lp.sel; sel >= 0 && sel < len(rows) {
			return act(rows[sel])
		}
		return a, nil
	case "ctrl+x": // unlink a missing branch outright
		if sel := a.lp.sel; sel >= 0 && sel < len(rows) && rows[sel].missing {
			return a, a.toggleLink(a.lp.taskID, rows[sel])
		}
		return a, nil
	case "ctrl+o":
		return a, a.modal.Open(modalLinkRepo, false,
			fmt.Sprintf("repo path to list branches from (#%d)", a.lp.taskID), a.lp.taskID, "")
	case "up", "ctrl+p":
		if a.lp.sel > 0 {
			a.lp.sel--
		}
		return a, nil
	case "down", "ctrl+n":
		if a.lp.sel < len(rows)-1 {
			a.lp.sel++
		}
		return a, nil
	case "backspace":
		if r := []rune(a.lp.query); len(r) > 0 {
			a.lp.query = string(r[:len(r)-1])
		}
		a.lp.sel = 0
		return a, nil
	}
	if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		if idx := int(msg.Text[0] - '1'); idx < len(rows) {
			return act(rows[idx])
		}
		return a, nil
	}
	if msg.Text != "" {
		a.lp.query += msg.Text
		a.lp.sel = 0
	}
	return a, nil
}

// toggleLink links or unlinks one row in the store.
func (a app) toggleLink(taskID int64, r linkRow) tea.Cmd {
	return func() tea.Msg {
		if r.linked {
			if err := a.store.UnlinkBranch(a.ctx, taskID, r.repo, r.name); err != nil {
				return errMsg{err}
			}
			return linkToggledMsg{taskID: taskID, repo: r.repo, branch: r.name}
		}
		l, err := a.store.LinkBranch(a.ctx, taskID, r.repo, r.name)
		if err != nil {
			return errMsg{err}
		}
		return linkToggledMsg{taskID: taskID, repo: r.repo, branch: r.name, link: l, added: true}
	}
}

// relinkTo points the pending missing link at the chosen branch.
func (a app) relinkTo(r linkRow) (tea.Model, tea.Cmd) {
	from := a.lp.relinkRow
	if r.missing {
		a.status = flash{text: "that branch is missing too — pick one that exists"}
		return a, nil
	}
	if r.repo != from.repo {
		a.status = flash{text: fmt.Sprintf("pick a branch in %s", from.repo)}
		return a, nil
	}
	id, t := a.lp.relinkID, a.lp.task
	a.lp.relinkID = 0
	return a, func() tea.Msg {
		if err := a.store.RelinkBranch(a.ctx, id, r.name); err != nil {
			return errMsg{err}
		}
		return linkRelinkedMsg{task: t, branch: r.name}
	}
}

// applyLinkToggle flips the row in place, keeping the opening order so the
// cursor does not move mid-visit.
func (a *app) applyLinkToggle(msg linkToggledMsg) {
	if a.lp.open && a.lp.taskID == msg.taskID {
		for i := range a.lp.rows {
			r := &a.lp.rows[i]
			if r.repo == msg.repo && r.name == msg.branch {
				r.linked, r.linkID = msg.added, msg.link.ID
				if !msg.added && r.missing {
					a.lp.rows = slices.Delete(a.lp.rows, i, i+1)
				}
				break
			}
		}
		a.lp.sel = min(a.lp.sel, max(len(a.linkPickerMatches())-1, 0))
	}
	text := fmt.Sprintf("#%d unlinked from %s", msg.taskID, msg.branch)
	if msg.added {
		text = fmt.Sprintf("#%d linked to %s", msg.taskID, msg.branch)
	}
	a.status = flash{kind: flashEdit, text: text}
}

// submitLinkRepo adds a typed repo path to the open picker and reloads it.
func (a app) submitLinkRepo(taskID int64, value string) (tea.Model, tea.Cmd) {
	if value == "" || !a.lp.open || a.lp.taskID != taskID {
		return a, nil
	}
	repo := filepath.Clean(value)
	if !slices.Contains(a.lp.extra, repo) {
		a.lp.extra = append(a.lp.extra, repo)
	}
	return a, a.loadLinkPicker(a.lp.task, a.lp.extra, true)
}

// linkPickerView renders the chooser box.
func (a app) linkPickerView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 20)
	cb := s.CardBorder
	hbar := strings.Repeat(g.RuleH, w-4)
	row := func(content string) string {
		gap := max(w-5-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	title := truncTail(a.lp.label, max(w-40, 10), g.Ellipsis)
	head := s.Accent.Bold(true).Render(g.CaretClosed+" ") +
		s.Title.Render("branches of ") +
		s.Dimmed.Render(fmt.Sprintf("#%d %s", a.lp.taskID, title))
	hint := s.FooterKey.Render("ctrl+o") + s.Muted.Render(" repo  ") +
		s.FooterKey.Render("esc") + s.Muted.Render(" closes")
	head += strings.Repeat(" ", max(w-5-lipgloss.Width(head)-lipgloss.Width(hint), 1)) + hint
	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR), row(head),
		"  " + cb.Render(g.TeeRight+hbar+g.TeeLeft),
		row(s.Accent.Bold(true).Render("❯ ") + s.Title.Render(a.lp.query) + s.Accent.Render("▏"))}
	if a.lp.relinkID != 0 {
		lines = append(lines, row(s.Accent.Render("re-linking "+a.lp.relinkRow.name+": pick the branch it moved to")))
	}

	rows := a.linkPickerMatches()
	switch {
	case len(a.lp.rows) == 0:
		lines = append(lines, row("  "+s.Muted.Render("no branches")))
	case len(rows) == 0:
		lines = append(lines, row("  "+s.Muted.Render("no matching branches")))
	}
	sel := min(a.lp.sel, len(rows)-1)
	lastRepo := ""
	for i, r := range rows {
		if r.repo != lastRepo {
			lastRepo = r.repo
			lines = append(lines, row(s.Muted.Render(truncTail(r.repo, max(w-8, 10), g.Ellipsis))))
		}
		num := fmt.Sprintf("%d ", i+1)
		box := s.CheckOpen.Render(g.BoxUnchecked)
		if r.linked {
			box = s.CheckDone.Render(g.BoxChecked)
		}
		var tail string
		switch {
		case r.missing:
			tail = "missing"
		default:
			tail = fmt.Sprintf("%d commits", r.commits)
			if r.commits == 1 {
				tail = "1 commit"
			}
			if r.dirty {
				tail += " " + g.Ellipsis + " uncommitted"
			}
		}
		fixed := 2 + len(num) + lipgloss.Width(box) + 1 + 2 + runeWidth(tail)
		label := truncTail(r.name, max(w-5-fixed, 10), g.Ellipsis)
		titleStyle, tailStyle := s.Dimmed, s.Faint
		if i == sel {
			titleStyle, tailStyle = s.Title, s.Muted
		}
		if r.missing {
			tailStyle = s.Accent
		}
		content := "  " + s.Muted.Render(num)
		if i == sel {
			content = s.SelBar.Render(g.SelBar+" ") + s.Accent.Render(num)
		}
		content += box + " " + titleStyle.Render(label) + "  " + tailStyle.Render(tail)
		lines = append(lines, row(content))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}
