package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jwstover/tend/internal/task"
)

// listItem adapts a task.Task to the bubbles list.Item interface, carrying
// its sub-task progress for the N/M meta column.
type listItem struct {
	t           task.Task
	done, total int64
	expanded    bool
	tags        []string // carried on the item so `/` can match tag text
}

// FilterValue feeds the list's built-in `/` filtering.
func (i listItem) FilterValue() string {
	v := i.t.Title
	for _, tag := range i.tags {
		v += " " + tag
	}
	return v
}

func (i listItem) rowTask() task.Task { return i.t }

// childItem is an expanded sub-task row at any depth. Depth is the only
// thing separating it from a top-level row: it carries the same task and
// gets the same detail pane.
type childItem struct {
	t           task.Task
	depth       int // 1 for direct children; indentation = depth cells
	done, total int64
	expanded    bool
}

// An empty FilterValue keeps child rows out of `/` results: filtering
// matches top-level tasks only, like the section headings.
func (i childItem) FilterValue() string { return "" }

func (i childItem) rowTask() task.Task { return i.t }

// rowItem is any selectable task row — top-level or expanded child.
type rowItem interface {
	list.Item
	rowTask() task.Task
}

// sectionItem is a non-selectable heading row that labels the state group
// below it. An empty FilterValue keeps headings out of `/` filter results.
type sectionItem struct {
	state task.State
	count int
}

func (i sectionItem) FilterValue() string { return "" }

// spacerItem is a blank breathing-room row between state groups.
type spacerItem struct{}

func (spacerItem) FilterValue() string { return "" }

// stateOrder is the display order of section groups: active work first,
// then the queue, then everything waiting.
var stateOrder = []task.State{
	task.StateDoing,
	task.StateTodo,
	task.StateBlocked,
	task.StateInbox,
	task.StateSomeday,
	task.StateDone,
}

// toGroupedItems lays top-level tasks out under one section heading per
// state, in stateOrder, preserving the store's ordering within each group.
// Expanded branches slide their (cached) children in below the parent,
// recursively; collapsed children surface only as the N/M count.
func toGroupedItems(tasks []task.Task, counts map[int64]task.ChildCount,
	expanded map[int64]bool, children map[int64][]task.Task, tags map[int64][]string) []list.Item {
	groups := make(map[task.State][]task.Task)
	for _, t := range tasks {
		if t.ParentID != nil {
			continue
		}
		groups[t.State] = append(groups[t.State], t)
	}
	items := make([]list.Item, 0, len(tasks)+2*len(stateOrder))
	for _, s := range stateOrder {
		group := groups[s]
		if len(group) == 0 {
			continue
		}
		if len(items) > 0 {
			items = append(items, spacerItem{})
		}
		items = append(items, sectionItem{state: s, count: len(group)})
		for _, t := range group {
			items = appendTaskRows(items, t, counts, expanded, children, tags)
		}
		delete(groups, s)
	}
	// States missing from stateOrder still get rendered rather than
	// silently dropped.
	for _, t := range tasks {
		if t.ParentID != nil {
			continue
		}
		if _, leftover := groups[t.State]; leftover {
			items = appendTaskRows(items, t, counts, expanded, children, tags)
		}
	}
	return items
}

// appendTaskRows emits a top-level task row plus, when expanded, its
// child rows.
func appendTaskRows(items []list.Item, t task.Task, counts map[int64]task.ChildCount,
	expanded map[int64]bool, children map[int64][]task.Task, tags map[int64][]string) []list.Item {
	c := counts[t.ID]
	_, loaded := children[t.ID]
	items = append(items, listItem{
		t: t, done: c.Done, total: c.Total, tags: tags[t.ID],
		// The caret reflects what's actually showing: a branch awaiting
		// its first children load still reads closed.
		expanded: expanded[t.ID] && c.Total > 0 && loaded,
	})
	if expanded[t.ID] {
		items = appendChildRows(items, t.ID, 1, counts, expanded, children)
	}
	return items
}

