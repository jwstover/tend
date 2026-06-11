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
}

// FilterValue feeds the list's built-in `/` filtering.
func (i listItem) FilterValue() string {
	v := i.t.Title
	if i.t.Project != nil {
		v += " " + *i.t.Project
	}
	return v
}

func (i listItem) rowTask() task.Task { return i.t }

// childItem is an expanded sub-task row at any depth. It remembers its
// owning top-level task so the detail pane can follow the branch root.
type childItem struct {
	t           task.Task
	owner       task.Task // top-level ancestor
	depth       int       // 1 for direct children; indentation = depth cells
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
	expanded map[int64]bool, children map[int64][]task.Task) []list.Item {
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
			items = appendTaskRows(items, t, counts, expanded, children)
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
			items = appendTaskRows(items, t, counts, expanded, children)
		}
	}
	return items
}

// appendTaskRows emits a top-level task row plus, when expanded, its
// child rows.
func appendTaskRows(items []list.Item, t task.Task, counts map[int64]task.ChildCount,
	expanded map[int64]bool, children map[int64][]task.Task) []list.Item {
	c := counts[t.ID]
	_, loaded := children[t.ID]
	items = append(items, listItem{
		t: t, done: c.Done, total: c.Total,
		// The caret reflects what's actually showing: a branch awaiting
		// its first children load still reads closed.
		expanded: expanded[t.ID] && c.Total > 0 && loaded,
	})
	if expanded[t.ID] {
		items = appendChildRows(items, t.ID, 1, t, counts, expanded, children)
	}
	return items
}

