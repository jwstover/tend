# AGENTS.md — `tend`, a terminal-native personal task tracker

> The project is named `tend` (binary `tend`, module `github.com/jwstover/tend`). Capture dumps tasks in; the TUI is where you *tend* to them.
> This document orients a Claude Code session on the tech stack, architecture, and purpose of the project as they exist today. It is not a roadmap — planned and in-progress work lives in `tend` itself, not here.

---

## 0. How to work on this project

- Keep the tree compiling and green: run `go build ./...` and `go test ./...` after every meaningful change; run `make lint` (`golangci-lint`) before considering work done.
- Prefer the standard library. The non-stdlib dependencies in §3 are each a deliberate, justified exception, not a precedent for adding more casually.
- When a decision isn't specified here, choose the simplest option that respects the layering in §4 and leave a `// TODO(owner):` note rather than inventing scope.
- This is a personal, single-user tool. It gets used daily, so correctness and a fast capture path matter more than generality.

---

## 0.1 Commit convention (Conventional Commits)

Releases are automated. Every commit subject **must** follow Conventional Commits:

```
<type>(<optional scope>): <imperative subject>
```

- **Types:** `feat` (user-visible feature → minor bump), `fix` (bug fix → patch bump), `refactor`, `docs`, `test`, `chore`, `ci`, `build`, `perf`. Only `feat`, `fix`, and breaking changes appear in the CHANGELOG and trigger a release.
- **Scopes** (optional, lowercase): `tui`, `cli`, `store`, `task`, `agent`, `mcp`, `jira`, `ci`. Omit when a change spans layers.
- **Breaking changes:** append `!` after the type/scope (`feat(cli)!: …`) or add a `BREAKING CHANGE:` footer. Pre-1.0, these bump the **minor** version, not the major.
- **Keep the existing body style:** a narrative body explaining the *why*, plus the `Co-Authored-By:` trailer. Conventional Commits only constrains the subject line.
- **Releases run via release-please** (see §11): merging to `main` updates a release PR with the CHANGELOG and version bump; merging that PR tags and publishes. **Never hand-edit `CHANGELOG.md` or create tags manually.**

---

## 1. What this is

A fast, keyboard-driven, terminal-native personal task/project tracker for a single user who lives in the command line. It started as a todo.txt-style TUI (inspired by `webstonehq/tuxedo`) with a real data model — long-form descriptions, sub-tasks, a custom workflow — and has since grown a second, closely related purpose: giving Claude Code agent sessions a durable, task-shaped home to work from.

Concretely, `tend` now does two things:

1. **Tracks tasks.** One local SQLite file, one user, no daemon, no multi-user sync.
2. **Manages Claude Code sessions bound to those tasks.** It launches, backgrounds, and resumes `claude` sessions tied to a task (via `tmux`), observes their status, logs a recap when one ends, and exposes the bound task's read/write surface to the running session over MCP.

It also makes one narrow, non-blocking outbound network call: expanding a pasted Jira issue URL into a real title. That call — and everything the Claude Code integration shells out to — always degrades quietly on failure; nothing in `tend`'s own workflow blocks on the network or on an external binary being present.

## 2. The core design principle (read this twice)

The system this replaces always failed for one reason: **capture was too slow, so the task list stayed incomplete, so it stopped being trusted, so it got abandoned.**

Therefore the entire design obeys one rule:

> **Capture is a dump. Organization is a separate, later act.**

Concretely:

- Capturing a task requires **nothing** — no project, no due date, no state. A bare title is a complete, valid task.
- The capture command (`tend add`) **does not start the TUI**. It opens the DB, inserts a row, and exits — sub-100ms, perceptually instant. A pasted Jira URL still returns fast: the summary lookup is a best-effort enrichment, not a gate.
- All richness — long-form body, sub-tasks, links, state, project, agent sessions — is added **later**, in the TUI, when the user is *processing*, not when they're *capturing*.
- Captured items land in an `inbox` state. Triage (processing the inbox) is a first-class, batched TUI flow, because an un-triaged inbox is just a graveyard with a friendlier name.

