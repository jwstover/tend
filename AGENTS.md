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
- **Scopes** (optional, lowercase): `tui`, `cli`, `store`, `task`, `workflow`, `runner`, `agent`, `sessions`, `mcp`, `jira`, `ci`. Omit when a change spans layers.
- **Breaking changes:** append `!` after the type/scope (`feat(cli)!: …`) or add a `BREAKING CHANGE:` footer. Pre-1.0, these bump the **minor** version, not the major.
- **Keep the existing body style:** a narrative body explaining the *why*, plus the `Co-Authored-By:` trailer. Conventional Commits only constrains the subject line.
- **Releases run via release-please** (see §11): merging to `main` updates a release PR with the CHANGELOG and version bump; merging that PR tags and publishes. **Never hand-edit `CHANGELOG.md` or create tags manually.**

---

## 1. What this is

A fast, keyboard-driven, terminal-native personal task/project tracker for a single user who lives in the command line. It started as a todo.txt-style TUI (inspired by `webstonehq/tuxedo`) with a real data model — long-form descriptions, sub-tasks, a custom workflow — and has since grown a second, closely related purpose: giving Claude Code agent sessions a durable, task-shaped home to work from.

Concretely, `tend` now does three things:

1. **Tracks tasks.** One local SQLite file, one user, no daemon, no multi-user sync. Every task belongs to exactly one **project** (a workspace) and carries zero or more **tags** (labels).
2. **Manages Claude Code sessions bound to those tasks.** It launches, backgrounds, and resumes `claude` sessions tied to a task (via `tmux`), observes their status, logs a recap when one ends, and exposes the bound task's read/write surface to the running session over MCP.
3. **Runs multi-step agent workflows on those tasks.** A workflow is a reusable, authored-in-the-TUI graph of steps — headless `claude -p` agent steps and human gates — joined by edges keyed on outcome (`done`, `approve`, `reject`, …). Running one on a task starts a per-run runner process that drives the steps in turn, hands each step's deliverable to the next, parks at gates, and can be paused, taken over interactively, resumed after a crash, or cancelled — all from the TUI, with every bit of state in the same SQLite file (§8, "Workflows").

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
│   ├── workflow/              # DOMAIN — agent workflows: types + rules, zero I/O. A sibling of task, not a tenant (own vocabulary).
│   │   ├── workflow.go        #   Workflow, Step (StepKind agent/gate), Edge, Run (RunState + Terminal), StepRun; Normalize*; the Err* values
│   │   ├── prompt.go          #   RenderPrompt / ValidatePrompt — prompt_md is a Go text/template over PromptData (Task, Cwd, Input, Feedback, Iteration, Outcomes, Subtasks)
│   │   ├── graph.go           #   Preview (text graph for the authoring view) + Validate (unreachable steps, unrouted outcomes, unbounded loops, agent steps with no permission mode, …)
│   │   ├── handoff.go         #   StepSystemPrompt / NudgePrompt / FallbackAllowed; the exact step-tool ids (mcp__tend__finish_step, …)
│   │   └── status.go          #   RunState.SessionStatus — maps a live run into the agent-session status vocabulary the TUI renders
│   ├── store/                 # PERSISTENCE — the ONLY place SQL lives.
│   │   ├── migrations/        #   *.sql, embedded via embed.FS, applied by goose on startup
│   │   ├── queries/           #   *.sql — input to sqlc (tasks, projects, tags, sessions, events, logs, settings, workflows)
│   │   ├── gen/                #   sqlc OUTPUT — generated; never hand-edited
│   │   ├── store.go            #   Store: wraps generated Queries, returns domain types, owns transactions
│   │   ├── projects.go / tags.go / dependencies.go / workflows.go   #   per-area Store methods (dependencies.go: AddDependency with the cycle check, Blockers/Blocking, BlockerCounts; workflows.go: definitions, runs, step runs, ClaimRun, FinishStepRun, …)
│   │   └── watch.go            #   Watcher: PRAGMA data_version poller behind the TUI's live reload
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
│   │   ├── launch.go               #   Launch/Resume: host a runner in a detached tmux session, refuse a live or ended run
│   │   └── restart.go              #   RestartStep: disown an unfinished step run (session id → '') so the next runner starts it over
│   ├── jira/                    # I/O EDGE — the only package that talks to the Jira REST API / keychain
│   │   ├── jira.go               #   URL parsing, issue summary fetch (bounded timeout, degrades to the bare key)
│   │   └── keyring.go             #   credential storage via the OS keychain
│   ├── mcpserver/                # MCP tool surface — third consumer of Store, alongside tui and cli
│   │   ├── server.go               #   builds the MCP server bound to one task, runs the stdio transport
│   │   ├── tools.go                 #   tool schemas + handlers (get_current_task, create_subtask, set_task_state, ...). No log-entry tool: log entries are the user's; agents write to the task body
│   │   ├── steps.go                 #   get_workflow_step + finish_step, registered only when bound to a workflow step run
│   │   ├── workflows.go             #   workflow authoring tools (list/get/create/update workflow, add/update/reorder/delete steps, set prompt, add/delete edges), registered for every session; every mutation returns the whole graph + preview + problems
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
│   │   ├── projects.go / projectpicker.go   #   projects column + the project picker overlay
│   │   ├── parentpicker.go / dependencypicker.go   #   `m`: move-to-parent picker; `b`: the multi-select "blocked by" picker
│   │   ├── palette.go / urlpicker.go / whichkey.go / modal.go / help.go   #   supporting overlays
│   │   ├── keys.go                       #   key bindings (source of truth — see the in-app `?` help too)
│   │   └── styles.go                      #   lipgloss styles, status glyphs
│   └── cli/                        # cobra commands
│       ├── root.go                   #   command tree, Store/MCPStoreFactory/TUIRunner interfaces, --db resolution
│       ├── add.go / ls.go / log.go / standup.go   #   fast, scriptable one-shots
│       ├── projects.go                 #   `tend projects` — list/add/rename/rm/archive/unarchive/cwd
│       ├── auth.go                    #   `tend auth jira {login,status,logout}`
│       ├── mcp.go                      #   hidden `tend mcp --task-id <id> [--step-run-id <id>]`, spawned by a launched claude session
│       ├── workflow.go                 #   `tend workflow` group: ls/start/status/approve/reject/pause/resume/cancel; hidden `run <run-id>` (the runner itself)
│       ├── workflow_logs.go            #   `tend workflow logs <run-id> [--step N] [-f] [--raw] [--runner]` — a step's stream-json log or runner.log, tail-followed
│       └── agent_hook.go                #   hidden `tend agent-hook <event>`, spawned by Claude Code's own hooks
│   └── version/                    # String(): ldflags-stamped release version, else runtime/debug build info
├── CHILD-ROW-PARITY.md            # scratch backlog note on list-row rendering; not required reading to orient
├── sqlc.yaml
├── go.mod
└── Makefile                       # build, test, lint, generate, install, snapshot, release-check
```

**Dependency direction points inward and must never be violated:**

```
cli ──┬──→ store ──→ task, workflow ──→ (nothing)
      ├──→ mcpserver ──→ task, workflow   (its own Store interface, satisfied by *store.Store)
      ├──→ runner ──→ agent, workflow, task   (its own Store interface; the one package that *runs* claude/tmux commands)
      ├──→ agent   (process control: claude/tmux, hook parsing, session-id/mcp-config generation)
      └──→ jira    (REST + keychain)

