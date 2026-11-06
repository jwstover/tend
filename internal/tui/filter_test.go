package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/task"
)

func TestMatchesFilter(t *testing.T) {
	tests := []struct {
		name  string
		query string
		title string
		tags  []string
		want  bool
	}{
		{"blank matches all", "  ", "anything", nil, true},
		{"substring of title", "ail", "Fix mail bug", nil, true},
		{"case-insensitive", "FIX", "fix it", nil, true},
		{"not fuzzy", "fxb", "fix bug", nil, false},
		{"all words must match", "fix mail", "Fix bug in mail", nil, true},
		{"word order is free", "mail fix", "Fix bug in mail", nil, true},
		{"one word missing", "fix zzz", "Fix bug", nil, false},
		{"matches a tag", "support", "Reply to Bob", []string{"Support"}, true},
		{"words split across title and tag", "reply support", "Reply to Bob", []string{"support"}, true},
		{"tag substring", "supp", "x", []string{"support"}, true},
		{"title only, no tag", "support", "x", []string{"other"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesFilter(tt.query, tt.title, tt.tags); got != tt.want {
				t.Errorf("matchesFilter(%q, %q, %v) = %v, want %v", tt.query, tt.title, tt.tags, got, tt.want)
			}
		})
	}
}

func TestFilterKeepsSelectionAndDetail(t *testing.T) {
	ctx := context.Background()
	m, s := newTestApp(t)
	for _, title := range []string{"alpha", "beta", "gamma"} {
		if _, err := s.AddTask(ctx, title); err != nil {
			t.Fatalf("AddTask: %v", err)
		}
	}
	m = drive(t, m, refreshMsg{})
	sel := func() string {
		t, _ := m.(app).selected()
		return t.Title
	}
	// Select "beta" (the store lists newest first or oldest first; find it).
	for i := 0; i < 5 && sel() != "beta"; i++ {
		m = drive(t, m, keyPress('j'))
	}
	if sel() != "beta" {
		t.Fatalf("could not select beta, on %q", sel())
	}

	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.(app).showDetail {
		t.Fatal("enter did not open the detail pane")
	}

	// Narrowing to a query beta still matches keeps it selected.
	m = drive(t, m, keyPress('/'))
	m = drive(t, m, keyPress('e'))
	m = drive(t, m, keyPress('t'))
	if sel() != "beta" {
		t.Errorf("selection after matching filter = %q, want beta", sel())
	}
	if m.(app).detailID == 0 {
		t.Error("detail pane lost its task under the filter")
	}

	// A query that hides beta falls to the first visible task row.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = drive(t, m, keyPress('g'))
	m = drive(t, m, keyPress('a'))
	if got := sel(); got != "gamma" {
		t.Errorf("selection after filter hiding beta = %q, want gamma", got)
	}
	if strings.Contains(ansi.Strip(m.View().Content), "nothing selected") {
		t.Error("detail pane says nothing selected")
	}

	// Clearing keeps the selected task selected.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if got := sel(); got != "gamma" {
		t.Errorf("selection after clear = %q, want gamma", got)
	}
}

func TestBuildFilterViewMatchesSubtasksUnderDimmedAncestors(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	tasks := []task.Task{
		{ID: 1, Title: "Parent one"},
		{ID: 2, Title: "Parent two"},
		{ID: 3, Title: "Middle", ParentID: id(1)},
		{ID: 4, Title: "needle leaf", ParentID: id(3)},
		{ID: 5, Title: "other leaf", ParentID: id(3)},
		{ID: 6, Title: "Parent two kid", ParentID: id(2)},
	}
	fv := buildFilterView("needle", tasks, nil)

	if len(fv.top) != 1 || fv.top[0].ID != 1 {
		t.Fatalf("top = %v, want only task 1", fv.top)
	}
	if got := fv.children[1]; len(got) != 1 || got[0].ID != 3 {
		t.Errorf("children of 1 = %v, want [3]", got)
	}
	if got := fv.children[3]; len(got) != 1 || got[0].ID != 4 {
		t.Errorf("children of 3 = %v, want [4] (non-matching sibling hidden)", got)
	}
	if !fv.dim[1] || !fv.dim[3] || fv.dim[4] {
		t.Errorf("dim = %v, want ancestors 1 and 3 dim, match 4 not", fv.dim)
	}
	if !fv.expanded[1] || !fv.expanded[3] {
		t.Errorf("expanded = %v, want ancestors open", fv.expanded)
	}

	// The dim flag reaches the rendered rows at every depth, not just the
	// top-level one: the middle ancestor is a child row.
	counts := map[int64]task.ChildCount{1: {Total: 1}, 3: {Total: 2}}
	rowDim := make(map[int64]bool)
	for _, it := range toGroupedItems(groupTasks(groupByState, fv.top, nil, Styles{}), counts, fv.expanded, fv.children, fv.dim) {
		switch r := it.(type) {
		case listItem:
			rowDim[r.t.ID] = r.dim
		case childItem:
			rowDim[r.t.ID] = r.dim
		}
	}
	if len(rowDim) != 3 || !rowDim[1] || !rowDim[3] || rowDim[4] {
		t.Errorf("row dim = %v, want rows 1 and 3 dim, 4 not", rowDim)
	}

	// A matching parent is not dim, even when a child matches as well.
	fv = buildFilterView("parent two", tasks, nil)
	if len(fv.top) != 1 || fv.top[0].ID != 2 || fv.dim[2] {
		t.Errorf("top = %v dim = %v, want task 2 undimmed", fv.top, fv.dim)
	}
}
