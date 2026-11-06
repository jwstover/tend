package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
