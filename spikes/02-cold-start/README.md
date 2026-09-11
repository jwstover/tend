# Spike 2: cold start (Burrito + exqlite one-shots vs the Go binary)

**Verdict: PASS, with one number to keep an eye on.** A Burrito single
binary carrying OTP 28.1, Elixir 1.19.1, and exqlite 0.40 runs every
tend-shaped one-shot in about 130 ms warm on an M4 Pro, against about 20 ms
for the Go binary. That is 6x slower and roughly 110 ms of it is the BEAM
booting, which no flag, dependency trim, or wrapper change moved by more
than a few milliseconds. It is also 40x inside the 5 s hook timeout, adds
about 110 ms to a `claude` session start that already takes seconds, and
sits at the edge of, not past, the "instant" band for `tend add`. Neither
fallback architecture from the task (long-lived node with shims, or a Go
capture path) is needed to proceed, but the numbers below are the ones a
Phase 5 build has to preserve.

Throwaway code, per task #207. Pinned exactly to `exqlite 0.40.0` and
`burrito 1.6.0` (which needs Zig 0.16.0, see `.tool-versions`).

## Run it

```sh
cd spikes/02-cold-start
asdf install                                  # zig 0.16.0
mix deps.get
MIX_ENV=prod mix release                      # -> burrito_out/coldstart_macos_arm64 (9.3 MB)
go build -o tmp/tend-go ../../cmd/tend        # the baseline, from the same tree
bench/bench.sh 30                             # sanity checks, then the table below
```

`bench/bench.sh` seeds a database with the Go binary (so it carries the real
schema and goose version table), checks that each Elixir one-shot actually
does its job, then times each side with `bench/time.pl` (fork/exec/waitpid
from perl, N runs, min/median/p95/max). Needs `sqlite3` and `perl`, both in
the macOS base system. Between rebuilds of the same version, delete
`~/Library/Application Support/.burrito/coldstart_erts-16.1_0.1.0` or
Burrito keeps running the previously extracted payload.

## What the pass criteria required and what was observed

Machine: Apple M4 Pro, macOS 26.6, warm page cache, N=30, milliseconds.
Go is `go build` of the tree at this commit (`v0.3.1`, `CGO_ENABLED=0`,
modernc sqlite). Burrito is the binary above, payload already extracted.

| One-shot | Go median (p95) | Burrito median (p95) | Criterion | Result |
|---|---|---|---|---|
| floor: `tend version` / `coldstart noop` | 20.6 (23.5) | 125.3 (132.1) | | |
| `tend add "bench task"` (open, version check, insert, print) | 20.2 (25.9) | 133.5 (138.7) | perceptually instant | edge: see below |
| `tend agent-hook Stop` (decode stdin, open, UPDATE) | 21.6 (24.9) | 135.1 (142.4) | well under the 5 s hook timeout | pass, 37x headroom |
| `tend mcp`, spawn to `initialize` response | 24.1 (25.7) | 131.6 (139.5) | session start not noticeably slower | pass, +108 ms on a multi-second start |
| first run after install (payload extraction, once) | n/a | 1300 to 2000 | | one-time, see below |

The database work is nearly free on both sides: `add` is the floor plus
8 ms on the BEAM (exqlite NIF load, open, three statements) and plus 0 ms in
Go. Everything that separates the columns is process start.

On "perceptually instant": 130 ms is under the ~150 ms threshold where a
keypress-to-result delay starts to register as lag, and about what
`git status` costs in a mid-sized repo, so `tend add foo` from a shell will
not feel broken. It is over the ~100 ms mark where Go's 20 ms feels
identical to zero, and side by side the difference is visible. It is the
largest UX regression the port carries and it is fixed at the VM floor, so
it cannot be tuned away later; the decision to accept it is being made here.

## Where the 125 ms goes

From `-init_debug` timelines (`bench/erlexec_direct.sh` launches the
extracted payload the way the Zig wrapper does, with `-mode` under our
control) and the `stats` command (`:erlang.statistics(:wall_clock)` at
application start):

| Phase | ms | Notes |
|---|---|---|
| wrapper, `execve` erlexec, `execve` beam.smp, dyld, VM init, preloaded modules | ~40 | before any `-init_debug` output. beam.smp links Cocoa and Carbon on macOS |
| `primLoad` of kernel's ~30 modules | ~16 | JIT-compiled at load |
| `logger` and `application_controller` start | ~16 | |
| `kernel` application start | ~30 | file_server, code_server, inet_db, user_drv/prim_tty, etc. |
| `stdlib`, `sasl`, `compiler`, `elixir` start | ~8 | Elixir itself is cheap |
| `logger` (Elixir), `telemetry`, `db_connection`, `exqlite` start | ~12 | logger/db_connection ride in as exqlite deps |
| `coldstart` start, dispatch, halt | ~3 to 10 | the actual work |

