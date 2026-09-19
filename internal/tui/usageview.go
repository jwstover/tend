package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/agent"
	"github.com/jwstover/tend/internal/usage"
)

// usageColW is the width of each numeric column in the usage tables.
const usageColW = 9

// usageView is the state of modeUsage. The data itself is the poller's,
// held on app (usage.go); only the scroll offset lives here.
type usageView struct{ scroll int }

// startUsage switches into the view. There is nothing to load: the
// poller already holds the data.
func (a *app) startUsage() {
	a.mode = modeUsage
	a.focus = paneTasks
	a.deletePending = false
	a.uv.scroll = 0
	a.resize()
}

// leaveUsage returns to the list view.
func (a *app) leaveUsage() tea.Cmd {
	a.mode = modeList
	a.focus = paneTasks
	a.deletePending = false
	a.resize()
	return a.loadTasks(modeList)
}

func (a app) usageMaxScroll() int {
	return max(len(a.usageLines(max(a.width, 20)))-max(a.bodyHeight, 1), 0)
}

func (a app) handleUsageKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	maxScroll := a.usageMaxScroll()
	scroll := func(to int) (tea.Model, tea.Cmd) {
		a.uv.scroll = max(min(to, maxScroll), 0)
		return a, nil
	}
	switch {
	// ctrl+c always quits; `q` closes the view like esc.
	case key.Matches(msg, a.keys.Quit) && msg.String() != "q":
		return a, tea.Quit
	case key.Matches(msg, a.keys.Back), key.Matches(msg, a.keys.Quit), key.Matches(msg, a.keys.Usage):
		return a, a.leaveUsage()
	case key.Matches(msg, a.keys.Help):
		a.helpOpen = true
		return a, nil
	case key.Matches(msg, a.keys.Palette):
		a.openPalette()
		return a, nil
	case key.Matches(msg, a.keys.Note):
		return a, a.modal.Open(modalLog, true, "note", 0, "")
	case key.Matches(msg, a.keys.ScrollDown):
		return scroll(a.uv.scroll + 1)
	case key.Matches(msg, a.keys.ScrollUp):
		return scroll(a.uv.scroll - 1)
	case key.Matches(msg, a.keys.PageDown):
		return scroll(a.uv.scroll + a.bodyHeight)
	case key.Matches(msg, a.keys.PageUp):
		return scroll(a.uv.scroll - a.bodyHeight)
	case msg.String() == "g":
		return scroll(0)
	case msg.String() == "G":
		return scroll(maxScroll)
	}
	return a, nil
}

func (a app) usageBody() string {
	h, w := max(a.bodyHeight, 1), max(a.width, 20)
	lines := a.usageLines(w)
	scroll := max(min(a.uv.scroll, max(len(lines)-h, 0)), 0)
	return strings.Join(fitPane(lines, w, h, scroll), "\n")
}

