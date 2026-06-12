# AGENTS.md — `tend`, a terminal-native personal task tracker

> The project is named `tend` (binary `tend`, module `github.com/jwstover/tend`). Capture dumps tasks in; the TUI is where you *tend* to them.
> This document is the single source of truth for the v1 build. Read it fully before writing code.

---

## 0. How to work on this project

- **Build gate by gate** (see §9). Do not jump ahead. Each gate ends in something the owner can actually use; the whole point of the project is *getting used*, not *getting featured*.
- **Stop at the end of each gate** and report status so the owner can dogfood before the next gate begins.
- Keep the tree compiling at all times. Run `go build ./...` and `go test ./...` after every meaningful change.
- **Do not add anything in the "Out of scope" list (§10)**, even if it seems easy or tempting. Scope creep is the failure mode here.
- Prefer the standard library. This is not a framework project; compose small libraries.
- When a decision isn't specified here, choose the simplest option that respects the layering in §6 and leave a `// TODO(owner):` note rather than inventing scope.

---

## 0.1 Commit convention (Conventional Commits)

Releases are automated. Every commit subject **must** follow Conventional Commits:

```
<type>(<optional scope>): <imperative subject>
```

- **Types:** `feat` (user-visible feature → minor bump), `fix` (bug fix → patch bump), `refactor`, `docs`, `test`, `chore`, `ci`, `build`, `perf`. Only `feat`, `fix`, and breaking changes appear in the CHANGELOG and trigger a release.
- **Scopes** (optional, lowercase): `tui`, `cli`, `store`, `task`, `ci`. Omit when a change spans layers.
- **Breaking changes:** append `!` after the type/scope (`feat(cli)!: …`) or add a `BREAKING CHANGE:` footer. Pre-1.0, these bump the **minor** version, not the major.
- **Keep the existing body style:** a narrative body explaining the *why*, plus the `Co-Authored-By:` trailer. Conventional Commits only constrains the subject line.
- **Releases run via release-please** (see §11): merging to `main` updates a release PR with the CHANGELOG and version bump; merging that PR tags and publishes. **Never hand-edit `CHANGELOG.md` or create tags manually.**

---

## 1. What this is

A fast, keyboard-driven, **terminal-native** personal task/project tracker for a single user who lives in the command line. Inspired by `webstonehq/tuxedo` (a todo.txt TUI) but with a real data model: long-form descriptions, sub-tasks, and a custom workflow.

It is **not** a multi-user app, not a server, not a sync product. One local SQLite file, one user, no daemon, no cloud.

## 2. The core design principle (read this twice)

The system this replaces always failed for one reason: **capture was too slow, so the task list stayed incomplete, so it stopped being trusted, so it got abandoned.**

Therefore the entire design obeys one rule:

> **Capture is a dump. Organization is a separate, later act.**

Concretely:

- Capturing a task requires **nothing** — no project, no due date, no state. A bare title is a complete, valid task.
- The capture command (`tend add`) **must not start the TUI**. It opens the DB, inserts a row, and exits. Target: sub-100ms, perceptually instant.
- All richness — long-form body, sub-tasks, links, state, project — is added **later**, in the TUI detail pane, when the user is *processing*, not when they're *capturing*.
- Captured items land in an `inbox` state. The TUI must make **triage** (processing the inbox) cheap and batched, because an un-triaged inbox is just a graveyard with a friendlier name.

Every feature decision defers to this principle. If a feature adds friction to capture, it goes behind capture or gets cut.

## 3. Tech stack (exact, verified June 2026)

Versions below were current as of June 2026; pull the latest patch releases at build time and adjust if APIs have moved.

**Language / toolchain**
- Go `1.26` (latest stable 1.26.4). Set `go 1.26` in `go.mod`.

