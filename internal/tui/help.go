package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jwstover/tend/internal/task"
)

// helpEntry is one binding row: the key cap(s) and what they do.
type helpEntry struct{ keys, desc string }

// helpGroup is a titled cluster of bindings.
type helpGroup struct {
	title   string
	entries []helpEntry
}

// helpGroups is the app's real key reference, grouped by intent.
func helpGroups() []helpGroup {
	return []helpGroup{
		{"NAVIGATE", []helpEntry{
			{"j / k", "down / up"},
			{"gg / G", "top / bottom"},
			{"⏎ / tab", "expand / collapse children"},
			{"l / h", "expand / collapse branch"},
			{"]", "toggle detail pane"},
			{"C", "show / hide completed + someday"},
			{"g", "group by state/priority/agent"},
			{"o / O", "open link(s) in body + log"},
		}},
		{"CAPTURE & FIND", []helpEntry{
			{"n", "quick-add to inbox"},
			{"a", "add sub-task"},
			{"/", "search the list"},
			{": / ctrl+p", "command palette"},
		}},
		{"PROCESS", []helpEntry{
			{"i", "triage the inbox"},
			{"x / space", "mark done"},
			{"c", "change state (chord)"},
			{"p", "set priority (chord)"},
			{"dd", "delete task (chord)"},
			{"T", "set tags"},
			{"P", "move to project"},
			{"u", "set due (triage)"},
			{"R", "rename task"},
			{"e", "edit body in $EDITOR"},
			{"r", "launch / resume a Claude session"},
			{"w", "run a workflow on the task"},
			{"v", "watch the task's workflow run"},
		}},
		{"PROJECTS", []helpEntry{
			{"h / [", "focus / toggle projects column"},
			{"n / R", "new / rename project"},
			{"w", "default cwd for new sessions"},
			{"A", "archive / restore project"},
			{"dd", "delete project; tasks → Unsorted"},
		}},
		{"STANDUP", []helpEntry{
			{"S", "open the standup view"},
			{"N / U", "note / note on task"},
			{"h / l", "shift window (standup)"},
			{"s", "group by task / time (standup)"},
			{"y", "yank markdown (standup)"},
		}},
		{"WORKFLOWS", []helpEntry{
			{"W", "open the workflows view"},
			{"h / l", "workflows ↔ steps pane"},
			{"n / R / D", "new / rename / duplicate"},
			{"e", "edit step prompt in $EDITOR"},
			{"m / p / t", "step model / permission / kind"},
			{"J / K", "move step down / up"},
			{"v", "validate prompt templates"},
			{"dd", "delete workflow or step"},
		}},
		{"RUN VIEW", []helpEntry{
			{"j / k / tab", "select step / scroll / pane"},
			{"l", "raw stream-json / rendered log"},
			{"p / cc", "pause or resume / cancel the run"},
			{"a / x", "approve / reject (with feedback) gate"},
			{"o", "pick any outcome the gate routes"},
			{"t", "take over paused step's session"},
		}},
	}
}

// Help overlay geometry. The box spans the terminal like the palette:
// `  │ ` on the left, `│` in the last column, so the content area is
// helpChromeCols narrower than the screen. Title, footer and bottom
// border are the helpChromeRows the body cannot use.
const (
	helpChromeCols = 5
	helpChromeRows = 3
	helpKeyCol     = 12 // key caps sit in a fixed column; the widest is "j / k / tab"
	helpIndent     = 2  // entry indent under a group title
	helpGutter     = 3  // blank columns between the two columns
)

// helpInnerWidth is the content width between the box borders.
func (a app) helpInnerWidth() int {
	return max(a.width, 30) - helpChromeCols
}

// helpPageRows is how many body rows fit between the box title and its
// footer once the app footer keeps the last screen row.
func (a app) helpPageRows() int {
	return max(a.height-1-helpChromeRows, 1)
}

// helpMaxScroll is the largest offset that still fills the page.
func (a app) helpMaxScroll() int {
	return max(len(a.helpBody())-a.helpPageRows(), 0)
}

