package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/task"
)

// dependencyRowIDs is the picker's visible rows, top to bottom, by task id.
func dependencyRowIDs(m tea.Model) []int64 {
	rows := m.(app).dependencyPickerMatches()
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.id)
	}
	return out
}

// blockerIDs is what the store says a task waits on, in edge order.
func blockerIDs(t *testing.T, s interface {
	Blockers(context.Context, int64) ([]task.Task, error)
}, id int64) []int64 {
	t.Helper()
	got, err := s.Blockers(context.Background(), id)
	if err != nil {
		t.Fatalf("Blockers(%d): %v", id, err)
	}
	out := make([]int64, 0, len(got))
	for _, b := range got {
		out = append(out, b.ID)
	}
	return out
}

func int64sEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// b lists every open task across projects except the one being edited
// and anything done, with the current blockers checked and listed first,
// the task's own project ahead of the others.
func TestDependencyPickerListsOpenTasksAcrossProjects(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	other, err := s.CreateProject(ctx, "elsewhere")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	subject, err := s.AddTask(ctx, "subject")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	local, err := s.AddTask(ctx, "local sibling")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	finished, err := s.AddTask(ctx, "finished")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.SetState(ctx, finished.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	foreign, err := s.AddTaskIn(ctx, other.ID, "foreign blocker")
	if err != nil {
		t.Fatalf("AddTaskIn: %v", err)
	}
	// The foreign task is already a blocker: it should lead even though a
	// same-project row would otherwise sort ahead of it.
	if err := s.AddDependency(ctx, subject.ID, foreign.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = selectTask(t, m, subject.ID)

	m = drive(t, m, keyPress('b'))
	a := m.(app)
	if !a.depPickerOpen {
		t.Fatal("b should open the dependency picker")
	}
	if a.depPickerTaskID != subject.ID {
		t.Errorf("picker task = %d, want %d", a.depPickerTaskID, subject.ID)
	}
	got := dependencyRowIDs(m)
	want := []int64{foreign.ID, local.ID}
	if !int64sEqual(got, want) {
		t.Fatalf("rows = %v, want %v (checked first, then own project; no self, no done)", got, want)
	}
	if !a.depPickerChecked[foreign.ID] || a.depPickerChecked[local.ID] {
		t.Errorf("checked = %v, want only the existing blocker #%d", a.depPickerChecked, foreign.ID)
	}
	rows := a.dependencyPickerMatches()
	if rows[0].project != "elsewhere" {
		t.Errorf("foreign row project label = %q, want elsewhere", rows[0].project)
	}
	if rows[1].project != "" {
		t.Errorf("same-project row should carry no project label, got %q", rows[1].project)
	}
	view := ansi.Strip(a.dependencyPickerView())
	for _, want := range []string{"dependencies of", "foreign blocker", "elsewhere", "local sibling", "esc"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker view lacks %q:\n%s", want, view)
		}
	}
}

// Picking a row adds the edge and the picker stays open with the row
// checked; picking it again removes the edge. The flash names both tasks.
func TestDependencyPickerTogglesAndStaysOpen(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	subject, err := s.AddTask(ctx, "subject")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	blocker, err := s.AddTask(ctx, "the blocker")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = selectTask(t, m, subject.ID)
	m = drive(t, m, keyPress('b'))
	if got := dependencyRowIDs(m); !int64sEqual(got, []int64{blocker.ID}) {
		t.Fatalf("rows = %v, want just #%d", got, blocker.ID)
	}

	m = drive(t, m, keyPress('1'))
	a := m.(app)
	if !a.depPickerOpen {
		t.Fatal("toggling a row must leave the picker open")
	}
	if !a.depPickerChecked[blocker.ID] {
		t.Error("the picked row should be checked once the store confirms")
	}
	if got := blockerIDs(t, s, subject.ID); !int64sEqual(got, []int64{blocker.ID}) {
		t.Errorf("store blockers = %v, want [%d]", got, blocker.ID)
	}
	if !strings.Contains(a.status.text, "now waits on") || !strings.Contains(a.status.text, "the blocker") {
		t.Errorf("flash = %q, want it to say the task now waits on the blocker", a.status.text)
	}
	if a.blockers[subject.ID] != (task.BlockerCount{Open: 1, Total: 1}) {
		t.Errorf("list blocker counts after the toggle = %+v, want one open of one", a.blockers[subject.ID])
	}

	// Enter on the same (still selected) row removes it again.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	a = m.(app)
	if !a.depPickerOpen {
		t.Fatal("un-toggling must leave the picker open too")
	}
	if a.depPickerChecked[blocker.ID] {
		t.Error("the row should be unchecked once the edge is removed")
	}
	if got := blockerIDs(t, s, subject.ID); len(got) != 0 {
		t.Errorf("store blockers = %v, want none", got)
	}
	if !strings.Contains(a.status.text, "no longer waits on") {
		t.Errorf("flash = %q, want it to say the task no longer waits", a.status.text)
	}
	if _, ok := a.blockers[subject.ID]; ok {
		t.Errorf("list blocker counts should drop the task once it waits on nothing: %+v", a.blockers)
	}
}

// A store refusal (here a cycle) reaches the status line as an error and
// leaves the row unchecked and the picker open.
func TestDependencyPickerCycleRefusalFlashesAndLeavesRowUnchecked(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	alpha, err := s.AddTask(ctx, "alpha")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	beta, err := s.AddTask(ctx, "beta")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.AddDependency(ctx, alpha.ID, beta.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	m = selectTask(t, m, beta.ID)
	m = drive(t, m, keyPress('b'))
	if got := dependencyRowIDs(m); !int64sEqual(got, []int64{alpha.ID}) {
		t.Fatalf("rows = %v, want just alpha #%d", got, alpha.ID)
	}

	m = drive(t, m, keyPress('1'))
	a := m.(app)
	if !a.status.isErr {
		t.Fatalf("flash = %+v, want an error for the cycle", a.status)
	}
	if !strings.Contains(a.status.text, "cycle") {
		t.Errorf("flash = %q, want it to mention the cycle", a.status.text)
	}
	if a.depPickerChecked[alpha.ID] {
		t.Error("a refused edge must not check the row")
	}
	if !a.depPickerOpen {
		t.Error("a refused edge should leave the picker open to try another row")
	}
	if got := blockerIDs(t, s, beta.ID); len(got) != 0 {
		t.Errorf("store blockers of beta = %v, want none", got)
	}
}

// Typing narrows the rows over title and project name, case-insensitively,
// resets the cursor, and the digit shortcuts follow the visible order.
func TestDependencyPickerTypeToFilter(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	subject, err := s.AddTask(ctx, "subject")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	var cherry task.Task
	for _, title := range []string{"Apple pie", "banana", "cherry"} {
		tk, err := s.AddTask(ctx, title)
		if err != nil {
			t.Fatalf("AddTask(%q): %v", title, err)
		}
		if title == "cherry" {
			cherry = tk
		}
	}
	m = drive(t, m, refreshMsg{})
	m = selectTask(t, m, subject.ID)
	m = drive(t, m, keyPress('b'))
	if got := dependencyRowIDs(m); len(got) != 3 {
		t.Fatalf("rows = %v, want the three other tasks", got)
	}
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.(app).depPickerSel != 1 {
		t.Fatalf("sel after down = %d, want 1", m.(app).depPickerSel)
	}

	for _, r := range "CHER" {
		m = drive(t, m, keyPress(r))
	}
	a := m.(app)
	if got := dependencyRowIDs(m); !int64sEqual(got, []int64{cherry.ID}) {
		t.Fatalf("rows after typing CHER = %v, want just cherry #%d", got, cherry.ID)
	}
	if a.depPickerSel != 0 {
		t.Errorf("sel after typing = %d, want reset to 0", a.depPickerSel)
	}

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := m.(app).depPickerQuery; got != "CHE" {
		t.Fatalf("query after backspace = %q, want CHE", got)
	}

	// The digit toggles by visible index, so 1 is the filtered hit, and
	// the filter survives the toggle.
	m = drive(t, m, keyPress('1'))
	a = m.(app)
	if !a.depPickerOpen || a.depPickerQuery != "CHE" {
		t.Errorf("after the toggle: open=%v query=%q, want the picker still up with its filter", a.depPickerOpen, a.depPickerQuery)
	}
	if got := blockerIDs(t, s, subject.ID); !int64sEqual(got, []int64{cherry.ID}) {
		t.Errorf("store blockers = %v, want [%d]", got, cherry.ID)
	}
}

// The detail pane lists what a task waits on under BLOCKED BY, with the
// open count and done blockers checked off, and what waits on it under
// BLOCKS.
func TestRenderDetailShowsBlockedByAndBlocks(t *testing.T) {
	styles := DefaultStyles()
	g := styles.Glyphs
	subject := task.Task{ID: 12, Title: "subject", State: task.StateTodo}
	open := task.Task{ID: 7, Title: "open blocker", State: task.StateReview}
	done := task.Task{ID: 8, Title: "done blocker", State: task.StateDone}
	dependent := task.Task{ID: 20, Title: "waits on subject", State: task.StateTodo}

	out := ansi.Strip(renderDetail(subject, nil, []task.Task{open, done}, []task.Task{dependent},
		nil, nil, nil, nil, nil, styles, 80))
	for _, want := range []string{
		"BLOCKED BY", "1 open",
		g.State[task.StateReview] + " open blocker  #7",
		g.BoxChecked + " done blocker  #8",
		"press b to edit dependencies",
		"BLOCKS", g.State[task.StateTodo] + " waits on subject  #20",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("detail lacks %q:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "BLOCKED BY"), strings.Index(out, "BLOCKS  1"); i < 0 || j < 0 || i > j {
		t.Errorf("BLOCKED BY (%d) should come before BLOCKS (%d)", i, j)
	}

	// Every blocker done: the header says so in place of a count.
	out = ansi.Strip(renderDetail(subject, nil, []task.Task{done}, nil,
		nil, nil, nil, nil, nil, styles, 80))
	if !strings.Contains(out, "BLOCKED BY  all done") {
		t.Errorf("detail with only done blockers should read 'all done':\n%s", out)
	}

	// No edges either way: neither section appears.
	out = ansi.Strip(renderDetail(subject, nil, nil, nil, nil, nil, nil, nil, nil, styles, 80))
	if strings.Contains(out, "BLOCKED BY") || strings.Contains(out, "BLOCKS") {
		t.Errorf("detail without dependencies should have no dependency sections:\n%s", out)
	}
}

// The list row's dependency cell reads `⊘N` while N blockers are open and
// flips to the glyph plus a check once every blocker is done, driven by
// the counts loadTasks fetches. Blank when the task waits on nothing.
func TestListRowDependencyCell(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	subject, err := s.AddTask(ctx, "subject")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	blocker, err := s.AddTask(ctx, "blocker")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if err := s.AddDependency(ctx, subject.ID, blocker.ID); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	a := m.(app)
	g := a.styles.Glyphs
	blocked, done := g.State[task.StateBlocked], g.State[task.StateDone]

	render := func(a app, t task.Task, width int) string {
		d := taskDelegate{styles: a.styles, blockers: a.blockers}
		return ansi.Strip(d.renderRow(listItem{t: t}, false, width))
	}
	for _, width := range []int{100, compactMetaWidth - 1} {
		row := render(a, subject, width)
		if !strings.HasSuffix(row, " "+blocked+"1") {
			t.Errorf("width %d: row with one open blocker should end in %q:\n%q", width, blocked+"1", row)
		}
		if other := render(a, blocker, width); !strings.HasSuffix(other, "   ") {
			t.Errorf("width %d: row that waits on nothing should end in a blank cell:\n%q", width, other)
		}
		if w := len([]rune(row)); w != width {
			t.Errorf("width %d: row is %d cells wide:\n%q", width, w, row)
		}
	}

	if err := s.SetState(ctx, blocker.ID, task.StateDone); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	m = drive(t, m, refreshMsg{})
	a = m.(app)
	if a.blockers[subject.ID] != (task.BlockerCount{Open: 0, Total: 1}) {
		t.Fatalf("blocker counts after the blocker is done = %+v", a.blockers[subject.ID])
	}
	if row := render(a, subject, 100); !strings.HasSuffix(row, " "+blocked+done) {
		t.Errorf("row with every blocker done should end in %q:\n%q", blocked+done, row)
	}
}

// The cell is three columns in both glyph sets, whatever it shows, or the
// meta block stops aligning.
func TestDepCellWidthInBothGlyphSets(t *testing.T) {
	for _, g := range []glyphs{unicodeGlyphs(), asciiGlyphs()} {
		styles := DefaultStyles()
		styles.Glyphs = g
		d := taskDelegate{styles: styles}
		cases := map[string]task.BlockerCount{
			"none":     {},
			"one open": {Open: 1, Total: 1},
			"two open": {Open: 2, Total: 3},
			"many":     {Open: 123, Total: 200},
			"all done": {Open: 0, Total: 2},
		}
		for name, c := range cases {
			cell := d.depCell(c, depCellWidth)
			if w := runeWidth(cell.text); w != depCellWidth {
				t.Errorf("%q %s: cell %q is %d wide, want %d", g.State[task.StateBlocked], name, cell.text, w, depCellWidth)
			}
		}
		blocked, done := g.State[task.StateBlocked], g.State[task.StateDone]
		if got := d.depCell(task.BlockerCount{Open: 2, Total: 2}, depCellWidth).text; got != " "+blocked+"2" {
			t.Errorf("open cell = %q, want %q", got, " "+blocked+"2")
		}
		if got := d.depCell(task.BlockerCount{Total: 2}, depCellWidth).text; got != " "+blocked+done {
			t.Errorf("all-done cell = %q, want %q", got, " "+blocked+done)
		}
	}
}