// appendChildRows walks an expanded branch depth-first, one indent cell
// per level. Branches whose children haven't loaded yet render closed
// until their childrenLoadedMsg arrives.
func appendChildRows(items []list.Item, parentID int64, depth int,
	counts map[int64]task.ChildCount, expanded map[int64]bool,
	children map[int64][]task.Task) []list.Item {
	for _, c := range children[parentID] {
		cc := counts[c.ID]
		_, loaded := children[c.ID]
		exp := expanded[c.ID] && cc.Total > 0 && loaded
		items = append(items, childItem{
			t: c, depth: depth,
			done: cc.Done, total: cc.Total, expanded: exp,
		})
		if exp {
			items = appendChildRows(items, c.ID, depth+1, counts, expanded, children)
		}
	}
	return items
}

// taskCount reports how many items are top-level tasks (the header's
// "shown" count; expanded children don't inflate it).
func taskCount(items []list.Item) int {
	n := 0
	for _, it := range items {
		if _, ok := it.(listItem); ok {
			n++
		}
	}
	return n
}

// moveOffHeading nudges the selection onto the nearest task when it sits
// on a heading or spacer, preferring direction dir (+1 down, -1 up).
func moveOffHeading(m *list.Model, dir int) {
	items := m.VisibleItems()
	idx := m.Index()
	if idx < 0 || idx >= len(items) {
		return
	}
	if _, isTask := items[idx].(rowItem); isTask {
		return
	}
	for _, d := range []int{dir, -dir} {
		for j := idx + d; 0 <= j && j < len(items); j += d {
			if _, isTask := items[j].(rowItem); isTask {
				m.Select(j)
				return
			}
		}
	}
}

// taskDelegate renders rows per the design spec: a front gutter carrying
// the selection bar and the agent-session marker, then the state dot,
// caret slot, flexible title, and a fixed right-aligned meta block that
// opens with the priority.
type taskDelegate struct {
	styles Styles
	// sessions is the latest agent-session status per task id, used for
	// the row marker. Nil is a normal state, not an error: it just means
	// no row carries a marker.
	sessions map[int64]task.SessionStatus
	// tags is every task's tags by id, for the #tag meta column. Like
	// sessions it rides on the delegate rather than the item: it is purely
	// visual, so only the renderer needs it.
	tags map[int64][]string
}

// sessionCell renders the agent-session marker for a task row: a
// two-cell column, second half of the front gutter, that stays blank
// unless the task's most recent session is one the user might want to
// act on.
//
// Only live-ish statuses show. A task whose session merely ended would
// otherwise carry a marker forever — every task ever worked on would
// grow permanent chrome, which is noise rather than signal. Ended and
// unknown render as blank, so the marker means "there is a session doing
// something right now."
func (d taskDelegate) sessionCell(id int64) seg {
	st, ok := d.sessions[id]
	if !ok {
		return seg{"  ", d.styles.Normal}
	}
	switch st {
	case task.SessionStarting, task.SessionWorking, task.SessionBlocked, task.SessionIdle:
	default:
		return seg{"  ", d.styles.Normal}
	}
	glyph, style := sessionStatusCell(d.styles, st)
	return seg{glyph + " ", style}
}

func (d taskDelegate) Height() int  { return 1 }
func (d taskDelegate) Spacing() int { return 0 }

