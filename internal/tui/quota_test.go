package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/agent"
)

func testQuota(session, week int) agent.Quota {
	return agent.Quota{
		Session: &agent.QuotaLimit{Label: "session", Percent: session},
		Week:    &agent.QuotaLimit{Label: "week (all models)", Percent: week},
	}
}

// testQuotaReset is testQuota with a reset time set on the session limit.
func testQuotaReset(session, week int, resetsAt time.Time) agent.Quota {
	q := testQuota(session, week)
	q.Session.ResetsAt = resetsAt
	return q
}

// quotaRowWith drives a quota reading into a fresh app at the given width
// and returns its quota row with the styling stripped.
func quotaRowWith(t *testing.T, width int, msgs ...quotaMsg) (app, string) {
	t.Helper()
	// The expectations below spell out the unicode set's labels and bars;
	// don't let a TEND_GLYPHS in the developer's shell change them.
	t.Setenv("TEND_GLYPHS", "")
	m, _ := newTestApp(t)
	m = drive(t, m, tea.WindowSizeMsg{Width: width, Height: 30})
	for _, msg := range msgs {
		m = drive(t, m, msg)
	}
	a := m.(app)
	return a, ansi.Strip(a.quotaLine())
}

// The first poll has to land straight away, not an interval in: a minute
// of empty row on every start would read as broken.
func TestQuotaPollerFetchesImmediatelyThenOnTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var got []tea.Msg
	sent := make(chan struct{}, 8)
	fetch := func(context.Context) (agent.Quota, error) { return testQuota(10, 20), nil }
	go runQuotaPoller(ctx, fetch, 20*time.Millisecond, func(m tea.Msg) {
		mu.Lock()
		got = append(got, m)
		mu.Unlock()
		sent <- struct{}{}
	})

	for i := range 2 {
		select {
		case <-sent:
		case <-time.After(time.Second):
			t.Fatalf("poll %d never arrived", i+1)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if q, ok := got[0].(quotaMsg); !ok || q.err != nil || q.quota.Session.Percent != 10 {
		t.Errorf("first message = %#v, want the fetched quota", got[0])
	}
}

func TestQuotaRowShowsGaugesAndResets(t *testing.T) {
	resetsAt := time.Now().Add(2*time.Hour + 10*time.Minute)
	_, row := quotaRowWith(t, 140, quotaMsg{quota: testQuotaReset(48, 6, resetsAt)})
	for _, want := range []string{
		"5h ▰▰▰▰▰▱▱▱▱▱ 48%",
		"wk ▰▱▱▱▱▱▱▱▱▱ 6%",
		"resets in",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("row missing %q:\n%q", want, row)
		}
	}
	if !strings.HasPrefix(row, "  5h") {
		t.Errorf("row should start with the session limit:\n%q", row)
	}
}

// The header used to carry the gauges at its far right; now it never
// shows a reading at all, wide or narrow.
func TestHeaderHasNoQuota(t *testing.T) {
	t.Setenv("TEND_GLYPHS", "")
	m, _ := newTestApp(t)
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	before := ansi.Strip(m.(app).headerLine())
	m = drive(t, m, quotaMsg{quota: testQuota(48, 6)})
	after := ansi.Strip(m.(app).headerLine())
	if before != after {
		t.Errorf("a quota reading changed the header:\n%q\n%q", before, after)
	}
	if strings.Contains(after, "%") {
		t.Errorf("header shows quota info:\n%q", after)
	}
}

