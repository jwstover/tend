# Agent sessions — design & phased plan

> Status: Phase 1 implemented and manually confirmed (2026-08-17) — this session was itself
> launched from tend via `r`, confirming session-id pinning, terminal handoff, and transcript
> resolution work against the real `claude` CLI. Phase 2's recap logging is also implemented and
> manually confirmed (2026-08-17); its auto-naming half is implemented and unit-tested but not yet
> manually confirmed against the real CLI. Phase 5 (MCP task read/write) is implemented, unit- and
> integration-tested, and manually confirmed (2026-08-18) with a real `tend mcp` subprocess driven
> over stdio JSON-RPC end to end against a live SQLite file. Phase 4.1 (tmux-backed
> launch/attach/background) is implemented and unit-tested, with its tmux behavior verified
> end-to-end against real tmux 3.7b using a stand-in for `claude`, but not yet manually confirmed
> against the real CLI from tend's own UI. Phase 3 and Phases 4.2-4.4 are unstarted. This is a
> project-specific addendum to `AGENTS.md`, not a replacement for it — the layering, conventions,
> and commit rules in `AGENTS.md` still govern everything built here.

## 0. The problem

Tasks in `tend` already carry their own context (`body_md`, sub-tasks, the note log) so resuming
one is "free" — that's the core design principle in `AGENTS.md` §2. Claude Code sessions are the
one piece of a task's context that currently lives entirely outside that model: sessions pile up
across many tasks, there's no record of which session belongs to which task, and stepping away
from a task loses whatever context lived only in a transcript.

Three separable problems, deliberately not solved by one feature:

1. **Association** — which sessions belong to which task (a list, queryable).
2. **Resumability** — relaunching a specific past session in one keystroke, in the right directory.
3. **Continuity** — not losing context when a session ends and the task goes cold for a while.

Phase 1 solves (1) and (2). Phase 2 solves (3) by extending the existing log/standup machinery
rather than inventing something new. Recall/search (a fourth, fuzzier problem) is explicitly
deferred — see §5.

## 1. Verified assumptions

Before committing to this design, the two load-bearing technical assumptions were spiked directly
against the installed `claude` CLI (v2.1.233):

- **`claude --session-id <uuid> ...`** pins a *new* session's transcript to a UUID chosen ahead of
  time. Confirmed: the transcript lands at `~/.claude/projects/<encoded-cwd>/<uuid>.jsonl`, so
  tend never needs to scan the filesystem to discover a session's ID after launch — it generates
  the UUID, stores the row, and launches.
- **`claude --resume <uuid> ...`** reopens that exact session with its context intact. Confirmed
  via a headless round-trip (`-p` print mode): a second call recalled the first call's reply.
- **`-n/--name <name>`** sets a display name shown in Claude Code's own `/resume` picker and
  terminal title — worth passing the task title here so a session is self-labeling even outside
  tend.
- **Terminal handoff** (`tea.ExecProcess`) is not a new mechanism: `internal/tui/detail.go:234-253`
  (`editBodyCmd`) already suspends the TUI and hands the terminal to an arbitrary interactive
  program (`$EDITOR`). `claude` in interactive mode is the same category of program (full-screen,
  raw-mode, owns stdin/stdout until exit) — no new integration risk here, just reuse.

## 2. Data model

One new table, sibling to `log_entries`/`task_events`, not a column on `tasks` — a task having
*many* sessions is the whole point (the stated pain point: complex tasks spawn more than one).

```sql
CREATE TABLE agent_sessions (
  id             INTEGER PRIMARY KEY,
  task_id        INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  external_id    TEXT    NOT NULL,        -- the claude --session-id UUID
  cwd            TEXT    NOT NULL,        -- working directory it ran/resumes in
  label          TEXT    NOT NULL,        -- task title, snapshotted at launch (like task_events.task_title)
  started_at     TEXT    NOT NULL DEFAULT (datetime('now')),
  last_active_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_sessions_task_id ON agent_sessions(task_id);
```

Notes on choices that deviate from the first pass discussed in conversation:

