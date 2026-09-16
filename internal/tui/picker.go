package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// picker is the shared chooser overlay every bottom-anchored picker in this
// package is built from: a type-to-filter list with arrow/ctrl-n/ctrl-p
// navigation, a digit shortcut per visible row, and a scroll window once the
// matches outgrow pickerMaxRows.
//
// app is copied by value on every Update, so a picker must stay a plain
// value with no pointers into the app: label and filter must be pure
// functions of T alone and must never close over an app, or they would go
// stale the moment the app that made them is discarded.

// pickerMaxRows caps how many item rows any picker shows at once. Past it
// the rows scroll under the highlight instead of the box growing with the
// list: the overlay splice in View keeps the bottom rows, so a box taller
// than the screen loses its title and filter prompt first.
const pickerMaxRows = 10

// pickerChrome is every screen row a picker costs besides its item rows:
// the app header and footer, the box's top border, title row, divider and
// filter prompt, its bottom border, and the overflow indicator.
const pickerChrome = 8

// pickerAction is what one key press asked the caller to do.
type pickerAction int

const (
	pickerNone   pickerAction = iota // handled inside: moved, typed, filtered
	pickerPick                       // the user chose a row
	pickerCancel                     // esc
)

// choiceRow is one row of a picker whose rows are actions: what it is
// called, a line saying what it does, and the command it fires.
type choiceRow struct {
	label, desc string
	act         func(a *app) tea.Cmd
}

// picker is a generic value type: T is whatever a specific overlay chooses
// between (a workflow, a task, a plain string outcome, a choiceRow, ...).
type picker[T any] struct {
	open  bool
	items []T // the filterable rows, in their natural order
	query string
	sel   int // index into rows(): 0..pinned-1 are the pinned rows
	top   int // first visible item row in the scroll window

	// label is the text the filter matches. Pure; never closes over app.
	label func(T) string
	// filter overrides fuzzyFilter(query, items, label). Pure. Only the
	// command palette sets one, to keep its exact-alias tier and its
	// synthetic "add <text>" row.
	filter func(query string, items []T) []T
	// pinned is how many rows sit above the items, take part in
	// navigation, but are not filtered and carry no number (the session
	// picker's "+ new session").
	pinned int
	// numbered draws the 1-9 row numbers and turns the digit shortcuts on.
	numbered bool
	// footer is 1 when the view draws a hint row under the list, so the
	// height budget accounts for it.
	footer int
	// maxRows overrides pickerMaxRows.
	maxRows int
}

// matches is the rows the query keeps.
func (p picker[T]) matches() []T {
	if p.filter != nil {
		return p.filter(p.query, p.items)
	}
	return fuzzyFilter(p.query, p.items, p.label)
}

// rowCount is every navigable row: the pinned ones plus the matches.
func (p picker[T]) rowCount() int { return p.pinned + len(p.matches()) }

// at resolves a rows() index to an item. It reports false for a pinned
// row and for an index past the end, so callers bound-check by using it.
func (p picker[T]) at(idx int) (T, bool) {
	rows := p.matches()
	if i := idx - p.pinned; i >= 0 && i < len(rows) {
		return rows[i], true
	}
	var zero T
	return zero, false
}

// current is the highlighted item.
func (p picker[T]) current() (T, bool) { return p.at(p.sel) }

// visible is how many item rows fit: maxRows, or fewer on a terminal too
// short for that many plus the chrome. Before the first WindowSizeMsg the
// height is 0 and only the cap applies.
func (p picker[T]) visible(termHeight int) int {
	n := p.maxRows
	if n <= 0 {
		n = pickerMaxRows
	}
	if termHeight > 0 {
		n = min(n, termHeight-pickerChrome-p.pinned-p.footer)
	}
	return max(n, 1)
}

// pickerWindow returns where the scroll window over n matching items
// starts so the highlight stays inside it: it moves only when the
// highlight would leave it, and never past the end. Pure, so the view can
// derive the same window the key handler stored - and re-clamp after a
// resize with no key press.
func pickerWindow(sel, top, n, vis, pinned int) int {
	if i := sel - pinned; i >= 0 {
		if i < top {
			top = i
		}
		if i >= top+vis {
			top = i - vis + 1
		}
	}
	return max(min(top, n-vis), 0)
}

// scroll settles the highlight and the window after a move, a filter
// change or an open with a non-zero sel.
func (p *picker[T]) scroll(termHeight int) {
	n := len(p.matches())
	p.sel = max(min(p.sel, p.pinned+n-1), 0)
	p.top = pickerWindow(p.sel, p.top, n, p.visible(termHeight), p.pinned)
}

// key applies one key press. Anything that only moves the highlight or
// edits the filter is handled here and reported as pickerNone; enter and
// a live digit shortcut report pickerPick with the rows() index chosen
// (bound-check it with at()); esc reports pickerCancel. A caller with a
// key of its own - the gate picker's ctrl+enter - must test for it before
// calling this.
func (p *picker[T]) key(msg tea.KeyPressMsg, termHeight int) (pickerAction, int) {
	switch msg.String() {
	case "esc":
		return pickerCancel, -1
	case "enter":
		return pickerPick, p.sel
	case "up", "ctrl+p":
		p.sel--
		p.scroll(termHeight)
		return pickerNone, -1
	case "down", "ctrl+n":
		p.sel++
		p.scroll(termHeight)
		return pickerNone, -1
	case "backspace":
		if r := []rune(p.query); len(r) > 0 {
			p.query = string(r[:len(r)-1])
		}
		p.sel, p.top = 0, 0
		return pickerNone, -1
	}
	if p.numbered && len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
		rows := p.matches()
		vis := p.visible(termHeight)
		top := pickerWindow(p.sel, p.top, len(rows), vis, p.pinned)
		if i := top + int(msg.Text[0]-'1'); i < min(top+vis, len(rows)) {
			return pickerPick, p.pinned + i
		}
	}
	if msg.Text != "" {
		p.query += msg.Text
		p.sel, p.top = 0, 0
		return pickerNone, -1
	}
	return pickerNone, -1
}