Every feature decision defers to this principle. If a feature adds friction to capture, it goes behind capture.

## 3. Tech stack

**Language / toolchain**
- Go `1.26.4` (see `go.mod`).

**TUI layer — Charm, v2 line:**
- `charm.land/bubbletea/v2` — the framework (The Elm Architecture: Model / Update / View).
- `charm.land/lipgloss/v2` — styling and layout.
- `charm.land/bubbles/v2` — prebuilt components (`list`, `viewport`, `textinput`, `key`).
- `charm.land/glamour/v2` — renders the markdown body to styled ANSI for the detail pane. Pure (same input → same output), so it's safe inside the Update/View loop.

**Data layer**
- `modernc.org/sqlite` — pure-Go SQLite, no cgo. This gives a single static, cross-compilable binary. Do not use a cgo driver.
- `sqlc` — a dev tool / code generator, not a runtime dependency. Write SQL in `internal/store/queries`, run `sqlc generate` (or `make generate`), get type-safe Go in `internal/store/gen`. No ORM, no query builder.
- `github.com/pressly/goose/v3` — embeds `.sql` migrations (`internal/store/migrations`, via `embed.FS`) and applies them on startup.

**CLI layer**
- `github.com/spf13/cobra` — the command tree.

**Agent-session layer (Claude Code integration)**
- No Go dependency here — `internal/agent` shells out directly to two external binaries, each treated as an optional capability, not a hard requirement:
  - `claude` — the Claude Code CLI. Launched/resumed via `exec.Cmd` built by `internal/agent`, handed to the TUI's `tea.ExecProcess` for the terminal handoff.
  - `tmux` — wraps `claude` so a session can be backgrounded and later reattached (including from a second `tend` process). Runs on a dedicated `-L tend` socket with a generated config; never touches the user's own tmux server or config.
- `github.com/modelcontextprotocol/go-sdk` — the official Go MCP SDK, used by `internal/mcpserver` to expose task read/write tools to a running `claude` session over stdio.

**Jira integration**
- `net/http` (stdlib) — a single GET per pasted issue URL, bounded by a short timeout.
- `github.com/zalando/go-keyring` — stores Jira credentials in the OS keychain, never in a config file.
- `golang.org/x/term` — reads the API token from a terminal without echoing it.

**Explicitly rejected:** any ORM (GORM/ent), any cgo, any web framework. Go composes libraries; it does not need a Phoenix-style framework here.

### A note on Bubble Tea

Bubble Tea is The Elm Architecture: a `Model`, `Update(msg) (Model, Cmd)`, and `View()`. The single most important rule: **`Update` stays pure; all side effects are `tea.Cmd`s that return a `Msg`.** A DB read, a `claude`/`tmux` invocation, or a Jira lookup is never a blocking call inside `Update` — it's a `Cmd` that runs off the loop and sends a message back (e.g. `tasksLoadedMsg`, `sessionFinishedMsg`, `pollTickMsg`). Keep the Model thin: it holds UI state and dispatches to `store`/`task`/`agent`; business logic does not live in `Update`.

## 4. Project structure

