package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func testEntries(st Styles) []panelEntry {
	return []panelEntry{
		{key: "t", desc: "todo", keyStyle: st.PanelKey},
		{key: "d", desc: "doing", keyStyle: st.PanelKey},
		{key: "b", desc: "blocked", keyStyle: st.PanelKey},
		{key: "x", desc: "done", keyStyle: st.PanelKey},
		{key: "s", desc: "someday", keyStyle: st.PanelKey},
		{key: "esc", desc: "cancel", keyStyle: st.Dimmed},
	}
}

func TestRenderKeyPanel(t *testing.T) {
	st := DefaultStyles()
	tests := []struct {
		name      string
		width     int
		maxHeight int
	}{
		{name: "wide single row", width: 100, maxHeight: 3}, // border + title + 1 row
		{name: "narrow wraps", width: 30, maxHeight: 6},
		{name: "zero width", width: 0, maxHeight: 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderKeyPanel(st, tc.width, "state", testEntries(st))
			if h := lipgloss.Height(got); h > tc.maxHeight {
				t.Errorf("height = %d, want <= %d:\n%s", h, tc.maxHeight, got)
			}
			plain := ansi.Strip(got)
			for _, want := range []string{"state", "t todo", "b blocked", "esc cancel"} {
				if !strings.Contains(plain, want) {
					t.Errorf("panel missing %q:\n%s", want, plain)
				}
			}
			if tc.width > 0 {
				for _, line := range strings.Split(got, "\n") {
					if w := lipgloss.Width(line); w > tc.width {
						t.Errorf("line wider than panel (%d > %d): %q", w, tc.width, ansi.Strip(line))
					}
				}
			}
		})
	}
}

func TestRenderKeyPanelNarrowWrapsRows(t *testing.T) {
	st := DefaultStyles()
	got := renderKeyPanel(st, 30, "state", testEntries(st))
	if h := lipgloss.Height(got); h <= 3 {
		t.Errorf("height = %d at width 30, want > 3 (entries should wrap)", h)
	}
}

func TestRenderKeyPanelEmpty(t *testing.T) {
	if got := renderKeyPanel(DefaultStyles(), 80, "state", nil); got != "" {
		t.Errorf("renderKeyPanel(nil entries) = %q, want \"\"", got)
	}
}
