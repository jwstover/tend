package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/agent"
)

// quotaPollInterval is how often the header's quota segment re-asks
// claude. `/usage` is local and free, but it is still a claude process
// start, and the limits it reports move over hours, not seconds.
const quotaPollInterval = time.Minute

// Usage thresholds, in percent of a limit: at quotaWarnAt a gauge turns
// amber, past quotaHotAbove red.
const (
	quotaWarnAt   = 60
	quotaHotAbove = 80
)

// quotaGaugeCells is how many cells wide one gauge's bar is.
const quotaGaugeCells = 10

// quotaMsg carries one `/usage` answer to the event loop.
type quotaMsg struct {
	quota agent.Quota
	err   error
}

// runQuotaPoller asks fetch once straight away, so the quota segment shows
// up with the first frame rather than a minute in, then again on every
// tick until ctx is canceled. Like runSessionPoller it is a goroutine
// rather than a tea.Cmd so the segment is current, not a minute stale,
// when an attached session hands the terminal back.
func runQuotaPoller(ctx context.Context, fetch func(context.Context) (agent.Quota, error), interval time.Duration, send func(tea.Msg)) {
	poll := func() {
		q, err := fetch(ctx)
		if ctx.Err() != nil {
			return
		}
		send(quotaMsg{quota: q, err: err})
	}
	poll()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}

// startQuotaPoller runs runQuotaPoller against the real claude, unless
// claude is not installed -- then there is nothing to ask and the header
// simply carries no quota segment.
func startQuotaPoller(ctx context.Context, send func(tea.Msg)) {
	if agent.CheckInstalled() != nil {
		return
	}
	go runQuotaPoller(ctx, agent.FetchQuota, quotaPollInterval, send)
}

// applyQuota folds one poll into the app. A failed call keeps the last
// good reading, marked stale, so one hiccup does not blank the quota
// segment; an account with no subscription limits (ErrNoQuota) shows
// nothing at all.
func (a app) applyQuota(msg quotaMsg) app {
	switch {
	case msg.err == nil:
		a.quota, a.quotaLoaded, a.quotaStale = msg.quota, true, false
	case errors.Is(msg.err, agent.ErrNoQuota):
		a.quota, a.quotaLoaded, a.quotaStale = agent.Quota{}, false, false
	default:
		a.quotaStale = a.quotaLoaded
	}
	return a
}

// quotaDetail is how much of each limit the header's quota segment spells
// out; the segment steps down through these until it fits.
type quotaDetail int

const (
	quotaFull   quotaDetail = iota // 5h ▰▰▰▰▰▱▱▱▱▱ 48%  in 2h 10m
	quotaNoBars                    // 5h 48%  in 2h 10m
	quotaShort                     // 5h 48% (in 2h 10m)
	quotaBare                      // 5h 48%
)

// quotaLimits renders the limits at the given detail level, joined with
// HeaderSep, e.g. `5h ▰▰▰▰▰▱▱▱▱▱ 48%  in 2h 10m  ·  wk ▰▱▱▱▱▱▱▱▱▱ 6%  in
// 3d`. Empty when there is no reading to show.
func (a app) quotaLimits(detail quotaDetail, now time.Time) string {
	if !a.quotaLoaded {
		return ""
	}
	var parts []string
	for _, l := range []struct {
		label string
		limit *agent.QuotaLimit
	}{
		{"5h", a.quota.Session},
		{"wk", a.quota.Week},
	} {
		if l.limit == nil {
			continue
		}
		style := quotaStyle(a.styles, l.limit.Percent, a.quotaStale)
		part := style.Render(l.label) + " "
		if detail == quotaFull {
			part += quotaGauge(a.styles, style, l.limit.Percent, a.quotaStale) + " "
		}
		part += style.Render(fmt.Sprintf("%d%%", l.limit.Percent))

		if r := quotaResetText(l.limit, now); r != "" {
			mutedStyle := a.styles.Muted
			if a.quotaStale {
				mutedStyle = a.styles.Faint
			}
			switch detail {
			case quotaFull, quotaNoBars:
				part += mutedStyle.Render("  " + r)
			case quotaShort:
				part += mutedStyle.Render(" (" + r + ")")
			}
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, a.styles.HeaderSep.Render("  ·  "))
}

// quotaResetText says when a limit resets: relative while it is close, a
// clock once it is a day or more out. Falls back to claude's own text,
// minus its "(Zone/Name)" suffix, when the clause did not parse.
func quotaResetText(l *agent.QuotaLimit, now time.Time) string {
	if l.ResetsAt.IsZero() {
		text := l.Resets
		if i := strings.LastIndex(text, "("); i >= 0 {
			text = text[:i]
		}
		return strings.TrimSpace(text)
	}
	d := l.ResetsAt.Sub(now)
	switch {
	case d <= 0:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int((d+time.Minute-1)/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("in %dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		t := l.ResetsAt.In(time.Local)
		layout := "Mon 3pm"
		if t.Minute() != 0 {
			layout = "Mon 3:04pm"
		}
		return t.Format(layout)
	}
}

// quotaSegment is the header's centered quota text at the most detailed
// level that fits avail cells, "" when there is nothing loaded or nothing
// fits even at the barest level -- the caller then falls back to showing
// no segment at all rather than one that collides with the rest of the
// header.
func (a app) quotaSegment(avail int, now time.Time) string {
	if !a.quotaLoaded {
		return ""
	}
	for _, detail := range []quotaDetail{quotaFull, quotaNoBars, quotaShort, quotaBare} {
		if q := a.quotaLimits(detail, now); lipgloss.Width(q) <= avail {
			return q
		}
	}
	return ""
}

// quotaStyle colors a gauge by how much of its limit is spent. A stale
// reading goes faint whatever it says, so an old red never reads as live.
func quotaStyle(s Styles, pct int, stale bool) lipgloss.Style {
	switch {
	case stale:
		return s.Faint
	case pct > quotaHotAbove:
		return s.QuotaHot
	case pct >= quotaWarnAt:
		return s.QuotaWarn
	default:
		return s.QuotaOK
	}
}

// quotaGauge draws the bar: pct of quotaGaugeCells filled in style, the
// rest faint, with the set's end-cap glyphs on the first and last cell.
// Any usage at all fills at least one cell, so a gauge never reads as
// untouched when it is not.
func quotaGauge(s Styles, style lipgloss.Style, pct int, stale bool) string {
	g := s.Glyphs
	filled := min(max((pct*quotaGaugeCells+50)/100, 0), quotaGaugeCells)
	if pct > 0 && filled == 0 {
		filled = 1
	}
	rest := s.Faint
	if stale {
		style = s.Faint
	}
	var on, off strings.Builder
	for i := range quotaGaugeCells {
		var glyphOn, glyphOff string
		switch i {
		case 0:
			glyphOn, glyphOff = g.GaugeLeftOn, g.GaugeLeftOff
		case quotaGaugeCells - 1:
			glyphOn, glyphOff = g.GaugeRightOn, g.GaugeRightOff
		default:
			glyphOn, glyphOff = g.GaugeMidOn, g.GaugeMidOff
		}
		if i < filled {
			on.WriteString(glyphOn)
		} else {
			off.WriteString(glyphOff)
		}
	}
	return style.Render(on.String()) + rest.Render(off.String())
}
