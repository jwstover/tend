package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/task"
)

// The list marker is deliberately selective: it exists to say "a session
// is doing something right now", so a task whose session merely ended
// must not carry chrome forever.
func TestSessionCellShowsOnlyLiveStatuses(t *testing.T) {
	styles := DefaultStyles()
	for _, tc := range []struct {
		status task.SessionStatus
		want   bool
	}{
		{task.SessionStarting, true},
		{task.SessionWorking, true},
		{task.SessionBlocked, true},
		{task.SessionIdle, true},
		{task.SessionEnded, false},
		{task.SessionUnknown, false},
		{task.SessionStatus("something-new"), false},
	} {
		d := taskDelegate{styles: styles, sessions: map[int64]task.SessionStatus{7: tc.status}}
		cell := ansi.Strip(d.sessionCell(7).text)
		got := strings.TrimSpace(cell) != ""
		if got != tc.want {
			t.Errorf("status %q: marker shown = %v, want %v", tc.status, got, tc.want)
		}
		// Whatever it decides, the column keeps its width so the
		// columns after it stay aligned across rows.
		if w := len([]rune(cell)); w != 2 {
			t.Errorf("status %q: cell width = %d runes, want 2", tc.status, w)
		}
	}
}

// A task with no session at all is the common case; it must render the
// same blank column as one whose session ended.
func TestSessionCellBlankWhenTaskHasNoSession(t *testing.T) {
	d := taskDelegate{styles: DefaultStyles()}
	if got := ansi.Strip(d.sessionCell(7).text); got != "  " {
		t.Errorf("sessionCell = %q, want two blanks", got)
	}
}

// Every glyph in the set has to be one cell wide, the invariant the rest
// of the glyph table already holds. The plan sketched a lightning bolt
// and a pause sign, both East Asian Wide, which would have shifted every
// column after them one cell to the right.
func TestSessionGlyphsAreSingleWidth(t *testing.T) {
	for _, g := range []glyphs{unicodeGlyphs(), asciiGlyphs()} {
		for status, glyph := range g.Session {
			if w := ansi.StringWidth(glyph); w != 1 {
				t.Errorf("glyph %q for status %q has width %d, want 1", glyph, status, w)
			}
		}
	}
}

// An out-of-band status value must degrade to unknown rather than
// rendering an empty string and knocking the row out of alignment —
// section 8.2 stores status as plain TEXT with no foreign key precisely
// so this can happen.
func TestSessionStatusCellFallsBackToUnknown(t *testing.T) {
	styles := DefaultStyles()
	glyph, _ := sessionStatusCell(styles, task.SessionStatus("from-the-future"))
	if want := styles.Glyphs.Session[task.SessionUnknown]; glyph != want {
		t.Errorf("glyph = %q, want the unknown glyph %q", glyph, want)
	}
}

// The end-to-end wiring: a status written to the store by a hook has to
// reach the rendered row. The map rides on the delegate rather than on
// the items, so a refresh that rebuilds items but forgets the delegate
// would still pass every unit test above.
func TestListRowShowsLiveSessionStatus(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)

	parent, err := s.AddTask(ctx, "ship the thing")
	if err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	if _, err := s.CreateSession(ctx, parent.ID, "ext-1", "/tmp/work", parent.Title, "tend-ext-1"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := s.SetSessionStatus(ctx, "ext-1", task.SessionBlocked); err != nil {
		t.Fatalf("SetSessionStatus: %v", err)
	}
	m = drive(t, m, refreshMsg{})

	a := m.(app)
	if got := a.sessionStatus[parent.ID]; got != task.SessionBlocked {
		t.Fatalf("app.sessionStatus[%d] = %q, want %q", parent.ID, got, task.SessionBlocked)
	}
	want := a.styles.Glyphs.Session[task.SessionBlocked]
	if !strings.Contains(ansi.Strip(a.View().Content), want) {
		t.Errorf("rendered view is missing the blocked-session glyph %q", want)
	}
}

