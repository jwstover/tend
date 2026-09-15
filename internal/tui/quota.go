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

// quotaPollInterval is how often the header's usage gauges re-ask claude.
// `/usage` is local and free, but it is still a claude process start, and
// the limits it reports move over hours, not seconds.
const quotaPollInterval = time.Minute

// Usage thresholds, in percent of a limit: at quotaWarnAt a gauge turns
// amber, past quotaHotAbove red.
const (
	quotaWarnAt   = 60
	quotaHotAbove = 80
)

// quotaGaugeCells is how many cells wide one gauge's bar is.
const quotaGaugeCells = 8

// quotaMsg carries one `/usage` answer to the event loop.
type quotaMsg struct {
	quota agent.Quota
	err   error
}

// runQuotaPoller asks fetch once straight away, so the gauges show up with
// the first frame rather than a minute in, then again on every tick until
// ctx is canceled. Like runSessionPoller it is a goroutine rather than a
// tea.Cmd so the gauges are current, not a minute stale, when an attached
// session hands the terminal back.
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
// simply carries no gauges.
func startQuotaPoller(ctx context.Context, send func(tea.Msg)) {
	if agent.CheckInstalled() != nil {
		return
	}
	go runQuotaPoller(ctx, agent.FetchQuota, quotaPollInterval, send)
}

// applyQuota folds one poll into the app. A failed call keeps the last
// good reading, marked stale, so one hiccup does not blank the header; an
// account with no subscription limits (ErrNoQuota) shows nothing at all.
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

// quotaChrome renders the header's usage gauges, `5h ▰▰▰▰▱▱▱▱ 48%  ·  wk
// ▰▱▱▱▱▱▱▱ 6%`, or without the bars when bars is false. Empty when there
// is no reading to show.
func (a app) quotaChrome(bars bool) string {
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
		if bars {
			part += quotaGauge(a.styles, style, l.limit.Percent, a.quotaStale) + " "
		}
		parts = append(parts, part+style.Render(fmt.Sprintf("%d%%", l.limit.Percent)))
	}
	return strings.Join(parts, a.styles.HeaderSep.Render("  ·  "))
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