// appendChildRows walks an expanded branch depth-first, one indent cell
// per level. Branches whose children haven't loaded yet render closed
// until their childrenLoadedMsg arrives.
func appendChildRows(items []list.Item, parentID int64, depth int, owner task.Task,
	counts map[int64]task.ChildCount, expanded map[int64]bool,
	children map[int64][]task.Task) []list.Item {
	for _, c := range children[parentID] {
		cc := counts[c.ID]
		_, loaded := children[c.ID]
		exp := expanded[c.ID] && cc.Total > 0 && loaded
		items = append(items, childItem{
			t: c, owner: owner, depth: depth,
			done: cc.Done, total: cc.Total, expanded: exp,
		})
		if exp {
			items = appendChildRows(items, c.ID, depth+1, owner, counts, expanded, children)
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

// taskDelegate renders rows per the design spec: selection gutter, state
// dot, caret slot, flexible title, and a fixed right-aligned meta block.
type taskDelegate struct {
	styles Styles
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

// renderHeading draws `  <glyph> <label>  ─────── <count>`.
func (d taskDelegate) renderHeading(sec sectionItem, width int) string {
	g := d.styles.Glyphs
	label := strings.ToLower(string(sec.state))
	count := fmt.Sprintf("%d", sec.count)
	used := 2 + runeWidth(g.State[sec.state]) + 1 + len(label) + 2 + 1 + len(count)
	fill := max(width-used, 0)
	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(d.styles.State[sec.state].Render(g.State[sec.state] + " "))
	b.WriteString(d.styles.State[sec.state].Bold(true).Render(label))
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

func (d taskDelegate) renderRow(it listItem, selected bool, width int) string {
	t := it.t
	g := d.styles.Glyphs
	s := d.styles

	segs := make([]seg, 0, 12)

	// Selection gutter (2).
	if selected {
		segs = append(segs, seg{g.SelBar + " ", s.SelBar})
	} else {
		segs = append(segs, seg{"  ", s.Normal})
	}

	// State dot (2): bold for the attention states.
	dot := s.State[t.State]
	if t.State == task.StateDoing || t.State == task.StateBlocked {
		dot = dot.Bold(true)
	}
	segs = append(segs, seg{g.State[t.State] + " ", dot})

	// Caret slot (2): disclosure state when the task has children.
	if it.total > 0 {
		segs = append(segs, seg{caretGlyph(g, it.expanded) + " ", caretStyle(s, selected)})
	} else {
		segs = append(segs, seg{"  ", s.Normal})
	}

	// Priority sits just before the title; blank when unset so titles
	// stay aligned across rows.
	segs = append(segs, d.priCell(t.Priority))
	segs = append(segs, seg{" ", s.Normal})

	// Right meta block, fixed-width columns; absent fields stay blank so
	// alignment holds across rows.
	var meta []seg
	if width >= compactMetaWidth {
		meta = append(meta, d.projectCell(t.Project, 10))
		meta = append(meta, seg{" ", s.Normal})
		meta = append(meta, d.dueCell(t.Due, 7))
		meta = append(meta, seg{" ", s.Normal})
		meta = append(meta, d.subCell(it.done, it.total, 4))
	} else {
		meta = append(meta, d.dueCell(t.Due, 6))
		meta = append(meta, seg{" ", s.Normal})
		meta = append(meta, d.subCell(it.done, it.total, 4))
	}
	metaW := segWidth(meta)

	// Flexible title; only the title truncates.
	lead := segWidth(segs)
	titleW := max(width-lead-1-metaW, 1)
	titleStyle := s.Title
	if t.State == task.StateDone {
		titleStyle = s.TitleDone
	}
	title := truncTail(t.Title, titleW, g.Ellipsis)
	segs = append(segs, seg{title, titleStyle})

	// Gap, then the meta block flush right.
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

// renderChildRow draws an expanded sub-task at any depth: gutter, depth
// cells of indent (indentation alone conveys nesting), caret slot when the
// node has its own children, checkbox, title.
func (d taskDelegate) renderChildRow(it childItem, selected bool, width int) string {
	g := d.styles.Glyphs
	s := d.styles

	segs := make([]seg, 0, 6)

	// Selection gutter (2).
	if selected {
		segs = append(segs, seg{g.SelBar + " ", s.SelBar})
	} else {
		segs = append(segs, seg{"  ", s.Normal})
	}

	// Indent: one cell per level.
	segs = append(segs, seg{strings.Repeat(" ", it.depth), s.Normal})

	// Caret slot (2): only when this node has children of its own.
	if it.total > 0 {
		segs = append(segs, seg{caretGlyph(g, it.expanded) + " ", caretStyle(s, selected)})
	} else {
		segs = append(segs, seg{"  ", s.Normal})
	}

	// Checkbox: sub-task "done" is the done state.
	done := it.t.State == task.StateDone
	if done {
		segs = append(segs, seg{g.BoxChecked + " ", s.CheckDone})
	} else {
		segs = append(segs, seg{g.BoxUnchecked + " ", s.CheckOpen})
	}

	titleStyle := s.Dimmed
	switch {
	case done:
		titleStyle = s.SubDoneText
	case selected:
		titleStyle = s.Title
	}
	titleW := max(width-segWidth(segs), 1)
	title := truncTail(it.t.Title, titleW, g.Ellipsis)
	segs = append(segs, seg{title, titleStyle})

	// Pad to full width so the selected-row background fills the line.
	if gap := width - segWidth(segs); gap > 0 {
		segs = append(segs, seg{strings.Repeat(" ", gap), s.Normal})
	}

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

// priCell is the 2-col priority column: flag + letter (A–D).
func (d taskDelegate) priCell(p *int64) seg {
	letter := task.PriorityLetter(p)
	if letter == "" {
		return seg{"  ", d.styles.Normal}
	}
	return seg{d.styles.Glyphs.Flag + letter, d.styles.Priority[*p]}
}

// projectCell is the 10-col `#name` column, tail-ellipsis.
func (d taskDelegate) projectCell(project *string, w int) seg {
	if project == nil {
		return seg{strings.Repeat(" ", w), d.styles.Normal}
	}
	return seg{padRight(truncTail("#"+*project, w, d.styles.Glyphs.Ellipsis), w), d.styles.Project}
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
