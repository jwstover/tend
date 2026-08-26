# Projects & tags — design & phased plan

> Status: **Phases 0 and 1 implemented (2026-08-26)** — migration `00007`, the domain types, and the whole
> `Store` surface are in, with the fixture-based migration test of §2.2 passing and the full suite,
> lint and formatting green. Rehearsed against a copy of the real database (74 tasks, schema 6):
> both project strings migrated to tags with matching counts, every task landed in `Unsorted`, and
> `tasks.project` is gone. Phase 1 added the projects column, the focus refactor, `h`/`l`
> fallthrough, list scoping, capture-target persistence, project CRUD and the `P` move picker;
> suite, lint and gofmt green, and the whole flow re-verified end to end on a fresh copy of the
> real database. Phases 2-4 are unstarted. This is a
> project-specific addendum to `AGENTS.md`, not a replacement for it — the layering (§4), Go conventions (§8), and commit rules (§0.1) still govern everything
> built here. It supersedes `AGENTS.md` §10's "Project hierarchy beyond a single flat `project`
> string" exclusion, which §7 below replaces with a narrower one.

## 0. The problem

`tend` currently holds one flat list of every live task. The owner's tasks span several distinct
efforts (this repo, work repos, home), and a single list forces all of them into view at once.
That is the same failure mode `AGENTS.md` §5 names for the live-view filter — an overwhelming list
stops being read — arriving through a different door.

Today's `tasks.project` is a nullable free-text column: a *label* rendered as a `#name` cell in the
list row (`internal/tui/list.go:463`), settable via the `P` prompt. It cannot group, cannot be
navigated, cannot be counted, and cannot scope a view. It is a tag wearing the wrong name.

The change is a promotion and a rename, in one move:

1. **Projects become a first-class record** — a row in a `projects` table that owns tasks, like a
   workspace. Every task belongs to exactly one. A third TUI column lists them; selecting one
   scopes the task list to it.
2. **Today's `project` string becomes a tag** — what it always was. Tags stay a labelling
   mechanism and gain multi-value support.

## 1. Decisions taken

Settled with the owner before design (2026-08-26):

| # | Question | Decision |
|---|---|---|
| 1 | Tag shape | **Real many-to-many** — `tags` + `task_tags`. A task carries 0..N tags. |
| 2 | Capture target | **The selected project.** The TUI's focused project is the capture target; `tend add` uses a persisted active project. |
| 3 | Existing `project` values | **Become tags only.** Every task lands in a seeded default project; the project list starts effectively empty and is built by hand. |
| 4 | `h`/`l` semantics | **Fall through when there's nothing to collapse** — mirrors the existing `l`-on-a-leaf-opens-detail idiom rather than rebinding the tree keys. |

## 2. Data model

Three new tables plus a key/value settings table, and one new column on `tasks`.

```sql
CREATE TABLE projects (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  archived_at TEXT,                                  -- non-null hides it from the column
  created_at  TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO projects (id, name, sort_order) VALUES (1, 'Unsorted', 0);

CREATE TABLE tags (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE
);

CREATE TABLE task_tags (
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
  PRIMARY KEY (task_id, tag_id)
);
CREATE INDEX idx_task_tags_tag ON task_tags(tag_id);

-- Small key/value bag for state that must outlive a TUI process. First key
-- is active_project_id (§3); showCompleted and the standup toggles are
-- obvious later tenants.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

ALTER TABLE tasks ADD COLUMN project_id INTEGER NOT NULL DEFAULT 1;
CREATE INDEX idx_tasks_project ON tasks(project_id);
```

### 2.1 Why `project_id` has no `REFERENCES` clause

This is not an oversight. **SQLite refuses to add a `REFERENCES` column with a non-NULL default**
when foreign keys are on — and they are, via the DSN (`AGENTS.md` §5). Verified against SQLite
3.51.0:

```
sqlite> ALTER TABLE tasks ADD COLUMN pid INTEGER NOT NULL DEFAULT 1 REFERENCES projects(id);
Runtime error: Cannot add a REFERENCES column with non-NULL default value
```

That leaves three options:

- **A `NOT NULL DEFAULT 1` column with no FK** (chosen). Verified to work, and `ALTER TABLE tasks
  DROP COLUMN project` alongside it also works on 3.35+. The whole migration stays a single
  transactional goose step. Referential integrity for the one operation that can break it —
  deleting a project — moves into `Store.DeleteProject`, which reassigns the project's tasks to
  `Unsorted` inside a transaction before deleting the row. This follows precedent already in the
  schema: `task_events.task_id` and `agent_sessions.status` are both deliberately un-keyed with
  documented reasons.
- **A nullable FK column.** Keeps the FK but abandons the `NOT NULL` invariant that decision (2)
  depends on — "every task belongs to a project" would become untrue in the data and every read
  path would need a nil branch.
- **A full 12-step table rebuild** (create `tasks_new`, copy, drop, rename). Gets both the FK and
  `NOT NULL`, but requires `PRAGMA foreign_keys=OFF`, which cannot be set inside a transaction —
  so the migration needs `-- +goose NO TRANSACTION` and can leave the database half-migrated on
  failure. It also drops and must hand-recreate the three `task_events` triggers, and the
  intermediate `DROP TABLE tasks` would cascade-delete every `agent_sessions` row if FKs were
  left on. Rejected: real risk to a live database, in exchange for an invariant the store already
  has to enforce.

The `DEFAULT 1` earns its keep beyond the migration: `INSERT INTO tasks (title) VALUES (?)` stays
valid, so `CreateTask` — the hot capture path — needs no change for the fallback case.

### 2.2 Data migration (`00007_projects_and_tags.sql`)

```sql
-- Old flat project strings become tags, attached to the same tasks.
-- OR IGNORE because the NOCASE collation folds 'Home' and 'home' together.
INSERT OR IGNORE INTO tags (name)
SELECT DISTINCT trim(project) FROM tasks
WHERE project IS NOT NULL AND trim(project) <> '';

INSERT OR IGNORE INTO task_tags (task_id, tag_id)
SELECT t.id, g.id
FROM tasks t JOIN tags g ON g.name = trim(t.project)
WHERE t.project IS NOT NULL AND trim(t.project) <> '';

ALTER TABLE tasks DROP COLUMN project;
```

Every task keeps `project_id = 1` (`Unsorted`) per decision (3).

The `Down` migration must reverse this — re-add `project TEXT`, restore each task's first tag into
it, drop the new tables. It is lossy for multi-tag tasks by definition; say so in a comment.

**The `DROP COLUMN` is the one destructive step in this plan.** Back up `tend.db` before the first
run, and cover the migration with a store test that seeds a fixture database in the *old* schema,
migrates it, and asserts the tags landed.

### 2.3 Domain types

`internal/task` gains two small types and one field. `Task` stays a pure mirror of its row:

```go
type Project struct {
    ID         int64
    Name       string
    SortOrder  int64
    ArchivedAt *time.Time
    CreatedAt  time.Time
    UpdatedAt  time.Time
}

// Task gains:
//     ProjectID int64
```

Tags are deliberately **not** a field on `Task`. The list view needs tags for every visible row,
and per-row queries would be N+1; the existing idiom for exactly this is a batch map loaded
alongside the tasks and passed to the delegate — `ChildCounts` and `SessionStatuses` both work this
way (`internal/tui/app.go:106-113`). So: `Store.TagsByTask(ctx) (map[int64][]string, error)` for
lists, `Store.TagsForTask(ctx, id) ([]string, error)` for the detail pane and MCP.

## 3. Active project & capture

Decision (2) says capture targets the selected project. The TUI knows its selection; `tend add`
running in a bare shell does not — so the selection has to be persisted.

- `settings['active_project_id']` holds it. The TUI writes it (in a `tea.Cmd`, never inline in
  `Update`) whenever the projects-column cursor lands on a real project.
- **Selecting the `All` row does not write it.** `All` is a view, not a place to put things;
  capture continues to target whichever real project was last selected.
- `tend add` resolves the active project, falling back to `Unsorted` if the key is missing or
  points at a deleted row.

