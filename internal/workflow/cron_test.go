package workflow

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseCron(t *testing.T) {
	for _, expr := range []string{"*/15 * * * *", "0 9 * * 1-5", "30 2 1,15 * *", "0 0 * jan,jul sun", "@daily", "@Midnight", "0 0 * * 7", "5/20 * * * *", "  0   9 * * MON-FRI "} {
		if _, err := ParseCron(expr); err != nil {
			t.Errorf("ParseCron(%q): %v", expr, err)
		}
	}
	bad := []struct{ expr, want string }{
		{"* * * *", "5 fields"},
		{"60 * * * *", `minute "60" out of range 0-59`},
		{"*/0 * * * *", `minute "*/0"`},
		{"5-1 * * * *", `minute "5-1"`},
		{"* 25 * * *", `hour "25" out of range 0-23`},
		{"* * 0 * *", "day-of-month"},
		{"* * * 13 *", "month"},
		{"* * * foo *", "month"},
		{"* * * * 8", "day-of-week"},
		{"* * L * *", "day-of-month"},
		{"@every 5m", "5 fields"},
	}
	for _, tc := range bad {
		_, err := ParseCron(tc.expr)
		if !errors.Is(err, ErrInvalidCron) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseCron(%q) = %v, want ErrInvalidCron containing %q", tc.expr, err, tc.want)
		}
	}
}

func TestCronString(t *testing.T) {
	c, _ := ParseCron("@weekly")
	if c.String() != "0 0 * * 0" {
		t.Errorf("String = %q", c.String())
	}
	c, _ = ParseCron("  */5   * * * * ")
	if c.String() != "*/5 * * * *" {
		t.Errorf("String = %q", c.String())
	}
}

func TestCronNext(t *testing.T) {
	utc := time.UTC
	at := func(y int, m time.Month, d, h, mi, s int) time.Time { return time.Date(y, m, d, h, mi, s, 0, utc) }
	tests := []struct {
		name, expr  string
		after, want time.Time
	}{
		{"every 15 min", "*/15 * * * *", at(2026, 3, 10, 10, 7, 30), at(2026, 3, 10, 10, 15, 0)},
		{"strictly after a match", "*/15 * * * *", at(2026, 3, 10, 10, 15, 0), at(2026, 3, 10, 10, 30, 0)},
		{"hour rollover", "0 * * * *", at(2026, 3, 10, 10, 0, 1), at(2026, 3, 10, 11, 0, 0)},
		{"day rollover", "30 2 * * *", at(2026, 3, 10, 3, 0, 0), at(2026, 3, 11, 2, 30, 0)},
		{"month rollover", "0 0 1 * *", at(2026, 3, 10, 0, 0, 0), at(2026, 4, 1, 0, 0, 0)},
		{"year rollover", "0 0 1 1 *", at(2026, 3, 10, 0, 0, 0), at(2027, 1, 1, 0, 0, 0)},
		{"weekdays skip weekend", "0 9 * * 1-5", at(2026, 3, 13, 9, 0, 0), at(2026, 3, 16, 9, 0, 0)}, // Fri -> Mon
		{"named month and dow", "0 0 * jul sun", at(2026, 3, 10, 0, 0, 0), at(2026, 7, 5, 0, 0, 0)},
		{"dom or dow", "0 0 13 * fri", at(2026, 3, 1, 0, 0, 0), at(2026, 3, 6, 0, 0, 0)},
		{"dom or dow, dom first", "0 0 13 * fri", at(2026, 3, 12, 0, 0, 0), at(2026, 3, 13, 0, 0, 0)},
		{"dow 7 is sunday", "0 0 * * 7", at(2026, 3, 10, 0, 0, 0), at(2026, 3, 15, 0, 0, 0)},
		{"dom and star dow", "0 0 15 * *", at(2026, 3, 16, 0, 0, 0), at(2026, 4, 15, 0, 0, 0)},
		{"feb 29", "0 0 29 2 *", at(2026, 3, 1, 0, 0, 0), at(2028, 2, 29, 0, 0, 0)},
		{"step from N", "5/20 * * * *", at(2026, 3, 10, 10, 6, 0), at(2026, 3, 10, 10, 25, 0)},
		{"daily macro", "@daily", at(2026, 12, 31, 23, 59, 59), at(2027, 1, 1, 0, 0, 0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := ParseCron(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.Next(tc.after)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("Next(%s) = %s, want %s", tc.after, got, tc.want)
			}
			if !got.After(tc.after) || got.Second() != 0 || got.Nanosecond() != 0 {
				t.Errorf("Next(%s) = %s: not strictly after, or not on a minute", tc.after, got)
			}
		})
	}
}

func TestCronNextImpossible(t *testing.T) {
	c, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrInvalidCron) {
		t.Errorf("err = %v, want ErrInvalidCron", err)
	}
}

func TestCronNextDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	at := func(m time.Month, d, h, mi int) time.Time { return time.Date(2026, m, d, h, mi, 0, 0, ny) }

	// Spring forward 2026-03-08: 02:30 does not exist, so that day is skipped.
	c, _ := ParseCron("30 2 * * *")
	got, _ := c.Next(at(time.March, 8, 0, 0))
	if want := at(time.March, 9, 2, 30); !got.Equal(want) {
		t.Errorf("spring forward: %s, want %s", got, want)
	}

	// Fall back 2026-11-01: 01:30 happens twice and fires once.
	c, _ = ParseCron("30 1 * * *")
	first, _ := c.Next(at(time.November, 1, 0, 0))
	next, _ := c.Next(first)
	if want := at(time.November, 2, 1, 30); !next.Equal(want) {
		t.Errorf("fall back: second fire %s, want %s", next, want)
	}

	// An hourly job still fires through the repeated hour.
	c, _ = ParseCron("0 * * * *")
	got, _ = c.Next(first)
	if !got.After(first) || got.Sub(first) > 2*time.Hour {
		t.Errorf("hourly across fall back: %s after %s", got, first)
	}
}
