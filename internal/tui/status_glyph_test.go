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
