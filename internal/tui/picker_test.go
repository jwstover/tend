package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestPickerWindow(t *testing.T) {
	cases := []struct {
		name                     string
		sel, top, n, vis, pinned int
		want                     int
	}{
		// pinned = 1, moved verbatim from the old sessionPickerWindow table.
		{"new row keeps the window", 0, 3, 25, 10, 1, 3},
		{"inside the window", 5, 0, 25, 10, 1, 0},
		{"one past the bottom scrolls by one", 11, 0, 25, 10, 1, 1},
		{"far below jumps", 25, 0, 25, 10, 1, 15},
		{"above the window pulls it up", 2, 5, 25, 10, 1, 1},
		{"window never overruns the end", 25, 20, 25, 10, 1, 15},
		{"fewer rows than the window", 3, 2, 3, 10, 1, 0},
		{"no rows", 0, 0, 0, 10, 1, 0},
		// pinned = 0: an ordinary picker with no pinned rows.
		{"unpinned inside the window", 5, 0, 25, 10, 0, 0},
		{"unpinned one past the bottom scrolls by one", 10, 0, 25, 10, 0, 1},
		{"unpinned above the window pulls it up", 2, 5, 25, 10, 0, 2},
	}
	for _, c := range cases {
		if got := pickerWindow(c.sel, c.top, c.n, c.vis, c.pinned); got != c.want {
			t.Errorf("%s: pickerWindow(%d, %d, %d, %d, %d) = %d, want %d",
				c.name, c.sel, c.top, c.n, c.vis, c.pinned, got, c.want)
		}
	}
}

func testItems(n int) []string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf("item %d", i)
	}
	return items
}

func TestPickerKeyNavigationClampsAndScrolls(t *testing.T) {
	p := picker[string]{items: testItems(15), label: func(s string) string { return s }}

	for range 20 {
		p.key(tea.KeyPressMsg{Code: tea.KeyDown}, 0)
	}
	if p.sel != len(p.items)-1 {
		t.Errorf("sel = %d after running past the end, want %d", p.sel, len(p.items)-1)
	}
	if vis := p.visible(0); p.top != len(p.items)-vis {
		t.Errorf("top = %d, want %d (window pinned to the end)", p.top, len(p.items)-vis)
	}

	for range 30 {
		p.key(tea.KeyPressMsg{Code: tea.KeyUp}, 0)
	}
	if p.sel != 0 || p.top != 0 {
		t.Errorf("sel=%d top=%d after running past the top, want 0, 0", p.sel, p.top)
	}
}

func TestPickerKeyTypingResetsSelectionAndWindow(t *testing.T) {
	p := picker[string]{items: []string{"apple", "banana", "cherry"}, label: func(s string) string { return s }}
	p.key(tea.KeyPressMsg{Code: tea.KeyDown}, 0)
	if p.sel != 1 {
		t.Fatalf("sel = %d after one down, want 1", p.sel)
	}

	action, _ := p.key(tea.KeyPressMsg{Code: 'x', Text: "x"}, 0)
	if action != pickerNone || p.sel != 0 || p.top != 0 {
		t.Errorf("typing should reset sel/top to 0, got action=%v sel=%d top=%d", action, p.sel, p.top)
	}

	// backspace is rune-safe on a multi-byte query.
	p.query = "héllo"
	p.key(tea.KeyPressMsg{Code: tea.KeyBackspace}, 0)
	if p.query != "héll" {
		t.Errorf("query after backspace = %q, want %q", p.query, "héll")
	}
}