- **No `status` column.** tend runs `claude` synchronously via `tea.ExecProcess` — by definition,
  when tend regains control the process has exited. There's no concurrent "active" state to track
  in v1 (that would only matter for `--bg` background agents, which are out of scope — see §4).
  `last_active_at` (bumped on every launch/resume) is all the recency signal the picker needs.
  Revisited in Phase 4 (§8.2) once sessions can genuinely run concurrently with tend's own UI.
- **`cwd` lives on the session, not the task.** A task doesn't map 1:1 to a directory — one
  session might run against the app repo, another against a docs repo, for the same task. The
  launch prompt defaults to the most-recently-used cwd among that task's existing sessions, else
  the directory `tend` itself was started from.
- **`label` is a snapshot, not free text.** It's populated from the task title at launch time,
  the same convention `task_events.task_title` and the `log_entries` → `tasks.title` join already
  use elsewhere, so sessions still read correctly after a task is renamed or deleted. No new
  prompt/UI needed to collect it.
- **`ON DELETE CASCADE` on `task_id`**, matching sub-tasks (`tasks.parent_id`) rather than the
  survives-deletion pattern used by `log_entries`/`task_events`. A session's reason to exist in
  tend is resuming *that task's* work; once the task is gone (a deliberate `dd`-confirmed action)
  there's nothing in tend for it to resume into. The underlying Claude Code transcript is
  untouched either way — only tend's pointer to it goes away.

## 3. Package layout

Follows the existing dependency direction exactly (`AGENTS.md` §4): `task` knows nothing about
I/O; `store` is the only SQL; `tui`/`cli` consume `store` through an interface.

```
internal/
├── task/
│   └── session.go        # Session domain type — zero I/O, mirrors log.go's LogEntry shape
├── store/
│   ├── migrations/00004_agent_sessions.sql
│   ├── queries/sessions.sql
│   └── store.go          # CreateSession / ListSessionsForTask / TouchSession
├── agent/                # NEW — the I/O edge for the claude CLI, same role internal/jira
│   │                      # plays for the Jira REST API: the only package that shells out
│   │                      # to `claude` or generates session UUIDs.
│   ├── agent.go           # LaunchCmd(cwd, sessionID, label) / ResumeCmd(cwd, externalID) *exec.Cmd
│   └── session_id.go      # NewSessionID() — UUIDv4 via crypto/rand; no new dependency
└── tui/
    ├── sessions.go         # launch/resume tea.Cmds (ExecProcess), sessionpicker overlay
    └── detail.go           # + a SESSIONS section, same shape as SUB-TASKS / LOG
```

`internal/agent` is intentionally named for the *capability*, not the vendor, but it will only
target the `claude` binary in phase 1 — no multi-provider abstraction. `AGENTS.md` §0 is explicit
about not designing for hypothetical future requirements; if a second agent CLI ever matters,
generalize then, with a real second case in hand.

## 4. Phase 1 — association + launch/resume

**Deliver:**

- `00004_agent_sessions.sql` migration, `sqlc` queries, `Store` methods (`CreateSession`,
  `ListSessionsForTask`, `TouchSession`).
- `task.Session` domain type.
- `internal/agent`: session ID generation, `LaunchCmd`/`ResumeCmd` command builders. Missing
  `claude` binary (`exec.LookPath` failure) degrades to a clear status-line error, never a raw
  `exec: "claude": executable file not found in $PATH`.
- TUI: a new `r` keybinding (free in the current `keyMap`). On the selected task:
  - No sessions yet → prompts for a cwd (prefilled with the best-guess default) and launches.
  - One or more sessions exist → opens a picker overlay (same shape as `urlpicker.go`: numbered
    rows, ↑/↓ or digit to choose, ⏎ to act) listing sessions newest-first plus a `+ new session`
    row. Choosing an existing row resumes it in its stored cwd; choosing `+ new` prompts for a
    cwd like the zero-session case.
  - Either path suspends the TUI via `tea.ExecProcess`, execs `claude`, and on return writes/bumps
    the `agent_sessions` row and refreshes the detail pane.
  - The detail pane (`detail.go`) grows a `SESSIONS` section under `LOG`, same visual language as
    `SUB-TASKS`: count, relative time, cwd tail, label.
- `docs/agent-sessions-plan.md` (this file) — kept up to date as the phases land.

