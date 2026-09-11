I want to port this application from Go to Elixir and Breeze. Investigate what it would take to make that happen. 

https://hex.pm/packages/breeze

## Plan (Plan workflow, 2026-09-11)

### Headline

Feasible, but it is a full rewrite rather than a port. ~21k lines of hand-written Go (plus ~20k of tests). Breeze is the only credible Bubble Tea analogue in Elixir and its component set covers everything tend uses, but it is a 0.x, one-maintainer, 61-star project (v0.5.1, 2026-08-27, MIT, Gary Rennie of Phoenix core). The risky parts are not the TUI translation; they are distribution, cold-start latency, subprocess control, and the ExecProcess-style terminal handoff. Run the four spikes below before committing to any code.

### What exists today (from codebase survey)

- `internal/tui` is ~56% of hand-written code: 6 view modes, 9 overlay pickers, 3-pane resizable layout, tree list, vim chord state machines, ~70 context-overloaded keys, one 237-field model with a 536-line key switch (`internal/tui/app.go`).
- `internal/store`: SQLite via pure-Go `modernc.org/sqlite`, WAL + FK pragmas, 15 goose migrations, sqlc-generated queries, SQL triggers feeding an append-only `task_events` log, a pinned single-connection `PRAGMA data_version` watcher (`watch.go`), millisecond-precision CAS on session status.
- `internal/mcpserver`: go-sdk over stdio, 15 task tools + 2 step tools, one `tend mcp` process spawned by `claude` per session. Output schemas must be objects, `move_task.parent_id` uses 0 as a sentinel.
- `internal/runner` + `internal/agent`: spawns `claude -p --output-format stream-json` with `Setpgid` + `kill(-pid, SIGTERM)` + 5s WaitDelay, tees stdout to a jsonl log, settles outcomes with a nudge-once rule, resumes after crash. Runners live in detached tmux sessions (`-L tend`). Hooks (`tend agent-hook`) run synchronously with a 5s timeout between claude turns.
- `internal/workflow`: `prompt_md` is a Go `text/template` with `missingkey=error`. Stored rows in user DBs are Go templates.
- Multi-process by design: TUI, N claude sessions each with a `tend mcp` child, N runners, transient one-shots, all coordinating through the SQLite file and tmux. Exactly 3 goroutines and 1 channel in the whole non-test tree.
- Distribution contract: `CGO_ENABLED=0`, 4 static archives via goreleaser, `go install`, sub-100ms `tend add`, `exec.LookPath("tend")` resolving the same binary for hooks and MCP config.

### Recommended stack (from web research)

