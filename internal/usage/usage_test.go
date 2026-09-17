package usage

import (
	"math/rand"
	"testing"
	"time"
)

func TestTokensAdd(t *testing.T) {
	a := Tokens{Input: 1, Output: 2, CacheCreation: 3, CacheRead: 4, Ephemeral5m: 5, Ephemeral1h: 6}
	b := Tokens{Input: 10, Output: 20, CacheCreation: 30, CacheRead: 40, Ephemeral5m: 50, Ephemeral1h: 60}
	want := Tokens{Input: 11, Output: 22, CacheCreation: 33, CacheRead: 44, Ephemeral5m: 55, Ephemeral1h: 66}

	got := a.Add(b)
	if got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
	if a != (Tokens{Input: 1, Output: 2, CacheCreation: 3, CacheRead: 4, Ephemeral5m: 5, Ephemeral1h: 6}) {
		t.Errorf("Add mutated its receiver: %+v", a)
	}
}

func TestTokensTotals(t *testing.T) {
	tk := Tokens{Input: 50, Output: 7, CacheCreation: 50, CacheRead: 900}
	if got, want := tk.InputTotal(), int64(1000); got != want {
		t.Errorf("InputTotal = %d, want %d", got, want)
	}
	if got, want := tk.Total(), int64(1007); got != want {
		t.Errorf("Total = %d, want %d", got, want)
	}
	if got, want := tk.CacheHitRatio(), 0.9; got != want {
		t.Errorf("CacheHitRatio = %v, want %v", got, want)
	}
	if got := (Tokens{}).CacheHitRatio(); got != 0 {
		t.Errorf("CacheHitRatio of zero value = %v, want 0", got)
	}
}

func TestTokensIsZero(t *testing.T) {
	if !(Tokens{}).IsZero() {
		t.Error("zero value IsZero() = false, want true")
	}
	tests := []struct {
		name string
		tk   Tokens
	}{
		{"Input", Tokens{Input: 1}},
		{"Output", Tokens{Output: 1}},
		{"CacheCreation", Tokens{CacheCreation: 1}},
		{"CacheRead", Tokens{CacheRead: 1}},
		{"Ephemeral5m", Tokens{Ephemeral5m: 1}},
		{"Ephemeral1h", Tokens{Ephemeral1h: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.tk.IsZero() {
				t.Errorf("%+v IsZero() = true, want false", tt.tk)
			}
		})
	}
}

func TestSumSinceGroupBy(t *testing.T) {
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{Tokens: Tokens{Output: 1}, At: base, Cwd: "/proj/a", Model: "opus", SessionID: "s1"},
		{Tokens: Tokens{Output: 2}, At: base.Add(time.Hour), Cwd: "/proj/a", Model: "haiku", SessionID: "s1"},
		{Tokens: Tokens{Output: 4}, At: base.Add(2 * time.Hour), Cwd: "/proj/b", Model: "opus", SessionID: "s2"},
		{Tokens: Tokens{Output: 8}, At: base.Add(3 * time.Hour), Cwd: "/proj/b", Model: "haiku", SessionID: "s2"},
	}

	if got, want := Sum(entries).Output, int64(15); got != want {
		t.Errorf("Sum = %d, want %d", got, want)
	}

	cutoff := base.Add(2 * time.Hour)
	got := Since(entries, cutoff).Output
	if want := int64(4 + 8); got != want {
		t.Errorf("Since at cutoff = %d, want %d (must include the entry exactly at cutoff)", got, want)
	}
	excl := Since(entries, cutoff.Add(time.Nanosecond)).Output
	if want := int64(8); excl != want {
		t.Errorf("Since one ns after cutoff = %d, want %d (must exclude the entry now before cutoff)", excl, want)
	}

	byProject := GroupBy(entries, func(e Entry) string { return e.Cwd })
	if got, want := byProject["/proj/a"].Output, int64(3); got != want {
		t.Errorf("GroupBy(Cwd)[/proj/a] = %d, want %d", got, want)
	}
	if got, want := byProject["/proj/b"].Output, int64(12); got != want {
		t.Errorf("GroupBy(Cwd)[/proj/b] = %d, want %d", got, want)
	}

	byModel := GroupBy(entries, func(e Entry) string { return e.Model })
	if got, want := byModel["opus"].Output, int64(5); got != want {
		t.Errorf("GroupBy(Model)[opus] = %d, want %d", got, want)
	}
	if got, want := byModel["haiku"].Output, int64(10); got != want {
		t.Errorf("GroupBy(Model)[haiku] = %d, want %d", got, want)
	}
}

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{Tokens: Tokens{Output: 1}, At: now.Add(-1 * time.Hour), SessionID: "A"},
		{Tokens: Tokens{Output: 2}, At: now.Add(-3 * time.Hour), SessionID: "B"},
		{Tokens: Tokens{Output: 4}, At: now.Add(-5 * time.Hour), SessionID: "A"}, // exactly at the 5h boundary: in
		{Tokens: Tokens{Output: 8}, At: now.Add(-6 * time.Hour), SessionID: "C"},
		{Tokens: Tokens{Output: 16}, At: now.Add(-8 * 24 * time.Hour), SessionID: "D"},
		{Tokens: Tokens{Output: 32}, At: now.Add(-2 * time.Hour), SessionID: ""}, // in window, no session id
	}
	rand.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })

	s := Summarize(entries, now)

	if got, want := s.Last5h.Output, int64(1+2+4+32); got != want {
		t.Errorf("Last5h.Output = %d, want %d", got, want)
	}
	if got, want := s.Last7d.Output, int64(1+2+4+8+32); got != want {
		t.Errorf("Last7d.Output = %d, want %d (excludes only the 8-day-old entry)", got, want)
	}
	if got, want := s.AllTime.Output, int64(1+2+4+8+16+32); got != want {
		t.Errorf("AllTime.Output = %d, want %d", got, want)
	}
	if s.Messages != 4 {
		t.Errorf("Messages = %d, want 4", s.Messages)
	}
	if s.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2 (empty session id must not count as a third)", s.Sessions)
	}
	wantFirst := now.Add(-5 * time.Hour)
	if !s.FirstInWindow.Equal(wantFirst) {
		t.Errorf("FirstInWindow = %v, want %v", s.FirstInWindow, wantFirst)
	}
	if !s.Now.Equal(now) {
		t.Errorf("Now = %v, want %v", s.Now, now)
	}

	empty := Summarize(nil, now)
	if !empty.AllTime.IsZero() || !empty.Last5h.IsZero() || !empty.Last7d.IsZero() {
		t.Errorf("Summarize(nil, now) = %+v, want all zero", empty)
	}
	if !empty.FirstInWindow.IsZero() {
		t.Errorf("Summarize(nil, now).FirstInWindow = %v, want zero", empty.FirstInWindow)
	}
}