func TestPickerDigitPicksVisibleRowElseTypes(t *testing.T) {
	items := []string{"a", "b", "c"}
	p := picker[string]{items: items, label: func(s string) string { return s }, numbered: true}
	action, idx := p.key(tea.KeyPressMsg{Code: '2', Text: "2"}, 0)
	if action != pickerPick || idx != 1 {
		t.Errorf("digit 2 over 3 rows = %v, %d, want pickerPick, 1", action, idx)
	}

	p2 := picker[string]{items: items, label: func(s string) string { return s }, numbered: true}
	action, idx = p2.key(tea.KeyPressMsg{Code: '7', Text: "7"}, 0)
	if action != pickerNone || p2.query != "7" {
		t.Errorf("digit 7 past the end should type instead, got action=%v query=%q", action, p2.query)
	}

	// Window-relative numbering: with the window scrolled to top=5, "1"
	// picks item 5, not item 0.
	p3 := picker[string]{items: testItems(20), label: func(s string) string { return s }, numbered: true, sel: 5, top: 5}
	action, idx = p3.key(tea.KeyPressMsg{Code: '1', Text: "1"}, 0)
	if action != pickerPick || idx != 5 {
		t.Errorf("digit 1 with top=5 = %v, %d, want pickerPick, 5", action, idx)
	}
}

func TestPickerDigitIsTextWhenUnnumbered(t *testing.T) {
	p := picker[string]{items: []string{"a", "b", "c"}, label: func(s string) string { return s }}
	for _, r := range "123456789" {
		action, _ := p.key(tea.KeyPressMsg{Code: r, Text: string(r)}, 0)
		if action != pickerNone {
			t.Fatalf("digit %q should type when unnumbered, got action %v", r, action)
		}
	}
	if p.query != "123456789" {
		t.Errorf("query = %q, want every digit typed", p.query)
	}
}

func TestPickerPinnedRowIsNeverFiltered(t *testing.T) {
	p := picker[string]{
		items: []string{"apple", "banana"}, label: func(s string) string { return s },
		pinned: 1, query: "zzz",
	}
	if got := p.rowCount(); got != 1 {
		t.Errorf("rowCount = %d with a query matching nothing, want 1 (just the pinned row)", got)
	}
	if _, ok := p.at(0); ok {
		t.Errorf("at(0) should report false for the pinned row")
	}
}

func TestPickerRenderFitsWidthAndShowsOverflow(t *testing.T) {
	p := picker[string]{items: testItems(20), label: func(s string) string { return s }, numbered: true}
	s := DefaultStyles()
	view := p.render(s, 60, 30, pickerView[string]{
		icon:  "» ",
		title: "pick one",
		row:   func(it string, selected bool, w int) string { return it },
	})

	lines := strings.Split(view, "\n")
	width := lipgloss.Width(lines[0])
	if !strings.Contains(lines[0], "┌") && !strings.Contains(lines[0], "+") {
		t.Errorf("first line should be the box's top border:\n%s", lines[0])
	}
	for i, l := range lines {
		if lipgloss.Width(l) != width {
			t.Errorf("line %d width = %d, want %d (every row should line up):\n%s", i, lipgloss.Width(l), width, view)
		}
	}
	if !strings.Contains(view, "↓ 10 more") {
		t.Errorf("a 20-item picker on a 30-row terminal should show the overflow row:\n%s", view)
	}
}

func TestPalettePicksKeepsAliasTierAndAddRow(t *testing.T) {
	cmds := []paletteCommand{
		{label: "Toggle detail pane"},
		{label: "Quit", aliases: []string{"q", "quit"}},
		{label: "Quick-add to inbox"},
	}

	got := palettePicks("q", cmds)
	if len(got) == 0 || got[0].label != "Quit" {
		t.Fatalf("palettePicks(q) = %q, want Quit first (an exact alias beats a fuzzy hit)", paletteLabels(got))
	}

	got = palettePicks("add buy milk", cmds)
	if len(got) == 0 || got[0].label != `Add task: "buy milk"` {
		t.Fatalf(`palettePicks(add buy milk) = %q, want the synthetic add row first`, paletteLabels(got))
	}
}

func paletteLabels(cmds []paletteCommand) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.label
	}
	return out
}
