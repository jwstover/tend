# Parent vs. child row rendering — a follow-up backlog

> Scratch note for a follow-up, not a design doc.
>
> **Update — the session marker moved to the front gutter, and §3 and §7.3
> went with it.** Both rows now run through one renderer, `renderTaskRow`,
> so the column order is shared by construction. §6's stale comment is
> gone with the function that carried it, and child rows picked up the
> priority cell on the way. What remains open is §7.2 (the meta block on
> child rows — due date above all) and §7.4 (`/` matching sub-tasks).

Two functions in `internal/tui/list.go` draw task rows, and they have
drifted apart:

- `renderRow(it listItem, …)` — a top-level task.
- `renderChildRow(it childItem, …)` — an expanded sub-task at any depth.

The drift matters because a sub-task is a full `task.Task`. It carries
tags, a due date, a priority, its own sub-tasks and its own agent
sessions — the child row just doesn't show most of it.

## 1. Column inventory

Left to right, exactly as each function appends segments.

One renderer draws both now, so the inventory is a single column. Depth 0
is a top-level row; anything deeper is a sub-task.

| # | `renderTaskRow` | at depth 0 |
|---|---|---|
| 1 | **front gutter** (3): selection bar + session marker | same |
| 2 | indent (1 cell per depth level) | zero-width |
| 3 | state dot (2) | same |
| 4 | caret slot (2) | same |
| 5 | priority cell (2) + space | same |
| 6 | title (flexible) | same |
| 7 | gap, padding the row to full width | gap + **meta block** |

Historical, for the record — what the two functions rendered before:

| # | `renderRow` (parent) | `renderChildRow` (child) |
|---|---|---|
| 1 | selection gutter (2) | selection gutter (2) |
| 2 | **state dot** (2) | **indent** (1 cell per depth level) |
| 3 | **session marker** (2) | **caret slot** (2) |
| 4 | **caret slot** (2) | **state dot** (2) |
| 5 | **priority cell** (2) + space | **session marker** (2) |
| 6 | title (flexible) | title (flexible) |
| 7 | gap + **meta block** | pad to width |

Meta block (parent only), fixed-width so columns line up down the list:

- `width >= compactMetaWidth` (78): `tagsCell(12)` · `dueCell(7)` · `subCell(4)`
- narrower: `dueCell(6)` · `subCell(4)` — the tag column is the first thing dropped

## 2. What a child row does not render

| Field | On the task? | Parent row | Child row |
|---|---|---|---|
| Tags | yes | `#a #b +N` | **not shown** |
| Due date | yes | `dueCell`, colour-coded by urgency | **not shown** |
| Priority | yes | `priCell`, flag + A–D | ~~not shown~~ — shown since the gutter move |
| Sub-task count | yes | `subCell`, `N/M` | **not shown** (caret only) |

The due date is the one I'd call an actual bug rather than a style
choice: a sub-task can be overdue and the list gives no sign of it. The
parent's `subCell` counts its children, so an overdue grandchild is
invisible at both levels.

Reproduced directly — a child with `tags=[childtag] due=2026-12-01
priority=true` in the store renders as a bare title, while its parent on
the same screen shows `#parenttag  0/1`.

## 3. Ordering inconsistency — fixed

**Resolved by the gutter move.** One renderer draws both rows, so there is
one order. The inconsistency this section recorded was:

```
parent:  gutter  dot  session  caret  priority  title …
child:   gutter  indent  caret  dot  session  title
```

The state dot and session marker swapped sides of the caret depending on
depth, which is why the columns didn't line up when a branch was expanded.
`TestParentAndChildRowsShareColumnOrder` and
`TestSessionMarkerIsAtTheSameColumnAtEveryDepth` now pin the order so the
two can't drift apart again.

## 4. Filtering

```go
func (i listItem) FilterValue() string  // title + tags
func (i childItem) FilterValue() string { return "" }
```

An empty `FilterValue` keeps child rows out of `/` results entirely, and
that is deliberate (`/` matches top-level tasks, like the section
headings). Consequence worth deciding on: **a sub-task cannot be found by
search at all**, not by title and not by tag. Now that tags exist and a
sub-task can carry its own, that exclusion is worth revisiting.

## 5. Title styling

| | parent | child |
|---|---|---|
| default | `s.Title` | `s.Dimmed` |
| done | `s.TitleDone` | `s.SubDoneText` |
| selected | (no special case) | `s.Title` |

The child needs the `selected` case because its default is dimmed; the
parent doesn't because the selected-row background is applied uniformly
afterwards. Fine as-is, listed for completeness.

## 6. Stale comment — fixed

`renderChildRow`'s doc comment says it draws a "checkbox":

```go
// renderChildRow draws an expanded sub-task at any depth: gutter, depth
// cells of indent (indentation alone conveys nesting), caret slot when the
// node has its own children, checkbox, title.
```

It rendered a **state dot** (`g.State[it.t.State]`), not `g.BoxChecked` /
`g.BoxUnchecked` — the body even explained why ("a sub-task in doing or
blocked has to read as such rather than collapsing to an unchecked box").
The comment predated that change and went away with the merge into
`renderTaskRow`. The checkbox glyphs are still used, but in the detail
pane's sub-task list (`detail.go`), not here.

## 7. Options for the follow-up

Roughly in increasing order of change:

1. ~~**Fix the stale comment.**~~ Done — §6.
2. **Give child rows the meta block**, at least `dueCell`. The child's
   flexible title simply gives up the width, exactly as the parent's
   does. Cheapest real fix, and it closes the overdue-sub-task hole.
3. ~~**Align the column order.**~~ Done — both rows read
   `gutter(bar+session) · indent · dot · caret · priority · title · meta`,
   with indent zero-width at depth 0, and the two functions did collapse
   into one (`renderTaskRow`).
4. **Let `/` match sub-tasks.** Needs a decision about what a matching
   child does to its parent's row — show the parent as context, or hoist
   the child out of the tree for the duration of the filter.

(4) is the one with a real design question left in it; (2) is mechanical.