**This is a footgun and the plan should say so plainly:** capturing from an arbitrary shell now
lands the task wherever the TUI last pointed, which may not be where the owner is thinking.
`AGENTS.md` §2 forbids making capture *ask*, so the mitigation is feedback and an override, not a
prompt:

- `tend add` echoes the destination: `added #42 to Unsorted: buy milk`.
- `tend add -p/--project <name>` overrides for one invocation. It **resolves only** — an unknown
  name is an error with a `tend projects add` hint, so a typo can't silently create a project.
- `tend projects use <name>` sets the active project from the shell.

If this proves annoying in practice, the escape hatch is cwd-based inference (a project gains an
optional `cwd` prefix, and `tend add` prefers a match) — designed later, not now.

## 4. TUI layout & focus

### 4.1 Three panes

```
┌─────────────┬───────────────────────────┬──────────────────────┐
│ projects    │ tasks                     │ detail               │
│             │                           │                      │
│ ▸ All    23 │ ● ▸ ⚑A fix the flaky …    │ # fix the flaky test │
│   Unsorted 4│ ○     write the plan      │                      │
│ ▸ tend    7 │ ◐   ▸ ship projects col   │ Sub-tasks 2/5        │
│   hapi   12 │                           │ …                    │
└─────────────┴───────────────────────────┴──────────────────────┘
      20 cols          flex                      existing split
```

- The projects pane is a fixed **20 columns**: name plus a right-aligned live count.
- It is independently toggleable (`[`, mirroring `]` for detail) and **auto-hides below 100 cols**,
  where `splitWidths` already collapses to a single full-width pane.
- `All` is a synthetic first row, not a `projects` table row. Archived projects are hidden.
- The count is live top-level tasks in that project — the same population the list renders as rows,
  so the number matches what selecting it produces.

`splitWidths()` (`app.go:1194`) becomes `paneWidths() (projW, listW, detailW int, full bool)`,
computing `projW` first and handing `width - projW - 1` to the existing detail-split thresholds
unchanged. `ruleLine`/`bottomChrome` take a slice of divider columns instead of a single `splitAt`.

### 4.2 Focus

`detailFocused bool` becomes an explicit three-state focus. This is the one structural refactor in
the plan — 11 call sites plus tests — and it is what makes the rest legible:

```go
type pane int

const (
    paneProjects pane = iota
    paneTasks
    paneDetail
)
```

`h`/`l` per decision (4), extending the fallthrough rule already in `app.go:738-782`:

| Focus | `l` | `h` |
|---|---|---|
| projects | → tasks | (nothing) |
| tasks | branch has children and is collapsed → expand; else → detail | branch expanded → collapse; child row → collapse parent; else → **projects** |
| detail | (scroll) | → tasks |

`esc` keeps backing out one step at a time along the same chain.

### 4.3 Keys while the projects pane is focused

A `handleProjectsKey` branch sits alongside `handleTriageKey`, claiming only its own keys and
letting everything else fall through to the global switch — so `q`, `:`/`ctrl+p`, `?`, `S`, and
`i` keep working from the projects column.

| Key | Action |
|---|---|
| `j`/`k`, `gg`/`G` | move the cursor; a real project writes `active_project_id` and reloads the task list |
| `l`, `⏎` | focus the task list |
| `n` | new project (prompt) |
| `R` | rename the project under the cursor |
| `dd` | delete — reassigns its tasks to `Unsorted`; refuses on `Unsorted` itself |
| `A` | archive / unarchive |

In the task list, `P` changes meaning from "set project string" to **move this task to a project**
(a picker overlay cloned from the palette/urlpicker idiom, per the vault's 2026-06-15 decision on
overlays), and `T` becomes "set tags" — a prompt taking a space/comma-separated list, seeded with
the task's current tags.

**Moving a task moves its subtree.** A sub-task in a different project from its parent is
incoherent; `AddChild` copies the parent's `project_id`, and `SetTaskProject` walks the tree.

## 5. Layer impact

Nothing here violates `AGENTS.md` §4's dependency direction; the new record is just another domain
type flowing outward.