```
tend/
├── cmd/
│   └── tend/
│       └── main.go          # entrypoint: wires the concrete Store/MCP-Store/TUI runner into cli.Execute. THIN — wiring only.
├── internal/
│   ├── task/                 # DOMAIN — types + rules, zero I/O. Depends on nothing.
│   │   ├── task.go           #   Task, State, priority/date normalization
│   │   ├── session.go        #   Session, SessionStatus — a Claude Code session bound to a task
│   │   ├── event.go          #   Event (activity log), Summarize (standup aggregation)
│   │   └── log.go            #   LogEntry (manual standup notes), StandupMarkdown rendering
│   ├── store/                 # PERSISTENCE — the ONLY place SQL lives.
│   │   ├── migrations/        #   *.sql, embedded via embed.FS, applied by goose on startup
│   │   ├── queries/           #   *.sql — input to sqlc
│   │   ├── gen/                #   sqlc OUTPUT — generated; never hand-edited
│   │   └── store.go            #   Store: wraps generated Queries, returns domain types, owns transactions
│   ├── agent/                  # I/O EDGE — the only package that shells out to `claude`/`tmux`
│   │   ├── agent.go             #   LaunchCmd/ResumeCmd — builds the exec.Cmd; never runs it
│   │   ├── tmux.go               #   dedicated-socket tmux wrapping: wrap/attach/has-session/kill/capture-pane
│   │   ├── hooks.go              #   Claude Code hook payload parsing + injected --settings generation
│   │   ├── status.go             #   ClassifyPane — capture-pane text → "working", for the one status no hook reports
│   │   ├── mcp_config.go          #   per-session --mcp-config file pointing at `tend mcp --task-id <id>`
│   │   └── session_id.go           #   UUIDv4 generation for --session-id
│   ├── jira/                    # I/O EDGE — the only package that talks to the Jira REST API / keychain
│   │   ├── jira.go               #   URL parsing, issue summary fetch (bounded timeout, degrades to the bare key)
│   │   └── keyring.go             #   credential storage via the OS keychain
│   ├── mcpserver/                # MCP tool surface — third consumer of Store, alongside tui and cli
│   │   ├── server.go               #   builds the MCP server bound to one task, runs the stdio transport
│   │   ├── tools.go                 #   tool schemas + handlers (get_current_task, create_subtask, set_task_state, ...)
│   │   └── store.go                  #   mcpserver's own narrow Store interface
│   ├── tui/                       # PRESENTATION — Bubble Tea
│   │   ├── app.go                   #   root Model (Init/Update/View), message wiring
│   │   ├── list.go                   #   grouped/tree list view
│   │   ├── detail.go                  #   detail pane: glamour body, sub-tasks, SESSIONS, LOG
│   │   ├── triage.go                   #   inbox processing view
│   │   ├── standup.go                   #   standup view: notes + activity summary, yank to clipboard
│   │   ├── sessions.go                   #   launch/resume/background tea.Cmds, session picker, recap + status polling
│   │   ├── palette.go / urlpicker.go / whichkey.go / modal.go / help.go   #   supporting overlays
│   │   ├── keys.go                       #   key bindings (source of truth — see the in-app `?` help too)
│   │   └── styles.go                      #   lipgloss styles, status glyphs
│   └── cli/                        # cobra commands
│       ├── root.go                   #   command tree, Store/MCPStoreFactory/TUIRunner interfaces, --db resolution
│       ├── add.go / ls.go / log.go / standup.go   #   fast, scriptable one-shots
│       ├── auth.go                    #   `tend auth jira {login,status,logout}`
│       ├── mcp.go                      #   hidden `tend mcp --task-id <id>`, spawned by a launched claude session
│       └── agent_hook.go                #   hidden `tend agent-hook <event>`, spawned by Claude Code's own hooks
├── docs/                          # design notes for past feature work; not required reading to orient
├── sqlc.yaml
├── go.mod
└── Makefile                       # build, test, lint, generate, install, snapshot, release-check
```

**Dependency direction points inward and must never be violated:**

```
cli ──┬──→ store ──→ task ──→ (nothing)
      ├──→ mcpserver ──→ (its own Store interface, satisfied by *store.Store)
      ├──→ agent   (process control: claude/tmux, hook parsing, session-id/mcp-config generation)
      └──→ jira    (REST + keychain)

tui ──┴──→ store, agent, jira   (same rules — tui never touches SQL, exec, or HTTP directly outside these)
```

- `task` (domain) knows nothing about SQLite, exec, or HTTP.
- `store` is the only package that imports the generated SQL code or builds queries.
- `agent` is the only package that builds `claude`/`tmux` commands or parses Claude Code hook payloads; it never runs a command itself, so it stays testable without a real terminal.
- `jira` is the only package that calls the Jira REST API or touches the OS keychain.
- `mcpserver` depends on `task` and declares its own `Store` interface (same "accept interfaces, return structs" convention as `cli`) rather than importing `store` directly.
- `tui` and `cli` consume `store`/`agent`/`jira`/`mcpserver` through interfaces they declare, plus `task` types. They never touch SQL, exec, or HTTP directly.
- `internal/` is used because the Go compiler forbids imports from outside the module — correct for an application's guts.

