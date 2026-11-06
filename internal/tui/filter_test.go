package tui

import "testing"

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