**TUI layer — Charm, v2 line** (the v2 release shipped Feb 2026; module paths moved to the `charm.land` vanity domain):
- `charm.land/bubbletea/v2` — the framework (The Elm Architecture: Model / Update / View).
- `charm.land/lipgloss/v2` — styling and layout.
- `charm.land/bubbles/v2` — prebuilt components (use `list`, `viewport`, `textinput`, `key`).
- `charm.land/glamour/v2` — renders the markdown body to styled ANSI for the detail pane. It is pure (same input → same output), so it's safe inside the Update/View loop.

**Data layer**
- `modernc.org/sqlite` — **pure-Go SQLite, no cgo.** This is mandatory: it gives a single static cross-compilable binary. Do NOT use the cgo driver `mattn/go-sqlite3`.
- `sqlc` (v1.31.x) — a **dev tool / code generator**, not a runtime dependency. Write SQL → run `sqlc generate` → get type-safe Go. No ORM, no query builder.
- Access generated code through Go's standard `database/sql` with the modernc driver.

**CLI layer**
- `github.com/spf13/cobra` — command tree (`tend`, `tend add`, `tend ls`, `tend done`).

**Migrations**
- `github.com/pressly/goose/v3` — embed `.sql` migrations via `embed.FS`, apply on startup.

**Explicitly rejected:** any ORM (GORM/ent), any cgo, any web framework. Go composes libraries; it does not need a Phoenix-style framework here.

### A note for the implementer on Bubble Tea

Bubble Tea is The Elm Architecture: a `Model`, `Update(msg) (Model, Cmd)`, and `View()`. The single most important rule: **`Update` stays pure; all side effects are `tea.Cmd`s that return a `Msg`.** A DB read is not a blocking call inside `Update` — it is a `Cmd` that runs off the loop and sends e.g. a `tasksLoadedMsg` back. Keep the Model thin: it holds UI state and dispatches to `store`/`task`; business logic does not live in `Update`.

## 4. Project structure

```
tend/
├── cmd/
│   └── tend/
│       └── main.go          # entrypoint: open DB, run migrations, build Store, dispatch to CLI. THIN — wiring only.
├── internal/
│   ├── task/                # DOMAIN — types + rules, zero I/O. Depends on nothing.
│   │   ├── task.go          #   Task, State, validation
│   │   └── task_test.go
│   ├── store/               # PERSISTENCE — the ONLY place SQL lives.
│   │   ├── migrations/      #   *.sql, embedded via embed.FS
│   │   ├── queries/         #   *.sql — input to sqlc
│   │   ├── gen/             #   sqlc OUTPUT — generated; never hand-edit
│   │   ├── store.go         #   Store: wraps generated Queries, returns domain types, owns transactions
│   │   └── store_test.go    #   tested against a temp SQLite file
│   ├── tui/                 # PRESENTATION — Bubble Tea
│   │   ├── app.go           #   root Model (Init/Update/View)
│   │   ├── list.go          #   list view
│   │   ├── detail.go        #   detail pane (glamour-rendered body)
│   │   ├── triage.go        #   inbox processing view
│   │   ├── keys.go          #   key bindings
│   │   └── styles.go        #   lipgloss styles
│   └── cli/                 # cobra commands
│       ├── root.go
│       ├── add.go
│       ├── ls.go
│       └── done.go
├── sqlc.yaml
├── go.mod
├── go.sum
└── Makefile                 # build, test, generate, install targets
```

**Dependency direction points inward and must never be violated:**

```
cli ─┐
     ├─→ store ─→ task ─→ (nothing)
tui ─┘
```

- `task` (domain) knows nothing about SQLite.
- `store` is the only package that imports the generated SQL code or builds queries.
- `tui` and `cli` depend on `store` **through a `Store` interface they consume**, plus `task` types. They never touch SQL.
- `internal/` is used because the Go compiler forbids imports from outside the module — correct for an application's guts.

## 5. Data model (SQLite)

Two tables. **Links and notes are NOT a separate table** — they live inline in the markdown body (`body_md`) as plain markdown. This was a deliberate simplification.