tui ──┴──→ store, agent, jira, workflow, runner (Launch/Resume/RestartStep only)   (same rules — tui never touches SQL, exec, or HTTP directly outside these)
```

- `task` and `workflow` (the two domain packages) know nothing about SQLite, exec, or HTTP. `workflow` is a sibling of `task`, not part of it: a workflow has its own vocabulary (Step, Edge, Run, StepRun) and is bound to a task only per run.
- `store` is the only package that imports the generated SQL code or builds queries; it returns `task.*` and `workflow.*` values.
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
  parent_id    INTEGER REFERENCES tasks(id) ON DELETE CASCADE,  -- sub-tasks via self-reference. Writable: Store.SetParent
                                                 -- re-parents a task (nil = top level), refusing cycles in Go and
                                                 -- re-projecting the sub-tree when the new parent is in another project.
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

CREATE TABLE task_dependencies ( -- "task_id cannot be worked on until depends_on_id is done" (migration 00016).
  task_id, depends_on_id          -- A many-to-many like task_tags, both sides ON DELETE CASCADE, so a deleted
    REFERENCES tasks ON DELETE CASCADE,   -- task stops blocking anything. CHECK (task_id <> depends_on_id); longer
  created_at,                     -- cycles are refused in Go by Store.AddDependency/SetDependencies. Deliberately
  PRIMARY KEY (task_id, depends_on_id)    -- NOT tied to the `blocked` state: the state is what the user says, the rows
);                                -- are what the graph says, and the TUI derives "waiting on N" from them.

CREATE TABLE settings (          -- key/value bag for state that must outlive a TUI process. First tenant:
  key TEXT PRIMARY KEY,           -- active_project_id, so a bare-shell `tend add` can read the last TUI selection.
  value TEXT NOT NULL
);

CREATE TABLE task_events (       -- append-only activity log behind `tend standup`; not a foreign key to tasks —
  id, task_id, task_title,        -- the log must outlive the tasks it describes. Populated by AFTER INSERT/UPDATE/DELETE
  kind TEXT CHECK (kind IN         -- triggers on `tasks`, not Go-layer writes, so OLD/NEW state is free and
    ('created','state','deleted',  -- cascade-deleted sub-tasks still get an event. The exceptions are 'project'
     'project','parent')),         -- (00008) and 'parent' (00015): the store writes those, one row per user action,
  old_value, new_value, created_at -- because a move also re-projects the sub-tree and a trigger would log every row.
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
  started_at, last_active_at TEXT NOT NULL,    -- nullable status_updated_at distinguishes "never observed"
                                                -- from "observed at row creation".
  workflow_step_run_id INTEGER REFERENCES workflow_step_runs(id) ON DELETE SET NULL
                                    -- migration 00009: set when the session ran a workflow step, so the SESSIONS
                                    -- section and agents view can say which step (task.Session.StepRunID; Headless()
                                    -- = step run set AND tmux_session ''). NULL for an ordinary session. SET NULL,
                                    -- not CASCADE: the session and its transcript outlive the run record.
);

-- Agent workflows (migration 00009 + 00011/00012/00013): three DEFINITION tables and two RECORD tables.
-- Definitions are rows behind sqlc, not files, and are read live at each step start (no snapshot) — so what
-- actually ran is copied onto workflow_step_runs at the time it ran. Deleting a workflow or step still
-- referenced by a non-terminal run is refused by the Store (workflow.ErrInUse), as DeleteProject stands in
-- for a foreign key.

CREATE TABLE workflows (            -- a named, reusable procedure; not bound to a project or task (binding is per run)
  id, name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  created_at, updated_at
);
CREATE TABLE workflow_steps (       -- a node: kind 'agent' (headless claude driven by prompt_md) or 'gate' (a pause for a
  id, workflow_id REFERENCES workflows ON DELETE CASCADE,        -- human decision; prompt_md is the reviewer's text)
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent','gate')),
  prompt_md TEXT NOT NULL DEFAULT '',        -- a Go text/template (workflow.RenderPrompt); validated at authoring time
  model, permission_mode TEXT NOT NULL DEFAULT '',   -- '' = inherit claude's default; opaque to tend, forwarded as-is
  sort_order INTEGER NOT NULL DEFAULT 0,     -- AUTHORING order only; execution order is defined by the edges
  created_at, updated_at
);
CREATE TABLE workflow_edges (       -- keyed by outcome: the edges leaving a step ARE its allowed outcomes, and an outcome
  id, from_step_id, to_step_id       -- with no edge ends the run. One primitive covers handoff (done -> next), review
    REFERENCES workflow_steps ON DELETE CASCADE,   -- verdicts (approve/reject) and loop-back with feedback (reject -> an
  outcome TEXT NOT NULL COLLATE NOCASE,            -- earlier step). Outcomes are normalised (trimmed, lower-cased) by the
  max_iterations INTEGER,                          -- Store. max_iterations bounds a loop-back; NULL = unbounded.
  UNIQUE (from_step_id, outcome)
);
CREATE TABLE workflow_runs (        -- one execution of a workflow against a task; cascades with the task (like a session)
  id, workflow_id, task_id            -- AND with its workflow (a finished run's history is history of that definition).
    REFERENCES ... ON DELETE CASCADE,
  cwd TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending'          -- pending/running/waiting_review/paused | done/failed/cancelled (terminal).
    CHECK (state IN (...)),                      -- Unlike agent_sessions.status this IS a workflow the runner moves rows
  current_step_run_id INTEGER                    -- through, so the schema pins the set. Terminal is final: a retry is a new run.
    REFERENCES workflow_step_runs(id) ON DELETE SET NULL,   -- where the runner is (or paused at); crash resume re-enters here
  tmux_session TEXT NOT NULL DEFAULT '',         -- the RUNNER's tmux session (`tend-wf-<run-id>`), '' before one starts
  error TEXT NOT NULL DEFAULT '',                -- why it failed (00011): the runner's pane dies with it, so the message lives here
  started_at, ended_at                           -- ended_at is set exactly when state becomes terminal
);
CREATE TABLE workflow_step_runs (   -- one execution of one step within a run: one claude session for an agent step, a
  id, run_id REFERENCES workflow_runs ON DELETE CASCADE,        -- pause for a gate. The DB stores paths, not logs.
  step_id REFERENCES workflow_steps ON DELETE CASCADE,
  iteration INTEGER NOT NULL DEFAULT 1,          -- how many times this step has run within the run (max_iterations, {{.Iteration}})
  session_external_id TEXT NOT NULL DEFAULT '',  -- the claude --session-id; '' for a gate, or for an unfinished step nobody
                                                 -- owns (runner.RestartStep cleared it: the next runner starts it over)
  prompt_rendered, model, permission_mode TEXT NOT NULL DEFAULT '',   -- what ACTUALLY ran (the definition is read live)
  system_prompt TEXT NOT NULL DEFAULT '',        -- the runner's --append-system-prompt hand-off block (00013); '' for a gate
  input TEXT NOT NULL DEFAULT '',                -- the previous step's deliverable
  feedback TEXT NOT NULL DEFAULT '',             -- what a loop-back edge carried here (00012); '' when reached going forward
  outcome, deliverable TEXT NOT NULL DEFAULT '', -- what this step handed off; outcome is '' until finished, never after
  log_path TEXT NOT NULL DEFAULT '',             -- the step's stream-json file: ${XDG_DATA_HOME}/tend/runs/<run-id>/<step-run-id>.jsonl
  started_at, ended_at                           -- ended_at is the authoritative "finished" marker (workflow.StepRun.Finished)
);
```