// usageLines lays the whole view out. Everything is precomputed by the
// poller; this only formats.
func (a app) usageLines(width int) []string {
	s := a.styles
	now := time.Now()
	lines := []string{"", "  " + s.SubHeader.Render("QUOTA")}

	if !a.quotaLoaded {
		lines = append(lines, "    "+s.Muted.Render("no subscription limits reported (claude not installed, or an API-key login)"))
	} else {
		type row struct {
			label string
			l     *agent.QuotaLimit
		}
		var rows []row
		for _, r := range []row{{"5h", a.quota.Session}, {"wk", a.quota.Week}} {
			if r.l != nil {
				rows = append(rows, r)
			}
		}
		for i := range a.quota.Extra {
			rows = append(rows, row{a.quota.Extra[i].Label, &a.quota.Extra[i]})
		}
		labelW := 0
		for _, r := range rows {
			labelW = max(labelW, runeWidth(r.label))
		}
		for _, r := range rows {
			pct := r.l.Percent
			style := quotaStyle(s, pct, a.quotaStale)
			line := "    " + style.Render(padRight(r.label, labelW)) + "  " +
				quotaGauge(s, style, pct, a.quotaStale) + " " + style.Render(fmt.Sprintf("%3d%%", pct)) +
				s.Muted.Render("  resets "+quotaResetText(r.l, now))
			// Under a day away the relative text alone does not say when.
			if !r.l.ResetsAt.IsZero() && r.l.ResetsAt.Sub(now) < 24*time.Hour {
				line += s.Faint.Render(" (" + r.l.ResetsAt.In(time.Local).Format("Mon 3:04pm") + ")")
			}
			if a.quotaStale {
				line += s.Faint.Render("  stale")
			}
			lines = append(lines, line)
		}
	}

	lines = append(lines, "")
	if !a.usageLoaded {
		return append(lines, "  "+s.Muted.Render("scanning ~/.claude/projects…"))
	}

	sum := a.usage
	lines = append(lines, a.usageTableRow("TOKENS", [3]string{"5h", "7d", "all time"}, width, s.SubHeader, s.Faint))
	ratio := func(t usage.Tokens) string {
		if t.InputTotal() == 0 {
			return "–"
		}
		return fmt.Sprintf("%d%%", int(t.CacheHitRatio()*100+0.5))
	}
	for _, r := range []struct {
		label string
		cells [3]string
	}{
		{"total", [3]string{fmtTokens(sum.Last5h.Total()), fmtTokens(sum.Last7d.Total()), fmtTokens(sum.AllTime.Total())}},
		{"cache hit", [3]string{ratio(sum.Last5h), ratio(sum.Last7d), ratio(sum.AllTime)}},
		{"output", [3]string{fmtTokens(sum.Last5h.Output), fmtTokens(sum.Last7d.Output), fmtTokens(sum.AllTime.Output)}},
	} {
		lines = append(lines, a.usageTableRow(r.label, r.cells, width, s.Muted, s.CountNum))
	}
	busy := fmt.Sprintf("%d messages across %d sessions in the last 5h", sum.Messages, sum.Sessions)
	if !sum.FirstInWindow.IsZero() {
		busy += " · since " + sum.FirstInWindow.In(time.Local).Format("3:04pm")
	}
	lines = append(lines, "  "+s.Muted.Render(busy))

	labelW := a.usageLabelWidth(width)
	section := func(title string, rows []usage.BreakdownRow, label func(string) string) {
		lines = append(lines, "", a.usageTableRow(title, [3]string{"5h", "7d", "all time"}, width, s.SubHeader, s.Faint))
		if len(rows) == 0 {
			lines = append(lines, "  "+s.Muted.Render("none"))
			return
		}
		for _, r := range rows {
			var cells [3]string
			for i, t := range []usage.Tokens{r.Last5h, r.Last7d, r.AllTime} {
				if t.IsZero() {
					cells[i] = "–"
				} else {
					cells[i] = fmtTokens(t.Total())
				}
			}
			lines = append(lines, a.usageTableRow(label(r.Key), cells, width, s.Muted, s.CountNum))
		}
	}
	b := a.usageBreakdowns
	ell := s.Glyphs.Ellipsis
	section("BY PROJECT", b.byProject, func(k string) string {
		if k == "(unknown)" {
			return k
		}
		return truncHead(tildePath(k), labelW, ell)
	})
	section("BY MODEL", b.byModel, func(k string) string { return truncTail(k, labelW, ell) })
	section("MAIN VS SUB-AGENT", b.byAgent, func(k string) string { return truncTail(k, labelW, ell) })

	if a.usageSkipped > 0 {
		lines = append(lines, "", "  "+s.Muted.Render(fmt.Sprintf("%d transcript file(s) could not be read", a.usageSkipped)))
	}
	return lines
}

// usageLabelWidth is the label column's width at a total width.
func (a app) usageLabelWidth(width int) int {
	return max(width-4-3*usageColW-2*2, 8)
}

// usageTableRow lays out one label plus three right-aligned columns. The
// caller has already truncated label to the label column. The dash for
// an empty cell is dimmed; cellStyle colors the rest.
func (a app) usageTableRow(label string, cells [3]string, width int, labelStyle, cellStyle lipgloss.Style) string {
	var b strings.Builder
	b.WriteString("  " + labelStyle.Render(padRight(label, a.usageLabelWidth(width))))
	for _, c := range cells {
		st := cellStyle
		if c == "–" {
			st = a.styles.Faint
		}
		b.WriteString("  " + st.Render(padLeft(c, usageColW)))
	}
	return b.String()
}

// truncHead is truncTail's mirror: it keeps the end of s, which is the
// part of a path that tells one project from another.
func truncHead(s string, w int, ell string) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w <= runeWidth(ell) {
		return string(r[len(r)-w:])
	}
	return ell + string(r[len(r)-(w-runeWidth(ell)):])
}

// fmtTokens abbreviates a token count: 950, 1.2k, 45k, 1.5M, 3.2B. The
// one decimal is kept below 100 of a unit, and the unit is chosen after
// rounding so 999_950 reads 1.0M, never 1000k.
func fmtTokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 99_950:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	case n < 999_500:
		return fmt.Sprintf("%dk", (n+500)/1000)
	case n < 99_950_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n < 999_500_000:
		return fmt.Sprintf("%dM", (n+500_000)/1_000_000)
	default:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	}
}