// pickerRowLine lays one row inside the box: `  | ` then the content
// padded out to the closing `|` at the last column.
func pickerRowLine(s Styles, width int, content string) string {
	g, cb := s.Glyphs, s.CardBorder
	w := max(width, 20)
	gap := max(w-5-lipgloss.Width(content), 0)
	return "  " + cb.Render(g.RuleV) + " " + content + strings.Repeat(" ", gap) + cb.Render(g.RuleV)
}

// pickerFrame stacks a header row and the body rows into the bordered box
// every overlay uses: top border, header, divider, body, bottom border.
// When header is "" there is no title row -- the palette's case -- and the
// divider instead separates body's first row (the filter prompt) from the
// rest, so the box looks exactly as it always has either way.
func pickerFrame(s Styles, width int, header string, body []string) string {
	g, cb := s.Glyphs, s.CardBorder
	w := max(width, 20)
	hbar := strings.Repeat(g.RuleH, w-4)
	divider := "  " + cb.Render(g.TeeRight+hbar+g.TeeLeft)

	lines := []string{"  " + cb.Render(g.BoxTL+hbar+g.BoxTR)}
	rest := body
	switch {
	case header != "":
		lines = append(lines, pickerRowLine(s, w, header), divider)
	case len(body) > 0:
		lines = append(lines, pickerRowLine(s, w, body[0]), divider)
		rest = body[1:]
	}
	for _, b := range rest {
		lines = append(lines, pickerRowLine(s, w, b))
	}
	lines = append(lines, "  "+cb.Render(g.BoxBL+hbar+g.BoxBR))
	return strings.Join(lines, "\n")
}

// pickerMore is the overflow row's text for a window [top, end) over n
// rows: how many are scrolled off each end, named only when there are
// some; "" when the window holds everything.
func pickerMore(top, end, n int) string {
	var parts []string
	if top > 0 {
		parts = append(parts, fmt.Sprintf("↑ %d more", top))
	}
	if end < n {
		parts = append(parts, fmt.Sprintf("↓ %d more", n-end))
	}
	return strings.Join(parts, "   ")
}

// pickerView is how one picker draws itself. Every string field is
// already styled by the caller.
type pickerView[T any] struct {
	icon  string // leading glyph on the header row
	title string // header content after the icon
	hint  string // trailing muted hint on the header row
	right string // right-aligned on the header row (dep picker's "esc closes")
	// footer is a hint row under the list; set picker.footer non-empty
	// with it so the height budget accounts for it.
	footer  string
	empty   string // plain text shown when items is empty
	noMatch string // plain text shown when the filter matched nothing
	// pinned renders a pinned row whole, gutter included.
	pinned func(i int, selected bool, w int) string
	// row renders an item row's content - everything after the selection
	// gutter and the row number, which render draws. w is what is left of
	// the content width after that prefix.
	row func(it T, selected bool, w int) string
}

// render lays out the picker: the header (if any), the filter prompt, any
// pinned rows, the matching item rows in their scroll window, an overflow
// row when they don't all fit, and the footer (if any).
func (p picker[T]) render(s Styles, width, termHeight int, v pickerView[T]) string {
	g := s.Glyphs
	w := max(width, 20)
	cw := w - 5

	header := v.icon + v.title + v.hint
	if v.right != "" {
		gap := max(cw-lipgloss.Width(header)-lipgloss.Width(v.right), 1)
		header += strings.Repeat(" ", gap) + v.right
	}

	body := []string{
		s.Accent.Bold(true).Render("❯ ") + s.Title.Render(p.query) + s.Accent.Render("▏"),
	}

	for i := 0; i < p.pinned; i++ {
		body = append(body, v.pinned(i, p.sel == i, cw))
	}

	rows := p.matches()
	switch {
	case len(p.items) == 0 && v.empty != "":
		body = append(body, "  "+s.Muted.Render(v.empty))
	case len(rows) == 0 && v.noMatch != "":
		body = append(body, "  "+s.Muted.Render(v.noMatch))
	}

	vis := p.visible(termHeight)
	top := pickerWindow(p.sel, p.top, len(rows), vis, p.pinned)
	end := min(top+vis, len(rows))
	for i := top; i < end; i++ {
		selected := p.sel == p.pinned+i
		prefix := "  "
		if selected {
			prefix = s.SelBar.Render(g.SelBar + " ")
		}
		if p.numbered {
			num := fmt.Sprintf("%d ", i-top+1)
			if selected {
				prefix += s.Accent.Render(num)
			} else {
				prefix += s.Muted.Render(num)
			}
		}
		body = append(body, prefix+v.row(rows[i], selected, cw-lipgloss.Width(prefix)))
	}
	if more := pickerMore(top, end, len(rows)); more != "" {
		body = append(body, "  "+s.Muted.Render(more))
	}
	if v.footer != "" {
		body = append(body, v.footer)
	}
	return pickerFrame(s, w, header, body)
}
