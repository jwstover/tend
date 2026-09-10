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

1. **Tracks tasks.** One local SQLite file, one user, no daemon, no multi-user sync. Every task belongs to exactly one **project** (a workspace) and carries zero or more **tags** (labels).
2. **Manages Claude Code sessions bound to those tasks.** It launches, backgrounds, and resumes `claude` sessions tied to a task (via `tmux`), observes their status, logs a recap when one ends, and exposes the bound task's read/write surface to the running session over MCP.

It also makes one narrow, non-blocking outbound network call: expanding a pasted Jira issue URL into a real title. That call — and everything the Claude Code integration shells out to — always degrades quietly on failure; nothing in `tend`'s own workflow blocks on the network or on an external binary being present.

## 2. The core design principle (read this twice)

The system this replaces always failed for one reason: **capture was too slow, so the task list stayed incomplete, so it stopped being trusted, so it got abandoned.**

Therefore the entire design obeys one rule:

> **Capture is a dump. Organization is a separate, later act.**

Concretely:

- Capturing a task requires **nothing** — no project, no due date, no state. A bare title is a complete, valid task (it lands in the default `Unsorted` project).
- The capture command (`tend add`) **does not start the TUI**. It opens the DB, inserts a row, and exits — sub-100ms, perceptually instant. A pasted Jira URL still returns fast: the summary lookup is a best-effort enrichment, not a gate.
- All richness — long-form body, sub-tasks, links, state, project, tags, agent sessions — is added **later**, in the TUI, when the user is *processing*, not when they're *capturing*.
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
│   │   ├── task.go           #   Task, State, priority/date normalization, tags
│   │   ├── project.go        #   Project — the per-task workspace grouping
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
│   │   ├── headless.go           #   HeadlessCmd (`claude -p` for a workflow step) + RunHeadless: stream-json tee'd to a step log, final result parsed
│   │   ├── stream.go             #   RenderStream: stream-json → display lines (assistant text, tool calls) for the run view; skips the noise
│   │   ├── tmux.go               #   dedicated-socket tmux wrapping: wrap/attach/has-session/kill/capture-pane
│   │   ├── hooks.go              #   Claude Code hook payload parsing + injected --settings generation
│   │   ├── status.go             #   ClassifyPane — capture-pane text → "working", for the one status no hook reports
│   │   ├── mcp_config.go          #   per-session --mcp-config file pointing at `tend mcp --task-id <id>` (+ `--step-run-id` for a workflow step)
│   │   ├── runner.go              #   RunnerCmd (`tend workflow run <id>` via os.Executable) + RunnerSessionName (`tend-wf-<run-id>`)
│   │   └── session_id.go           #   UUIDv4 generation for --session-id
│   ├── runner/                   # WORKFLOW RUNNER — drives one run step to step; the process behind `tend workflow run`
│   │   ├── runner.go               #   Runner.Run: claim, pick next step by edge, exec agent steps / wait at gates, crash resume
│   │   ├── exec.go                 #   ClaudeExec — the production Exec seam (HeadlessCmd/HeadlessResumeCmd + RunHeadless)
│   │   └── launch.go               #   Launch/Resume: host a runner in a detached tmux session, refuse a live or ended run
│   ├── jira/                    # I/O EDGE — the only package that talks to the Jira REST API / keychain
│   │   ├── jira.go               #   URL parsing, issue summary fetch (bounded timeout, degrades to the bare key)
│   │   └── keyring.go             #   credential storage via the OS keychain
│   ├── mcpserver/                # MCP tool surface — third consumer of Store, alongside tui and cli
│   │   ├── server.go               #   builds the MCP server bound to one task, runs the stdio transport
│   │   ├── tools.go                 #   tool schemas + handlers (get_current_task, create_subtask, set_task_state, ...). No log-entry tool: log entries are the user's; agents write to the task body
│   │   ├── steps.go                 #   get_workflow_step + finish_step, registered only when bound to a workflow step run
│   │   └── store.go                  #   mcpserver's own narrow Store interface
│   ├── tui/                       # PRESENTATION — Bubble Tea
│   │   ├── app.go                   #   root Model (Init/Update/View), message wiring
│   │   ├── list.go                   #   grouped/tree list view, project column
│   │   ├── detail.go                  #   detail pane: glamour body, sub-tasks, SESSIONS, WORKFLOWS, LOG
│   │   ├── triage.go                   #   inbox processing view
│   │   ├── standup.go                   #   standup view: notes + activity summary, yank to clipboard
│   │   ├── workflows.go                  #   workflows authoring view: workflows + steps, prompt in $EDITOR
│   │   ├── sessions.go                   #   launch/resume/background tea.Cmds, session picker, recap + status polling
│   │   ├── workflowrun.go                #   `w`: run a workflow on a task (picker, cwd prompt, runner launch)
│   │   ├── runview.go                    #   `v`: watch a run — WORKFLOWS rows, RUNS sidebar, step list + tail-followed log, pause/cancel/gate
│   │   ├── takeover.go                   #   `t`: pause the run, resume the step's session interactively, then the continue/hand-back/rerun/abandon picker
│   │   ├── agents.go                     #   `A`: agents view — a project's sessions across its tasks; join, kill, task detail or tailed step log
│   │   ├── palette.go / urlpicker.go / whichkey.go / modal.go / help.go   #   supporting overlays
│   │   ├── keys.go                       #   key bindings (source of truth — see the in-app `?` help too)
│   │   └── styles.go                      #   lipgloss styles, status glyphs
│   └── cli/                        # cobra commands
│       ├── root.go                   #   command tree, Store/MCPStoreFactory/TUIRunner interfaces, --db resolution
│       ├── add.go / ls.go / log.go / standup.go   #   fast, scriptable one-shots
│       ├── projects.go                 #   `tend projects` — list/add/rename/rm/archive/unarchive/cwd
│       ├── auth.go                    #   `tend auth jira {login,status,logout}`
│       ├── mcp.go                      #   hidden `tend mcp --task-id <id> [--step-run-id <id>]`, spawned by a launched claude session
│       ├── workflow.go                 #   `tend workflow resume <run-id>`; hidden `tend workflow run <run-id>` (the runner itself)
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
      ├──→ runner ──→ agent, workflow, task   (its own Store interface; the one package that *runs* claude/tmux commands)
      ├──→ agent   (process control: claude/tmux, hook parsing, session-id/mcp-config generation)
      └──→ jira    (REST + keychain)