## 5. Data model (SQLite)

```sql
CREATE TABLE states (
  name              TEXT PRIMARY KEY,
  sort_order        INTEGER NOT NULL,
  is_terminal       INTEGER NOT NULL DEFAULT 0,  -- done-like; excluded from the live view
  hidden_by_default INTEGER NOT NULL DEFAULT 0   -- e.g. someday/backlog; excluded from the live view
);
-- Seed rows: inbox(0), todo(1), doing(2), blocked(3), done(4,terminal), someday(5,hidden)

CREATE TABLE tasks (
  id           INTEGER PRIMARY KEY,
  title        TEXT NOT NULL,
  body_md      TEXT NOT NULL DEFAULT '',         -- long-form description + links + notes; rendered with glamour
  state        TEXT NOT NULL DEFAULT 'inbox' REFERENCES states(name),
  parent_id    INTEGER REFERENCES tasks(id) ON DELETE CASCADE,  -- sub-tasks via self-reference
  project      TEXT,
  priority     INTEGER,                          -- nullable; 1 (highest, "A") .. 4 ("D")
  due          TEXT,                              -- ISO 8601 date, nullable
  snooze_until TEXT,                               -- ISO date; while set and in the future, hidden from the live view
  created_at, updated_at, completed_at TEXT
);

CREATE TABLE task_events (       -- append-only activity log behind `tend standup`; not a foreign key to tasks —
  id, task_id, task_title,        -- the log must outlive the tasks it describes. Populated by AFTER INSERT/UPDATE/DELETE
  kind TEXT CHECK (kind IN         -- triggers on `tasks`, not Go-layer writes, so OLD/NEW state is free and
    ('created','state','deleted')),  -- cascade-deleted sub-tasks still get an event.
  old_value, new_value, created_at
);

CREATE TABLE log_entries (        -- manual standup notes (TUI `U`/`N`, or `tend log`); task_id is optional
  id, task_id, body, created_at    -- context, not a foreign key, for the same reason as task_events.
);

CREATE TABLE agent_sessions (
  id, task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,  -- unlike the two logs above, a session
  external_id TEXT NOT NULL,        -- the claude --session-id UUID       has no reason to outlive its task.
  cwd, label TEXT NOT NULL,          -- working dir; label is the task title snapshotted at launch, then
                                       -- overwritten by an auto-generated per-session label (see §8).
  tmux_session TEXT NOT NULL DEFAULT '',   -- name of the wrapping tmux session; empty = not attachable
  needs_recap INTEGER NOT NULL DEFAULT 0,   -- set when backgrounded rather than exited; drained once really over
  status TEXT NOT NULL DEFAULT 'unknown',    -- unknown/starting/working/idle/blocked/ended — a cache of an
  status_updated_at TEXT,                     -- out-of-band observation (hooks + polling), not a workflow;
  started_at, last_active_at TEXT NOT NULL     -- nullable status_updated_at distinguishes "never observed"
);                                              -- from "observed at row creation".
```

Semantics:
- **Sub-tasks** are `parent_id` self-references; the UI computes child completion for a progress indicator and gives sub-tasks the same detail-pane functionality as top-level tasks (body, log, sessions).
- **Long-form body** is the re-entry-cost killer: a task carries its own context so resuming it is free.
- **`snooze_until`** defers a task out of the live view until its wake date.
- **Live view** = tasks whose state has `is_terminal = 0` AND `hidden_by_default = 0` AND (`snooze_until` is null OR in the past).
- **Agent sessions** are per-task, not per-project: a task can have many sessions (e.g. one per repo it touches), each independently launchable/resumable/backgroundable. See §8 for the full lifecycle.

### Connection / DSN

SQLite is single-writer. `tend add`, the TUI, `tend mcp`, and `tend agent-hook` may all open the file concurrently, so it's opened in WAL mode with a busy timeout:

