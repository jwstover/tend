# Spike 1: terminal handoff (Breeze suspends, child owns the tty, Breeze resumes)

**Verdict: PASS.** A Breeze 0.5.1 / Termite 0.4.4 app on OTP 28 can release
raw mode and the alternate screen, hand the real tty to `claude`, `$EDITOR`,
or a tmux client for the child's whole run, and reacquire the tty with a
correct full repaint afterwards, including across a tmux detach/reattach at
a different size while the child is running. The port does not stop here.

It is not free, though. Neither Breeze nor Termite has an `ExecProcess`
equivalent, and the BEAM adds three problems Go never had (the port spawner's
`setsid()`, the port pipe inherited by daemonised grandchildren, and a blocking
tty read hidden inside `prim_tty`). All three have workarounds that are
contained in one ~250-line module (`lib/spike/handoff.ex`), but that module
reaches into private surfaces of Termite, Breeze, and OTP's `user_drv`. The
production version wants an upstream `Breeze.Server.exec/2` or a vendored
patch, not this shape. Details below.

Throwaway code, per task #207. Pinned exactly to `breeze 0.5.1`.

## Run it

```sh
cd spikes/01-terminal-handoff
mix deps.get
bin/spike            # e $EDITOR, c claude, t claude in tmux, s shell in tmux, p probe, q quit
test/drive.sh        # every scenario below, driven through a scratch tmux server
```

`test/drive.sh` needs `tmux`, `nvim` (or set `$EDITOR`), `perl`, and `claude`
(`SKIP_CLAUDE=1` to skip those steps). It runs the app in a detached tmux
server, sends keys, and asserts on `capture-pane` output. Last run: ALL PASS
(29 assertions). The handoff writes a step-by-step trace to `tmp/handoff.log`.

## What the pass criteria required and what was observed

| Criterion | Evidence (from `test/drive.sh` and manual runs) |
|---|---|
| Breeze releases raw mode and the alt screen | Child output appears on the main screen; `stty -a` from inside the child shows `icanon isig ... echo` (cooked); typed lines reach a plain `sh` `read` |
| Child owns the tty for its full run | nvim, `claude`, `tmux new-session -- claude` all render and take keyboard input; Breeze paints nothing over them, including on resize |
| App reacquires and redraws correctly afterwards | Alt screen is back, clock ticks, full frame repainted (not a diff) at the current size; `q` then exits 0 with the shell restored |
| tmux detach/reattach mid-child | Outer client detached and reattached at 120x40 then 100x35 while `claude` ran inside tmux: the resize reached the child (`claude` relaid out to 100 columns) and the app repainted at 100x34 on return. Inner detach (`prefix d`) returns to the app in about 1 s with the claude session still alive; pressing `t` again reattaches; `/exit` tears it down |

Timings from the trace: release to child spawn is about 5 ms; child exit to
repaint about 15 ms (two perl helpers and the two `user_drv` calls).

## How the handoff works

The view spawns a helper process (`Spike.Handoff.exec/4`) and returns its
term *unchanged* so Breeze does not render (it renders after an event only if
the term changed). The view then ignores the tick timer until the helper
reports back. The helper:

1. Writes the escape sequences Breeze's `InputRouter` set up, in reverse:
   enhanced keyboard off, cursor shown, exit alt screen.
2. `:shell.start_interactive({:noshell, :cooked})`. `user_drv` accepts this
   repeatedly in noshell mode and calls `prim_tty:reinit`, which restores
   cooked termios. (This is the same public call Termite uses to go raw.)
3. `:prim_tty.disable_reader/1` on the state record fished out of
   `:sys.get_state(:user_drv)`. Without this the VM keeps reading fd 0 and
   steals keystrokes. This is exactly what OTP's shell does around
   `$EDITOR` in `user_drv:open_editor/2`.
4. Rewrites Termite's SIGWINCH subscription (a `persistent_term` map) so
   WINCH is delivered to the helper, which forwards it to the child with
   `kill -WINCH`. Breeze would otherwise force a full redraw over the child.
5. `Port.open({:spawn_executable, "/bin/sh"}, [:nouse_stdio, :exit_status, ...])`
   with `exec 3>&- 4>&-; exec "$0" "$@"`. `nouse_stdio` makes the child
   inherit fds 0/1/2 from the VM (the tty) and moves the port's own pipe to
   fds 3/4, which the wrapper closes before exec.