| Package | Work |
|---|---|
| `store/migrations` | `00007_projects_and_tags.sql` (§2) |
| `store/queries` | new `projects.sql`, `tags.sql`; `tasks.sql` gains a project filter + the subtree move |
| `store/gen` | `sqlc generate` — never hand-edited |
| `task` | `Project`; `Task.ProjectID`; tag-name normalization (trim, reject blank, fold case) |
| `store` | project CRUD, `TagsByTask`/`TagsForTask`/`SetTaskTags`, `ActiveProject`/`SetActiveProject`, filtered list queries, `DeleteProject`'s reassign-then-delete transaction |
| `tui` | new `projects.go`; `app.go` focus refactor + pane widths + key routing; `list.go` tag cell; `detail.go` project/tag lines; `keys.go`, `help.go`, `palette.go`, `triage.go` |
| `cli` | new `projects.go`; `add.go` gains `-p`; `ls.go` scopes and renders tags |
| `mcpserver` | `set_task_project` changes meaning; new `set_task_tags`, `list_projects`, `get_current_project` |

### 5.1 Query notes

One filtered query beats two near-duplicates. `sqlc.narg` lets `All` and a scoped project share
a statement:

```sql
-- name: ListLiveTasks :many
SELECT t.*
FROM tasks t
JOIN states s ON s.name = t.state
WHERE s.is_terminal = 0
  AND s.hidden_by_default = 0
  AND (t.snooze_until IS NULL OR t.snooze_until <= date('now'))
  AND (sqlc.narg(project_id) IS NULL OR t.project_id = sqlc.narg(project_id))
ORDER BY s.sort_order, t.priority IS NULL, t.priority, t.id;
```

The subtree move wants a recursive CTE:

```sql
-- name: SetTaskProjectTree :exec
WITH RECURSIVE tree(id) AS (
  SELECT sqlc.arg(id)
  UNION ALL
  SELECT t.id FROM tasks t JOIN tree ON t.parent_id = tree.id
)
UPDATE tasks
SET project_id = sqlc.arg(project_id), updated_at = datetime('now')
WHERE id IN (SELECT id FROM tree);
```

**Resolved 2026-08-26 by probing sqlc v1.31.1 directly, before any Go was written:**

- **`sqlc.narg` twice in one predicate: works.** `ListLiveTasks` keeps the single-query form above.
- **`WITH RECURSIVE` does not work at all** — not feeding an `UPDATE` (`relation "tree" does not
  exist`) and not even feeding a plain `SELECT` (`*ast.ResTarget has nil name`). The subtree move
  became `SetTasksProject`, a single `UPDATE ... WHERE id IN (sqlc.slice(ids))`, with the ids
  collected by `subtreeIDs` — a breadth-first walk in Go over `ListChildIDs`, inside the same
  transaction. `sqlc.slice` in an `UPDATE ... IN` was probed and works, so the move is still one
  write. The walk carries a `seen` set: `parent_id` cannot cycle in practice, but it iterates
  database rows and a cycle would otherwise loop forever.

### 5.2 A sqlc trap worth knowing about

`ListProjects` first failed with `mismatched input 'SELECp'`, and a second query in the same file
failed as collateral. Bisected to a genuine sqlc v1.31.1 bug:

> **sqlc corrupts any query using `alias.*` when that query's comment block contains non-ASCII.**
> It is a byte-vs-rune offset error in star-expansion: one em dash shifts the rewrite and yields
> `SELECp`, two yield `SELp`. Explicit column lists are immune; so is an ASCII-only comment.

This codebase's comments are prose and use em dashes freely, so it is a live trap rather than a
curiosity. Two rules, both applied: **query-file comments stay ASCII-only**, and **`ListProjects`
enumerates its columns** rather than writing `p.*`, so it survives an em dash slipping back in.
Existing queries such as `ListLiveTasks` were unaffected only because a bare `t.*` with no
additional select item needs no rewrite.

## 6. Phases

Per `AGENTS.md` §0, each gate ends in something usable; **stop and report at the end of each one.**

