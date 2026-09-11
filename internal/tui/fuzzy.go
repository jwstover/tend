package tui

import (
	"strings"
	"unicode"
)

// fuzzyMatch reports whether every rune of query appears in s in order,
// case-insensitively, so "sos" finds "Simple One-shot". Whitespace in
// the query is ignored: a picker's filter is typed, not pasted, and a
// stray space should not empty the list. An empty query matches all.
func fuzzyMatch(query, s string) bool {
	rs := []rune(strings.ToLower(s))
	i := 0
	for _, q := range strings.ToLower(query) {
		if unicode.IsSpace(q) {
			continue
		}
		for i < len(rs) && rs[i] != q {
			i++
		}
		if i == len(rs) {
			return false
		}
		i++
	}
	return true
}

// fuzzyFilter keeps the items whose label fuzzy-matches the query, with
// the ones whose label contains the query outright (case-insensitively)
// listed first, and the remaining subsequence hits after them. Within
// each tier the input order is preserved, so a stable list stays stable
// as the query grows.
func fuzzyFilter[T any](query string, items []T, label func(T) string) []T {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return items
	}
	var exact, loose []T
	for _, it := range items {
		l := label(it)
		switch {
		case strings.Contains(strings.ToLower(l), q):
			exact = append(exact, it)
		case fuzzyMatch(q, l):
			loose = append(loose, it)
		}
	}
	return append(exact, loose...)
}