6. On `:exit_status`: restore the WINCH subscription, set fd 0 non-blocking,
   `:prim_tty.enable_reader/1`, `:shell.start_interactive({:noshell, :raw})`,
   set fd 0 blocking again, re-enter alt screen and friends, then send the
   Breeze server a synthetic `{reader_ref, {:signal, :winch}}`, which is the
   one message that makes it drop its frame cache and repaint fully.

## Findings the production design must absorb

1. **A view callback cannot block for the child.** Breeze dispatches to the
   view with a default 5 s `GenServer.call` from a Task; a long
   `handle_event` breaks the input pipeline. The handoff has to run in its
   own process and the view has to keep quiet (no assign changes, no
   invalidation) until it returns. In tend the DB watcher and session poller
   `handle_info`s would need the same gating, or, better, the suspension has
   to live in `Breeze.Server` so the server itself neither renders nor
   handles WINCH while a child runs. That is where Bubble Tea does it.
2. **Erlang port children run in a new session** (`erl_child_setup` calls
   `setsid()`). Consequences: `/dev/tty` cannot be opened by the child
   (inherited fds work fine), the kernel never sends the child SIGWINCH
   (hence the forwarding), Ctrl-C/Ctrl-Z job control from the tty targets
   the VM's process group rather than the child (irrelevant for raw-mode
   children like claude, nvim, and tmux, which read those keys as bytes).
   The tmux client, claude, and nvim all worked with forwarded WINCH.
3. **`:exit_status` waits for pipe EOF.** The spawn driver reports exit only
   after EOF on the port pipe *and* SIGCHLD. A daemonised grandchild (the
   tmux server forked by `tmux new-session`) inherits the pipe and the
   handoff hangs until that grandchild dies. Closing fds 3/4 in the child
   before exec fixes it. (Killing the tmux server released the hang
   instantly, which is how this was confirmed.)
4. **`prim_tty` reads are blocking.** It relies on `enif_select` saying
   "readable" before each `read()`. Keystrokes consumed by the child leave
   stale readiness notifications behind, and the first read after
   `enable_reader` blocks (in cooked mode, until a whole line arrives). The
   raw reinit then calls the reader synchronously and `user_drv` hangs,
   taking all IO in the VM with it. Seen live after nvim: pressing Enter
   unblocked it. Making fd 0 non-blocking for that window turns the stale
   read into EAGAIN, which `prim_tty` handles.
5. **Children expect a blocking stdin, and some leave it non-blocking.** A
   plain `sh` `read` fails with EAGAIN on a non-blocking tty (the probe
   exited in 83 ms). A tmux server that exits with a client still attached
   skips `tty_stop_tty` and leaves the shared description non-blocking. So
   the flag is cleared before every spawn. There is no `fcntl` in
   Elixir/OTP; the spike uses a perl one-liner inheriting fd 0 through
   `nouse_stdio`. Production would want a 20-line NIF or Zigler function,
   or to accept perl as a dependency (macOS and every Linux base image have
   it).
6. **Private surfaces touched:** `Breeze.Term`'s `server` and `terminal`
   fields (`term.reader` is nil for a root view; the ref is
   `term.terminal.reader`), Termite's `DefaultSignalHandler` persistent_term
   state shape, `user_drv`'s state record layout, `prim_tty`'s reader
   protocol, and Breeze.Server's private WINCH message. Each is a
   0.x-minor-version breakage risk. Pin exact versions.

## Recommendation for Phase 4 (sessions, detail edit, takeover)

Implement the handoff *inside* Breeze, not beside it: a
`Breeze.Server.exec(server, cmd, args, opts)` handled in the server process
(or the `InputRouter`, which is Termite's real parent) that does steps 1 to 6
synchronously. Running it in the server means no frame can be written and no
WINCH can be mishandled while the child runs, the view never needs a
"suspended" flag, and `Breeze.Term` internals stay private. Breeze already has
the two halves of this in `print_crash_details_to_scrollback/2` and
`restore_terminal_after_crash_scrollback/1`, so the patch is small. Offer it
upstream; carry it as a vendored fork until it lands. Findings 2 to 5 are OTP
facts and stay regardless of where the code lives.

Keep tend's tmux wrapping exactly as it is today (`agent.WrapTmux`,
`AttachCmd`): it is what makes detach-to-background work, and it is what makes
the outer-tmux resize case matter, since the tmux client only learns about the
new size through the forwarded WINCH.

## Not tested

Linux (the `setsid`, pipe-EOF, and `prim_tty` behaviours are in
platform-independent OTP code and should match, but `test/drive.sh` has only
run on macOS 26 with tmux 3.7b). Mouse mode (Breeze default off; the handoff
disables it defensively). Breeze `:inspector` and code reload (both off).