```sql
CREATE TABLE states (
  name              TEXT PRIMARY KEY,
  sort_order        INTEGER NOT NULL,
  is_terminal       INTEGER NOT NULL DEFAULT 0,  -- done-like; excluded from the live view
  hidden_by_default INTEGER NOT NULL DEFAULT 0   -- e.g. someday/backlog; excluded from the live view
);

-- Seed rows (sort_order, is_terminal, hidden_by_default):
--   inbox   (0,0,0)
--   todo    (1,0,0)
--   doing   (2,0,0)
--   blocked (3,0,0)
--   done    (4,1,0)
--   someday (5,0,1)

CREATE TABLE tasks (
  id           INTEGER PRIMARY KEY,
  title        TEXT NOT NULL,
  body_md      TEXT NOT NULL DEFAULT '',         -- long-form description + links + notes; rendered with glamour
  state        TEXT NOT NULL DEFAULT 'inbox' REFERENCES states(name),
  parent_id    INTEGER REFERENCES tasks(id) ON DELETE CASCADE,  -- sub-tasks via self-reference
  project      TEXT,                             -- flat string, nullable (no hierarchy in v1)
  priority     INTEGER,                          -- nullable; 1 = highest
  due          TEXT,                             -- ISO 8601 date, nullable
  snooze_until TEXT,                             -- ISO date; while set and in the future, hide from the live view
  created_at   TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
  completed_at TEXT
);

CREATE INDEX idx_tasks_state  ON tasks(state);
CREATE INDEX idx_tasks_parent ON tasks(parent_id);
```

Semantics:
- **Sub-tasks** are `parent_id` self-references; compute child completion for a progress indicator.
- **Long-form body** is the re-entry-cost killer: a task carries its own context (what it is, the next action, the Jira link, the conversation link) so resuming it is free.
- **`snooze_until`** is the resurfacing mechanism: defer a task and it leaves the live view until its wake date.
- **Live view** = tasks whose state has `is_terminal = 0` AND `hidden_by_default = 0` AND (`snooze_until` is null OR in the past). This default filter is what stops the list becoming overwhelming.

### Connection / DSN

SQLite is single-writer. Both `tend add` and the TUI may open the file, so open in WAL mode with a busy timeout:

```
file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)
```

DB path: default to `${XDG_DATA_HOME:-$HOME/.local/share}/tend/tend.db`, overridable via `--db` flag and a `TEND_DB` env var. Create the directory if missing.

### sqlc config (`sqlc.yaml`)

```yaml
version: "2"
sql:
  - engine: "sqlite"
    schema: "internal/store/migrations"
    queries: "internal/store/queries"
    gen:
      go:
        package: "gen"
        out: "internal/store/gen"
        emit_interface: true
        emit_json_tags: false
        emit_empty_slices: true
```

## 6. Command surface

| Command | Behavior |
| --- | --- |
| `tend` | Launch the TUI (the no-arg path) |
| `tend add "<text>"` / `tend a "<text>"` | Instant capture to `inbox`. No TUI. Also reads from stdin: `echo "..." \| tend a` |
| `tend ls` | Plain-text dump of the live view to stdout (scriptable, no TUI) |
| `tend done <id>` | Mark a task complete from the shell |

Global flags: `--db <path>`.

## 7. TUI

Built on Bubble Tea v2 + Bubbles v2 + Lip Gloss v2; Glamour v2 renders the body.

- **List view (default).** Shows the live view only (see filter in §5). Vim navigation (`j`/`k`/`gg`/`G`), `/` to search, `:` / `Ctrl-P` command palette, `n` for in-app quick add.
- **Detail pane (`]` toggles).** The heart of the tool: glamour-rendered markdown body (where links, notes, and context live), plus a sub-task checklist. Include simple URL detection so the user can open a link found in the body (under the cursor, or all of them) via the OS opener.
- **Triage view.** Filtered to `inbox`. Fast keys to set state, assign a project/due, open the body in `$EDITOR` to flesh it out, or send to `someday`/`done`. This is the batched processing pass.
- **Editing the body.** Shell out to `$EDITOR` (do not build an in-terminal markdown editor in v1).

## 8. Conventions (Go, for a developer new to Go)