**Acceptance:** from a task's detail pane, `r` shows past sessions (if any) plus "new session";
launching pins a session ID, hands the terminal to `claude`, and on return the session is
recorded and visible in the SESSIONS list; resuming a listed session relaunches `claude --resume`
in its original directory. No CLI surface yet — this is TUI-only, matching the existing split
where interactive/exploratory actions live in the TUI and `cli` stays limited to fast, scriptable
one-shots (`add`/`ls`/`done`/`log`/`standup`).

**Manual test (2026-08-17): passed.** Launched a session from a task's detail pane via `r`; this
transcript is that session. Confirms the full loop against the real CLI, not just the earlier
spike in §1: session-id pinning, `tea.ExecProcess` terminal handoff, and the launched session
landing in `agent_sessions` and the SESSIONS list.

**Known issue (2026-08-17), resolved by auto-naming (§5):** the SESSIONS section (`detail.go`) and
the picker (`sessions.go`) originally rendered `filepath.Base(sess.Cwd)` as the row's identity, not
`sess.Label` — harmless when a task's sessions run in different directories, but launching every
session from the same repo (the common case) made every row read as that repo's directory name,
surfaced by the manual test above where every row just said "tend". Fixed by switching both views
to render `sess.Label` directly, now that §5's auto-naming keeps it meaningfully distinct per
session instead of a static task-title snapshot.

## 5. Phase 2 — continuity (recap logging + auto-naming implemented)

**Deliver:** when a launched or resumed session's `tea.ExecProcess` returns, fire a headless
follow-up (`claude -p "<one-paragraph recap prompt>" --resume <id>`) and store the result as a
`task.LogEntry` linked to the task — reusing `Store.AddLogEntry`, no new table. This shows up for
free in the task's existing LOG section and in `tend standup`, which is the actual fix for "I
took a break and lost the thread": the next time the task is opened, the recap is sitting right
there next to the manual notes.

The recap call fires fully async off the update loop, consistent with how `captureTask`'s Jira
lookup already runs without blocking capture — via `app.recapSessionCmd`
(`internal/tui/sessions.go`), batched alongside the existing session-write command from both the
`sessionFinishedMsg` and `sessionResumedMsg` handlers (`internal/tui/app.go`). A failed/timed-out
recap call (bounded by `agent.RecapTimeout`, 90s) is swallowed silently — no error flash — since
losing an automatic recap isn't something the user needs to act on. Recap entries are prefixed
`"[Claude] Session recap — "` (`recapNotePrefix`) so they read as clearly distinct from hand-typed
notes in the LOG section, with no schema change.

**Acceptance:** ending a session appends a recap note to the task's LOG section without any
manual step. **Implemented, covered by tests** (`internal/tui/recap_test.go`), **and manually
confirmed end-to-end against the real CLI (2026-08-17)**, the same way Phase 1 was verified.

**Auto-naming — implemented (2026-08-17).** The same headless follow-up call is also the fix for
the known display issue in §4: `agent.RecapPrompt` now asks for a two-line `LABEL: ...` /
`RECAP: ...` reply instead of a bare paragraph, and `agent.ParseRecapResponse` splits the two
apart. A successfully-parsed label is persisted via the new `Store.UpdateSessionLabel` (keyed by
`external_id`, since that's what `recapSessionCmd` has in hand for both a freshly launched session
— whose row id it never sees — and a resumed one alike), replacing the static task-title snapshot
`CreateSession` wrote at launch. `detail.go`'s SESSIONS section and `sessions.go`'s picker now
render `sess.Label` directly instead of `filepath.Base(sess.Cwd)`. A reply that doesn't parse into
the two-marker shape falls back to exactly the pre-auto-naming behavior — the whole trimmed
response logged as the recap, no rename — so a malformed model reply degrades gracefully rather
than losing the recap outright. This is strictly better than either option considered and declined
in §4: it costs no extra keystrokes (unlike a launch prompt) and it actually distinguishes sessions
on the same task (unlike reusing the task title), because it's derived from the one thing that's
genuinely different between them — what happened. (Not pursued: switching the recap call to a
cheaper/faster model tier for this classification — `RecapCmd` has no model flag today; worth
revisiting only if recap latency/cost becomes a real complaint.)

## 6. Phase 3 — recall (deferred, not designed)

