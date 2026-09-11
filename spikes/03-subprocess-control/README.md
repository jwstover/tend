# Spike 3: subprocess control (MuonTrap runs `claude -p`)

**Verdict: PASS, with MuonTrap used as a port program, not through
`MuonTrap.Daemon`, and with a process-group signal layered on top for
cancel.** Under the `muontrap` wrapper, `claude -p --output-format
stream-json` streams into a jsonl file line by line as the events are
emitted, a cancel mid-run terminates claude, and every MCP server and Bash
shell claude had started is gone within a second. If the BEAM itself is
SIGKILLed, muontrap takes claude down with it, which a bare Erlang port does
not do. Three things had to be worked around to get there and one limit
stays, all recorded below. The runner never needs claude's stdin, so
erlexec is not needed.

Throwaway code, per task #207. Pinned to `muontrap 2.0.0`; `jason` only for
the assertions. Machine: Apple M4 Pro, macOS 26.6, claude 2.1.269, OTP 28.1,
Elixir 1.19.1.

## Run it

```sh
cd spikes/03-subprocess-control
mix deps.get
go build -o /tmp/tend-go ../../cmd/tend      # the MCP server the cancel test spawns
test/drive.sh fake                            # offline: BEAM crash + fake child scenarios
test/drive.sh                                 # plus the three real claude -p runs (haiku, cents)
```

`mix run -e 'Subproc.main(System.argv())' -- <scenario>` runs one scenario;
`lib/subproc.ex` lists them. Each prints `PASS`/`FAIL` for its assertions
and `NOTE` for observations. Last full run: `DRIVE: ALL PASS` (see the
numbers in the tables below).

## What is in here

- `lib/subproc/runner.ex`: the shape a production step runner would take.
  One GenServer opens the `muontrap` binary from the hex package as a port
  with `--capture-output`, splits the stream on newlines with its own
  unbounded buffer, writes each line to the jsonl file as it completes,
  acks bytes back to muontrap, and offers two cancel modes.
- `lib/subproc/procs.ex`: process-tree inspection via `ps`, scoped to the
  tree under one pid (this machine had a dozen other `claude` processes
  running throughout).
- `lib/subproc/scenarios.ex`: the assertions. `test/fake_claude.sh` stands
  in for claude offline: one JSON line per 300 ms, a 40 KB line, two
  `sh -c` grandchildren each running a `sleep 600` great-grandchild (the
  claude, shell, command shape of the Bash tool).
- `test/drive.sh`: runs everything, including the BEAM `kill -9` test that
  cannot be run from inside the BEAM.

## Pass criteria and what was observed