- **`Store` wraps sqlc.** sqlc generates a `Queries` struct; wrap it in a `Store` that returns `task.Task` values and owns transactions. `tui` and `cli` depend on a small `Store` *interface* they declare ("accept interfaces, return structs"). This keeps SQL from leaking upward and makes the UI testable without a real DB.
- **Errors are values, wrapped with `%w`:** `fmt.Errorf("loading task %d: %w", id, err)`. Match with `errors.Is`/`errors.As`. Handle errors at the boundaries (a Cobra command, a Bubble Tea Cmd), not deep in `store`.
- **Thread `context.Context`** as the first argument of every `store` method, even before cancellation is needed.
- **`Update` is pure; side effects are `Cmd`s** (see §3 note).
- **Generated code is never hand-edited.** Regenerate with `sqlc generate` (wire it into `make generate` and a `//go:generate` directive).
- **Tests:** standard-library `testing`, table-driven. Concentrate coverage on `task` (the rules) and `store` (against a temp SQLite file). Verify the TUI mostly by using it.

## 9. Build gates

Build in this order. Each gate is independently usable; **stop and report at the end of each one.**

**Gate 1 — Capture (no TUI).**
Deliver: `go.mod`, `sqlc.yaml`, schema + migrations (run on startup via goose), the `Store` with its interface, and the `tend add` / `tend a` / `tend ls` commands.
Acceptance: from a cold shell, `tend add "something"` returns near-instantly and the row is queryable via `tend ls`. No TUI exists yet.
*This is the most important gate. If capture isn't effortless here, fix that before anything else — it's the entire premise.*

**Gate 2 — See and process.**
Deliver: the TUI list view, the detail pane (glamour body + sub-task checklist), and the triage view (inbox filter, set state/project/due, edit body in `$EDITOR`).
Acceptance: the owner can read back captured tasks, flesh them out, and move them out of the inbox.

**Gate 3 — Workflow.**
Deliver: full state transitions, snooze/defer with `snooze_until`, the live-vs-hidden default filtering, and completion.
Acceptance: deferred and terminal/hidden tasks disappear from the default view and resurface correctly.

Anything past Gate 3 is a *wanted* feature — which is exactly when to be suspicious, because the goal is using the tool, not grooming it.

## 10. Out of scope for v1 (do not build)

- Sync / multi-device
- Phone capture / PWA
- The external append-only inbox-file drain (schema supports it; build later)
- Recurrence
- Natural-language parsing of the add prompt
- Project hierarchy beyond a single flat `project` string
- Themes, densities, saved searches
- Anything requiring a network call

---

## 11. Versioning & releases

Fully automated; no manual tagging. The flow:

1. **Commit** to `main` using Conventional Commits (§0.1).
2. **release-please** (`.github/workflows/release.yml`) maintains an open `chore(main): release X.Y.Z` PR with the generated `CHANGELOG.md` and version bump. It owns the changelog and the GitHub release notes — don't touch them by hand.
3. **Merge that PR** to cut a release: release-please creates the `vX.Y.Z` tag and GitHub release, and **GoReleaser** (gated on `release_created` in the same workflow) cross-compiles static binaries (darwin/linux × amd64/arm64, `CGO_ENABLED=0`) and attaches the archives + `checksums.txt`.

**Versioning rules** (pre-1.0, set in `release-please-config.json`): `feat` → minor, `fix` → patch, breaking → minor.

**Version reporting:** `internal/version` exposes `String()`. It returns the ldflags-stamped value in release builds, falling back to `runtime/debug` build info so `go install …@vX.Y.Z` and local builds still report a sensible version. Surfaced via `tend version` and `tend --version` (cobra's `Version` field, wired in `internal/cli/root.go`).

**Local checks:** `make release-check` (validates `.goreleaser.yaml`) and `make snapshot` (builds artifacts into `dist/` without tagging or publishing).

> One-time follow-up: after `v0.1.0` ships, remove `"release-as": "0.1.0"` from `release-please-config.json` (a `chore:` commit) so version inference takes over.