**Phase 0 — Schema and store. Done (2026-08-26).** The migration, `sqlc generate`, domain types,
and every `Store` method above with tests (including the old-schema fixture migration test of
§2.2). User-visible changes kept to the minimum that avoids a broken intermediate state:
`tend ls` renders `#tag` where it rendered `@project`; `tend add` echoes its destination project;
`P` (set project string) became `T` (set tags), taking a space- or comma-separated list seeded
with the task's current tags; and the MCP `set_task_project` became `set_task_tags`. The projects
column, project CRUD and the `P` project-move picker are Phase 1 — `projectFilter` is threaded
through every query and left `nil`, so the views behave exactly as they did before.
*Acceptance — met:* `go test ./...`, `make lint` and `gofmt` all green; a copy of the real
database (74 tasks at schema 6) migrated with both project strings intact as tags at matching
counts.

**Phase 1 — The projects column. Done (2026-08-26).** `internal/tui/projects.go`, the focus
refactor (landed on its own first, as planned), `paneWidths`, `h`/`l`, task-list scoping,
capture-target persistence, project create/rename/delete/archive, and the `P` move picker
(`internal/tui/projectpicker.go`).
*Acceptance — met:* projects create and switch with `h`/`j`/`k`/`l`; the task list scopes to the
selection; `tend add` from a bare shell lands in the last-selected project and names it.

Two refinements the plan didn't anticipate:

- **`Unsorted` is pinned above user projects** (`sort_order = -1` in the seed) rather than sorting
  into the alphabet. It is the bucket everything lands in and every delete falls back to, so it
  belongs next to `All`; letting it float to wherever "U" lands made the one fixed point in the
  column move around.
- **The column yields to the detail split, not the other way round.** Below 100 columns it hides
  outright (as designed), and it *also* hides whenever keeping it would push the detail pane into
  replacing the task list. The list is the primary surface. `resize` moves focus off the column
  whenever it disappears, so the keyboard is never stranded on a pane nobody can see.

**Phase 2 — Tags UI.** The `T` prompt, the multi-tag list cell, tags in the detail pane, and tags
in the list's `/` filter value.
*Acceptance:* a task carries several tags, all visible in the row and editable in one prompt.

**Phase 3 — CLI and MCP surface.** `tend projects` (`ls`/`add`/`use`/`rm`), `tend add -p`,
`tend ls --project/--all`, and the MCP tool changes. The MCP change is breaking — `feat(mcp)!`,
which pre-1.0 is a minor bump (`AGENTS.md` §0.1).
*Acceptance:* a Claude session launched from a task can read its project and set tags.

**Phase 4 — Scoping and polish.** Triage scopes to the selected project (`All` triages
everything); a `project` kind for `task_events` so standup can report moves; project archiving;
`AGENTS.md` §5/§10 updated to match reality.

Standup stays global: it is a time report, not a project view. Easy to revisit.

## 7. Out of scope

Replaces `AGENTS.md` §10's flat-project exclusion with a narrower one. Do not build:

- **Nested projects.** One level. A project has no parent.
- **Per-project settings** — no per-project state machine, no per-project default priority.
- **Tag filtering as a view.** Tags render and are editable; filtering the list *by tag* is a
  separate feature, and `/` already matches tag text.
- **cwd-based project inference** for `tend add` (§3 names it as the escape hatch if the active
  project proves annoying — design it then, on evidence).
- **Project reordering UI.** `sort_order` exists in the schema; nothing writes it but the seed.
- **Moving tags between projects**, project templates, cross-project views beyond `All`.

## 8. Risks

| Risk | Mitigation |
|---|---|
| `DROP COLUMN project` is destructive and irreversible in practice | Back up `tend.db`; fixture-based migration test; a documented (lossy) `Down` |
| sqlc's SQLite parser rejects `narg` or the recursive CTE (§5.1) | Run `sqlc generate` as the *first* task of Phase 0; both fallbacks are named and cheap |
| The focus refactor touches 11 `detailFocused` sites and several TUI tests | Mechanical and compiler-checked; do it as one isolated commit before adding the pane |
| Three panes need ~120 cols to be comfortable | Projects pane auto-hides below 100; `[` toggles it manually |
| Shell capture lands in a surprising project (§3) | `tend add` echoes the destination; `-p` override; `tend projects use` |
| New TUI tests hit the known `collect()` 100ms race | Use the established `waitFor` + `drive(t, m, refreshMsg{})` pattern (vault note, 2026-07-09) |