142 modules end up loaded. The bare `erl -noshell -eval 'halt().'` on the
same OTP install is 128 ms median, so the release and the wrapper add
essentially nothing on top of the VM: this is what a BEAM process costs to
start on this machine.

## Findings the production build must absorb

1. **Burrito is passing `"-mode embedded"` as one argv element**, so erl
   never sees a mode flag and the release runs in *interactive* mode. That
   is an accident that helps: measured directly with the same payload,
   embedded mode loads all 664 modules of the boot script up front and
   costs 171 to 196 ms VM-start-to-app-start against 96 to 109 interactive
   (about +60 ms per one-shot, with a wider tail). erl takes the *first*
   `-mode` it sees, so a `-mode interactive` in `vm.args` cannot override a
   corrected wrapper flag. Phase 5 needs to pin this deliberately: either
   patch the wrapper, or make sure a Burrito fix does not silently add 60
   ms to every hook.
2. **`-noshell`, not `-noinput`.** `-noinput` crashes `prim_tty`
   (`function_clause` in `prim_tty:read/2`) on the first `io:get_line`, so
   a hook that reads its payload from stdin cannot use it. Burrito passes
   `-noshell`, which is right; stdin reads via `IO.read(:stdio, :eof)` and
   `IO.binread(:stdio, :line)` both work under it.
3. **`{:burrito, ..., runtime: false}` is mandatory.** As a runtime dep it
   pulls req, finch, mint, ssl, crypto, public_key, asn1, jason into the
   release and *starts* them at boot: +25 ms per one-shot (150 vs 125) and
   191 loaded modules instead of 142. Argument access then has to be
   `:init.get_plain_arguments/0` rather than `Burrito.Util.Args`, which is
   fine, the wrapper puts argv after `-extra`.
4. **VM flags do nothing here.** `+S 1:1 +SDcpu 1:1 +SDio 1 +A 0 +sbwt
   none +sbwtdcpu none +sbwtdio none` moved the median by under 2 ms
   (within noise), though it did cut VM-start-to-app-start from 100 to 90
   ms. Leave the defaults unless something else needs them.
5. **The wrapper is free.** Burrito's Zig launcher (read metadata, check
   the install dir, `execve`) costs at most a few ms: 126 ms through the
   wrapper vs 134 through a `sh` script calling erlexec directly, and the
   script itself is 12 ms of that.
6. **First run extracts the payload.** 9.3 MB binary, 38 MB extracted to
   `~/Library/Application Support/.burrito/<app>_erts-<v>_<appv>/`,
   1.3 to 2.0 s the first time (xz decompress plus writing ~1,500 files).
   This happens once per install or upgrade, and it will happen inside
   whatever tend command the user runs first. Since the payload directory
   is keyed by app version, a release that ships the same version string
   twice runs the stale payload; every release must bump the version, and
   a `tend` upgrade should probably trigger the extraction eagerly rather
   than leaving it to the first `tend add` or, worse, the first hook.
7. **Stdout flush on halt.** `System.halt/1` from inside `Application.start/2`
   works and flushes stdout (OTP 26+ default); the confirmation line
   arrives every time. `System.stop/1` would add the application shutdown
   walk for nothing.
8. **Payload contents.** Even with the trimmed application list, Burrito's
   payload carries crypto (14 MB, from the NIF recompile step), asn1, iex,
   and runtime_tools. None of them load at boot in interactive mode, so
   this is size, not time. Worth trimming in Phase 5 for the download, not
   for cold start.

## Options if 130 ms ever becomes unacceptable

Recorded because the task asked for the decision to be explicit, not
because the spike recommends taking either now.

- **Long-lived node with thin shims.** A ~200-line Zig or C `tend` shim
  that connects to a Unix socket on a resident BEAM (started by the TUI, or
  on demand) would put `add`, the hooks, and the MCP stdio bridge at ~5 ms.
  Costs: a daemon lifecycle (who starts it, when does it exit, what happens
  when the socket is stale), two binaries to ship, and the MCP bridge has to
  proxy a full-duplex stdio stream. It is the right shape if the numbers
  above double on Linux or on older hardware, and it can be added later
  without changing the Elixir side, since every one-shot is already a
  function call on a store.
- **Keep a Go capture path.** Only `tend add` and `agent-hook` would stay
  in Go against the shared SQLite file. Cheap, but it keeps a second
  toolchain, a second migration reader, and two release pipelines alive
  for two commands. Not recommended.

## Not tested

Linux (the BEAM's Linux boot is usually a little faster than macOS, since
beam.smp is not linking Cocoa, but it is unmeasured). Cold page cache after
a reboot, where the 38 MB payload has to come off disk. Older or
Intel hardware. Windows, which the Go build does not support either.
Whether `-mode interactive` has any downside for the long-running TUI and
runner processes (it should not: it is the mode `mix run` and `iex -S mix`
use, and code loading on first call is a one-time cost per module).