// A failed poll keeps the last reading rather than blanking it, but marks
// it stale; the next good poll clears the mark. ErrNoQuota is different:
// that account has nothing to show, so the reading goes.
func TestApplyQuota(t *testing.T) {
	a, _ := quotaRowWith(t, 140, quotaMsg{quota: testQuota(70, 30)}, quotaMsg{err: errors.New("claude wedged")})
	if !a.quotaLoaded || !a.quotaStale || a.quota.Session.Percent != 70 {
		t.Errorf("after a failed poll: loaded=%v stale=%v quota=%+v, want the old reading kept and stale",
			a.quotaLoaded, a.quotaStale, a.quota.Session)
	}
	a = a.applyQuota(quotaMsg{quota: testQuota(71, 30)})
	if a.quotaStale || a.quota.Session.Percent != 71 {
		t.Errorf("after recovery: stale=%v pct=%d, want fresh 71", a.quotaStale, a.quota.Session.Percent)
	}
	a = a.applyQuota(quotaMsg{err: agent.ErrNoQuota})
	if a.quotaLoaded || a.quotaLine() != "" {
		t.Error("ErrNoQuota should clear the reading")
	}
	// A failure before any reading has nothing to mark stale.
	fresh, _ := quotaRowWith(t, 140, quotaMsg{err: errors.New("boom")})
	if fresh.quotaLoaded || fresh.quotaStale {
		t.Errorf("failure with no reading: loaded=%v stale=%v, want neither", fresh.quotaLoaded, fresh.quotaStale)
	}
}

func TestQuotaRowBlankWithoutReading(t *testing.T) {
	for name, msgs := range map[string][]quotaMsg{
		"no poll yet":                    nil,
		"no subscription limits":         {{err: agent.ErrNoQuota}},
		"failed with no earlier reading": {{err: errors.New("boom")}},
	} {
		t.Run(name, func(t *testing.T) {
			_, row := quotaRowWith(t, 140, msgs...)
			if row != "" {
				t.Errorf("row = %q, want empty", row)
			}
		})
	}
}

func TestQuotaStyleThresholds(t *testing.T) {
	s := DefaultStyles()
	for _, tc := range []struct {
		pct   int
		stale bool
		want  lipgloss.Style
		name  string
	}{
		{0, false, s.QuotaOK, "ok"},
		{59, false, s.QuotaOK, "ok"},
		{60, false, s.QuotaWarn, "warn"},
		{80, false, s.QuotaWarn, "warn"},
		{81, false, s.QuotaHot, "hot"},
		{100, false, s.QuotaHot, "hot"},
		{95, true, s.Faint, "faint"},
	} {
		got := quotaStyle(s, tc.pct, tc.stale)
		if got.GetForeground() != tc.want.GetForeground() {
			t.Errorf("pct %d stale %v: got %v, want %s", tc.pct, tc.stale, got.GetForeground(), tc.name)
		}
	}
}

// End caps go on the first and last cell whatever the fill, which is what
// lets a Nerd Font draw one continuous bar.
func TestQuotaGaugeComposition(t *testing.T) {
	s := DefaultStyles()
	s.Glyphs = nerdGlyphs()
	g := s.Glyphs
	mids := func(glyph string, n int) string { return strings.Repeat(glyph, n) }
	for _, tc := range []struct {
		pct  int
		want string
	}{
		{0, g.GaugeLeftOff + mids(g.GaugeMidOff, 8) + g.GaugeRightOff},
		{1, g.GaugeLeftOn + mids(g.GaugeMidOff, 8) + g.GaugeRightOff},
		{50, g.GaugeLeftOn + mids(g.GaugeMidOn, 4) + mids(g.GaugeMidOff, 4) + g.GaugeRightOff},
		{100, g.GaugeLeftOn + mids(g.GaugeMidOn, 8) + g.GaugeRightOn},
		{140, g.GaugeLeftOn + mids(g.GaugeMidOn, 8) + g.GaugeRightOn},
	} {
		if got := ansi.Strip(quotaGauge(s, s.QuotaOK, tc.pct, false)); got != tc.want {
			t.Errorf("pct %d: gauge = %q, want %q", tc.pct, got, tc.want)
		}
	}
}