Cross-session search/recall was the original prompt for this whole feature, and deliberately the
least-specified part. Once phase 2 is producing real recap notes, recall is mostly "full-text
search over `log_entries`," which is a much smaller problem than indexing raw transcripts. Not
scheduling this until there's real data to look at and a clearer sense of what "find it" should
mean in practice.

## 7. Out of scope for phase 1 (do not build)

- Any agent CLI other than `claude` (see §3).
- `tend session` CLI subcommands (list/resume from the shell). Cheap to add later; not needed to
  hit the stated pain point, which is inherently an interactive "pick one and jump in" flow.
- Background/detached agents (`--bg` / `claude agents`) — a different concurrency model than the
  synchronous handoff this design relies on. Revisited in Phase 4 (§8) via a tmux-backed handoff
  instead of `claude`'s own `--bg` flag.
- Renaming or manually editing a session's label — declined on purpose, not just deferred; see
  §4's known issue and §5's auto-naming note for the preferred direction instead.
- Deleting an individual session record (cascades only via task delete).
- Anything from §5/§6 above.

## 8. Phase 4 — background sessions & cross-instance reconnect (not started)

Prompted by a direct ask: launch a session, hand it to the background without ending it, return to
the tend UI, and later attach to that same running session — potentially from a second `tend`
process. Phase 1 deliberately scoped this out (§7: "a different concurrency model than the
synchronous handoff this design relies on"); revisiting now that phase 1's handoff mechanism is
proven against the real CLI. Independent of Phase 2/3 — no ordering dependency either way.

**Prior art:** [herdr](https://herdr.dev) is a terminal multiplexer purpose-built for AI coding
agents (Claude Code, Codex, Amp, Devin CLI) — panes are classified into blocked/working/done/idle
in a sidebar. Its identity layer is a `SessionStart` hook that reports session identity to a local
socket; the ongoing blocked/working/done/idle state, however, comes from pattern-matching the
rendered pane ("screen manifest detection"), not from hooks — Claude Code has no push event for
"still running a tool." §8.2/§8.3 split on the same line: hooks for identity + coarse lifecycle,
screen-scraping only for the gap hooks can't cover.

### 8.1 tmux-backed launch/attach — implemented (2026-08-18)

`tea.ExecProcess` previously handed the terminal straight to `claude` — when tend suspended,
`claude` *was* the foreground process, so there was no backgrounded state to return to and nothing
a second process could attach to. Fixed: tend execs `tmux`, not `claude` directly; `claude` runs
inside a tmux session that outlives the attached client.

**Verified against tmux 3.7b, not assumed.** Three findings drove the shape of this, each probed
directly rather than taken from folklore:

- **tmux's nesting guard is per-server**, matched on the attaching client's tty against *that
  server's* panes — not on `$TMUX` alone. Probed from inside a real pane: `tmux attach` →
  "sessions should be nested with care"; `tmux -L other attach` → a plain "no sessions", i.e. the
  guard never fired. The dedicated `-L tend` socket is therefore **load-bearing for the nested
  case**, not just hygiene: it's what lets tend launch a session when tend itself was started from
  inside the user's own tmux, with no `unset TMUX` hack. Don't "simplify" it away.
- **`bind -n C-h` does not capture Backspace.** tmux parses `0x08` as `C-h` and `0x7F` as `BSpace`
  into separate key-table entries. Probed by injecting raw bytes into a pty: C-h, Backspace, C-h
  fired the binding exactly twice. Terminfo here is `kbs=^?` for both `xterm-256color` and
  `tmux-256color`. The only setup where this bites is a terminal whose Backspace sends `0x08`
  (`kbs=^H`); accepted.
- **Setting `prefix` does not clear tmux's default key table.** With `status off`, a stray
  `C-Space c` would silently create a second window with nothing on screen to reveal it, stranding
  the user in what looks like a broken claude. Hence `unbind -a -T prefix`.

**Implemented:**

- `internal/agent/tmux.go` — `SocketName`, `SessionName`, `TmuxInstalled`, `ConfigPath`,
  `WriteConfig`, `WrapTmux`, `AttachCmd`, `HasSession`, `KillSession`. `LaunchCmd`/`ResumeCmd` were
  left alone: `WrapTmux` *transforms* a direct claude command into a tmux one, so the degrade path
  is simply "don't wrap" and the existing command builders stay independently testable.
- All tmux calls target a dedicated server, `tmux -L tend -f <generated config>`. Never touches the
  user's `~/.tmux.conf`, prefix, or plugins, and stays out of their `tmux ls`.
- **Session name is `tend-<externalID>`**, not `tend-<agent_sessions.id>` as this section originally
  specified. The row id doesn't exist at launch time — `CreateSession` runs when the terminal
  handoff *returns* (`internal/tui/app.go`), deliberately, so a failed launch saves nothing. The
  external UUID is already generated before `LaunchCmd`, is shell-safe, and is what Claude Code
  reports as `session_id` in hook payloads, which makes §8.2's correlation a direct lookup rather
  than a join. Naming by row id would have forced writing the row before the handoff and losing the
  don't-save-on-error property.
- **Backgrounding needs no new tend keybinding.** `C-h` (bare) or `C-Space d` detaches; the
  `attach-session` process exits 0, which is all `tea.ExecProcess`'s callback sees, and tend's TUI
  resumes exactly as it does when claude exits cleanly — except the tmux server keeps `claude`
  running underneath.
- **Resume takes one of two paths.** If `has-session` says the tmux session is alive, it *attaches*
  to the running claude rather than starting a second one against the same session id. Otherwise
  (never backgrounded, or the server died with the host) it falls back to `claude --resume` wrapped
  in a fresh tmux session. A pre-8.1 row with no stored name derives the name it would have had, so
  old sessions become attachable from their first resume.
- **Cross-instance reconnect falls out for free**: any tend on the same host, given the name from
  the DB row and the shared `-L tend` socket, runs the same attach command. No new IPC beyond what
  tend already shares — the sqlite DB and the tmux socket. `attach-session -d` so a second tend
  *takes over* rather than mirroring at min-size, which reads as a rendering bug.
- Missing tmux degrades to the pre-8.1 direct-exec behavior. Launch and resume still work;
  backgrounding and reconnect just aren't offered. tmux is a capability, never a requirement.

**Generated config** lives at `$XDG_CONFIG_HOME/tend/tmux.conf` — a stable path, not a temp file,
because tmux reads `-f` **only when the server starts** and never re-reads it. Iterating on the
config therefore needs `tmux -L tend kill-server` to take effect. Two lines look like boilerplate
and aren't: `unbind -a -T prefix` (above), and `escape-time 10` — esc is Claude Code's interrupt
key, and tmux's default delay makes esc-to-interrupt feel dead through the extra layer, which would
be the most-noticed regression of wrapping claude in tmux at all. The rest preserves what nesting
otherwise degrades: truecolor (`terminal-features ",*:RGB"`), OSC 52 copy (`set-clipboard on`), and
sizing to the attached client (`aggressive-resize on`). `status off` keeps first use
indistinguishable from a direct launch.

**Detach vs. exit is the one real trap.** Both are a clean exit 0 from `tea.ExecProcess`, but the
Phase 2 recap call (`claude -p --resume <id>`) must not fire against a session that's merely
detached — that would put two processes on one session id and one transcript JSONL. The callback
resolves it by asking tmux: `HasSession` alive → backgrounded → skip the recap and record the debt;
dead → the session really ended → recap exactly as before. The MCP config temp file is likewise
only cleaned up when the session is really over, since a backgrounded claude still holds it.

**Schema:** migration `00005_tmux_sessions.sql` adds `agent_sessions.tmux_session TEXT` (stored
explicitly rather than re-derived, so the naming scheme can change without a backfill) and
`agent_sessions.needs_recap INTEGER`.

**Known limitation, closed by 8.2:** a session that is backgrounded and never re-attached logs no
recap. `needs_recap` records the debt but nothing drains it yet — 8.2's `SessionEnd` hook marks the
session ended, and any tend instance settles the pending recap on refresh. This is a stated gap,
not an oversight, and it gives 8.2 a second job beyond status glyphs.

**Verified end-to-end (2026-08-18)** with a stand-in for `claude`, launched nested inside a real
tmux session: launch attaches with no nesting guard; `C-h` exits the client with status 0 while the
session stays alive; `AttachCmd` reattaches; the inner process exiting destroys the session and the
server, leaving no orphan. Unit tests cover argv construction (`internal/agent/tmux_test.go`), the
schema round-trip (`internal/store/store_test.go`), and the recap-skip behavior
(`internal/tui/background_test.go`). Not yet exercised against the real `claude` binary from tend's
own UI — that's the manual confirmation step, as in Phases 1 and 2.

**Out of scope for 8.1:** cross-host reconnect (needs SSH-forwarded sockets — materially bigger, no
concrete need); a background-only launch (`-d`, never attach); a kill affordance for a wedged
session (`KillSession` exists but nothing in the TUI calls it yet — distinct from §7's ruled-out
deletion of session *records*). Also unaddressed: the tmux server freezes the environment of
whichever tend process started it, and `update-environment` only refreshes its own list on attach —
pass `-e` explicitly if that ever bites.

### 8.2 Status, part 1 — hook wiring (identity + coarse lifecycle)

Reintroduces the `status` column §2 explicitly declined for phase 1, now that sessions really can
run concurrently with tend's own UI.

- New hidden CLI command `tend agent-hook <event>`: reads Claude Code's hook JSON payload from
  stdin, writes into sqlite via the existing `Store` — no new socket server, tend already owns a
  shared, WAL-mode DB every instance can write to.
- Injected via a generated `--settings` file at launch time (`agent.LaunchCmd`), wiring:
  - `SessionStart` — confirms `external_id` ↔ `tmux_session` ↔ task row (mostly a sanity check;
    tend already knows this at launch time).
  - `Stop` — fires when Claude finishes a turn and sits at the input prompt → status `idle`.
  - `Notification` — fires on permission prompts / idle nudges → status `blocked`.
- Migration `00006_agent_session_status.sql` adds `agent_sessions.status TEXT NOT NULL DEFAULT
  'unknown'` and `status_updated_at TEXT`.
- Covers start/idle/blocked cleanly. Does **not** cover "actively working" — Claude Code has no
  hook that fires mid-tool-call — hence §8.3.

### 8.3 Status, part 2 — capture-pane polling ("working" detection)

The brittle half, same tradeoff herdr accepts for the same reason: no first-party alternative
exists for the interactive TUI (only headless `--output-format stream-json` is machine-readable,
and that's a different, non-interactive mode tend isn't switching to here).

- While a tend process is in the foreground, a `tea.Tick`-driven poller (~3-5s) runs `tmux -L tend
  capture-pane -t <name> -p -S -5` for each task with a live session and pattern-matches the tail
  against known Claude Code TUI chrome (spinner, "esc to interrupt", permission-box borders).
- Isolated in one function, `internal/agent/status.go: classifyPane(text string) Status`, so the
  matching patterns can be updated independently as Claude Code's own TUI changes without touching
  anything else.
- Treated as a refiner, not a source of truth: `Stop`/`Notification` hook events set
  `idle`/`blocked` authoritatively with a timestamp; the poller only overrides to `working` when
  the pane shows active-tool chrome *and* no hook event has landed more recently.
- No daemon: this only runs inside a live tend process, foregrounded — consistent with tend's
  current no-background-process model. A session backgrounded from every tend instance simply stops
  getting fresh status until some instance re-observes it (acceptable — status is a convenience
  indicator, not the source of truth for whether the session is alive, which `has-session` always
  answers directly).

### 8.4 UI surfacing

- `detail.go`'s SESSIONS section and `sessions.go`'s `sessionPickerView`
  (`internal/tui/sessions.go:149-191`) each grow a status glyph per row (⚡ working, ⏸ blocked, ✓
  idle/done, sourced from `agent_sessions.status`), next to the existing relative-time age.
- `list.go` task rows show the same glyph for the task's most-recently-active session (join on
  `task_id`, order by `last_active_at`), so a background session's state is visible without opening
  the task.

**Open decisions before starting (flag now, resolve at implementation time, same convention as §5):**

- Poll interval, and whether it should back off when the pane is unchanged between ticks.
- Whether `classifyPane` patterns should live as plain Go string matches or get pulled into a small
  data table, given they're expected to need updates as Claude Code's TUI evolves.
- Sequencing 8.1 vs 8.2/8.3 — 8.1 (background + reconnect) is independently useful and answers the
  original ask on its own; 8.2/8.3 (status) can land later as a separate, self-contained follow-up.

## 9. Phase 5 — task read/write via MCP (not started)

Prompted directly by ongoing work on this feature itself: this plan started life as a scratch
markdown file in the project root before becoming this doc. The recurring pain is structural — a
Claude Code session working *on* tend (or any project) has no way to turn "here's what I'm doing"
into an actual tend task/subtask tree; it only has a markdown scratchpad it invents per-project.
Phase 5 closes that gap by handing a launched session real read/write access to the task store
it's already associated with (§2's `agent_sessions.task_id`), via MCP rather than by growing
tend's interactive surfaces.