// Update runs after the list has handled navigation; if the cursor landed
// on a heading or spacer, keep it moving in the direction of travel.
func (d taskDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd {
	dir := 1
	if k, ok := msg.(tea.KeyPressMsg); ok &&
		(key.Matches(k, m.KeyMap.CursorUp) || key.Matches(k, m.KeyMap.PrevPage)) {
		dir = -1
	}
	moveOffHeading(m, dir)
	return nil
}

// compactMetaWidth is the width below which the meta block drops to
// due + sub only.
const compactMetaWidth = 78

func (d taskDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	switch it := item.(type) {
	case spacerItem:
		return
	case sectionItem:
		fmt.Fprint(w, d.renderHeading(it, m.Width()))
	case listItem:
		fmt.Fprint(w, d.renderRow(it, index == m.Index(), m.Width()))
	case childItem:
		fmt.Fprint(w, d.renderChildRow(it, index == m.Index(), m.Width()))
	}
}

// renderHeading draws `   <glyph> <label>  ─────── <count>`. Its lead
// matches the rows' three-cell gutter, so a section glyph sits in the
// same column as the state dots below it.
func (d taskDelegate) renderHeading(sec sectionItem, width int) string {
	g := d.styles.Glyphs
	label := strings.ToLower(string(sec.state))
	count := fmt.Sprintf("%d", sec.count)
	used := 3 + runeWidth(g.State[sec.state]) + 1 + len(label) + 2 + 1 + len(count)
	fill := max(width-used, 0)
	// The done section celebrates with complete-green; every other heading
	// wears its state color.
	headStyle := d.styles.State[sec.state]
	if sec.state == task.StateDone {
		headStyle = d.styles.CheckDone
	}
	var b strings.Builder
	b.WriteString("   ")
	b.WriteString(headStyle.Render(g.State[sec.state] + " "))
	b.WriteString(headStyle.Bold(true).Render(label))
	b.WriteString("  ")
	b.WriteString(d.styles.GroupRule.Render(strings.Repeat(g.RuleH, fill)))
	b.WriteString(" ")
	b.WriteString(d.styles.GroupCount.Render(count))
	return b.String()
}

// seg is a styled run of text; rows compose segs so the selected-row
// background fill can be applied uniformly.
type seg struct {
	text  string
	style lipgloss.Style
}

// rowSpec is the per-row input to renderTaskRow: everything the two item
// types have in common, plus the depth that distinguishes them.
type rowSpec struct {
	t           task.Task
	depth       int
	done, total int64
	expanded    bool
}

// gutterCells renders the three-cell front gutter every task row opens
// with: the selection bar, then the agent-session marker.
//
// Two segments rather than one because the bar wears the accent style
// while the marker is colored by session status. Keeping the marker
// flush left is the point — it scans straight down the screen at every
// nesting depth, instead of sitting mid-row where the indent shifts it.
func (d taskDelegate) gutterCells(id int64, selected bool) []seg {
	bar := seg{" ", d.styles.Normal}
	if selected {
		bar = seg{d.styles.Glyphs.SelBar, d.styles.SelBar}
	}
	return []seg{bar, d.sessionCell(id)}
}

// renderRow draws a top-level task row.
func (d taskDelegate) renderRow(it listItem, selected bool, width int) string {
	return d.renderTaskRow(rowSpec{
		t: it.t, done: it.done, total: it.total, expanded: it.expanded,
	}, selected, width)
}

// renderChildRow draws an expanded sub-task at any depth. Indentation
// alone conveys nesting; every other column matches a top-level row.
//
// childItem is field-for-field a rowSpec, so this converts rather than
// copying: if the two ever diverge, this line stops compiling instead of
// quietly dropping a field.
func (d taskDelegate) renderChildRow(it childItem, selected bool, width int) string {
	return d.renderTaskRow(rowSpec(it), selected, width)
}

// renderTaskRow draws one task row at any depth:
//
//	gutter(3) indent(depth) dot(2) caret(2) title ... pri(2+1) meta
//
// Top-level and sub-task rows share every column, so they share one
// renderer. Depth 0 additionally fills the right-hand meta block past
// the priority, which is the only structural difference left between
// them — the two were separate functions once and drifted into
// different column orders (CHILD-ROW-PARITY.md).
func (d taskDelegate) renderTaskRow(r rowSpec, selected bool, width int) string {
	t := r.t
	g := d.styles.Glyphs
	s := d.styles

	segs := make([]seg, 0, 12)

	// Front gutter (3): selection bar + agent-session marker.
	segs = append(segs, d.gutterCells(t.ID, selected)...)

	// Indent: one cell per level, zero-width at top level.
	if r.depth > 0 {
		segs = append(segs, seg{strings.Repeat(" ", r.depth), s.Normal})
	}

	// State dot (2): bold for the attention states. Sub-tasks get the
	// same treatment — one in doing or blocked has to read as such
	// rather than collapsing to an unchecked box.
	dot := s.State[t.State]
	if t.State == task.StateDoing || t.State == task.StateBlocked {
		dot = dot.Bold(true)
	}
	segs = append(segs, seg{g.State[t.State] + " ", dot})

	// Caret slot (2): disclosure state when the task has children.
	if r.total > 0 {
		segs = append(segs, seg{caretGlyph(g, r.expanded) + " ", caretStyle(s, selected)})
	} else {
		segs = append(segs, seg{"  ", s.Normal})
	}

	// Right meta block, fixed-width columns; absent fields stay blank so
	// alignment holds down the list. Priority leads it, directly after
	// the title, so the flag reads as an attribute of the task rather
	// than a prefix on its name.
	meta := d.metaCells(r, width)
	metaW := segWidth(meta)

	// Flexible title; only the title truncates.
	lead := segWidth(segs)
	titleW := max(width-lead-1-metaW, 1)
	title := truncTail(t.Title, titleW, g.Ellipsis)
	segs = append(segs, seg{title, d.titleStyle(t, r.depth, selected)})

	// Gap, then the meta block flush right. With no meta block the gap
	// alone pads the row to full width, so the selected-row background
	// fills the line.
	gap := max(width-lead-runeWidth(title)-metaW, 0)
	segs = append(segs, seg{strings.Repeat(" ", gap), s.Normal})
	segs = append(segs, meta...)

	var b strings.Builder
	for _, sg := range segs {
		st := sg.style
		if selected {
			st = st.Background(s.Palette.AccentBg)
		}
		b.WriteString(st.Render(sg.text))
	}
	return b.String()
}

// titleStyle picks the title treatment. Sub-task titles dim so top-level
// rows stay dominant — except under the cursor, which is the row being
// read.
func (d taskDelegate) titleStyle(t task.Task, depth int, selected bool) lipgloss.Style {
	s := d.styles
	done := t.State == task.StateDone
	if depth == 0 {
		if done {
			return s.TitleDone
		}
		return s.Title
	}
	switch {
	case done:
		return s.SubDoneText
	case selected:
		return s.Title
	}
	return s.Dimmed
}

// caretGlyph picks ▸ or ▾ for a branch's disclosure state.
func caretGlyph(g glyphs, expanded bool) string {
	if expanded {
		return g.CaretOpen
	}
	return g.CaretClosed
}

// caretStyle brightens the caret to accent-bold on the selected row.
func caretStyle(s Styles, selected bool) lipgloss.Style {
	if selected {
		return s.SelBar
	}
	return s.Caret
}

// metaCells builds the right-hand meta block: priority, then tags, due
// and sub-task progress (tags drop out below compactMetaWidth).
//
// Sub-task rows show only their priority; the rest of the block is blank
// padding of the same width, so the priority column runs straight down
// the list at every depth instead of sliding right on child rows.
func (d taskDelegate) metaCells(r rowSpec, width int) []seg {
	s := d.styles
	meta := []seg{d.priCell(r.t.Priority), {" ", s.Normal}}

	var rest []seg
	if width >= compactMetaWidth {
		rest = append(rest, d.tagsCell(d.tags[r.t.ID], tagsCellWidth))
		rest = append(rest, seg{" ", s.Normal})
		rest = append(rest, d.dueCell(r.t.Due, 7))
		rest = append(rest, seg{" ", s.Normal})
		rest = append(rest, d.subCell(r.done, r.total, 4))
	} else {
		rest = append(rest, d.dueCell(r.t.Due, 6))
		rest = append(rest, seg{" ", s.Normal})
		rest = append(rest, d.subCell(r.done, r.total, 4))
	}
	if r.depth > 0 {
		return append(meta, seg{strings.Repeat(" ", segWidth(rest)), s.Normal})
	}
	return append(meta, rest...)
}

// priCell is the 2-col priority column: flag + letter (A–D).
func (d taskDelegate) priCell(p *int64) seg {
	letter := task.PriorityLetter(p)
	if letter == "" {
		return seg{"  ", d.styles.Normal}
	}
	return seg{d.styles.Glyphs.Flag + letter, d.styles.Priority[*p]}
}

// tagsCellWidth is the fixed width of the `#a #b` meta column. Twelve
// rather than the ten the single-value project column used: it is the
// narrowest that still fits a realistic tag beside an overflow counter
// ("#support +2") without chopping into the tag itself.
const tagsCellWidth = 12

// tagsCell is the fixed-width `#a #b` column. Fixed so the meta columns
// stay aligned down the list; the detail pane is where the complete tag
// list is always readable.
func (d taskDelegate) tagsCell(tags []string, w int) seg {
	if len(tags) == 0 {
		return seg{strings.Repeat(" ", w), d.styles.Normal}
	}
	return seg{padRight(fitTags(tags, w, d.styles.Glyphs.Ellipsis), w), d.styles.Tag}
}

// fitTags renders as many whole tags as fit in w and collapses the rest
// into a `+N` counter.
//
// The counter is the point: truncating the joined list instead turns
// three tags into "#customer...", which loses both which tags a task
// carries and that there is more than one. "#support +2" keeps one tag
// legible and is honest about the remainder.
func fitTags(tags []string, w int, ell string) string {
	hashed := make([]string, len(tags))
	for i, t := range tags {
		hashed[i] = "#" + t
	}
	if full := strings.Join(hashed, " "); runeWidth(full) <= w {
		return full
	}
	// Drop tags from the right until the survivors plus the counter fit.
	for k := len(hashed) - 1; k >= 1; k-- {
		candidate := fmt.Sprintf("%s +%d", strings.Join(hashed[:k], " "), len(hashed)-k)
		if runeWidth(candidate) <= w {
			return candidate
		}
	}
	// Not even one whole tag plus the counter fits: keep the counter and
	// truncate the first tag, so the row still reports how many there are.
	suffix := ""
	if len(hashed) > 1 {
		suffix = fmt.Sprintf(" +%d", len(hashed)-1)
	}
	return truncTail(hashed[0], max(w-runeWidth(suffix), 1), ell) + suffix
}

// dueCell is the right-aligned due column, colored by urgency.
func (d taskDelegate) dueCell(due *string, w int) seg {
	if due == nil {
		return seg{strings.Repeat(" ", w), d.styles.Normal}
	}
	label, style := dueLabel(*due, d.styles, time.Now())
	return seg{padLeft(truncTail(label, w, d.styles.Glyphs.Ellipsis), w), style}
}

// dueLabel renders an ISO date compactly and picks the urgency style. Due
// dates are ISO YYYY-MM-DD, so lexical comparison against today is exact.
func dueLabel(due string, s Styles, now time.Time) (string, lipgloss.Style) {
	today := now.Format("2006-01-02")
	switch {
	case due == today:
		return "today", s.DueToday
	case due < today:
		return shortDate(due), s.DueOver
	default:
		return shortDate(due), s.DueFuture
	}
}

// shortDate renders an ISO date as e.g. "Jun 8" (fits the 7-col column).
func shortDate(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("Jan 2")
}

// subCell is the right-aligned N/M sub-task count: complete-green at N==M,
// muted otherwise.
func (d taskDelegate) subCell(done, total int64, w int) seg {
	if total == 0 {
		return seg{strings.Repeat(" ", w), d.styles.Normal}
	}
	style := d.styles.SubPartial
	if done == total {
		style = d.styles.SubFull
	}
	return seg{padLeft(fmt.Sprintf("%d/%d", done, total), w), style}
}

// --- cell helpers (rune-width math on plain text, styled afterwards) ---

func runeWidth(s string) int { return len([]rune(s)) }

func segWidth(segs []seg) int {
	n := 0
	for _, s := range segs {
		n += runeWidth(s.text)
	}
	return n
}

// truncTail clips s to w columns with a trailing ellipsis.
func truncTail(s string, w int, ell string) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w <= runeWidth(ell) {
		return string(r[:w])
	}
	return string(r[:w-runeWidth(ell)]) + ell
}

func padRight(s string, w int) string {
	return s + strings.Repeat(" ", max(w-runeWidth(s), 0))
}

func padLeft(s string, w int) string {
	return strings.Repeat(" ", max(w-runeWidth(s), 0)) + s
}

// newTaskList builds a list component configured as a bare task list; the
// app draws its own header and help line.
func newTaskList(styles Styles) list.Model {
	l := list.New(nil, taskDelegate{styles: styles}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	return l
}
