package tui

import (
	"slices"
	"testing"
)

func TestFuzzyMatch(t *testing.T) {
	cases := []struct {
		query, s string
		want     bool
	}{
		{"", "anything", true},
		{"sos", "Simple One-shot", true},
		{"SOS", "simple one-shot", true},
		{"one shot", "Simple One-shot", true}, // spaces in the query are skipped
		{"simple", "Simple One-shot", true},
		{"shot", "Simple One-shot", true},
		{"tohs", "Simple One-shot", false}, // out of order
		{"ssx", "Simple One-shot", false},  // a rune that never appears
		{"simple", "", false},
	}
	for _, c := range cases {
		if got := fuzzyMatch(c.query, c.s); got != c.want {
			t.Errorf("fuzzyMatch(%q, %q) = %v, want %v", c.query, c.s, got, c.want)
		}
	}
}

// Substring hits come first, in input order, then the looser in-order
// hits, and an empty query passes the list through untouched.
func TestFuzzyFilterRanksSubstringHitsFirst(t *testing.T) {
	items := []string{"Simple One-shot", "Review then ship", "Ship it", "Triage"}
	id := func(s string) string { return s }

	if got := fuzzyFilter("", items, id); !slices.Equal(got, items) {
		t.Errorf("empty query = %q, want the input unchanged", got)
	}
	if got, want := fuzzyFilter("ship", items, id), []string{"Review then ship", "Ship it"}; !slices.Equal(got, want) {
		t.Errorf("ship = %q, want %q", got, want)
	}
	// "sos" is a substring of nothing and an in-order subsequence of one
	// name only: the other three hold no 'o' at all.
	if got, want := fuzzyFilter("sos", items, id), []string{"Simple One-shot"}; !slices.Equal(got, want) {
		t.Errorf("sos = %q, want %q", got, want)
	}
	// A substring hit outranks a subsequence hit that sits earlier in the list.
	if got, want := fuzzyFilter("it", items, id), []string{"Ship it", "Simple One-shot", "Review then ship"}; !slices.Equal(got, want) {
		t.Errorf("it = %q, want %q", got, want)
	}
	if got := fuzzyFilter("zzz", items, id); len(got) != 0 {
		t.Errorf("zzz = %q, want nothing", got)
	}
}