### 9.1 Package layout

New package, sibling to `agent`/`store`, following the same layering `AGENTS.md` §4 requires of
`cli`/`tui`:

```
internal/
├── mcpserver/            # NEW — the MCP tool surface, third consumer of Store
│   ├── server.go         # builds the *mcp.Server, registers tools, runs the stdio transport
│   ├── tools.go          # tool schemas + handlers, one per row in §9.3
│   └── server_test.go
└── cli/
    └── mcp.go            # NEW — hidden `tend mcp` cobra command, wires mcpserver
```

`mcpserver` declares its own narrow `Store` interface (same "accept interfaces, return structs"
convention as `cli.Store`, `internal/cli/root.go:19-27`) rather than depending on the concrete
`*store.Store` — keeps `cli → mcpserver → store → task` intact, no new violation of the dependency
direction in `AGENTS.md` §4.

New dependency: `github.com/modelcontextprotocol/go-sdk` (the official Go MCP SDK, stdio
transport). Accepted despite `AGENTS.md` §3's stdlib-first bias — same exception already made for
`spf13/cobra`/`pressly/goose`: hand-rolling JSON-RPC plus the MCP handshake isn't worth it for a
real protocol.

### 9.2 Wiring — per-session `--mcp-config`, not a project `.mcp.json`