Semantics:
- **Sub-tasks** are `parent_id` self-references; the UI computes child completion for a progress indicator and gives sub-tasks the same detail-pane functionality as top-level tasks (body, log, sessions).
- **Projects** are a hard grouping: every task has exactly one `project_id`, defaulting to `Unsorted` (id 1). Deleting a project reassigns its tasks to `Unsorted` inside a transaction (`Store.DeleteProject`); `Unsorted` itself cannot be deleted.
- **Tags** are the soft, multi-valued labelling mechanism (`task_tags`), and are what the pre-00007 flat `project` string became.
- **Dependencies** (`task_dependencies`) say a task waits on other tasks: each blocker must be done before the task can be worked on. A task can wait on several tasks, in any project, and several can wait on one. The store refuses self-dependency and cycles (`task.ErrSelfDependency`, `task.ErrDependencyCycle`); `Store.SetDependencies` is the wholesale replace (the `SetTags` shape) and `Store.BlockerCounts` the batch `{Open, Total}` map the list view renders from (the `ChildCounts` shape). Nothing changes a task's *state* automatically: `blocked` stays the user's call, and a task whose blockers are all done simply reads as free to start. Over MCP (`internal/mcpserver/tools.go`), `create_task` and `create_subtask` take an optional `depends_on` id list, `set_task_dependencies` replaces the list wholesale, `add_task_dependency` / `remove_task_dependency` edit one edge, and every task the tools return carries `depends_on`, `blocks` and a derived `is_blocked`.
- **Long-form body** is the re-entry-cost killer: a task carries its own context so resuming it is free.
- **`snooze_until`** defers a task out of the live view until its wake date.
- **Live view** = tasks whose state has `is_terminal = 0` AND `hidden_by_default = 0` AND (`snooze_until` is null OR in the past).
- **Agent sessions** are per-task, not per-project: a task can have many sessions (e.g. one per repo it touches), each independently launchable/resumable/backgroundable. See §8 for the full lifecycle.
- **Workflows** are global definitions; a **run** binds one to a task, and a **step run** is one execution of one step inside a run (one claude session for an agent step). Execution order comes from `workflow_edges`, never `sort_order`: the runner picks every step after the first by looking up the previous step run's outcome among the edges leaving its step. The Store owns the transitions (`ClaimRun` is a compare-and-swap from `pending`, `SetRunState` refuses a terminal run with `workflow.ErrRunEnded`, `FinishStepRun` is one-shot and refuses an outcome the step's edges don't route). See §8, "Workflows".

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
| `tend workflow` / `tend workflow ls` | The workflow command group (`internal/cli/workflow.go`): the scriptable surface over the same store as the TUI, mirroring `tend projects`. Bare or `ls` lists workflows with their step counts. Authoring stays in the TUI (§7, `W`); names resolve, never create. |
| `tend workflow start <workflow> --task <id> [--cwd <dir>]` | The CLI counterpart of the TUI's `w` chord: same pre-flight (steps exist, prompts render, `claude` and `tmux` present — each named when missing), same cwd default (task's last session, then project default, then the shell's), writes a `pending` run and launches its runner in tmux (`runner.Launch`). A runner that cannot start fails the run with the reason. |
| `tend workflow status [<run-id>]` | Live runs: id, workflow, task, state, current step, elapsed, and `runner gone` when tmux has no `tend-wf-<run-id>` session (silent when tmux cannot be asked). With a run id: the run in full — steps in `status` order with outcome or live state, a failed run's error, and the input a waiting gate is reviewing plus the commands to decide it. |
| `tend workflow approve <run-id> [--feedback ...]` / `reject <run-id> --feedback ...` | Decide the gate a run is waiting at: the same one-shot `FinishStepRun` the TUI's `a`/`x` and `finish_step` make. `--feedback` is the gate's deliverable, and `runner.nextStep` decides what that means: on a loop-back edge (reject) the target step keeps its input and gets the text as `{{.Feedback}}`; on a forward edge (the usual approve) the text *replaces* the reviewed deliverable as the next step's `{{.Input}}`. So `reject` requires non-blank feedback, and `approve` leaves it out by default so the gate passes its input through unchanged, as the TUI's `a` does. Refused, naming why, for a run not waiting for review, a current step that is not a gate, a gate already decided, or an outcome the gate's edges do not route. |
| `tend workflow pause <run-id>` / `cancel <run-id>` | Write `paused` / `cancelled`; the runner polls for both and stops the step (a paused step's session stays resumable). Pausing a `pending` run, or anything on an ended run, is refused. |
| `tend workflow logs <run-id> [--step N] [-f] [--raw] [--runner]` | A step's log rendered as the run view shows it (`agent.RenderStreamLine`; `--raw` for the stream-json lines). `--step N` is the step's number in `status <run-id>`, default the current step; a gate has no log. `-f` follows until the step finishes or the run ends. `--runner` tails `runner.log` instead. |
| `tend workflow resume <run-id>` | Start a fresh runner for a workflow run whose runner died (host reboot, tmux server killed) or was paused. Refuses a run that has ended (`ErrRunEnded`) or whose runner is still alive (`runner.ErrRunnerAlive`, via `tmux has-session`). Needs `tmux` (`runner.ErrNoTmux`). |
| `tend workflow run <run-id>` | Hidden. The runner: drives one workflow run to a terminal state and exits — hosted in tmux session `tend-wf-<run-id>` by `start`, the TUI's `w` chord, or `resume`, never run by hand. `--takeover` re-enters a run left `running` by a dead runner instead of claiming it from `pending`. |
| `tend mcp --task-id <id> [--step-run-id <id>]` | Hidden. Runs tend's MCP server over stdio, bound to one task — spawned by a launched `claude` session, never by the user directly. Task tools: `get_current_task`, `get_task`, `list_subtasks`, `create_task`, `create_subtask`, `update_task_body`, `append_task_body`, `set_task_state`, `set_task_project`, `move_task`, `set_task_tags`, `set_task_priority`, `set_task_due`, `get_current_project`, `list_projects`. `--step-run-id` (set by the workflow runner) adds the step tools `get_workflow_step` and `finish_step` for that step run. Workflow authoring tools, available to every session regardless of flags: `list_workflows`, `get_workflow`, `create_workflow`, `update_workflow`, `add_workflow_step`, `update_workflow_step`, `set_step_prompt`, `reorder_workflow_steps`, `delete_workflow_step`, `add_workflow_edge`, `delete_workflow_edge` (no `delete_workflow` — that stays in the TUI). |
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

- **List view (default).** A grouped/tree view of the live view (sub-tasks nest under their parent), with vim-style navigation, search, a `:`/`Ctrl-P` command palette, and quick add. Tasks group by state by default; the `g` chord regroups by priority or by latest agent-session status (`gg` stays "top of list"). A project column scopes the list to the selected project (or shows all). `m` opens a move-to-parent picker that re-parents the selected task and its sub-tree under another task in the project, or promotes it to the top level (the `(top level)` row). `b` opens the **dependency picker** (`dependencypicker.go`): every open task across every project, the ones the selected task already waits on checked and listed first; ⏎ or a digit toggles a row (`AddDependency`/`RemoveDependency`) and the picker stays open, so several blockers are set in one visit; a refused edge (a cycle) shows in the status line and leaves the row unchecked. The list row carries a dependency cell after the sub-task count: `⊘N` in the blocked colour while N blockers are still open, a green check once every blocker is done (the task is free to start), blank when it waits on nothing.
- **Detail pane.** The heart of the tool: glamour-rendered markdown body, a sub-task checklist, a `BLOCKED BY` section (the tasks this one waits on, done ones checked off, with an open count) and a `BLOCKS` section (the tasks waiting on this one), a `SESSIONS` section (this task's Claude Code sessions — launch, resume, or attach to a backgrounded one), and a `LOG` section (manual notes plus auto-generated session recaps). Scrollable and independently focusable so long histories are reachable. URL detection lets the user open a link under the cursor or all of them via the OS opener.
- **Triage view.** Filtered to `inbox`. Fast keys to set state, assign a project, add tags or a due date, open the body in `$EDITOR`, or send to `someday`/`done` — the batched processing pass.
- **Standup view.** Manual notes grouped by task plus a generated activity summary (completed/blocked/started, derived from `task_events`); yank the whole thing as markdown.
- **Workflows view.** Authoring for agent workflows (`internal/workflow`): the workflows on the left, the selected one's steps on the right in `sort_order`, three panes walked with `h`/`l` (workflows → steps → the selected step's edges). Create/rename/duplicate/delete workflows; add, reorder and delete steps; set a step's model, permission mode and kind (agent/gate); edit its prompt template in `$EDITOR`. The step list doubles as the **text graph preview** (`workflow.Preview`): numbered steps, each annotated inline with every edge that is not the linear default `done -> next step` (`2  review  [approve -> 3]  [reject -> 1 (max 3)]`), and `[done -> end]` on a step nothing leaves. The **EDGES** sub-list under it shows the selected step's edges as `on <outcome> -> <step> (max N)`; `n` adds one and `e` edits one through a three-stage flow (outcome prompt, target-step picker, max-iterations prompt, esc anywhere abandons it; a renamed outcome deletes the old row since `SetEdge` upserts on `(from, outcome)`), `dd` deletes one. Adding a step after one with no edges writes `done -> new step` for you, so a linear workflow needs no edge work. `v` runs `workflow.Validate` (unreachable steps, a non-final step nothing leaves, a prompt naming an outcome its edges do not route, an agent loop-back with no `max_iterations`, templates that fail to render, an agent step with no permission mode -- a headless `claude -p` with none denies every tool call that needs approval and the runner fails the run on the denials, so the mode is asked for explicitly even where the user's own `~/.claude/settings.json` would let the calls through) and lists the problems under the steps, recomputed on every reload so they disappear as they are fixed. An agent session can draft a workflow over MCP (§8, "Authoring over MCP") and it shows up here as it is written, since the view reloads on every DB change like the rest of the app.
- **Running a workflow (`w`).** From the list or detail pane, `w` picks a workflow to run on the selected task (the picker is type-to-filter: a fuzzy, in-order match on the name, substring hits listed first; a digit picks by visible row), checks it can run at all (it has steps, every step prompt renders, `claude` and `tmux` are on `$PATH`), prompts for a cwd (defaulting to the task's last session directory), then writes a `pending` run and starts its runner in a detached tmux session (`runner.Launch`). The chord returns to the TUI at once; the runner (§8) drives every step headlessly from there. A runner that cannot be started fails the run with the reason. The runner's own output is in its tmux session (`tmux -L tend attach -t tend-wf-<run-id>`) and `runner.log`, but watching a run never needs either — see the run view below.
- **Watching a run (`v`, run view).** The detail pane gains a `WORKFLOWS` section under `SESSIONS`: one row per run on the task (state glyph, workflow · current step, state, elapsed, "output Ns ago" from the step log's mtime, and "runner gone" when the run says a runner owns it but no `tend-wf-<run-id>` tmux session exists). `v` opens the run view on the task's latest run, with every run on the task in a `RUNS` sidebar on the left (the projects column's width; `j`/`k` there switch the watched run, reloaded with it so states stay current): the run's step runs in the middle — iteration marker for a loop-back, outcome or the live state, the current step highlighted — and the selected step's log on the right, rendered from its stream-json (`agent.RenderStream`: assistant text and `⚙ Tool: summary` lines; `v` flips to the raw lines). `h`/`l` walk runs → steps → log and back, as they move between panes everywhere in the app; `l` is never a toggle. The log tail-follows while the run is live. Controls write exactly what the CLI would: `p` pauses (`SetRunState(paused)`; the runner SIGTERMs the step) or resumes a paused run (`runner.Resume`), `cc` cancels, `a`/`x` decide the gate the run is waiting at (`FinishStepRun`, refused for an outcome the gate's edges don't route), and `t` takes the current step over (next bullet). The TUI never drives the run: everything it shows is read from SQLite and the log file, and nothing is derived from stream events. Live updates ride the session poller's tick — `pollRuns` snapshots the active runs (state, current step, log mtime) and the shared `sessionsPolledMsg` reloads whichever view is up; there is no second timer.
- **Taking over a step (`t`, `internal/tui/takeover.go`).** Moving from watching a run to driving it, over the existing resume path — a headless step is one claude session, and the same session resumes interactively once nothing else drives it. `t` on a running or paused agent step writes `paused` on the run, waits for the runner to let go (its `tend-wf-<run-id>` tmux session disappearing, via the `runnerAlive` seam; a runner that already died skips the wait; one that never stops leaves the run paused with a flash after `runnerStopTimeout`), then resumes the step's session exactly as `r` does — tmux-wrapped, terminal handed over, and with the step's MCP tools bound (`WriteMCPConfig` with the step run id, so `finish_step` works by hand too). `r` on a paused run's step session from the session picker or the agents view is the same takeover (`resumeGuardedCmd`); while the run is live and not paused it stays refused. On return the takeover picker asks what the run should do: **continue the run** (pick the step's outcome from its edges, paste an optional deliverable into a modal, `FinishStepRun`, then `runner.Resume`; a step that already called `finish_step` inside the session just resumes), **hand the step back** (`runner.Resume` with the step untouched: the runner continues the same session headlessly), **rerun the step** (`runner.RestartStep` clears the step run's session id, and the next runner starts the step over on a fresh session, same step run and iteration), or **abandon** (`cancelled`); esc leaves the run paused. No recap fires for a takeover return, since a `claude -p --resume` recap would race the runner's own headless resume of the same session. A backgrounded (detached) takeover session keeps the run paused with no picker — it is still a live claude on the step's transcript. Because every resume goes through `runner.Resume`, taking over a step whose runner died behaves like `tend workflow resume` afterwards. Taking over a gate is approving or rejecting it; `t` says so.
- **Agents view (`A`).** Every Claude Code session in the selected project, across its tasks, in one list — the projects column stays on the left and scopes it exactly as it scopes the task list (`Store.ListSessionsForProject`, joined through the owning task). Live sessions by default, ordered blocked → working → idle → starting, then most recently active; `C` adds the ended ones. Each row is the picker's row (status glyph, label, its task, time since last active) plus a `headless` marker for a workflow step's session. The right pane is the session's detail: for an interactive session, its task's detail pane (body, sub-tasks, SESSIONS, WORKFLOWS, LOG) so the recap is one keypress away; for a headless one, its step's stream-json log, tail-followed off the poller's tick exactly as the run view does (`v` in the pane flips raw; `v` from the list opens the run view and comes back here). `⏎`/`r` joins the selected session through the same guard as the `r` picker (a headless session is refused while its run is live and not paused). `dd` kills it: an interactive session through `agent.KillSession` on its tmux session, then `SetSessionStatus(ended)` — a status write, never `DeleteSession`, since a session that ran is the task's history; a headless one through its run (`SetRunState(cancelled)`, which the runner polls for and SIGTERMs the step on), so process ownership stays with the runner.
- **Editing the body.** Shells out to `$EDITOR` — there is no in-terminal markdown editor.
- **Live updates.** The TUI reloads on its own when another process commits to the database — an agent session's MCP tool call, `tend add` from another shell, a workflow runner. There is no daemon or socket: `store.Watcher` polls SQLite's `PRAGMA data_version` (the mechanism the SQLite docs name for this) on a dedicated pinned connection every 500ms, and a change becomes a `dbChangedMsg` that runs the same reload fan-out as a mutation, minus the status flash. Rules for that path: background reloads re-select the list row **by task id**, never by index, and never reset the detail pane's scroll; a change arriving mid-input (`/` filter, prompt, note modal) or while a reload is already in flight is deferred and applied once, not dropped — the pragma read that noticed it is consumed, so there is no second chance to see it.

Full key bindings live in `internal/tui/keys.go` and are discoverable in-app via `?` — not duplicated here since they're a fast-moving implementation detail, not architecture.

## 8. Agent sessions (Claude Code integration)

A task can have one or more Claude Code sessions bound to it, launched and managed from the detail pane's `SESSIONS` section (`r`). This is the mechanism by which a task carries not just its own notes but a live or resumable record of the agent work done against it.

- **Launch/resume.** `internal/agent.LaunchCmd`/`ResumeCmd` build the `claude` invocation; the TUI hands it to `tea.ExecProcess` for the terminal handoff. A session's `claude --session-id` is generated up front (`session_id.go`) so tend never has to discover it after the fact. `LaunchCmdWith` adds the per-step extras a workflow run needs — an initial prompt (claude's positional argument), `--model`, and `--permission-mode` — and is otherwise `LaunchCmd`.
- **Row at launch (tend task #177).** The `agent_sessions` row is written *before* the handoff (`Store.CreateSession`, inside `launchSessionCmd`'s Cmd), with status `starting` and `status_updated_at` stamped, so the session's own hooks land on a row from its very first turn — a fresh session reads `starting` then `idle` without a resume, and a headless runner has something to watch. It used to be written when `tea.ExecProcess` returned, which dropped every hook a brand-new session fired (`SessionStart`, the first `Stop`). The handoff returning only touches the row (`sessionFinishedMsg`); a handoff that returns an error deletes it (`Store.DeleteSession`) so a broken launch leaves no phantom session. The runner writes its step's session row the same way, right before exec.
- **Backgrounding.** Sessions run inside `claude` wrapped in `tmux`, on a dedicated `-L tend` socket with a generated, hands-off config (`internal/agent/tmux.go`). Detaching (`C-h` or `C-Space d`) returns to tend while `claude` keeps running; resuming a task with a live backgrounded session re-attaches instead of starting a second process. Any `tend` instance on the host can attach, since it's the same socket and the same shared SQLite file.
- **Status.** `agent_sessions.status` is populated two ways: Claude Code hooks (`SessionStart`/`Stop`/`Notification`/`SessionEnd`), injected via a per-session `--settings` file and reported through the hidden `tend agent-hook` command; and, for the one state no hook covers (actively generating/running a tool, "working"), a poller in `internal/tui` that reads the pane's rendered text via `tmux capture-pane` and classifies it (`internal/agent/status.go`). Hook-reported status always wins a race against the poller's guess (a compare-and-swap on `status_updated_at`). A workflow step's session has no pane of its own (it runs inside the runner's tmux session), so the runner writes `working` itself right before exec and `ended` right after; hooks still land in between. What the list row, the `g a` grouping and the detail glyphs show is `Store.SessionStatuses`, which merges live runs in: a task with a non-terminal run takes the run's state mapped into this vocabulary (`workflow.RunState.SessionStatus`: pending→starting, running→working, waiting_review→blocked, paused→idle), except that a paused run yields to the task's latest session status when there is one (a takeover in progress) — so a gate waiting at a run with no session rows still reads blocked, with no fake session row and no second glyph family. Terminal runs fall through to the sessions.
- **Recap.** When a session's terminal handoff returns (ended, not backgrounded), tend fires a headless `claude -p --resume` follow-up asking for a short label + recap, and logs the recap as a normal `LogEntry` on the task — the fix for "I took a break and lost the thread." A session backgrounded instead of exited defers this (`needs_recap`) until some tend instance observes it's really gone.
- **MCP.** `tend mcp --task-id <id>` is spawned by `claude` itself (via a per-session `--mcp-config` tend writes at launch, `internal/agent/mcp_config.go`) and gives that session direct read/write access to its bound task — creating sub-tasks, updating the body, changing state, setting project/tags/priority/due — without inventing a scratch markdown file. The full tool list is in §6; the handlers are in `internal/mcpserver/tools.go`. There is deliberately no log-entry tool: log entries are the user's, agents write to the task body. The same server also carries the workflow authoring surface (`internal/mcpserver/workflows.go`, `registerWorkflowTools`): every session, not only a workflow step's, can list, create and edit workflow definitions — drafting a workflow is ordinary task work and binds to no task or run. See "Authoring over MCP" under Workflows below.

### Workflows

A workflow (`internal/workflow`, §5) is a graph of steps a task can run. This is the third thing tend does (§1), and it reuses everything above rather than adding a parallel system: an agent step **is** a Claude Code session on the task (an `agent_sessions` row with `workflow_step_run_id` set, the same `--session-id`/`--mcp-config`/`--settings` plumbing, the same hooks), a gate is a pause, and every bit of run state is a row in the same SQLite file.

**How it squares with "no daemon."** Nothing in tend runs unless something is being tended. A workflow run gets exactly one process for exactly its lifetime: `tend workflow run <run-id>`, hosted in the tmux session `tend-wf-<run-id>` on the same `-L tend` socket the interactive sessions use, started by the TUI's `w` chord or `tend workflow start` (both via `runner.Launch`) and gone when the run reaches a terminal state. It is not a scheduler, does not watch for new runs, and is never started at login; two runs are two processes. Because all of the runner's state is in SQLite (`workflow_runs.state`, `current_step_run_id`, the step run rows), the process is disposable: the TUI and the `tend workflow` CLI drive a run only by writing rows (`paused`, `cancelled`, a gate's `FinishStepRun`) that the runner polls for, a dead runner is replaced by `tend workflow resume` (or a takeover's `runner.Resume`) rather than recovered, and the TUI reads everything it shows from the DB and the step log file over the existing 500ms poll — no socket, no IPC. The surfaces, in the order a reader meets them: authoring in the **Workflows view** and running/watching via `w`/`v`/`t`/`A` (§7); the `tend workflow` command group (§6); the five tables (§5); the runner and step tools (the bullets below); the domain rules in `internal/workflow` (§4).

- **Headless steps.** `HeadlessCmd` is the `claude -p` counterpart of `LaunchCmdWith` for a workflow runner: same pinned `--session-id`, `--mcp-config` and `--settings` (hooks and MCP tools work in print mode), plus `--output-format stream-json --verbose` and, when `LaunchOpts.AppendSystemPrompt` is set, `--append-system-prompt` (adds to claude's default system prompt rather than replacing it; accepted on a `--resume` turn too). `RunHeadless` runs it with stdout tee'd live to a per-step log (`StepLogPath`: `${XDG_DATA_HOME}/tend/runs/<run-id>/<step-run-id>.jsonl`, path stored on the step run) and parses the final `result` event into a `HeadlessResult` — the fallback deliverable for a step that never calls `finish_step` and has no outcome but `done`. A `-p` run with no `--permission-mode` denies tool calls silently and still reports success; the only signal is `PermissionDenials`. Cancelling the ctx SIGTERMs the step's process group; the session stays resumable with `claude --resume`.
- **Workflow runner.** `internal/runner` is the process behind a workflow run: `tend workflow run <run-id>`, hosted in tmux session `tend-wf-<run-id>` on the same `-L tend` socket, one process per run, gone when the run ends — never a daemon. It claims the run (`Store.ClaimRun`, a CAS, so two runners for one run see one winner; tmux's own duplicate-session refusal is the second guard), then loops: pick the next step from the previous step run's outcome via the live `workflow_edges` (no edge → run `done`; an edge's `max_iterations` exceeded → run `failed` with a message on `workflow_runs.error`), render the prompt (`Input` = previous deliverable, or the previous step's own input when its deliverable is empty; a loop-back edge to a step that already ran carries it as `Feedback` instead and keeps the original `Input`), write the step run and its session row (tmux_session `''` — the pane is the runner's, not claude's), and `RunHeadless` it with the log path stored first. Every agent step also gets a runner-built system prompt block (`workflow.StepSystemPrompt`, via `--append-system-prompt`, recorded on the step run as `system_prompt`, migration 00013): which step of which workflow this is, that the run cannot continue until `mcp__tend__finish_step` is called with one of the step's outcomes, and that the tool does not end the session — so the hand-off contract never depends on a `prompt_md` author remembering it. On exit, an outcome written by `finish_step` stands; no result, an error result, or any `PermissionDenials` fails the run loudly. A step that ran fine but never called `finish_step` falls back to the stream's final text as a `done` deliverable **only when `done` is its sole outcome** (`workflow.FallbackAllowed`: no edges, or a single `done` edge). Any other step is nudged once — its own session is resumed with `workflow.NudgePrompt` over the crash-resume path, same step run, not a new iteration — and if that turn also ends without `finish_step` the run fails naming the step and the outcomes it routes, rather than routing on a guessed `done` (seen for real: a haiku review step that "approved" in prose and ended the run silently). Gate steps park the run in `waiting_review` and poll the step run for a decision. `paused`/`cancelled` written by the TUI or CLI are polled between and during steps; a pause SIGTERMs the step and leaves its session resumable. **Crash resume:** state lives in SQLite, so `tend workflow resume <run-id>` (refused while the runner's tmux session is alive) starts a runner with `--takeover` that re-enters at `current_step_run_id`: a log that already holds a `result` event settles the step without claude; otherwise the step's session is continued with `claude -p --resume <id>` (same session id, context intact — verified against claude 2.1.267); a session that cannot be continued restarts the step under a new session id on the same step run. An unfinished step run with an **empty session id** is one nobody owns — `runner.RestartStep` cleared it, the TUI takeover picker's "rerun the step" — and is started over the same way before the log or the old session is consulted (the log is appended to, not truncated). The runner's progress goes to its pane and to `runs/<run-id>/runner.log`.
- **Step tools.** A workflow step's session is spawned with `--step-run-id` as well (`runner.ClaudeExec` passes the step run to `WriteMCPConfig`), which registers two more tools bound to that one step run: `get_workflow_step` (workflow and step names, iteration, `Input`, `Feedback`, and the allowed outcomes, read live from the step's edges — `done` alone when it has none) and `finish_step(outcome, deliverable)`, the hand-off. `finish_step` normalizes the outcome the way edges are stored, refuses one the step does not route (naming the ones it does), and is one-shot (`Store.FinishStepRun`); it does not end the session. The runner tells every step's session about these tools by their exact ids (`mcp__tend__finish_step`, `mcp__tend__get_workflow_step`, constants in `internal/workflow`) in the injected system prompt, and its exit-time fallback only applies to a step whose sole outcome is `done`; everything else must hand off through `finish_step` or the run fails (see "Workflow runner"). `Feedback` is persisted on `workflow_step_runs` (migration 00012) alongside `input` so the tool reads back exactly what the prompt was rendered with. An ordinary session never sees these tools.
- **Authoring over MCP.** `internal/mcpserver/workflows.go` exposes enough of the store's workflow, step and edge writes for an agent session to draft a workflow from a description (tend task #188): `list_workflows`, `get_workflow` (by id or case-insensitive name), `create_workflow`, `update_workflow` (rename/description), `add_workflow_step`, `update_workflow_step` (name, kind, model, permission mode), `set_step_prompt`, `reorder_workflow_steps`, `delete_workflow_step`, `add_workflow_edge` and `delete_workflow_edge`. They are registered for every session (`registerWorkflowTools` in `server.go`), not only a step's — nothing here is bound to a task or a run. The convention is that `get_workflow` and **every mutation** return the whole graph: the steps in authoring order with their prompts, settings and outgoing edges (`to_step` by id and name, `max_iterations`), a `preview` from `workflow.PreviewText` (the same text the Workflows view shows) and `problems` from `workflow.Validate` — so the agent sees the effect of each edit, and what the validator would flag, without a second call; a workflow with no steps yet is a draft, not a problem, and reports none. `add_workflow_step` mirrors the TUI's default-edge convenience: unless `link_from_previous` is false, the previous step, if it has no edges yet, is linked to the new one with `done -> new`, so steps added in order form a linear workflow with no edge work (the previous step is read before the append, so "previous" is unambiguous when two sessions author at once). `prompt_md` goes through `workflow.ValidatePrompt` on `add_workflow_step` and `set_step_prompt` — a template that does not render is refused at authoring time rather than failing every run at launch (a gate's prompt is ignored). `model` and `permission_mode` accept `inherit` for the stored `""`; permission modes are restricted to the TUI picker's four (`default`, `acceptEdits`, `bypassPermissions`, `plan`) because a value claude rejects fails the run long after authoring, while models are not restricted to the three aliases since claude accepts full ids. `add_workflow_edge` upserts on `(from_step_id, outcome)` like `SetEdge`, and `delete_workflow_edge` addresses an edge by the same natural key — "drop review's reject route" — rather than an edge id the agent would have to look up first; a miss names the outcomes the step does route. `delete_workflow_step` removes the step and every edge touching it, refused once a live run has executed it. There is deliberately **no `delete_workflow`**: an agent confusing two ids should at worst lose a step it added, not a procedure the user authored; deleting a workflow stays in the TUI. These tools are for drafting — the Workflows view (§7), which reloads live as the rows land, is where the user refines the result.

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
