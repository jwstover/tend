package usage

import (
	"testing"
	"time"
)

func TestBreakdown(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	mk := func(cwd string, ago time.Duration, out int64) Entry {
		return Entry{At: now.Add(-ago), Cwd: cwd, Tokens: Tokens{Output: out}}
	}
	entries := []Entry{
		mk("a", time.Hour, 10),
		mk("a", 48*time.Hour, 20),
		mk("a", 30*24*time.Hour, 40),
		mk("b", 2*time.Hour, 5),
		mk("b", 30*24*time.Hour, 1000),
		mk("c", 30*24*time.Hour, 1),
	}
	rows := Breakdown(entries, now, func(e Entry) string { return e.Cwd })
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// a: 7d=30 beats b: 7d=5; c has no 7d and sorts last.
	if rows[0].Key != "a" || rows[1].Key != "b" || rows[2].Key != "c" {
		t.Fatalf("order = %s,%s,%s", rows[0].Key, rows[1].Key, rows[2].Key)
	}
	a := rows[0]
	if a.Last5h.Total() != 10 || a.Last7d.Total() != 30 || a.AllTime.Total() != 70 {
		t.Errorf("a = %d/%d/%d", a.Last5h.Total(), a.Last7d.Total(), a.AllTime.Total())
	}
	if b := rows[1]; b.Last5h.Total() != 5 || b.Last7d.Total() != 5 || b.AllTime.Total() != 1005 {
		t.Errorf("b = %d/%d/%d", b.Last5h.Total(), b.Last7d.Total(), b.AllTime.Total())
	}
	if c := rows[2]; !c.Last7d.IsZero() || c.AllTime.Total() != 1 {
		t.Errorf("c = %+v", c)
	}
}
