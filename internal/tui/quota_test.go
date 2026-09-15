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

// headerWith drives a quota reading into a fresh app at the given width
// and returns its header with the styling stripped.
func headerWith(t *testing.T, width int, msgs ...quotaMsg) (app, string) {
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
	return a, ansi.Strip(a.headerLine())
}

// The first poll has to land straight away, not an interval in: a minute
// of empty header on every start would read as broken.
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

func TestHeaderShowsQuotaSegment(t *testing.T) {
	resetsAt := time.Now().Add(2*time.Hour + 10*time.Minute)
	_, h := headerWith(t, 140, quotaMsg{quota: testQuotaReset(48, 6, resetsAt)})
	for _, want := range []string{
		"5h ▰▰▰▰▰▱▱▱▱▱ 48%",
		"wk ▰▱▱▱▱▱▱▱▱▱ 6%",
		"in 2h",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("header missing %q:\n%q", want, h)
		}
	}
	// The word "resets" is dropped from the label; only the relative or
	// clock time remains.
	if strings.Contains(h, "resets") {
		t.Errorf("header should not say \"resets\":\n%q", h)
	}
}

// The quota segment sits in the middle of the header, not tucked against
// either edge: the mode's left-hand label and its own right-hand text
// (the shown count here) both still appear, on either side of it.
func TestHeaderQuotaIsCentered(t *testing.T) {
	_, h := headerWith(t, 140, quotaMsg{quota: testQuota(48, 6)})
	left, quota, shown := strings.Index(h, "tend"), strings.Index(h, "48%"), strings.Index(h, "shown")
	if left < 0 || quota < 0 || shown < 0 {
		t.Fatalf("header missing a landmark:\n%q", h)
	}
	if left >= quota || quota >= shown {
		t.Errorf("quota should sit between the left and right segments:\n%q", h)
	}
	// Roughly centered: closer to the middle of the line than to either edge.
	mid := len(h) / 2
	if d := quota - mid; d < -20 || d > 20 {
		t.Errorf("quota at column %d is not near the header's midpoint %d:\n%q", quota, mid, h)
	}
}

// Before any reading, and for an account with no subscription limits,
// the header is exactly what it was before the quota segment existed.
func TestHeaderWithoutQuotaIsUnchanged(t *testing.T) {
	_, before := headerWith(t, 140)
	_, noQuota := headerWith(t, 140, quotaMsg{err: agent.ErrNoQuota})
	if before != noQuota {
		t.Errorf("ErrNoQuota changed the header:\n%q\n%q", before, noQuota)
	}
	if strings.Contains(before, "%") {
		t.Errorf("header shows quota info with no reading:\n%q", before)
	}
}

// A failed poll keeps the last reading rather than blanking it, but marks
// it stale; the next good poll clears the mark. ErrNoQuota is different:
// that account has nothing to show, so the reading goes.
func TestApplyQuota(t *testing.T) {
	a, _ := headerWith(t, 140, quotaMsg{quota: testQuota(70, 30)}, quotaMsg{err: errors.New("claude wedged")})
	if !a.quotaLoaded || !a.quotaStale || a.quota.Session.Percent != 70 {
		t.Errorf("after a failed poll: loaded=%v stale=%v quota=%+v, want the old reading kept and stale",
			a.quotaLoaded, a.quotaStale, a.quota.Session)
	}
	a = a.applyQuota(quotaMsg{quota: testQuota(71, 30)})
	if a.quotaStale || a.quota.Session.Percent != 71 {
		t.Errorf("after recovery: stale=%v pct=%d, want fresh 71", a.quotaStale, a.quota.Session.Percent)
	}
	a = a.applyQuota(quotaMsg{err: agent.ErrNoQuota})
	if a.quotaLoaded || strings.Contains(ansi.Strip(a.headerLine()), "%") {
		t.Error("ErrNoQuota should clear the reading")
	}
	// A failure before any reading has nothing to mark stale.
	fresh, _ := headerWith(t, 140, quotaMsg{err: errors.New("boom")})
	if fresh.quotaLoaded || fresh.quotaStale {
		t.Errorf("failure with no reading: loaded=%v stale=%v, want neither", fresh.quotaLoaded, fresh.quotaStale)
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

// As the header narrows, once the bars, the reset time, or the
// percentages disappear they stay gone -- nothing flickers back in a
// cell narrower -- the header is never wider than its terminal, and the
// left side is never pushed off.
func TestHeaderQuotaDegradesMonotonically(t *testing.T) {
	resetsAt := time.Now().Add(2*time.Hour + 10*time.Minute)
	q := quotaMsg{quota: testQuotaReset(48, 6, resetsAt)}
	sawBars, sawReset, sawPct := true, true, true
	for width := 140; width >= 15; width-- {
		_, h := headerWith(t, width, q)
		if w := lipgloss.Width(h); w > width {
			t.Fatalf("width %d: header is %d cells wide:\n%q", width, w, h)
		}
		if !strings.HasPrefix(h, "  tend") {
			t.Fatalf("width %d: left side lost:\n%q", width, h)
		}
		bars := strings.Contains(h, "▰")
		reset := strings.Contains(h, "in 2h") || strings.Contains(h, "(in ")
		pct := strings.Contains(h, "48%")
		if bars && !sawBars {
			t.Errorf("width %d: bars reappeared after disappearing at a wider width:\n%q", width, h)
		}
		if reset && !sawReset {
			t.Errorf("width %d: reset text reappeared after disappearing at a wider width:\n%q", width, h)
		}
		if pct && !sawPct {
			t.Errorf("width %d: percentages reappeared after disappearing at a wider width:\n%q", width, h)
		}
		sawBars, sawReset, sawPct = bars, reset, pct
	}
}

// The quota segment gives up its own detail -- bars, then the reset time
// -- before the mode's own right-hand text is dropped, so there is a
// width where "shown" is gone but the percentages are still there.
func TestHeaderQuotaOutlivesModeText(t *testing.T) {
	q := quotaMsg{quota: testQuota(48, 6)}
	for width := 140; width >= 15; width-- {
		_, h := headerWith(t, width, q)
		if strings.Contains(h, "48%") && !strings.Contains(h, "shown") {
			return
		}
	}
	t.Error("no width found where the quota segment survives after the mode's own right-hand text is dropped")
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
// push the whole header one cell right.
func TestQuotaGlyphWidths(t *testing.T) {
	for name, g := range map[string]glyphs{"unicode": unicodeGlyphs(), "ascii": asciiGlyphs(), "nerd": nerdGlyphs()} {
		for _, glyph := range []string{g.GaugeLeftOn, g.GaugeMidOn, g.GaugeRightOn, g.GaugeLeftOff, g.GaugeMidOff, g.GaugeRightOff} {
			if w := ansi.StringWidth(glyph); w != 1 {
				t.Errorf("%s: gauge glyph %q has width %d, want 1", name, glyph, w)
			}
		}
	}
}
