package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/store"
	"github.com/jwstover/tend/internal/task"
	"github.com/jwstover/tend/internal/vcs"
)

// diffVCS serves canned diffs per branch.
type diffVCS struct {
	fakeVCS
	missing   map[string]bool
	committed map[string]vcs.Diff
	err       error
}

func (f *diffVCS) BranchExists(_ context.Context, _, b string) bool { return !f.missing[b] }
func (f *diffVCS) CommittedDiff(_ context.Context, _, b string) (vcs.Diff, error) {
	return f.committed[b], f.err
}

// oneFile is a diff of one file whose single hunk is n context lines with
// one added line after `at` of them.
func oneFile(path string, n, at int) vcs.Diff {
	h := vcs.Hunk{OldStart: 1, NewStart: 1}
	old, nw := 0, 0
	for i := 0; i < n; i++ {
		old, nw = old+1, nw+1
		h.Lines = append(h.Lines, vcs.Line{Kind: vcs.LineContext, Text: fmt.Sprintf("line %d", i), OldNo: old, NewNo: nw})
		if i+1 == at {
			nw++
			h.Lines = append(h.Lines, vcs.Line{Kind: vcs.LineAdd, Text: "ADDED", NewNo: nw})
		}
	}
	h.OldLines, h.NewLines = old, nw
	f := vcs.File{NewPath: path, Status: vcs.StatusModified, Hunks: []vcs.Hunk{h}, Added: 1}
	return vcs.Diff{Files: []vcs.File{f}}
}

func readyMsg(taskID int64, d vcs.Diff) diffLoadedMsg {
	return diffLoadedMsg{taskID: taskID, link: task.BranchLink{ID: 1, Branch: "b"},
		links: []task.BranchLink{{ID: 1, Branch: "b"}}, state: diffReady, committed: d}
}

func newPane(h int) *diffPane {
	p := &diffPane{taskID: 7}
	p.SetSize(60, h)
	return p
}

func TestDiffPaneCursorKeepsScrolloff(t *testing.T) {
	p := newPane(12)
	p.sel = 0
	p.Apply(diffLoadedMsg{taskID: 7, link: task.BranchLink{ID: 0}, state: diffReady, committed: oneFile("a.go", 50, 10)})
	for i := 0; i < 20; i++ {
		p.HandleKey("j")
		if p.cursor-p.offset > p.bodyHeight()-1-diffScrolloff {
			t.Fatalf("after %d j, cursor %d is inside the bottom margin of offset %d", i+1, p.cursor, p.offset)
		}
	}
	for i := 0; i < 40; i++ {
		p.HandleKey("k")
	}
	if p.cursor != 0 || p.offset != 0 {
		t.Errorf("k past the top: cursor %d offset %d, want 0 0", p.cursor, p.offset)
	}
	p.HandleKey("ctrl+d")
	if p.cursor != 6 {
		t.Errorf("ctrl+d moved to %d, want half a page (6)", p.cursor)
	}
	p.HandleKey("G")
	if p.cursor != len(p.rows)-1 {
		t.Errorf("G: cursor %d, want last row %d", p.cursor, len(p.rows)-1)
	}
	p.HandleKey("ctrl+u")
	p.HandleKey("g")
	if p.cursor != 0 {
		t.Errorf("g: cursor %d, want 0", p.cursor)
	}
}

func TestDiffPaneReloadKeepsCursorOnTheSameCodeLine(t *testing.T) {
	p := newPane(12)
	p.Apply(diffLoadedMsg{taskID: 7, state: diffReady, committed: oneFile("a.go", 40, 2)})
	// Put the cursor on "line 20" (context, old and new numbers differ by 1).
	for p.rows[p.cursor].Text != "line 20" {
		p.HandleKey("j")
	}
	rel := p.cursor - p.offset
	// A reload in which a line was inserted above: every number shifts.
	d := oneFile("a.go", 40, 2)
	h := d.Files[0].Hunks[0]
	h.Lines = append([]vcs.Line{{Kind: vcs.LineAdd, Text: "NEW TOP", NewNo: 1}}, h.Lines...)
	for i := 1; i < len(h.Lines); i++ {
		h.Lines[i].NewNo++
	}
	h.NewLines++
	d.Files[0].Hunks[0] = h
	d.Files[0].Added++
	p.Apply(diffLoadedMsg{taskID: 7, state: diffReady, committed: d})
	if got := p.rows[p.cursor].Text; got != "line 20" {
		t.Fatalf("cursor is on %q after the reload, want line 20", got)
	}
	if p.cursor-p.offset != rel {
		t.Errorf("the line moved on screen: was row %d of the view, now %d", rel, p.cursor-p.offset)
	}
}