// A narrow row sheds the bars first, then shortens the reset text, then
// drops it, and only truncates as a last resort; the percentages are
// never silently dropped short of that.
func TestQuotaRowDegradesWhenNarrow(t *testing.T) {
	resetsAt := time.Now().Add(2*time.Hour + 10*time.Minute)
	q := quotaMsg{quota: testQuotaReset(48, 6, resetsAt)}
	wide, _ := quotaRowWith(t, 200, q)
	now := time.Now()
	widthOf := func(detail quotaRowDetail) int {
		return lipgloss.Width("  " + wide.quotaLimits(detail, now))
	}
	fullW, noBarsW, shortW, bareW := widthOf(quotaRowFull), widthOf(quotaRowNoBars), widthOf(quotaRowShort), widthOf(quotaRowBare)

	for _, tc := range []struct {
		name            string
		width           int
		bars, resets    bool
		shortForm, bare bool
	}{
		{"full", fullW, true, true, false, false},
		{"one narrower", fullW - 1, false, true, false, false},
		{"below no-bars", noBarsW - 1, false, false, true, false},
		{"below short", shortW - 1, false, false, false, true},
	} {
		_, row := quotaRowWith(t, tc.width, q)
		if w := lipgloss.Width(row); w > tc.width {
			t.Errorf("%s: row is %d cells, wider than %d:\n%q", tc.name, w, tc.width, row)
		}
		if strings.Contains(row, "\n") {
			t.Errorf("%s: row has a newline:\n%q", tc.name, row)
		}
		if got := strings.Contains(row, "▰"); got != tc.bars {
			t.Errorf("%s: bars shown = %v, want %v:\n%q", tc.name, got, tc.bars, row)
		}
		if got := strings.Contains(row, "resets"); got != tc.resets {
			t.Errorf("%s: literal \"resets\" = %v, want %v:\n%q", tc.name, got, tc.resets, row)
		}
		if tc.shortForm && !strings.Contains(row, "(in ") {
			t.Errorf("%s: want the short \"(in ...)\" form:\n%q", tc.name, row)
		}
		if tc.bare {
			if !strings.Contains(row, "5h 48%") || !strings.Contains(row, "wk 6%") {
				t.Errorf("%s: want bare percentages kept:\n%q", tc.name, row)
			}
			if strings.Contains(row, "(in ") {
				t.Errorf("%s: reset text should be gone at the bare level:\n%q", tc.name, row)
			}
		}
	}

	// Narrower than even the bare form still keeps the row to one line,
	// truncating rather than dropping the percentages silently -- though
	// at 8 cells there isn't room for one anyway.
	_, tiny := quotaRowWith(t, 8, q)
	if w := lipgloss.Width(tiny); w > 8 {
		t.Errorf("truncated row is %d cells, wider than 8:\n%q", w, tiny)
	}
	if strings.Contains(tiny, "\n") {
		t.Errorf("truncated row has a newline:\n%q", tiny)
	}
	if bareW > 8 && !strings.Contains(tiny, "…") {
		t.Errorf("row narrower than the bare form should be truncated with an ellipsis:\n%q", tiny)
	}
}

func TestQuotaResetText(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		l    agent.QuotaLimit
		want string
	}{
		{"30s away", agent.QuotaLimit{ResetsAt: now.Add(30 * time.Second)}, "in 1m"},
		{"42m away", agent.QuotaLimit{ResetsAt: now.Add(42 * time.Minute)}, "in 42m"},
		{"5h10m away", agent.QuotaLimit{ResetsAt: now.Add(5*time.Hour + 10*time.Minute)}, "in 5h 10m"},
		{"1m in the past", agent.QuotaLimit{ResetsAt: now.Add(-time.Minute)}, "now"},
		{"unparsed with zone", agent.QuotaLimit{Resets: "Sep 17, 11pm (America/New_York)"}, "Sep 17, 11pm"},
		{"unparsed short form", agent.QuotaLimit{Resets: "Fri"}, "Fri"},
		{"nothing at all", agent.QuotaLimit{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaResetText(&tc.l, now); got != tc.want {
				t.Errorf("quotaResetText = %q, want %q", got, tc.want)
			}
		})
	}

	// 50 hours out, on the hour, falls back to a weekday clock rather
	// than a relative count.
	l := agent.QuotaLimit{ResetsAt: now.Add(50 * time.Hour)}
	want := l.ResetsAt.In(time.Local).Format("Mon 3pm")
	if got := quotaResetText(&l, now); got != want {
		t.Errorf("quotaResetText (50h out) = %q, want %q", got, want)
	}
}

