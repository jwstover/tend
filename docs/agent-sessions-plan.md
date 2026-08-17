# Agent sessions — design & phased plan

> Status: Phase 1 implemented; manual end-to-end test passed (2026-08-17) — this session was
> itself launched from tend via `r`, confirming session-id pinning, terminal handoff, and
> transcript resolution work against the real `claude` CLI. This is a project-specific addendum
> to `AGENTS.md`, not a replacement for it — the layering, conventions, and commit rules in
> `AGENTS.md` still govern everything built here.

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

**Known issue, left as-is for now (2026-08-17):** the SESSIONS section (`detail.go`) and the
picker (`sessions.go`) both render `filepath.Base(sess.Cwd)` as the row's identity, not
`sess.Label`. Harmless when a task's sessions run in different directories, but launching every
session from the same repo (the common case) makes every row read as that repo's directory name
— surfaced by the manual test above, where every row just said "tend". `Label` is stored
correctly (the task title, snapshotted at launch) but isn't wired into either view. Considered
and explicitly declined for now: (a) show `Label` instead — but within one task's own session
list the title is constant across every row, so it doesn't actually distinguish session 1 from
session 2 on the same task, which is the case that matters most; (b) add a second launch-time
prompt for a short free-text label — rejected, not worth the extra keystroke on every launch. See
§5 for the preferred direction: auto-generate a real per-session name from what the session
actually did, the same way the recap note gets generated.

## 5. Phase 2 — continuity (not started)

**Deliver:** when a launched or resumed session's `tea.ExecProcess` returns, fire a headless
follow-up (`claude -p "<one-paragraph recap prompt>" --resume <id>`) and store the result as a
`task.LogEntry` linked to the task — reusing `Store.AddLogEntry`, no new table. This shows up for
free in the task's existing LOG section and in `tend standup`, which is the actual fix for "I
took a break and lost the thread": the next time the task is opened, the recap is sitting right
there next to the manual notes.

Needs a decision before starting: does the recap call block the UI (a brief "summarizing…" beat)
or fire fully async with a status-line flash on completion? Lean async, consistent with how
`captureTask`'s Jira lookup already runs off the update loop without blocking capture.

**Acceptance:** ending a session appends a recap note to the task's LOG section without any
manual step.

**Auto-naming (folded into this phase, not phase 1):** the same headless follow-up call is also
the fix for the known display issue in §4 — ask it for a short (few-word) description of what the
session actually did, alongside the recap paragraph, and store that as `agent_sessions.label`
instead of the static task-title snapshot. A cheap/fast model (e.g. Haiku) is the right tier for
this — it's a one-line classification of a transcript, not real work. This is strictly better
than either option considered and declined in §4: it costs no extra keystrokes (unlike a launch
prompt) and it actually distinguishes sessions on the same task (unlike reusing the task title),
because it's derived from the one thing that's genuinely different between them — what happened.

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
  synchronous handoff this design relies on.
- Renaming or manually editing a session's label — declined on purpose, not just deferred; see
  §4's known issue and §5's auto-naming note for the preferred direction instead.
- Deleting an individual session record (cascades only via task delete).
- Anything from §5/§6 above.
