# Parent vs. child row rendering — a follow-up backlog

> Scratch note for a follow-up, not a design doc. Everything below is
> **pre-existing behaviour**, unchanged by the projects/tags work:
> `renderChildRow` is byte-identical to its state at `567ad38`, the commit
> the `feat/projects-and-tags` branch started from.

Two functions in `internal/tui/list.go` draw task rows, and they have
drifted apart:

- `renderRow(it listItem, …)` — a top-level task.
- `renderChildRow(it childItem, …)` — an expanded sub-task at any depth.

The drift matters because a sub-task is a full `task.Task`. It carries
tags, a due date, a priority, its own sub-tasks and its own agent
sessions — the child row just doesn't show most of it.

## 1. Column inventory

Left to right, exactly as each function appends segments.

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
| Priority | yes | `priCell`, flag + A–D | **not shown** |
| Sub-task count | yes | `subCell`, `N/M` | **not shown** (caret only) |

The due date is the one I'd call an actual bug rather than a style
choice: a sub-task can be overdue and the list gives no sign of it. The
parent's `subCell` counts its children, so an overdue grandchild is
invisible at both levels.

Reproduced directly — a child with `tags=[childtag] due=2026-12-01
priority=true` in the store renders as a bare title, while its parent on
the same screen shows `#parenttag  0/1`.

## 3. Ordering inconsistency

The shared columns are in a **different order** on the two rows:

```
parent:  gutter  dot  session  caret  priority  title …
child:   gutter  indent  caret  dot  session  title
```

So the state dot and session marker swap sides of the caret depending on
depth. Nothing depends on this, but it makes the two functions harder to
read as variations of one thing, and it's why the columns don't visually
line up when a branch is expanded.

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

## 6. Stale comment

`renderChildRow`'s doc comment says it draws a "checkbox":

```go
// renderChildRow draws an expanded sub-task at any depth: gutter, depth
// cells of indent (indentation alone conveys nesting), caret slot when the
// node has its own children, checkbox, title.
```

It renders a **state dot** (`g.State[it.t.State]`), not `g.BoxChecked` /
`g.BoxUnchecked` — the body even explains why ("a sub-task in doing or
blocked has to read as such rather than collapsing to an unchecked box").
The comment predates that change. The checkbox glyphs are still used, but
in the detail pane's sub-task list (`detail.go`), not here.

## 7. Options for the follow-up

Roughly in increasing order of change:

1. **Fix the stale comment.** Free.
2. **Give child rows the meta block**, at least `dueCell`. The child's
   flexible title simply gives up the width, exactly as the parent's
   does. Cheapest real fix, and it closes the overdue-sub-task hole.
3. **Align the column order** so both rows read
   `gutter · indent · dot · session · caret · priority · title · meta`,
   with indent zero-width at depth 0. This is the version where the two
   functions could genuinely collapse into one.
4. **Let `/` match sub-tasks.** Needs a decision about what a matching
   child does to its parent's row — show the parent as context, or hoist
   the child out of the tree for the duration of the filter.

(3) and (4) are the ones with real design questions in them; (1) and (2)
are mechanical.