func TestDiffPaneDropsAResultForAnotherTask(t *testing.T) {
	p := newPane(10)
	if p.Apply(readyMsg(8, oneFile("a.go", 3, 1))) {
		t.Error("a result for task 8 was applied to the pane on task 7")
	}
	if len(p.rows) != 0 {
		t.Error("rows were built from a dropped result")
	}
}

func TestDiffPaneStates(t *testing.T) {
	st := DefaultStyles()
	for _, tc := range []struct {
		name string
		msg  diffLoadedMsg
		want string
	}{
		{"no link", diffLoadedMsg{taskID: 7, state: diffNoLink}, "no branch linked (L to link one)"},
		{"missing", diffLoadedMsg{taskID: 7, state: diffMissing, link: task.BranchLink{ID: 1, Branch: "gone"}}, "branch gone is missing (L to re-link)"},
		{"not a repo", diffLoadedMsg{taskID: 7, state: diffNoVCS, notice: "/x is not a git repository"}, "/x is not a git repository"},
	} {
		p := newPane(10)
		p.Apply(tc.msg)
		if got := ansi.Strip(p.View(st)); !strings.Contains(got, tc.want) {
			t.Errorf("%s: view %q lacks %q", tc.name, got, tc.want)
		}
	}
}

func TestDiffPaneBCyclesLinks(t *testing.T) {
	p := newPane(10)
	links := []task.BranchLink{{ID: 1, Branch: "one"}, {ID: 2, Branch: "two"}}
	p.Apply(diffLoadedMsg{taskID: 7, links: links, link: links[1], state: diffNoLink})
	if l, _ := p.SelectedLink(); l.ID != 2 {
		t.Fatalf("selected %d, want the newest (2)", l.ID)
	}
	if handled, reload := p.HandleKey("B"); !handled || !reload {
		t.Fatal("B should be handled and ask for a reload")
	}
	if l, _ := p.SelectedLink(); l.ID != 1 {
		t.Errorf("B selected %d, want 1 (wrapped)", l.ID)
	}
	// The selector line names both branches.
	if got := ansi.Strip(p.View(DefaultStyles())); !strings.Contains(got, "one") || !strings.Contains(got, "two") {
		t.Errorf("selector missing a branch:\n%s", got)
	}
	// A late result for the choice before B is dropped.
	if p.Apply(diffLoadedMsg{taskID: 7, want: 2, links: links, link: links[1], state: diffNoLink}) {
		t.Error("a result for the previous choice was applied")
	}
}

func TestLoadDiffCmdOutcomes(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tk, _ := s.AddTask(ctx, "x")
	run := func(f *diffVCS) diffLoadedMsg {
		return loadDiffCmd(ctx, s, f, tk.ID, 0)().(diffLoadedMsg)
	}
	f := &diffVCS{committed: map[string]vcs.Diff{"b": oneFile("a.go", 3, 1)}}
	if m := run(f); m.state != diffNoLink {
		t.Errorf("no links: state %v", m.state)
	}
	if _, err := s.LinkBranch(ctx, tk.ID, "/r", "b"); err != nil {
		t.Fatal(err)
	}
	if m := run(f); m.state != diffReady || m.committed.Empty() {
		t.Errorf("linked: state %v, diff empty %v", m.state, m.committed.Empty())
	}
	f.missing = map[string]bool{"b": true}
	if m := run(f); m.state != diffMissing {
		t.Errorf("missing branch: state %v", m.state)
	}
	f.missing, f.err = nil, vcs.ErrNotARepo
	if m := run(f); m.state != diffNoVCS || m.notice == "" {
		t.Errorf("not a repo: state %v notice %q", m.state, m.notice)
	}
	f.err = errors.New("boom")
	if m := run(f); m.state != diffFailed {
		t.Errorf("other failure: state %v", m.state)
	}
}