| Concern | Pick | Notes |
|---|---|---|
| TUI | Breeze 0.5.x (+ Termite, BackBreeze) | `mount/render/handle_event/handle_info`, `~H` sigil with Tailwind-style classes, blocks: list, tree, table, tabs, dropdown, input, textarea, markdown, scroll, panel, modal, spinner, keybinding_bar. Nested `<live>` child views. `Breeze.Test` snapshot testing. No fuzzy picker built in. |
| OTP | 28+ | Termite uses the public `:shell.start_interactive({:noshell, :raw})` API on 28+; on 27 it reaches into private `prim_tty` records. |
| SQLite | `exqlite` raw `Exqlite.Sqlite3` API, no Ecto | Precompiled NIFs for darwin/linux gnu+musl arm64/amd64. Ecto adds a 5-connection pool and boot cost. Hand-roll a `PRAGMA user_version` migration ladder. |
| MCP | `anubis_mcp` 2.0 if LGPL-3.0 is acceptable, else `ex_mcp` (MIT) or hand-roll (~400 LOC of newline-delimited JSON-RPC) | `hermes_mcp` is dead (renamed to anubis). Must set `config :logger, :default_handler, config: [type: :standard_error]` or logs corrupt the JSON-RPC stream. Must halt on stdin EOF. |
| Subprocess | MuonTrap 2.0 `MuonTrap.Daemon` | cgroup-kill on Linux, SIGTERM→SIGKILL on macOS. erlexec is the fallback if stdin writes or process-group signalling turn out to be needed. |
| Fuzzy filter | `seqfuzz` 0.2.1 | fzf/Sublime subsequence scoring with match indices; trivially vendorable. |
| Distribution | `mix release --include-erts` baseline, Burrito 1.6 for single binary | escript is out (exqlite NIF cannot load from an escript archive, elixir#5444 still open). Bakeware is dead. Burrito+exqlite on Windows is unconfirmed; darwin/linux only is fine since the Go code is POSIX-only anyway. |
| Process shape | Keep separate OS processes for TUI and MCP | A TUI in raw mode and an MCP stdio server cannot share stdio. tend already has this shape. Optional later: a `tend mcp` shim that pipes to a Unix socket on a long-lived node. |

### Phase 0: go/no-go spikes (do these first, ~1 week, throwaway code)

1. **Terminal handoff.** Build a minimal Breeze app that suspends itself, hands the raw tty to `claude` (or `$EDITOR`), and resumes cleanly on exit or tmux detach. This is the load-bearing UX of tend (`tea.ExecProcess` in sessions.go, detail.go, takeover.go). If Breeze/Termite cannot release and reacquire the tty, stop here.
2. **Cold start.** Burrito single binary with exqlite for macOS-arm64. Measure wall-clock for a `tend add`-shaped one-shot and a `tend agent-hook`-shaped one-shot against the Go binary. Targets: one-shots well under the 5s hook timeout and acceptable for "capture is perceptually instant"; MCP server boot fast enough that claude's session start is not noticeably slower. If unacceptable, decide between (a) a long-lived node with thin shims or (b) keeping a tiny Go/shell capture path.
3. **Subprocess control.** MuonTrap spawning `claude -p --output-format stream-json`, streaming stdout line by line to a jsonl file, cancelling mid-run, and confirming claude's grandchildren (its MCP servers, Bash shells) die. Confirm whether the runner ever needs to write to claude's stdin; if so, evaluate erlexec.
4. **Markdown and ANSI width.** Render a real task body through Breeze's `markdown` block and check fidelity against glamour. Confirm grapheme-aware width/truncate/wrap on styled strings for the row renderers, since `x/ansi` is used at 18 call sites and in 13 test files.

### Phase 1: core, no UI (~2 weeks)

- New mix project `tend` targeting OTP 28 / Elixir 1.18+. CLI dispatch via `Optimus` or hand-rolled `OptionParser`; keep the four-entry-point shape (tui, mcp, workflow run, one-shots).
- `Tend.Task`, `Tend.Workflow` domain modules: pure, zero-I/O, ported 1:1 from `internal/task` and `internal/workflow` including the 10 sentinel errors, `NormalizeName/Outcome`, `Validate` graph problems, `StepSystemPrompt`, `FallbackAllowed`.
- `Tend.Store` on raw exqlite: same DSN pragmas (WAL, busy_timeout 5000, foreign_keys ON), same 11 tables, same triggers. Migration ladder that reads the existing `goose_db_version` table so an in-place upgrade of an existing `tend.db` works. Port the store tests against a real temp DB.
- Change watcher: a GenServer holding one dedicated connection polling `PRAGMA data_version` every 500ms and broadcasting via `Registry`/`Phoenix.PubSub`-free `send`.
- Prompt templates: implement a Go `text/template` subset (`{{.Field}}`, `{{if}}`/`{{else}}`/`{{end}}`, `{{range}}`) with `missingkey=error` semantics so stored `prompt_md` rows keep working. Do not migrate users to EEx.

### Phase 2: MCP server and one-shot CLI (~1 week)

- `tend mcp --task-id N [--step-run-id M] --db P` over stdio with the 17 tools and identical schemas. Preserve object-wrapped list outputs and the `parent_id = 0` sentinel. Logger to stderr, halt on stdin EOF.
- `tend add/ls/log/standup/projects/version/auth jira`, `tend agent-hook`. Keyring via `System.cmd("security", ...)` on macOS and `secret-tool` on Linux, or drop keyring for a 0600 file.
- Port the 31 mcpserver tests and 52 cli tests.

### Phase 3: runner (~1.5 weeks)

- `Tend.Runner` as a GenServer per run: claim (CAS), resume decision tree (`resumeStep` four-way), `nextStep` routing with SortOrder-based loop-back detection and MaxIterations, `settle` (finish_step wins, permission denials fail, FallbackAllowed, nudge once), gate polling, pause/cancel via run-state watcher.
- Headless exec via MuonTrap behind an `Exec` behaviour so the 26 runner tests port with a fake.
- tmux orchestration (`-L tend`, generated conf, has-session/capture-pane asymmetry) ports as shell-outs nearly verbatim. Re-verify the `ClassifyPane` chrome strings against the current claude version.
- Hooks and mcp-config temp-file injection, with the executable path resolved from the release's `bin/tend` rather than `os.Executable()`.

### Phase 4: TUI (~5 to 7 weeks, the bulk)

Order by dependency and value:
1. App shell: alt screen, 3-pane layout (Breeze grid + breakpoints), pane focus, global keybindings, help overlay, whichkey chord panels, palette. Split the god-model: one parent view plus `<live>` child views per pane/mode, each owning its own assigns.
2. List view (tree list over Breeze `list`/`tree` blocks, section headers, custom row renderer, `/` filter via seqfuzz), detail view (markdown block + SUB-TASKS/SESSIONS/WORKFLOWS/LOG), pickers (project, parent, URL, state, priority, tags) built once as a generic fuzzy `dropdown`+`input` component.
3. Triage, standup (OSC 52 clipboard yank; write the escape sequence directly), sessions and takeover (the Phase 0 handoff spike, productionised), agents view.
4. Workflows authoring view and run view (tail-followed log via `File.stream!` + polling).
5. Live reload: `handle_info` on watcher broadcasts, with the same defer-while-editing coalescing rules as `liveReloadInFlight/Deferred`.
6. Port the 212 TUI tests to `Breeze.Test.render_text!` snapshots.

### Phase 5: release (~1 week)

- `mix release` with `include_erts: true`, `strip_beams`, empty `runtime.exs`, explicit `config :back_breeze, render_cache_max_memory_bytes:` to avoid `:os_mon`, trimmed application list.
- Burrito builds for darwin arm64/amd64 and linux arm64/amd64 (gnu, musl). GitHub Actions matrix, release-please retained. Document macOS Gatekeeper/codesigning.
- Cut a `0.x` of the Elixir build alongside the Go build until parity; the shared SQLite schema means both can coexist against one DB during the transition.

### Risks and open decisions

- **Breeze concentration risk.** Pin exact versions, expect breaking changes between 0.x minors, budget for upstream PRs or a vendored fork.
- **Cold start.** Most likely user-visible regression. Spike 2 decides the architecture; do not skip it.
- **MCP library license.** anubis_mcp is LGPL-3.0. Decide before Phase 2: accept it for a personal tool, use ex_mcp, or hand-roll.
- **Prompt template compatibility.** Go template subset is small but must match `missingkey=error` behaviour exactly or existing workflows break silently.
- **Windows.** Out of scope; the Go version is POSIX-only already.
- **Effort.** Roughly 11 to 14 weeks of focused work for parity, dominated by the TUI. A "port the store + MCP + CLI only" cut is ~4 weeks and would let Elixir and Go binaries coexist against one DB if a partial migration is preferred.

### Next steps

1. Decide whether to proceed to Phase 0 spikes or park this as a documented assessment.
2. If proceeding, create subtasks for the four spikes with explicit pass/fail criteria as written above.