`tend mcp --task-id <id> --db <path>` is a hidden cobra command (`Hidden: true`, same convention
as the phase-4 `agent-hook` command in §8.2) that opens a `Store` and blocks serving stdio. One
process per Claude Code session, spawned *by* `claude`, not by tend.

tend never touches a project's own `.mcp.json`. Instead `LaunchCmd`/`ResumeCmd`
(`internal/agent/agent.go:35-47`) gain a step that writes a **per-session** config to a temp file
and passes it via `--mcp-config`:

```go
// internal/agent/mcp_config.go
func WriteMCPConfig(taskID int64, dbPath string) (path string, cleanup func(), err error)
```

```json
{
  "mcpServers": {
    "tend": {
      "command": "tend",
      "args": ["mcp", "--task-id", "42", "--db", "/home/jwstover/.local/share/tend/tend.db"]
    }
  }
}
```

Both launch and resume get this — resuming is "continue the work," not just "reread the
transcript," so tools should be there either time. `cleanup()` runs on the same `tea.ExecProcess`
return path that already bumps `agent_sessions.last_active_at`; a stale temp file surviving a tend
crash mid-session is harmless. Gated the same way `--mcp-config` needs `tend` itself resolvable on
`$PATH` — should always be true (tend is the process adding the flag) but degrade quietly rather
than assume.