// helpColumn renders one group after another as styled rows: title,
// entries, then a blank separator. It also reports the widest row so the
// caller can decide whether two columns fit side by side.
func (a app) helpColumn(groups []helpGroup) (rows []string, width int) {
	s := a.styles
	title := s.State[task.StateInbox].Bold(true)
	indent := strings.Repeat(" ", helpIndent)
	for _, grp := range groups {
		rows = append(rows, " "+title.Render(grp.title))
		for _, e := range grp.entries {
			pad := strings.Repeat(" ", max(helpKeyCol-lipgloss.Width(e.keys), 1))
			rows = append(rows, indent+s.FooterKey.Render(e.keys)+pad+s.Dimmed.Render(e.desc))
		}
		rows = append(rows, "")
	}
	for _, r := range rows {
		width = max(width, lipgloss.Width(r))
	}
	return rows, width
}

// helpBody lays the groups out as the rows between the box title and its
// footer, before any scrolling. Two columns when the screen is wide
// enough for the widest row twice plus a gutter, otherwise one; groups
// stay whole and are split so the columns come out about even.
func (a app) helpBody() []string {
	groups := helpGroups()
	single, natural := a.helpColumn(groups)
	iw := a.helpInnerWidth()
	body := []string{""}
	if 2*natural+helpGutter > iw {
		return append(body, single...)
	}

	// Walk the groups into the left column until it holds half the rows.
	split, acc := len(groups), 0
	for i, grp := range groups {
		if acc >= len(single)/2 {
			split = i
			break
		}
		acc += len(grp.entries) + 2
	}
	left, _ := a.helpColumn(groups[:split])
	right, _ := a.helpColumn(groups[split:])
	for i := 0; i < max(len(left), len(right)); i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		l += strings.Repeat(" ", max(natural+helpGutter-lipgloss.Width(l), 0))
		body = append(body, strings.TrimRight(l+r, " "))
	}
	return body
}

// helpView renders the bordered key reference spliced above the footer:
// group titles in inbox orange, keys accent-bold in a fixed column,
// descriptions in fgDim. The body scrolls with j/k when it outgrows the
// screen; the footer row shows how many rows lie above and below.
func (a app) helpView() string {
	s, g := a.styles, a.styles.Glyphs
	w := max(a.width, 30)
	iw := a.helpInnerWidth()
	cb := s.CardBorder

	row := func(content string) string {
		content = ansi.Truncate(content, iw, "…")
		gap := max(iw-lipgloss.Width(content), 0)
		return "  " + cb.Render(g.RuleV) + " " + content +
			strings.Repeat(" ", gap) + cb.Render(g.RuleV)
	}

	body := a.helpBody()
	page := a.helpPageRows()
	off := min(max(a.helpScroll, 0), max(len(body)-page, 0))
	end := min(off+page, len(body))

	lines := []string{"  " + cb.Render(g.BoxTL+g.RuleH+" ") +
		s.Accent.Bold(true).Render("help") +
		cb.Render(" "+strings.Repeat(g.RuleH, max(w-11, 1))+g.BoxTR)}
	for _, r := range body[off:end] {
		lines = append(lines, row(r))
	}

	footer := " " + s.FooterKey.Render("esc") + s.Muted.Render(" close")
	if len(body) > page {
		footer += s.Muted.Render(" · ") + s.FooterKey.Render("j / k") + s.Muted.Render(" scroll")
		var hints []string
		if off > 0 {
			hints = append(hints, fmt.Sprintf("↑ %d", off))
		}
		if end < len(body) {
			hints = append(hints, fmt.Sprintf("↓ %d", len(body)-end))
		}
		hint := s.Muted.Render(strings.Join(hints, "  ") + " ")
		footer += strings.Repeat(" ", max(iw-lipgloss.Width(footer)-lipgloss.Width(hint), 1)) + hint
	}
	lines = append(lines, row(footer))
	lines = append(lines, "  "+cb.Render(g.BoxBL+strings.Repeat(g.RuleH, w-4)+g.BoxBR))
	return strings.Join(lines, "\n")
}