tui ──┴──→ store, agent, jira, runner (Launch only)   (same rules — tui never touches SQL, exec, or HTTP directly outside these)
```

- `task` (domain) knows nothing about SQLite, exec, or HTTP.
- `store` is the only package that imports the generated SQL code or builds queries.
- `agent` is the only package that builds `claude`/`tmux` commands or parses Claude Code hook payloads; it never runs a command itself, so it stays testable without a real terminal.
- `runner` is where those commands get *run* outside a terminal handoff: it executes headless steps (`agent.RunHeadless`) and starts tmux sessions, behind an `Exec` seam so its transition logic is tested against a fake. It declares its own `Store` interface and depends on `agent`, `workflow` and `task`, never on `store`, `tui` or `cli`.
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
-- Seed rows: inbox(0), todo(1), review(2), doing(3), blocked(4), done(5,terminal), someday(6,hidden)
-- (review arrived in migration 00014; the list view shows it above doing)

CREATE TABLE tasks (
  id           INTEGER PRIMARY KEY,
  title        TEXT NOT NULL,
  body_md      TEXT NOT NULL DEFAULT '',         -- long-form description + links + notes; rendered with glamour
  state        TEXT NOT NULL DEFAULT 'inbox' REFERENCES states(name),
  parent_id    INTEGER REFERENCES tasks(id) ON DELETE CASCADE,  -- sub-tasks via self-reference
  project_id   INTEGER NOT NULL DEFAULT 1,       -- added by migration 00007; points at projects(id), row 1 = 'Unsorted'.
                                                 -- Deliberately NO `REFERENCES` clause (SQLite rejects a non-NULL-default
                                                 -- FK column add); integrity for project deletes lives in Store.DeleteProject.
  priority     INTEGER,                          -- nullable; 1 (highest, "A") .. 4 ("D")
  due          TEXT,                              -- ISO 8601 date, nullable
  snooze_until TEXT,                               -- ISO date; while set and in the future, hidden from the live view
  created_at, updated_at, completed_at TEXT
);

CREATE TABLE projects (          -- a workspace; every task belongs to exactly one (migration 00007). The TUI's
  id, name TEXT NOT NULL          -- third column scopes the list to the selected project.
    UNIQUE COLLATE NOCASE,
  sort_order INTEGER NOT NULL DEFAULT 0,  -- 'Unsorted' is seeded as id 1, sort_order -1 (pinned first); it is the
  archived_at TEXT,                        -- default capture target and the fallback every delete path reassigns to,
  cwd TEXT NOT NULL DEFAULT '',            -- so Store.DeleteProject refuses to delete it. archived_at hides a project.
  created_at, updated_at                    -- cwd (migration 00010) is the default working directory a new Claude
);                                          -- session on one of its tasks is offered; '' = unset.

CREATE TABLE tags (              -- free-form labels; multi-valued per task. The old flat `tasks.project` string
  id, name TEXT NOT NULL          -- was migrated into here as one tag per task by 00007.
    UNIQUE COLLATE NOCASE
);
CREATE TABLE task_tags (         -- many-to-many join, both sides ON DELETE CASCADE.
  task_id, tag_id, PRIMARY KEY (task_id, tag_id)
);

CREATE TABLE settings (          -- key/value bag for state that must outlive a TUI process. First tenant:
  key TEXT PRIMARY KEY,           -- active_project_id, so a bare-shell `tend add` can read the last TUI selection.
  value TEXT NOT NULL
);

CREATE TABLE task_events (       -- append-only activity log behind `tend standup`; not a foreign key to tasks —
  id, task_id, task_title,        -- the log must outlive the tasks it describes. Populated by AFTER INSERT/UPDATE/DELETE
  kind TEXT CHECK (kind IN         -- triggers on `tasks`, not Go-layer writes, so OLD/NEW state is free and
    ('created','state','deleted','project')),  -- cascade-deleted sub-tasks still get an event.
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
- **Projects** are a hard grouping: every task has exactly one `project_id`, defaulting to `Unsorted` (id 1). Deleting a project reassigns its tasks to `Unsorted` inside a transaction (`Store.DeleteProject`); `Unsorted` itself cannot be deleted.
- **Tags** are the soft, multi-valued labelling mechanism (`task_tags`), and are what the pre-00007 flat `project` string became.
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
| `tend add "<text>"` / `tend a` | Instant capture to `inbox` in the default (`Unsorted`) project. No TUI. Reads stdin too (`echo "..." \| tend a`). `-p/--project <name>` targets one project for that invocation. A pasted Jira issue URL is expanded to a real title via one bounded, best-effort REST call. |
| `tend ls` | Plain-text dump of the live view across every project to stdout (scriptable, no TUI). `-p/--project <name>` narrows it |
| `tend projects` | List projects with live task counts |
| `tend projects add\|rename\|rm\|archive\|unarchive` | Manage projects. `rm` never deletes work — its tasks move to the default (`Unsorted`) project |
| `tend log "<note>"` | Capture a standup note instantly, no TUI |
| `tend standup` | Print a standup summary of recent activity as markdown |
| `tend auth jira login/status/logout` | Manage Jira credentials in the system keychain |
| `tend workflow resume <run-id>` | Start a fresh runner for a workflow run whose runner died (host reboot, tmux server killed) or was paused. Refuses a run that has ended or whose runner is still alive. |
| `tend workflow run <run-id>` | Hidden. The runner: drives one workflow run to a terminal state and exits — hosted in tmux session `tend-wf-<run-id>` by the TUI's `w` chord (or `resume`), never run by hand. `--takeover` re-enters a run left `running` by a dead runner. |
| `tend mcp --task-id <id> [--step-run-id <id>]` | Hidden. Runs tend's MCP server over stdio, bound to one task — spawned by a launched `claude` session, never by the user directly. `--step-run-id` (set by the workflow runner) adds the step tools `get_workflow_step` and `finish_step` for that step run. |
| `tend agent-hook <event>` | Hidden. Records a Claude Code hook event (session status) against its session — spawned by Claude Code itself via injected `--settings`. |
| `tend version` | Print the version |

Global flag: `--db <path>`.

There is no `tend done` — completing, deleting, and every other state transition happens in the TUI, which is where triage and task management actually live.

> **The shell holds no project state.** A bare `tend add` always lands in the default project and
> `tend ls` always shows every project, so the same command behaves identically in every terminal.
> Only the TUI, which has a visible selection, captures into a specific project without being told
> (it persists that selection through the `settings` table's `active_project_id`).

## 7. TUI

Built on Bubble Tea v2 + Bubbles v2 + Lip Gloss v2; Glamour v2 renders the body.

- **List view (default).** A grouped/tree view of the live view (sub-tasks nest under their parent), with vim-style navigation, search, a `:`/`Ctrl-P` command palette, and quick add. Tasks group by state by default; the `g` chord regroups by priority or by latest agent-session status (`gg` stays "top of list"). A project column scopes the list to the selected project (or shows all).
- **Detail pane.** The heart of the tool: glamour-rendered markdown body, a sub-task checklist, a `SESSIONS` section (this task's Claude Code sessions — launch, resume, or attach to a backgrounded one), and a `LOG` section (manual notes plus auto-generated session recaps). Scrollable and independently focusable so long histories are reachable. URL detection lets the user open a link under the cursor or all of them via the OS opener.
- **Triage view.** Filtered to `inbox`. Fast keys to set state, assign a project, add tags or a due date, open the body in `$EDITOR`, or send to `someday`/`done` — the batched processing pass.
- **Standup view.** Manual notes grouped by task plus a generated activity summary (completed/blocked/started, derived from `task_events`); yank the whole thing as markdown.
- **Workflows view.** Authoring for agent workflows (`internal/workflow`): the workflows on the left, the selected one's steps on the right in `sort_order`, three panes walked with `h`/`l` (workflows → steps → the selected step's edges). Create/rename/duplicate/delete workflows; add, reorder and delete steps; set a step's model, permission mode and kind (agent/gate); edit its prompt template in `$EDITOR`. The step list doubles as the **text graph preview** (`workflow.Preview`): numbered steps, each annotated inline with every edge that is not the linear default `done -> next step` (`2  review  [approve -> 3]  [reject -> 1 (max 3)]`), and `[done -> end]` on a step nothing leaves. The **EDGES** sub-list under it shows the selected step's edges as `on <outcome> -> <step> (max N)`; `n` adds one and `e` edits one through a three-stage flow (outcome prompt, target-step picker, max-iterations prompt, esc anywhere abandons it; a renamed outcome deletes the old row since `SetEdge` upserts on `(from, outcome)`), `dd` deletes one. Adding a step after one with no edges writes `done -> new step` for you, so a linear workflow needs no edge work. `v` runs `workflow.Validate` (unreachable steps, a non-final step nothing leaves, a prompt naming an outcome its edges do not route, an agent loop-back with no `max_iterations`, templates that fail to render) and lists the problems under the steps, recomputed on every reload so they disappear as they are fixed.
- **Running a workflow (`w`).** From the list or detail pane, `w` picks a workflow to run on the selected task, checks it can run at all (it has steps, every step prompt renders, `claude` and `tmux` are on `$PATH`), prompts for a cwd (defaulting to the task's last session directory), then writes a `pending` run and starts its runner in a detached tmux session (`runner.Launch`). The chord returns to the TUI at once; the runner (§8) drives every step headlessly from there. A runner that cannot be started fails the run with the reason. The runner's own output is in its tmux session (`tmux -L tend attach -t tend-wf-<run-id>`) and `runner.log`, but watching a run never needs either — see the run view below.
- **Watching a run (`v`, run view).** The detail pane gains a `WORKFLOWS` section under `SESSIONS`: one row per run on the task (state glyph, workflow · current step, state, elapsed, "output Ns ago" from the step log's mtime, and "runner gone" when the run says a runner owns it but no `tend-wf-<run-id>` tmux session exists). `v` opens the run view on the task's latest run, with every run on the task in a `RUNS` sidebar on the left (the projects column's width; `j`/`k` there switch the watched run, reloaded with it so states stay current): the run's step runs in the middle — iteration marker for a loop-back, outcome or the live state, the current step highlighted — and the selected step's log on the right, rendered from its stream-json (`agent.RenderStream`: assistant text and `⚙ Tool: summary` lines; `v` flips to the raw lines). `h`/`l` walk runs → steps → log and back, as they move between panes everywhere in the app; `l` is never a toggle. The log tail-follows while the run is live. Controls write exactly what the CLI would: `p` pauses (`SetRunState(paused)`; the runner SIGTERMs the step) or resumes a paused run (`runner.Resume`), `cc` cancels, `a`/`x` decide the gate the run is waiting at (`FinishStepRun`, refused for an outcome the gate's edges don't route), and `t` takes the current step over (next bullet). The TUI never drives the run: everything it shows is read from SQLite and the log file, and nothing is derived from stream events. Live updates ride the session poller's tick — `pollRuns` snapshots the active runs (state, current step, log mtime) and the shared `sessionsPolledMsg` reloads whichever view is up; there is no second timer.
- **Taking over a step (`t`, `internal/tui/takeover.go`).** Moving from watching a run to driving it, over the existing resume path — a headless step is one claude session, and the same session resumes interactively once nothing else drives it. `t` on a running or paused agent step writes `paused` on the run, waits for the runner to let go (its `tend-wf-<run-id>` tmux session disappearing, via the `runnerAlive` seam; a runner that already died skips the wait; one that never stops leaves the run paused with a flash after `runnerStopTimeout`), then resumes the step's session exactly as `r` does — tmux-wrapped, terminal handed over, and with the step's MCP tools bound (`WriteMCPConfig` with the step run id, so `finish_step` works by hand too). `r` on a paused run's step session from the session picker or the agents view is the same takeover (`resumeGuardedCmd`); while the run is live and not paused it stays refused. On return the takeover picker asks what the run should do: **continue the run** (pick the step's outcome from its edges, paste an optional deliverable into a modal, `FinishStepRun`, then `runner.Resume`; a step that already called `finish_step` inside the session just resumes), **hand the step back** (`runner.Resume` with the step untouched: the runner continues the same session headlessly), **rerun the step** (`runner.RestartStep` clears the step run's session id, and the next runner starts the step over on a fresh session, same step run and iteration), or **abandon** (`cancelled`); esc leaves the run paused. No recap fires for a takeover return, since a `claude -p --resume` recap would race the runner's own headless resume of the same session. A backgrounded (detached) takeover session keeps the run paused with no picker — it is still a live claude on the step's transcript. Because every resume goes through `runner.Resume`, taking over a step whose runner died behaves like `tend workflow resume` afterwards. Taking over a gate is approving or rejecting it; `t` says so.
- **Agents view (`A`).** Every Claude Code session in the selected project, across its tasks, in one list — the projects column stays on the left and scopes it exactly as it scopes the task list (`Store.ListSessionsForProject`, joined through the owning task). Live sessions by default, ordered blocked → working → idle → starting, then most recently active; `C` adds the ended ones. Each row is the picker's row (status glyph, label, its task, time since last active) plus a `headless` marker for a workflow step's session. The right pane is the session's detail: for an interactive session, its task's detail pane (body, sub-tasks, SESSIONS, WORKFLOWS, LOG) so the recap is one keypress away; for a headless one, its step's stream-json log, tail-followed off the poller's tick exactly as the run view does (`v` in the pane flips raw; `v` from the list opens the run view and comes back here). `⏎`/`r` joins the selected session through the same guard as the `r` picker (a headless session is refused while its run is live and not paused). `dd` kills it: an interactive session through `agent.KillSession` on its tmux session, then `SetSessionStatus(ended)` — a status write, never `DeleteSession`, since a session that ran is the task's history; a headless one through its run (`SetRunState(cancelled)`, which the runner polls for and SIGTERMs the step on), so process ownership stays with the runner.
- **Editing the body.** Shells out to `$EDITOR` — there is no in-terminal markdown editor.
- **Live updates.** The TUI reloads on its own when another process commits to the database — an agent session's MCP tool call, `tend add` from another shell, a workflow runner. There is no daemon or socket: `store.Watcher` polls SQLite's `PRAGMA data_version` (the mechanism the SQLite docs name for this) on a dedicated pinned connection every 500ms, and a change becomes a `dbChangedMsg` that runs the same reload fan-out as a mutation, minus the status flash. Rules for that path: background reloads re-select the list row **by task id**, never by index, and never reset the detail pane's scroll; a change arriving mid-input (`/` filter, prompt, note modal) or while a reload is already in flight is deferred and applied once, not dropped — the pragma read that noticed it is consumed, so there is no second chance to see it.

Full key bindings live in `internal/tui/keys.go` and are discoverable in-app via `?` — not duplicated here since they're a fast-moving implementation detail, not architecture.

## 8. Agent sessions (Claude Code integration)

A task can have one or more Claude Code sessions bound to it, launched and managed from the detail pane's `SESSIONS` section (`r`). This is the mechanism by which a task carries not just its own notes but a live or resumable record of the agent work done against it.

- **Launch/resume.** `internal/agent.LaunchCmd`/`ResumeCmd` build the `claude` invocation; the TUI hands it to `tea.ExecProcess` for the terminal handoff. A session's `claude --session-id` is generated up front (`session_id.go`) so tend never has to discover it after the fact. `LaunchCmdWith` adds the per-step extras a workflow run needs — an initial prompt (claude's positional argument), `--model`, and `--permission-mode` — and is otherwise `LaunchCmd`.
- **Headless steps.** `HeadlessCmd` is the `claude -p` counterpart of `LaunchCmdWith` for a workflow runner: same pinned `--session-id`, `--mcp-config` and `--settings` (hooks and MCP tools work in print mode), plus `--output-format stream-json --verbose` and, when `LaunchOpts.AppendSystemPrompt` is set, `--append-system-prompt` (adds to claude's default system prompt rather than replacing it; accepted on a `--resume` turn too). `RunHeadless` runs it with stdout tee'd live to a per-step log (`StepLogPath`: `${XDG_DATA_HOME}/tend/runs/<run-id>/<step-run-id>.jsonl`, path stored on the step run) and parses the final `result` event into a `HeadlessResult` — the fallback deliverable for a step that never calls `finish_step` and has no outcome but `done`. A `-p` run with no `--permission-mode` denies tool calls silently and still reports success; the only signal is `PermissionDenials`. Cancelling the ctx SIGTERMs the step's process group; the session stays resumable with `claude --resume`.
- **Workflow runner.** `internal/runner` is the process behind a workflow run: `tend workflow run <run-id>`, hosted in tmux session `tend-wf-<run-id>` on the same `-L tend` socket, one process per run, gone when the run ends — never a daemon. It claims the run (`Store.ClaimRun`, a CAS, so two runners for one run see one winner; tmux's own duplicate-session refusal is the second guard), then loops: pick the next step from the previous step run's outcome via the live `workflow_edges` (no edge → run `done`; an edge's `max_iterations` exceeded → run `failed` with a message on `workflow_runs.error`), render the prompt (`Input` = previous deliverable, or the previous step's own input when its deliverable is empty; a loop-back edge to a step that already ran carries it as `Feedback` instead and keeps the original `Input`), write the step run and its session row (tmux_session `''` — the pane is the runner's, not claude's), and `RunHeadless` it with the log path stored first. Every agent step also gets a runner-built system prompt block (`workflow.StepSystemPrompt`, via `--append-system-prompt`, recorded on the step run as `system_prompt`, migration 00013): which step of which workflow this is, that the run cannot continue until `mcp__tend__finish_step` is called with one of the step's outcomes, and that the tool does not end the session — so the hand-off contract never depends on a `prompt_md` author remembering it. On exit, an outcome written by `finish_step` stands; no result, an error result, or any `PermissionDenials` fails the run loudly. A step that ran fine but never called `finish_step` falls back to the stream's final text as a `done` deliverable **only when `done` is its sole outcome** (`workflow.FallbackAllowed`: no edges, or a single `done` edge). Any other step is nudged once — its own session is resumed with `workflow.NudgePrompt` over the crash-resume path, same step run, not a new iteration — and if that turn also ends without `finish_step` the run fails naming the step and the outcomes it routes, rather than routing on a guessed `done` (seen for real: a haiku review step that "approved" in prose and ended the run silently). Gate steps park the run in `waiting_review` and poll the step run for a decision. `paused`/`cancelled` written by the TUI or CLI are polled between and during steps; a pause SIGTERMs the step and leaves its session resumable. **Crash resume:** state lives in SQLite, so `tend workflow resume <run-id>` (refused while the runner's tmux session is alive) starts a runner with `--takeover` that re-enters at `current_step_run_id`: a log that already holds a `result` event settles the step without claude; otherwise the step's session is continued with `claude -p --resume <id>` (same session id, context intact — verified against claude 2.1.267); a session that cannot be continued restarts the step under a new session id on the same step run. An unfinished step run with an **empty session id** is one nobody owns — `runner.RestartStep` cleared it, the TUI takeover picker's "rerun the step" — and is started over the same way before the log or the old session is consulted (the log is appended to, not truncated). The runner's progress goes to its pane and to `runs/<run-id>/runner.log`.
- **Row at launch.** The `agent_sessions` row is written *before* the handoff (`Store.CreateSession`, inside `launchSessionCmd`'s Cmd), with status `starting`, so the session's own hooks land on a row from its very first turn — a fresh session reads `starting` then `idle` without a resume, and a headless runner has something to watch. The handoff returning only touches the row (`sessionFinishedMsg`); a handoff that returns an error deletes it (`Store.DeleteSession`) so a broken launch leaves no phantom session.
- **Backgrounding.** Sessions run inside `claude` wrapped in `tmux`, on a dedicated `-L tend` socket with a generated, hands-off config (`internal/agent/tmux.go`). Detaching (`C-h` or `C-Space d`) returns to tend while `claude` keeps running; resuming a task with a live backgrounded session re-attaches instead of starting a second process. Any `tend` instance on the host can attach, since it's the same socket and the same shared SQLite file.
- **Status.** `agent_sessions.status` is populated two ways: Claude Code hooks (`SessionStart`/`Stop`/`Notification`/`SessionEnd`), injected via a per-session `--settings` file and reported through the hidden `tend agent-hook` command; and, for the one state no hook covers (actively generating/running a tool, "working"), a poller in `internal/tui` that reads the pane's rendered text via `tmux capture-pane` and classifies it (`internal/agent/status.go`). Hook-reported status always wins a race against the poller's guess (a compare-and-swap on `status_updated_at`). A workflow step's session has no pane of its own (it runs inside the runner's tmux session), so the runner writes `working` itself right before exec and `ended` right after; hooks still land in between. What the list row, the `g a` grouping and the detail glyphs show is `Store.SessionStatuses`, which merges live runs in: a task with a non-terminal run takes the run's state mapped into this vocabulary (`workflow.RunState.SessionStatus`: pending→starting, running→working, waiting_review→blocked, paused→idle), except that a paused run yields to the task's latest session status when there is one (a takeover in progress) — so a gate waiting at a run with no session rows still reads blocked, with no fake session row and no second glyph family. Terminal runs fall through to the sessions.
- **Recap.** When a session's terminal handoff returns (ended, not backgrounded), tend fires a headless `claude -p --resume` follow-up asking for a short label + recap, and logs the recap as a normal `LogEntry` on the task — the fix for "I took a break and lost the thread." A session backgrounded instead of exited defers this (`needs_recap`) until some tend instance observes it's really gone.
- **MCP.** `tend mcp --task-id <id>` is spawned by `claude` itself (via a per-session `--mcp-config` tend writes at launch, `internal/agent/mcp_config.go`) and gives that session direct read/write access to its bound task — creating sub-tasks, updating the body, changing state, logging notes — without inventing a scratch markdown file. See `internal/mcpserver` for the tool set.
- **Step tools.** A workflow step's session is spawned with `--step-run-id` as well (`runner.ClaudeExec` passes the step run to `WriteMCPConfig`), which registers two more tools bound to that one step run: `get_workflow_step` (workflow and step names, iteration, `Input`, `Feedback`, and the allowed outcomes, read live from the step's edges — `done` alone when it has none) and `finish_step(outcome, deliverable)`, the hand-off. `finish_step` normalizes the outcome the way edges are stored, refuses one the step does not route (naming the ones it does), and is one-shot (`Store.FinishStepRun`); it does not end the session. The runner tells every step's session about these tools by their exact ids (`mcp__tend__finish_step`, `mcp__tend__get_workflow_step`, constants in `internal/workflow`) in the injected system prompt, and its exit-time fallback only applies to a step whose sole outcome is `done`; everything else must hand off through `finish_step` or the run fails (see "Workflow runner"). `Feedback` is persisted on `workflow_step_runs` (migration 00012) alongside `input` so the tool reads back exactly what the prompt was rendered with. An ordinary session never sees these tools.

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
