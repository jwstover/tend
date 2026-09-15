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

func TestHeaderShowsQuotaGauges(t *testing.T) {
	_, h := headerWith(t, 140, quotaMsg{quota: testQuota(48, 6)})
	g := unicodeGlyphs()
	for _, want := range []string{
		g.QuotaSession + " ▰▰▰▰▱▱▱▱ 48%",
		g.QuotaWeek + " ▰▱▱▱▱▱▱▱ 6%",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("header missing %q:\n%q", want, h)
		}
	}
	// Gauges come after the mode's own right-hand text, at the far right.
	if i, j := strings.Index(h, "shown"), strings.Index(h, "48%"); i < 0 || j < i {
		t.Errorf("gauges should follow the shown count:\n%q", h)
	}
	if !strings.HasSuffix(h, "6%  ") {
		t.Errorf("gauges should end the line:\n%q", h)
	}
}

// Before any reading, and for an account with no subscription limits,
// the header is exactly what it was before gauges existed.
func TestHeaderWithoutQuotaIsUnchanged(t *testing.T) {
	_, before := headerWith(t, 140)
	_, noQuota := headerWith(t, 140, quotaMsg{err: agent.ErrNoQuota})
	if before != noQuota {
		t.Errorf("ErrNoQuota changed the header:\n%q\n%q", before, noQuota)
	}
	if strings.Contains(before, "%") {
		t.Errorf("header shows gauges with no reading:\n%q", before)
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
	if a.quotaLoaded || a.quotaChrome(true) != "" {
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
		{0, g.GaugeLeftOff + mids(g.GaugeMidOff, 6) + g.GaugeRightOff},
		{1, g.GaugeLeftOn + mids(g.GaugeMidOff, 6) + g.GaugeRightOff},
		{50, g.GaugeLeftOn + mids(g.GaugeMidOn, 3) + mids(g.GaugeMidOff, 3) + g.GaugeRightOff},
		{100, g.GaugeLeftOn + mids(g.GaugeMidOn, 6) + g.GaugeRightOn},
		{140, g.GaugeLeftOn + mids(g.GaugeMidOn, 6) + g.GaugeRightOn},
	} {
		if got := ansi.Strip(quotaGauge(s, s.QuotaOK, tc.pct, false)); got != tc.want {
			t.Errorf("pct %d: gauge = %q, want %q", tc.pct, got, tc.want)
		}
	}
}

// A narrow header sheds the bars before the mode's text, and the mode's
// text before the percentages; the left side is never pushed off.
func TestHeaderQuotaDegradesWhenNarrow(t *testing.T) {
	q := quotaMsg{quota: testQuota(48, 6)}
	_, wide := headerWith(t, 140, q)
	const left = "  tend  ·  live"
	fullRight := strings.TrimLeft(strings.TrimPrefix(wide, left), " ")
	// At exactly left+right there is no gap left, so that layout no longer fits.
	for _, tc := range []struct {
		name                string
		width               int
		bars, shown, gauges bool
	}{
		{"wide", 140, true, true, true},
		{"no bars", lipgloss.Width(left) + lipgloss.Width(fullRight), false, true, true},
		{"no shown count", lipgloss.Width(left) + lipgloss.Width("0 shown  ·  5h 48%  ·  wk 6%  "), false, false, true},
		{"nothing fits", lipgloss.Width(left) + 5, false, false, false},
	} {
		_, h := headerWith(t, tc.width, q)
		if w := lipgloss.Width(h); w > tc.width {
			t.Errorf("%s: header is %d cells, wider than %d", tc.name, w, tc.width)
		}
		if got := strings.Contains(h, "▰"); got != tc.bars {
			t.Errorf("%s: bars shown = %v, want %v:\n%q", tc.name, got, tc.bars, h)
		}
		if got := strings.Contains(h, "shown"); got != tc.shown {
			t.Errorf("%s: shown count = %v, want %v:\n%q", tc.name, got, tc.shown, h)
		}
		if got := strings.Contains(h, "48%"); got != tc.gauges {
			t.Errorf("%s: gauges = %v, want %v:\n%q", tc.name, got, tc.gauges, h)
		}
		if !strings.HasPrefix(h, "  tend") {
			t.Errorf("%s: left side lost:\n%q", tc.name, h)
		}
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

// Every gauge glyph is one cell and every label at most two, in every
// set: a double-width icon would push the whole header one cell right.
func TestQuotaGlyphWidths(t *testing.T) {
	for name, g := range map[string]glyphs{"unicode": unicodeGlyphs(), "ascii": asciiGlyphs(), "nerd": nerdGlyphs()} {
		for _, glyph := range []string{g.GaugeLeftOn, g.GaugeMidOn, g.GaugeRightOn, g.GaugeLeftOff, g.GaugeMidOff, g.GaugeRightOff} {
			if w := ansi.StringWidth(glyph); w != 1 {
				t.Errorf("%s: gauge glyph %q has width %d, want 1", name, glyph, w)
			}
		}
		for _, glyph := range []string{g.QuotaSession, g.QuotaWeek} {
			if w := ansi.StringWidth(glyph); w < 1 || w > 2 {
				t.Errorf("%s: label %q has width %d, want 1 or 2", name, glyph, w)
			}
		}
	}
}