// diffTabApp is an app with the detail pane focused on the first of two
// tasks (the first has a sub-task, so a stray expand would show).
func diffTabApp(t *testing.T) (tea.Model, [2]task.Task) {
	t.Helper()
	ctx := context.Background()
	m, s := newTestApp(t)
	first, _ := s.AddTask(ctx, "first")
	second, _ := s.AddTask(ctx, "second")
	if _, err := s.AddChild(ctx, first.ID, "child"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkBranch(ctx, first.ID, "/r", "b"); err != nil {
		t.Fatal(err)
	}
	a := m.(app)
	a.vcs = &diffVCS{committed: map[string]vcs.Diff{"b": oneFile("a.go", 30, 3)}}
	a.showDetail = true
	m = drive(t, a, refreshMsg{})
	a = m.(app)
	a.focus = paneDetail
	a.resize()
	return a, [2]task.Task{first, second}
}

func selectedID(m tea.Model) int64 {
	n, _ := m.(app).selectedNode()
	return n.t.ID
}

func TestTabSwitchesToTheDiffTabAndBack(t *testing.T) {
	m, _ := diffTabApp(t)
	id := selectedID(m)
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	a := m.(app)
	if a.detailTab != tabDiff {
		t.Fatalf("tab left the pane on tab %v", a.detailTab)
	}
	a.dp.Apply(diffLoadedMsg{}) // no-op for the wrong task; the real load already ran
	if got := ansi.Strip(a.View().Content); !strings.Contains(got, "Task") || !strings.Contains(got, "Diff +1 −0") {
		t.Errorf("tab bar lacks the summary:\n%s", got)
	}
	if a.expanded[id] {
		t.Error("tab reached the list's expand toggle")
	}
	m = drive(t, a, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.(app).detailTab != tabTask {
		t.Error("shift+tab did not go back to the Task tab")
	}
}

func TestDiffTabKeysDoNotReachTheList(t *testing.T) {
	m, _ := diffTabApp(t)
	id := selectedID(m)
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	for _, k := range []tea.KeyPressMsg{keyPress('l'), keyPress('j'), keyPress('j'), keyPress('k')} {
		m = drive(t, m, k)
	}
	a := m.(app)
	if a.expanded[id] {
		t.Error("l expanded the task in the list")
	}
	if selectedID(m) != id {
		t.Error("j/k moved the list selection")
	}
	if a.dp.cursor != 1 {
		t.Errorf("line cursor at %d, want 1", a.dp.cursor)
	}
	if a.focus != paneDetail {
		t.Error("focus left the detail pane")
	}
	// h still gives focus back to the list.
	m = drive(t, m, keyPress('h'))
	if m.(app).focus == paneDetail {
		t.Error("h did not return focus to the list")
	}
}

func TestDiffTabSurvivesAChangeOfTaskAndDropsStaleResults(t *testing.T) {
	m, tasks := diffTabApp(t)
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	a := m.(app)
	a.focus = paneTasks
	m = drive(t, a, keyPress('j')) // onto the next row in the list
	for selectedID(m) == tasks[0].ID {
		m = drive(t, m, keyPress('j'))
	}
	a = m.(app)
	if a.detailTab != tabDiff {
		t.Fatal("the chosen tab did not survive moving to another task")
	}
	if a.dp.taskID != selectedID(m) {
		t.Errorf("pane is on task %d, selection is %d", a.dp.taskID, selectedID(m))
	}
	// A late result for the first task changes nothing.
	m = drive(t, a, readyMsg(tasks[0].ID, oneFile("zzz.go", 5, 1)))
	if len(m.(app).dp.rows) != 0 && m.(app).dp.rows[0].Path == "zzz.go" {
		t.Error("a stale result for another task was applied")
	}
}

func TestIdlePollTickRefreshesTheDiffTab(t *testing.T) {
	m, _ := diffTabApp(t)
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	a := m.(app)
	a.vcs = &diffVCS{committed: map[string]vcs.Diff{"b": oneFile("a.go", 30, 3)}}
	a.dp.loading = false
	m, cmd := a.Update(sessionsPolledMsg{})
	if cmd == nil || !m.(app).dp.loading {
		t.Error("an idle poll tick did not start a diff reload")
	}
	// ... but not while one is in flight.
	if _, cmd := m.(app).Update(sessionsPolledMsg{}); cmd != nil {
		t.Error("a second reload started while one was in flight")
	}
}