```
file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)
```

DB path: `${XDG_DATA_HOME:-$HOME/.local/share}/tend/tend.db` by default, overridable via `--db` or `TEND_DB`. The directory is created if missing.

### sqlc config (`sqlc.yaml`)

Schema in `internal/store/migrations`, queries in `internal/store/queries`, generated Go in `internal/store/gen` (package `gen`, with `emit_interface: true`). Regenerate with `sqlc generate` or `make generate` — never hand-edit the output.

## 6. Command surface

| Command | Behavior |
| --- | --- |
| `tend` | Launch the TUI (the no-arg path) |
| `tend add "<text>"` / `tend a` | Instant capture to `inbox`. No TUI. Reads stdin too (`echo "..." \| tend a`). A pasted Jira issue URL is expanded to a real title via one bounded, best-effort REST call. |
| `tend ls` | Plain-text dump of the live view to stdout (scriptable, no TUI) |
| `tend log "<note>"` | Capture a standup note instantly, no TUI |
| `tend standup` | Print a standup summary of recent activity as markdown |
| `tend auth jira login/status/logout` | Manage Jira credentials in the system keychain |
| `tend mcp --task-id <id>` | Hidden. Runs tend's MCP server over stdio, bound to one task — spawned by a launched `claude` session, never by the user directly. |
| `tend agent-hook <event>` | Hidden. Records a Claude Code hook event (session status) against its session — spawned by Claude Code itself via injected `--settings`. |
| `tend version` | Print the version |

Global flag: `--db <path>`.

There is no `tend done` — completing, deleting, and every other state transition happens in the TUI, which is where triage and task management actually live.

## 7. TUI

Built on Bubble Tea v2 + Bubbles v2 + Lip Gloss v2; Glamour v2 renders the body.