### 9.3 Task binding

`--task-id` is the load-bearing piece — it's how "update the current task" resolves without the
model guessing an ID, the concrete problem named at the top of this doc. The server loads that
task once at startup and exposes it as both a resource and a default:

| Tool | `Store` method | Notes |
|---|---|---|
| `get_current_task` | `GetTask` | no args; resolves `--task-id` |
| `list_subtasks` | `ListChildren` | defaults to current task |
| `get_task` | `GetTask` | explicit `task_id`, to inspect before touching |
| `create_task` | `AddTaskWithBody` | title + optional body_md; **not** scoped to current task — the "new parent task" case |
| `create_subtask` | `AddChild` | title; `parent_id` defaults to current task |
| `update_task_body` | `SetBody` | replaces body_md; defaults to current task |
| `set_task_state` | `SetState` | `todo`/`doing`/`blocked`/`done`/`someday`; defaults to current task |
| `set_task_project` / `set_task_priority` / `set_task_due` | matching `Set*` | defaults to current task |
| `add_log_entry` | `AddLogEntry` | note on current task — the direct replacement for a scratch doc's running notes |

Every mutating tool still accepts an explicit `task_id` override — the bound task is a convenience
default, not a hard sandbox. tend is single-user/local, so the risk being managed is *accidental*
scope (an agent editing the wrong task because it had to guess an ID), not an isolation boundary
between untrusted parties.