func TestGlyphsFor(t *testing.T) {
	if glyphsFor("nerd").GaugeMidOn != "\uee04" {
		t.Error(`glyphsFor("nerd") is not the Nerd Font set`)
	}
	if glyphsFor("ascii").GaugeMidOn != "#" {
		t.Error(`glyphsFor("ascii") is not the ASCII set`)
	}
	for _, name := range []string{"", "unicode", "bogus"} {
		if glyphsFor(name).GaugeMidOn != "▰" {
			t.Errorf("glyphsFor(%q) is not the unicode set", name)
		}
	}
}

// Every gauge glyph is one cell in every set: a double-width glyph would
// push the whole row one cell right.
func TestQuotaGlyphWidths(t *testing.T) {
	for name, g := range map[string]glyphs{"unicode": unicodeGlyphs(), "ascii": asciiGlyphs(), "nerd": nerdGlyphs()} {
		for _, glyph := range []string{g.GaugeLeftOn, g.GaugeMidOn, g.GaugeRightOn, g.GaugeLeftOff, g.GaugeMidOff, g.GaugeRightOff} {
			if w := ansi.StringWidth(glyph); w != 1 {
				t.Errorf("%s: gauge glyph %q has width %d, want 1", name, glyph, w)
			}
		}
	}
}

// The quota row's slot is always reserved, blank or not, so the rule and
// panes below it never shift up or down with a poll.
func TestFrameReservesQuotaRow(t *testing.T) {
	t.Setenv("TEND_GLYPHS", "")
	for _, tc := range []struct {
		name  string
		width int
	}{
		{"wide", 100},
		{"narrow", 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTestApp(t)
			m = drive(t, m, tea.WindowSizeMsg{Width: tc.width, Height: 30})
			m = drive(t, m, quotaMsg{quota: testQuota(48, 6)})
			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			if len(lines) != 30 {
				t.Fatalf("frame is %d lines, want 30", len(lines))
			}
			if !strings.Contains(lines[1], "5h") {
				t.Errorf("line 1 should be the quota row:\n%q", lines[1])
			}
			if !strings.Contains(lines[2], "─") && !strings.Contains(lines[2], "┬") {
				t.Errorf("line 2 should be the rule:\n%q", lines[2])
			}
		})
	}

	t.Run("no reading", func(t *testing.T) {
		m, _ := newTestApp(t) // 100x30
		lines := strings.Split(ansi.Strip(m.View().Content), "\n")
		if len(lines) != 30 {
			t.Fatalf("frame is %d lines, want 30", len(lines))
		}
		if strings.TrimSpace(lines[1]) != "" {
			t.Errorf("line 1 should be blank with no reading:\n%q", lines[1])
		}
	})

	t.Run("loading frame", func(t *testing.T) {
		m, _ := newTestApp(t)
		m = drive(t, m, quotaMsg{quota: testQuota(48, 6)})
		a := m.(app)
		a.loaded = false
		lines := strings.Split(ansi.Strip(a.View().Content), "\n")
		if len(lines) != a.height {
			t.Fatalf("loading frame is %d lines, want %d", len(lines), a.height)
		}
		if !strings.Contains(lines[1], "5h") {
			t.Errorf("loading frame line 1 should be the quota row:\n%q", lines[1])
		}
	})
}
