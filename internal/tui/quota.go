package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/agent"
)

// quotaPollInterval is how often the quota row re-asks claude. `/usage`
// is local and free, but it is still a claude process start, and the
// limits it reports move over hours, not seconds.
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

// runQuotaPoller asks fetch once straight away, so the quota row shows up
// with the first frame rather than a minute in, then again on every tick
// until ctx is canceled. Like runSessionPoller it is a goroutine rather
// than a tea.Cmd so the row is current, not a minute stale, when an
// attached session hands the terminal back.
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
// claude is not installed -- then there is nothing to ask and the quota
// row simply stays empty.
func startQuotaPoller(ctx context.Context, send func(tea.Msg)) {
	if agent.CheckInstalled() != nil {
		return
	}
	go runQuotaPoller(ctx, agent.FetchQuota, quotaPollInterval, send)
}

// applyQuota folds one poll into the app. A failed call keeps the last
// good reading, marked stale, so one hiccup does not blank the quota
// row; an account with no subscription limits (ErrNoQuota) shows nothing
// at all.
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

// quotaRowDetail is how much of each limit the quota row spells out; the
// row steps down through these until it fits the terminal.
type quotaRowDetail int

const (
	quotaRowFull   quotaRowDetail = iota // 5h ▰▰▰▰▰▱▱▱▱▱ 48%  resets in 2h 10m
	quotaRowNoBars                       // 5h 48%  resets in 2h 10m
	quotaRowShort                        // 5h 48% (in 2h 10m)
	quotaRowBare                         // 5h 48%
)

// quotaLimits renders the row's limits at the given detail level, joined
// with HeaderSep, e.g. `5h ▰▰▰▰▰▱▱▱▱▱ 48%  resets in 2h 10m  ·  wk
// ▰▱▱▱▱▱▱▱▱▱ 6%  resets in 3d`. Empty when there is no reading to show.
func (a app) quotaLimits(detail quotaRowDetail, now time.Time) string {
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
		if detail == quotaRowFull {
			part += quotaGauge(a.styles, style, l.limit.Percent, a.quotaStale) + " "
		}
		part += style.Render(fmt.Sprintf("%d%%", l.limit.Percent))

		if r := quotaResetText(l.limit, now); r != "" {
			mutedStyle := a.styles.Muted
			if a.quotaStale {
				mutedStyle = a.styles.Faint
			}
			switch detail {
			case quotaRowFull, quotaRowNoBars:
				part += mutedStyle.Render("  resets " + r)
			case quotaRowShort:
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

// quotaLine is the quota row: the limits at the most detailed level that
// fits a.width, "" if there is nothing loaded to show, and truncated as a
// last resort so the row is always exactly one line.
func (a app) quotaLine() string {
	if !a.quotaLoaded {
		return ""
	}
	now := time.Now()
	if a.width <= 0 {
		return "  " + a.quotaLimits(quotaRowFull, now)
	}
	var bare string
	for _, detail := range []quotaRowDetail{quotaRowFull, quotaRowNoBars, quotaRowShort, quotaRowBare} {
		row := "  " + a.quotaLimits(detail, now)
		if detail == quotaRowBare {
			bare = row
		}
		if lipgloss.Width(row) <= a.width {
			return row
		}
	}
	return ansi.Truncate(bare, a.width, "…")
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