Mutations going through these tools fire the same `task_events` rows any other `Store` caller
produces (`create_task`/`create_subtask` → `EventCreated`, `set_task_state` → `EventState`, etc.,
`internal/task/event.go`) — free auditability, and those events flow into `tend standup` like
anything else. No special-casing needed.

### 9.4 Deliberately excluded

- **No `delete_task` tool.** Matches the standing house rule (§7 declines session deletion for the
  same reason) — deletion stays a manual, `dd`-confirmed TUI action. An agent mass-deleting tasks
  from a misread instruction is worth designing out entirely, not mitigating.
- **No cross-task bulk operations** — no "update every task matching X." Every tool acts on one
  task at a time, so each call is auditable against what the transcript actually asked for.
- **No full-task-list dump tool** in v1. `list_subtasks`/`get_task` cover the "capture phases as
  subtasks" workflow without handing a session the whole task list as unsolicited context. Easy to
  add later (mirrors `tend ls` existing as a thin CLI wrapper) if it turns out to matter.

### 9.5 Lifecycle

`tend mcp` opens the DB the same way `cli`/`tui` already do — `store.Open` against the shared
WAL-mode file (`internal/store/store.go:42-62`), already a documented-safe pattern (§8.2: "tend
already owns a shared, WAL-mode DB every instance can write to"). No new concurrency design
needed. The process lives exactly as long as the `claude` session does; stdin closing is the
natural shutdown signal.

**Deliver:** `internal/mcpserver` package, `tend mcp` hidden command, `agent.WriteMCPConfig` plus
the `LaunchCmd`/`ResumeCmd` wiring, the nine tools in §9.3.

**Acceptance:** from a task-bound session, asking Claude to "split this into subtasks for each
phase" produces real `agent_sessions.task_id`-scoped subtask rows visible in the task's detail
pane, with no manual `tend` interaction and no scratch markdown file involved.

**Sequencing:** independent of Phase 2/3/4 — no ordering dependency. Reasonable to build next
given it's the directly-requested motivation for this doc's own drafting process.

**Implemented (2026-08-18), as designed above with one simplification.** `internal/mcpserver`
(`server.go`, `tools.go`, `store.go`), `tend mcp` (`internal/cli/mcp.go`), and
`agent.WriteMCPConfig` (`internal/agent/mcp_config.go`) landed together with `LaunchCmd`/
`ResumeCmd` gaining an `mcpConfigPath` parameter and `internal/tui/sessions.go` wiring it through
both launch and resume, cleaning up the temp config file once the terminal handoff returns —
matching §9.2 and §9.5 exactly. All nine tools from §9.3's table are registered via the generic
`mcp.AddTool`, which infers each tool's JSON input schema from the handler's argument struct
(required vs. optional falls out of `omitempty`/pointer fields), so there's no hand-written schema
to keep in sync with the Go types. Covered by `internal/mcpserver/server_test.go` (an in-memory
transport pair driving real tool calls against a fake `Store`), `internal/agent/mcp_config_test.go`,
and a manual end-to-end pass: a real `tend mcp` subprocess driven over stdio JSON-RPC against a live
SQLite file, confirming `get_current_task`, `add_log_entry`, and `create_subtask` all land real rows
visible to `tend ls`/the log table.

The one deviation from the design as written: `MCPStoreFactory`/`newMcpCmd` take a zero-arg
`func(ctx) (mcpserver.Store, error)` closure (mirroring `root.go`'s existing `openHere` pattern for
`cli.Store`) rather than threading `--db` through as an explicit function parameter — `--db` is
still a real flag on `tend mcp` (inherited from the persistent root flag, resolved the same way
every other subcommand resolves it), just not part of the factory's Go signature. Behavior matches
§9.2/§9.5 exactly; only the internal plumbing differs from the sketch.
