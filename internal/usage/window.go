package usage

import (
	"sort"
	"time"
)

// sortByTime orders entries oldest first. Transcripts are read in
// directory order, so entries arrive interleaved across sessions; a
// rollup that trends usage over time needs them ordered.
func sortByTime(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At.Before(entries[j].At) })
}

// Rolling5h and Rolling7d are the two spans an account's limits are
// usually described in. tend has no way to read the ceiling from these
// token totals alone -- see Quota for the actual limit, read live from
// `claude -p "/usage"`.
const (
	Rolling5h = 5 * time.Hour
	Rolling7d = 7 * 24 * time.Hour
)

// Summary is the usage picture a view shows: what has been spent over
// each span tend tracks, plus the busiest recent session so a spike has
// somewhere to point. Every field counts every session on the machine,
// not just tend's own runs, because they all draw on the same account.
type Summary struct {
	// Now is the instant the summary was taken, so a view can say how
	// stale it is and so the spans below are reproducible in a test.
	Now time.Time

	Last5h  Tokens
	Last7d  Tokens
	AllTime Tokens

	// Messages is how many assistant messages went into Last5h, which is
	// the honest denominator for "how busy has it been" -- tokens alone
	// conflate one enormous turn with many small ones.
	Messages int

	// Sessions is how many distinct sessions contributed to Last5h.
	Sessions int

	// FirstInWindow is the timestamp of the oldest message inside the
	// five-hour span, so a view can say how much of the window has
	// actually been used rather than implying a full five hours of it.
	FirstInWindow time.Time
}

// Summarize rolls entries up as of now. Entries need not be sorted.
func Summarize(entries []Entry, now time.Time) Summary {
	s := Summary{Now: now}
	cut5h := now.Add(-Rolling5h)
	cut7d := now.Add(-Rolling7d)
	sessions := make(map[string]struct{})
	for _, e := range entries {
		s.AllTime = s.AllTime.Add(e.Tokens)
		if !e.At.Before(cut7d) {
			s.Last7d = s.Last7d.Add(e.Tokens)
		}
		if !e.At.Before(cut5h) {
			s.Last5h = s.Last5h.Add(e.Tokens)
			s.Messages++
			if e.SessionID != "" {
				sessions[e.SessionID] = struct{}{}
			}
			if s.FirstInWindow.IsZero() || e.At.Before(s.FirstInWindow) {
				s.FirstInWindow = e.At
			}
		}
	}
	s.Sessions = len(sessions)
	return s
}

// BreakdownRow is one group's usage over each span Summary tracks.
type BreakdownRow struct {
	Key                     string
	Last5h, Last7d, AllTime Tokens
}

// Breakdown groups entries by key and totals each group over the 5h, 7d
// and all-time spans as of now, in one pass. Rows sort by Last7d.Total()
// desc, then AllTime.Total() desc, then Key asc, so the order is stable.
func Breakdown(entries []Entry, now time.Time, key func(Entry) string) []BreakdownRow {
	cut5h := now.Add(-Rolling5h)
	cut7d := now.Add(-Rolling7d)
	idx := make(map[string]int)
	var rows []BreakdownRow
	for _, e := range entries {
		k := key(e)
		i, ok := idx[k]
		if !ok {
			i = len(rows)
			idx[k] = i
			rows = append(rows, BreakdownRow{Key: k})
		}
		r := &rows[i]
		r.AllTime = r.AllTime.Add(e.Tokens)
		if !e.At.Before(cut7d) {
			r.Last7d = r.Last7d.Add(e.Tokens)
		}
		if !e.At.Before(cut5h) {
			r.Last5h = r.Last5h.Add(e.Tokens)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if x, y := a.Last7d.Total(), b.Last7d.Total(); x != y {
			return x > y
		}
		if x, y := a.AllTime.Total(), b.AllTime.Total(); x != y {
			return x > y
		}
		return a.Key < b.Key
	})
	return rows
}