- **List view (default).** A grouped/tree view of the live view (sub-tasks nest under their parent), with vim-style navigation, search, a `:`/`Ctrl-P` command palette, and quick add.
- **Detail pane.** The heart of the tool: glamour-rendered markdown body, a sub-task checklist, a `SESSIONS` section (this task's Claude Code sessions — launch, resume, or attach to a backgrounded one), and a `LOG` section (manual notes plus auto-generated session recaps). Scrollable and independently focusable so long histories are reachable. URL detection lets the user open a link under the cursor or all of them via the OS opener.
- **Triage view.** Filtered to `inbox`. Fast keys to set state, assign a project/due, open the body in `$EDITOR`, or send to `someday`/`done` — the batched processing pass.
- **Standup view.** Manual notes grouped by task plus a generated activity summary (completed/blocked/started, derived from `task_events`); yank the whole thing as markdown.
- **Editing the body.** Shells out to `$EDITOR` — there is no in-terminal markdown editor.

Full key bindings live in `internal/tui/keys.go` and are discoverable in-app via `?` — not duplicated here since they're a fast-moving implementation detail, not architecture.

## 8. Agent sessions (Claude Code integration)

A task can have one or more Claude Code sessions bound to it, launched and managed from the detail pane's `SESSIONS` section (`r`). This is the mechanism by which a task carries not just its own notes but a live or resumable record of the agent work done against it.

- **Launch/resume.** `internal/agent.LaunchCmd`/`ResumeCmd` build the `claude` invocation; the TUI hands it to `tea.ExecProcess` for the terminal handoff. A session's `claude --session-id` is generated up front (`session_id.go`) so tend never has to discover it after the fact.
- **Backgrounding.** Sessions run inside `claude` wrapped in `tmux`, on a dedicated `-L tend` socket with a generated, hands-off config (`internal/agent/tmux.go`). Detaching (`C-h` or `C-Space d`) returns to tend while `claude` keeps running; resuming a task with a live backgrounded session re-attaches instead of starting a second process. Any `tend` instance on the host can attach, since it's the same socket and the same shared SQLite file.
- **Status.** `agent_sessions.status` is populated two ways: Claude Code hooks (`SessionStart`/`Stop`/`Notification`/`SessionEnd`), injected via a per-session `--settings` file and reported through the hidden `tend agent-hook` command; and, for the one state no hook covers (actively generating/running a tool, "working"), a poller in `internal/tui` that reads the pane's rendered text via `tmux capture-pane` and classifies it (`internal/agent/status.go`). Hook-reported status always wins a race against the poller's guess (a compare-and-swap on `status_updated_at`).
- **Recap.** When a session's terminal handoff returns (ended, not backgrounded), tend fires a headless `claude -p --resume` follow-up asking for a short label + recap, and logs the recap as a normal `LogEntry` on the task — the fix for "I took a break and lost the thread." A session backgrounded instead of exited defers this (`needs_recap`) until some tend instance observes it's really gone.
- **MCP.** `tend mcp --task-id <id>` is spawned by `claude` itself (via a per-session `--mcp-config` tend writes at launch, `internal/agent/mcp_config.go`) and gives that session direct read/write access to its bound task — creating sub-tasks, updating the body, changing state, logging notes — without inventing a scratch markdown file. See `internal/mcpserver` for the tool set.

## 9. Jira integration

Pasting a Jira issue URL (a `/browse/KEY` link or a board URL with `selectedIssue=KEY`) into `tend add` expands it to `KEY: <issue summary>` via one GET to the Jira REST API, bounded by a short timeout. Credentials (site, email, API token) are collected once via `tend auth jira login` and stored in the OS keychain (`internal/jira/keyring.go`) — never in a config file. Any failure — no credentials, network error, timeout — degrades to capturing the bare key or URL; capture must never block on this.

## 10. Conventions (Go)

- **`Store` wraps sqlc.** sqlc generates a `Queries` struct; `store.Store` wraps it, returning `task.Task`/`task.Session`/etc. values and owning transactions. Consumers (`tui`, `cli`, `mcpserver`) depend on a small `Store` *interface* they declare ("accept interfaces, return structs"), keeping SQL from leaking upward.
- **Errors are values, wrapped with `%w`:** `fmt.Errorf("loading task %d: %w", id, err)`. Match with `errors.Is`/`errors.As`. Handle errors at the boundaries (a Cobra command, a Bubble Tea Cmd), not deep in `store`/`agent`/`jira`.
- **Thread `context.Context`** as the first argument of every `store` method.
- **`Update` is pure; side effects are `Cmd`s** (see §3).
- **Generated code is never hand-edited.** Regenerate with `sqlc generate` / `make generate`.
- **Degrade quietly at every I/O edge.** A missing `claude`/`tmux` binary, a failed Jira lookup, an unparseable hook payload — none of these should error loudly into the user's face when a quieter fallback exists. Reserve hard failures for things the user must act on.
- **Tests:** standard-library `testing`, table-driven. Concentrate coverage on `task` (rules), `store` (against a temp SQLite file), and `agent` (argv/parsing logic, with real-binary integration tests where practical). Verify the TUI mostly by using it.

## 11. Versioning & releases

Fully automated; no manual tagging. The flow:

1. **Commit** to `main` using Conventional Commits (§0.1).
2. **release-please** (`.github/workflows/release.yml`) maintains an open `chore(main): release X.Y.Z` PR with the generated `CHANGELOG.md` and version bump. It owns the changelog and the GitHub release notes — don't touch them by hand.
3. **Merge that PR** to cut a release: release-please creates the `vX.Y.Z` tag and GitHub release, and **GoReleaser** (gated on `release_created` in the same workflow) cross-compiles static binaries (darwin/linux × amd64/arm64, `CGO_ENABLED=0`) and attaches the archives + `checksums.txt`.

**Versioning rules** (pre-1.0, set in `release-please-config.json`): `feat` → minor, `fix` → patch, breaking → minor.

**Version reporting:** `internal/version` exposes `String()`, ldflags-stamped in release builds, falling back to `runtime/debug` build info otherwise. Surfaced via `tend version` and `tend --version`.

**Local checks:** `make release-check` (validates `.goreleaser.yaml`) and `make snapshot` (builds artifacts into `dist/` without tagging or publishing).