// The three-cell gutter has to show both signals at once: moving the
// marker into the gutter is only safe if it doesn't cost the selection
// bar on the row the cursor is on.
func TestGutterShowsSelectionBarAndSessionMarkerTogether(t *testing.T) {
	styles := DefaultStyles()
	d := taskDelegate{styles: styles, sessions: map[int64]task.SessionStatus{7: task.SessionWorking}}
	glyph := styles.Glyphs.Session[task.SessionWorking]

	for _, tc := range []struct {
		name     string
		selected bool
		want     string
	}{
		{"selected", true, styles.Glyphs.SelBar + glyph + " "},
		{"not selected", false, " " + glyph + " "},
	} {
		var got string
		for _, sg := range d.gutterCells(7, tc.selected) {
			got += ansi.Strip(sg.text)
		}
		if got != tc.want {
			t.Errorf("%s: gutter = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The gutter is fixed-width whatever it carries, so every column after
// it lines up down the list.
func TestGutterIsAlwaysThreeCells(t *testing.T) {
	styles := DefaultStyles()
	for _, tc := range []struct {
		name     string
		sessions map[int64]task.SessionStatus
		selected bool
	}{
		{"bare", nil, false},
		{"selected only", nil, true},
		{"marker only", map[int64]task.SessionStatus{7: task.SessionIdle}, false},
		{"both", map[int64]task.SessionStatus{7: task.SessionIdle}, true},
		{"ended session", map[int64]task.SessionStatus{7: task.SessionEnded}, false},
	} {
		d := taskDelegate{styles: styles, sessions: tc.sessions}
		w := 0
		for _, sg := range d.gutterCells(7, tc.selected) {
			w += len([]rune(ansi.Strip(sg.text)))
		}
		if w != 3 {
			t.Errorf("%s: gutter width = %d runes, want 3", tc.name, w)
		}
	}
}

// The marker used to sit mid-row, at a different offset on parent rows
// than on child rows, so it never scanned as a column. In the gutter it
// starts at the same cell on every row at every depth — that is the
// whole point of the move, and nothing else in the row may push it.
func TestSessionMarkerIsAtTheSameColumnAtEveryDepth(t *testing.T) {
	styles := DefaultStyles()
	glyph := styles.Glyphs.Session[task.SessionWorking]
	live := func(id int64) map[int64]task.SessionStatus {
		return map[int64]task.SessionStatus{id: task.SessionWorking}
	}

	pri := int64(1)
	due := "2026-12-01"
	parent := task.Task{ID: 1, Title: "parent", State: task.StateDoing, Priority: &pri, Due: &due}
	child := task.Task{ID: 2, Title: "child", State: task.StateTodo}
	deep := task.Task{ID: 3, Title: "deep", State: task.StateDone}

	rows := map[string]string{
		"top-level":     taskDelegate{styles: styles, sessions: live(1)}.renderRow(listItem{t: parent, done: 1, total: 2}, false, 100),
		"top-level sel": taskDelegate{styles: styles, sessions: live(1)}.renderRow(listItem{t: parent, done: 1, total: 2}, true, 100),
		"depth 1":       taskDelegate{styles: styles, sessions: live(2)}.renderChildRow(childItem{t: child, depth: 1}, false, 100),
		"depth 3":       taskDelegate{styles: styles, sessions: live(3)}.renderChildRow(childItem{t: deep, depth: 3}, false, 100),
	}
	for name, row := range rows {
		if got := runeIndex(ansi.Strip(row), glyph); got != 1 {
			t.Errorf("%s: session marker at column %d, want 1:\n%q", name, got, ansi.Strip(row))
		}
	}
}

// runeIndex is strings.Index in cells rather than bytes: the glyphs this
// file measures are multi-byte, so a byte offset is not a column.
func runeIndex(s, substr string) int {
	i := strings.Index(s, substr)
	if i < 0 {
		return -1
	}
	return len([]rune(s[:i]))
}

// Parent and child rows drifted into different column orders while they
// were separate functions (CHILD-ROW-PARITY.md 3). They share one
// renderer now; this pins the order so a future edit to one can't move a
// column on only half the rows.
func TestParentAndChildRowsShareColumnOrder(t *testing.T) {
	styles := DefaultStyles()
	g := styles.Glyphs
	d := taskDelegate{styles: styles}

	pri := int64(2)
	parent := task.Task{ID: 1, Title: "parent", State: task.StateTodo, Priority: &pri}
	child := task.Task{ID: 2, Title: "child", State: task.StateTodo, Priority: &pri}

	// gutter(3) then dot, caret, title — the child adds only indent.
	wantParent := "   " + g.State[task.StateTodo] + " " + g.CaretClosed + " " + "parent"
	wantChild := "    " + g.State[task.StateTodo] + " " + g.CaretClosed + " " + "child"

	gotParent := ansi.Strip(d.renderRow(listItem{t: parent, total: 1}, false, 100))
	gotChild := ansi.Strip(d.renderChildRow(childItem{t: child, depth: 1, total: 1}, false, 100))

	if !strings.HasPrefix(gotParent, wantParent) {
		t.Errorf("parent row = %q, want prefix %q", gotParent, wantParent)
	}
	if !strings.HasPrefix(gotChild, wantChild) {
		t.Errorf("child row = %q, want prefix %q", gotChild, wantChild)
	}

	// The priority follows the title on both rows, at the same column.
	pp, cp := runeIndex(gotParent, g.Flag+"B"), runeIndex(gotChild, g.Flag+"B")
	if pp < 0 || cp < 0 {
		t.Fatalf("priority badge missing:\nparent %q\nchild  %q", gotParent, gotChild)
	}
	if pp <= runeIndex(gotParent, "parent") || cp <= runeIndex(gotChild, "child") {
		t.Errorf("priority must follow the title:\nparent %q\nchild  %q", gotParent, gotChild)
	}
	if pp != cp {
		t.Errorf("priority at column %d on parent, %d on child — they must align\n%q\n%q",
			pp, cp, gotParent, gotChild)
	}
}

// The priority moved from before the title to the head of the meta
// block. It is a column, so it sits at one cell on every row: with and
// without tags, due and sub-task counts beside it, at both meta widths,
// and on child rows, whose otherwise-empty meta block pads to match.
func TestPriorityColumnIsAtTheSameCellOnEveryRow(t *testing.T) {
	styles := DefaultStyles()
	g := styles.Glyphs
	pri := int64(1)
	due := "2026-12-01"

	for _, width := range []int{100, compactMetaWidth - 1} {
		d := taskDelegate{styles: styles, tags: map[int64][]string{1: {"support"}}}
		bare := task.Task{ID: 2, Title: "bare", State: task.StateTodo, Priority: &pri}
		full := task.Task{ID: 1, Title: "full", State: task.StateDoing, Priority: &pri, Due: &due}
		child := task.Task{ID: 3, Title: "child", State: task.StateTodo, Priority: &pri}

		rows := map[string]string{
			"bare":     ansi.Strip(d.renderRow(listItem{t: bare}, false, width)),
			"full":     ansi.Strip(d.renderRow(listItem{t: full, done: 1, total: 3, expanded: true}, false, width)),
			"selected": ansi.Strip(d.renderRow(listItem{t: full, done: 1, total: 3}, true, width)),
			"depth 1":  ansi.Strip(d.renderChildRow(childItem{t: child, depth: 1}, false, width)),
			"depth 3":  ansi.Strip(d.renderChildRow(childItem{t: child, depth: 3}, false, width)),
		}
		want := runeIndex(rows["bare"], g.Flag+"A")
		if want < 0 {
			t.Fatalf("width %d: bare row has no priority badge: %q", width, rows["bare"])
		}
		for name, row := range rows {
			if got := runeIndex(row, g.Flag+"A"); got != want {
				t.Errorf("width %d, %s: priority at column %d, want %d:\n%q", width, name, got, want, row)
			}
			if w := len([]rune(row)); w != width {
				t.Errorf("width %d, %s: row is %d cells wide:\n%q", width, name, w, row)
			}
		}
	}
}

// Section headings lead with the same three cells the rows do, so a
// heading's glyph sits directly above the state dots it labels.
func TestHeadingGlyphAlignsWithRowStateDots(t *testing.T) {
	styles := DefaultStyles()
	d := taskDelegate{styles: styles}

	sections := groupTasks(groupByState, []task.Task{{ID: 1, Title: "a task", State: task.StateTodo}}, nil, styles)
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1", len(sections))
	}
	heading := ansi.Strip(d.renderHeading(sections[0].sectionItem, 100))
	row := ansi.Strip(d.renderRow(listItem{t: task.Task{ID: 1, Title: "a task", State: task.StateTodo}}, false, 100))

	glyph := styles.Glyphs.State[task.StateTodo]
	if h, r := runeIndex(heading, glyph), runeIndex(row, glyph); h != r {
		t.Errorf("heading glyph at column %d, row state dot at column %d — they must align\n%q\n%q",
			h, r, heading, row)
	}
}