| Criterion | Observed | Result |
|---|---|---|
| lines land in the jsonl file as they arrive | fake child at a 300 ms cadence: arrival gaps 306 to 319 ms, every line on disk before the next is emitted. Real claude: `system/init` on disk 1.4 s before `result` | pass |
| long lines survive | 40 KB line (4x muontrap's 10 KB window) intact and valid JSON | pass, but not through `MuonTrap.Daemon` (finding 1) |
| cancel terminates claude | both modes: claude dead, muontrap exited 0.8 to 0.95 s after the cancel | pass |
| no orphaned grandchildren | real claude with 4 MCP servers and a Bash `tail -f`: 0 of 7 descendants survive in either mode. Fake child that ignores its children: 4 of 4 grandchildren survive MuonTrap alone, 0 of 4 survive the group signal | pass with the group signal; see the limit in finding 4 |
| BEAM dies (`kill -9`) | muontrap and claude die with it; the fake's grandchildren are orphaned (no cgroup on macOS) | pass for the child, same limit for grandchildren |
| stdin | never needed; the child must be cut off from it (finding 2) | erlexec not required |

## Findings the production runner must absorb

1. **`MuonTrap.Daemon` corrupts long lines; use the port program directly.**
   The Daemon splits captured output on newlines for its `:logger_fun`, but
   caps the carried-over partial line at 256 bytes
   (`@max_data_to_buffer`). muontrap's flow-control window is 10 KB, so any
   line longer than that is guaranteed to straddle two port messages, and
   any line over 256 bytes that happens to straddle one is cut. Measured:
   the 40 046-byte line came out of the Daemon as 9 582 bytes of invalid
   JSON. A real `system/init` event was 6.9 KB in this run and tool results
   can be far larger. The fix is small: open
   `{:spawn_executable, MuonTrap.muontrap_path()}` yourself with
   `--capture-output`, keep your own buffer, and after each `{:data, bin}`
   write `Port.command(port, acks)` where acks is one byte per 256 bytes
   handled, value `n - 1` (`lib/subproc/runner.ex`, `encode_acks/1`). Sixty
   lines, and the process-kill guarantee is unchanged since that lives in
   the C program. `MuonTrap.cmd/3` has no such bug but buffers to the end,
   so it is not a streaming API.
2. **The child inherits muontrap's stdin, which is the ack pipe.** muontrap
   redirects the child's stdout (and optionally stderr) but never its stdin,
   so the child shares the pipe the BEAM writes acks into. `claude -p`
   reads stdin when it is not a TTY (it appends piped input to the prompt),
   so it would race muontrap for those bytes or block waiting for EOF.
   Reproduced with the fake: a child that does one `read` before emitting
   produced 0 lines in 2 s with stdin inherited and 7 lines with stdin from
   `/dev/null`. The runner wraps every command in
   `sh -c 'exec "$0" "$@" </dev/null' cmd args...`. This is also the answer
   to the task's stdin question: the Go runner never writes to claude's
   stdin (the prompt is positional, follow-up turns use `--resume`, and
   `--input-format stream-json` is the only stdin protocol claude has), and
   under muontrap the child must not even be able to read it. If that ever
   changes, erlexec is the library that owns the child's stdin
   (`:stdin` option, `:kill_group`, its own port program with the same
   dies-with-the-VM guarantee); nothing here argues for switching to it
   now.
3. **Cancel needs a process-group signal in addition to MuonTrap, and the
   BEAM already sets the group up.** muontrap without a cgroup signals
   only its immediate child: SIGTERM, then SIGKILL after `--delay-to-sigkill`
   (500 ms default; the runner passes 5000 to match the Go runner's
   WaitDelay). Grandchildren are never signalled. Erlang, however, starts
   every port program in its own process group (`erl_child_setup` calls
   `setsid`), so muontrap's pid is a pgid that contains claude and every
   descendant that did not leave it, and is distinct from the BEAM's. The
   runner's `:group_term` mode is `kill -TERM -- -<muontrap pid>`, an
   escalation timer, and `kill -KILL` on the group if anything is left,
   which is exactly today's `Setpgid` plus `kill(-pid, SIGTERM)`. OTP has no
   `kill(2)`; it shells out to `/bin/kill`. Two consequences of muontrap
   being in the group it is signalled with: muontrap treats the SIGTERM as
   "shut down" and runs its own SIGTERM-then-SIGKILL on the child, which
   overlaps harmlessly; and it exits 1 rather than relaying the child's
   `128 + signal`, so the exit status a cancelled step reports is
   muontrap's, not claude's. The runner knows it cancelled, so nothing is
   lost, but do not read 143 off it. `Port.close/1` alone (`:port_close`
   mode) also kills claude and gives no exit status at all.
4. **Claude's Bash tool runs its shell in a new process group, so no
   group signal reaches it.** In every real run the `/bin/zsh -c ...`
   claude spawned for the Bash tool had `pgid == its own pid`, with the
   command under it. That means the Go runner's `kill(-pid)` never reached
   it either; it and the MCP servers die because claude tears its children
   down on SIGTERM (observed: all 7 descendants gone in under 1 s, in both
   cancel modes). The uncovered case is claude being SIGKILLed, or dying
   without running its handlers, while a Bash command runs: the shell and
   its command survive on macOS under both the Go runner and this one. On
   Linux, muontrap's `:cgroup_base` closes it (it puts the child in a fresh
   cgroup v2 and uses `cgroup.kill`, which the kernel applies to every
   process in it regardless of pgid), provided the user has a writable
   cgroup directory; on a systemd desktop that is the user's own delegated
   slice, elsewhere it needs a one-time `chown` under `/sys/fs/cgroup`. The
   production runner should try `cgroup_base` on Linux and fall back to the
   group signal, and on macOS accept this gap as the one it already has.
5. **The BEAM crash path works and is the reason to keep muontrap at
   all.** `kill -9` on the BEAM while the fake child ran: muontrap sees EOF
   on its stdin, SIGTERMs the child, and both are gone 2.5 s later, with no
   Elixir code having run. The same test against a bare `Port.open` left
   the child and its `sleep`s running (checked by hand while writing the
   runner). Grandchildren follow the same rule as finding 4.
6. **Smaller notes.** With `--capture-output` and no `--capture-stderr`,
   the child's stderr is inherited from the BEAM, not captured: claude's
   "Input must be provided" usage error landed in the terminal, not the
   log. Merging it with `:stderr_to_stdout` would interleave text with the
   jsonl, so the production runner should either leave it inherited or
   redirect it to a file in the sh wrapper. The nested-claude environment
   (`CLAUDECODE`, `CLAUDE_CODE_*`) had to be unset for the child; the
   runner takes `env: [{"K", nil}]` for that. claude 2.1.269 emits a
   `system/status` event before `system/init`. `--allowedTools` is variadic
   and swallows a positional prompt that follows it; another flag must sit
   between them. Two claude guards bit the test itself and are unrelated
   to the mechanism: a bare `sleep N` Bash command is sometimes refused
   ("standalone sleep"), and `tail` on a path outside cwd is blocked, hence
   `tail -f hold.log` in the cwd.

## Not tested

Linux, and therefore `cgroup_base`, which is the only mechanism here that
covers finding 4; it is documented from the C source
(`deps/muontrap/c_src/muontrap.c`: `try_cgroup_kill`, `cleanup_all_children`)
rather than measured. Output rates above ~150 lines/s or single lines above
40 KB (the ack protocol has no size limit, but throughput under the 10 KB
window is unmeasured; `--stdio-window` raises it). A step that outlives
its runner GenServer for reasons other than a BEAM crash. Windows, which
muontrap does not build on.
