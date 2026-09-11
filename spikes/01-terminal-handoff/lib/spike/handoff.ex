defmodule Spike.Handoff do
  @moduledoc """
  Hands the real tty to a child process and takes it back: the Elixir
  analogue of Bubble Tea's `tea.ExecProcess`.

  Nothing in Breeze 0.5.1 or Termite 0.4.4 does this, so the spike does it
  from the outside, in a process of its own (Breeze dispatches to a view
  with a 5 s `GenServer.call`, so a view callback cannot block for the
  child's lifetime). The steps, and why each is needed:

  1. **Screen.** Undo what `Breeze.InputRouter` set up: enhanced keyboard
     reporting off, cursor shown, alternate screen left. Written straight
     to the tty through Termite, which is a plain `IO.write`.
  2. **Termios.** `:shell.start_interactive({:noshell, :cooked})` asks
     `user_drv` to `prim_tty:reinit` the tty in cooked mode. The call is
     re-entrant in noshell mode, so it toggles cleanly back to `:raw` later.
  3. **Input.** The BEAM's `prim_tty` reader would otherwise keep pulling
     bytes off fd 0 (Termite's reader is parked in `IO.getn/1` at all
     times, so there is always a pending read). `prim_tty:disable_reader/1`
     parks it. The state record it needs lives inside `user_drv`; we take
     it with `:sys.get_state/1`. This is exactly what OTP's own shell does
     to run `$EDITOR` (`user_drv:open_editor/2`).
  4. **SIGWINCH.** Termite forwards WINCH to the Breeze server, which would
     force a full redraw over the child. The subscription is rewritten in
     Termite's persistent_term state so WINCH comes here instead, and is
     forwarded to the child with `kill -WINCH`. The child needs that: the
     port spawner (`erl_child_setup`) calls `setsid()`, so the child is in
     its own session, not the tty's foreground process group, and the
     kernel will not signal it on resize.
  5. **Spawn.** `Port.open/2` with `:nouse_stdio`: the child inherits fds
     0/1/2 from the VM, which are the tty. It is exec'd through `/bin/sh`
     so the port's own pipe (fds 3/4) is closed first; otherwise a
     daemonised grandchild such as a tmux server keeps the pipe open and
     `:exit_status` never arrives. Wait for `:exit_status`.
  6. **Reacquire.** Reverse 3, 2, 1, then send the Breeze server a
     synthetic WINCH so it drops its frame cache and repaints everything.
     Two fcntl details around step 3's reverse: fd 0 is made non-blocking
     while the reader is re-enabled (a stale readiness notification would
     otherwise block user_drv), and made blocking again before any child
     runs (see `set_stdin_nonblocking/1`).
  """

  defstruct [:server, :reader, :terminal]

  @type t :: %__MODULE__{
          server: pid(),
          reader: reference(),
          terminal: Termite.Terminal.t()
        }

  @signal_state {Termite.Terminal.Shell.DefaultSignalHandler, :state}

  @doc """
  Builds the handoff context from a view's `Breeze.Term`. Reaches into
  fields Breeze documents as opaque: `server` (the Breeze.Server pid) and
  `terminal` (the `%Termite.Terminal{}`). The reader ref is taken from the
  terminal struct: `term.reader` is nil for a root view in Breeze 0.5.1.
  """
  def from_term(term) do
    %__MODULE__{server: term.server, reader: term.terminal.reader, terminal: term.terminal}
  end

  @doc """
  Runs `exe` with `args` on the tty and blocks until it exits.

  Options: `:cd` (working directory), `:env` (list of `{name, value | false}`
  strings; `false` removes the variable).

  Returns a map with `:status` (exit code, or `{:error, reason}`),
  `:winch` (count of WINCH signals forwarded), `:ms` (wall clock), and
  `:os_pid`.
  """
  def exec(%__MODULE__{} = ctx, exe, args, opts \\ []) do
    started = System.monotonic_time(:millisecond)

    trace(
      "exec #{exe} #{inspect(args)} reader=#{inspect(ctx.reader)} server=#{inspect(ctx.server)}"
    )

    tty = release(ctx)

    try do
      run(ctx, exe, args, opts)
    rescue
      e ->
        trace("child failed: #{Exception.message(e)}")
        %{status: {:error, Exception.message(e)}, winch: 0, os_pid: nil}
    catch
      kind, reason ->
        trace("child failed: #{inspect({kind, reason})}")
        %{status: {:error, {kind, reason}}, winch: 0, os_pid: nil}
    after
      reacquire(ctx, tty)
    end
    |> Map.put(:ms, System.monotonic_time(:millisecond) - started)
  end

  @doc """
  Appends a line to `$SPIKE_TRACE` (default `tmp/handoff.log` under the
  project) so the handoff can be followed from outside the tty it owns.
  """
  def trace(line) do
    path = System.get_env("SPIKE_TRACE") || Path.join(File.cwd!(), "tmp/handoff.log")
    File.mkdir_p!(Path.dirname(path))
    stamp = Calendar.strftime(DateTime.utc_now(), "%H:%M:%S.%f")
    File.write!(path, "#{stamp} #{inspect(self())} #{line}\n", [:append])
  rescue
    _ -> :ok
  end

  # -- release / reacquire ---------------------------------------------------

  defp release(ctx) do
    ctx.terminal
    |> Termite.Screen.disable_enhanced_keyboard()
    |> Termite.Screen.disable_mouse()
    |> Termite.Screen.show_cursor()
    |> Termite.Screen.exit_alt_screen()

    # Cooked first: reinit does its own disable/enable of the reader
    # around the termios change, so disabling before it would deadlock
    # user_drv (the reader only listens for `enable` while disabled).
    trace("cooked: #{inspect(:shell.start_interactive({:noshell, :cooked}))}")
    tty = user_drv_tty!()
    trace("disable_reader: #{inspect(:prim_tty.disable_reader(tty))}")
    # Children expect a blocking stdin (a plain `sh` `read` fails with
    # EAGAIN otherwise). An earlier child can leave the shared description
    # non-blocking: a tmux server that exits with a client still attached
    # skips its tty_stop_tty and leaves the flag set. Clear it every time.
    set_stdin_nonblocking(false)
    trace("winch redirected: #{inspect(redirect_winch(ctx.reader, self()))} subscription(s)")
    tty
  end

  defp reacquire(ctx, tty) do
    restore_winch(ctx.reader, ctx.server)
    set_stdin_nonblocking(true)
    # Enable first, for the same reason release disables last.
    trace("enable_reader: #{inspect(:prim_tty.enable_reader(tty))}")
    trace("raw: #{inspect(:shell.start_interactive({:noshell, :raw}))}")
    # reinit has round-tripped the reader, so every stale notification is
    # drained; back to the blocking fd children (and prim_tty) expect.
    set_stdin_nonblocking(false)

    ctx.terminal
    |> Termite.Screen.alt_screen()
    |> Termite.Screen.enable_enhanced_keyboard()
    |> Termite.Screen.hide_cursor()
    |> Termite.Screen.clear_screen()

    # A WINCH is the one message that makes Breeze.Server discard its
    # last-frame cache and repaint the whole screen (force_full_redraw).
    send(ctx.server, {ctx.reader, {:signal, :winch}})
    trace("reacquired; server alive=#{Process.alive?(ctx.server)}")
    :ok
  end

  # -- child -----------------------------------------------------------------

  # The child is exec'd through /bin/sh so it can first close fds 3 and 4,
  # the port's own pipe (that is what nouse_stdio moves them to). The
  # spawn driver reports :exit_status only after it sees EOF on that pipe
  # *and* SIGCHLD, so any grandchild that inherits the pipe and outlives
  # the child, such as the tmux server a `tmux new-session` client forks
  # and daemonises, would keep the handoff waiting until the grandchild
  # dies. Closing the pipe in the child gives the driver its EOF up front;
  # it then waits for SIGCHLD alone. `exec` keeps the child's pid equal
  # to the wrapper's, so WINCH forwarding still targets the right process.
  @wrapper ~S|exec 3>&- 4>&-; exec "$0" "$@"|

  defp run(ctx, exe, args, opts) do
    port_opts =
      [:nouse_stdio, :exit_status, args: ["-c", @wrapper, exe | args]] ++
        port_env(Keyword.get(opts, :env, [])) ++
        port_cd(Keyword.get(opts, :cd))

    port = Port.open({:spawn_executable, "/bin/sh"}, port_opts)
    {:os_pid, os_pid} = Port.info(port, :os_pid)
    trace("spawned os_pid=#{os_pid}")
    {status, winch} = wait(port, os_pid, ctx.reader, 0)
    trace("child exit_status=#{inspect(status)} winch=#{winch}")
    %{status: status, winch: winch, os_pid: os_pid}
  end

  defp wait(port, os_pid, reader, winch) do
    receive do
      {^port, {:exit_status, status}} ->
        {status, winch}

      {^reader, {:signal, :winch}} ->
        trace("WINCH -> kill -WINCH #{os_pid}")
        _ = System.cmd("kill", ["-WINCH", Integer.to_string(os_pid)], stderr_to_stdout: true)
        wait(port, os_pid, reader, winch + 1)

      other ->
        trace("unexpected message while waiting: #{inspect(other)}")
        wait(port, os_pid, reader, winch)
    end
  end

  defp port_env([]), do: []

  defp port_env(env) do
    [
      env:
        Enum.map(env, fn
          {k, false} -> {String.to_charlist(k), false}
          {k, v} -> {String.to_charlist(k), String.to_charlist(v)}
        end)
    ]
  end

  defp port_cd(nil), do: []
  defp port_cd(dir), do: [cd: String.to_charlist(dir)]

  # -- BEAM tty plumbing -----------------------------------------------------

  # prim_tty's reader never sets O_NONBLOCK; it relies on enif_select
  # saying "readable" before each read(). While the reader is parked, the
  # child consumes the keystrokes that triggered those notifications, so
  # the first read after enable_reader can block on an empty tty (in
  # cooked mode: until a whole line arrives). The raw-mode reinit that
  # follows calls the reader synchronously and user_drv hangs with it,
  # taking every IO request in the VM along. A non-blocking fd turns that
  # stale read into EAGAIN, which prim_tty already handles.
  #
  # The flag cannot stay set: it lives on the open file description the
  # children share, and a plain `sh` `read` on a non-blocking empty tty
  # fails with EAGAIN instead of waiting (seen: the probe exiting in 83 ms).
  # So it is set just before enable_reader and cleared right after the raw
  # reinit, which has round-tripped the reader and so drained the stale
  # notifications.
  #
  # There is no fcntl in the standard library, so a perl one-liner does it
  # on *its* fd 0, which nouse_stdio makes the same description as ours.
  @fcntl_perl ~S"""
  use Fcntl;
  my $on = shift;
  my $f = fcntl(STDIN, F_GETFL, 0) + 0;
  $f = $on ? ($f | O_NONBLOCK) : ($f & ~O_NONBLOCK);
  fcntl(STDIN, F_SETFL, $f) or die "fcntl: $!";
  my $g = fcntl(STDIN, F_GETFL, 0) + 0;
  exit((($g & O_NONBLOCK) ? 1 : 0) == $on ? 0 : 1);
  """

  defp set_stdin_nonblocking(on?) do
    case System.find_executable("perl") do
      nil ->
        trace("nonblock: perl not found, skipping")

      perl ->
        port =
          Port.open({:spawn_executable, perl}, [
            :nouse_stdio,
            :exit_status,
            args: ["-e", @fcntl_perl, if(on?, do: "1", else: "0")]
          ])

        receive do
          {^port, {:exit_status, status}} ->
            trace("nonblock=#{on?}: fcntl helper exit=#{status}")
        after
          2_000 -> trace("nonblock=#{on?}: fcntl helper timed out")
        end
    end
  end

  # user_drv is a gen_statem whose data is a #state{} record with the
  # prim_tty #state{} record in its first field. Both records are tagged
  # :state, so find the inner one by shape: it holds the reader as
  # {pid, ref}. disable/enable_reader only look at that field.
  defp user_drv_tty! do
    {_state_name, data} = :sys.get_state(:user_drv)

    data
    |> Tuple.to_list()
    |> Enum.find(fn
      t when is_tuple(t) and tuple_size(t) > 2 ->
        elem(t, 0) == :state and
          Enum.any?(Tuple.to_list(t), &match?({p, r} when is_pid(p) and is_reference(r), &1))

      _ ->
        false
    end)
    |> case do
      nil -> raise "could not find prim_tty state inside user_drv"
      tty -> tty
    end
  end

  # Termite's DefaultSignalHandler keeps {id => %{parent, ref, signals}} in
  # persistent_term and reads it on every signal. Point the subscription
  # for our reader ref at `to` for the duration of the child.
  defp redirect_winch(reader, to) do
    case :persistent_term.get(@signal_state, nil) do
      %{subscriptions: subs} = state ->
        subs =
          Map.new(subs, fn
            {id, %{ref: ^reader} = sub} -> {id, %{sub | parent: to}}
            other -> other
          end)

        :persistent_term.put(@signal_state, %{state | subscriptions: subs})
        Enum.count(subs, fn {_id, sub} -> sub.ref == reader end)

      _ ->
        0
    end
  end

  # The InputRouter, not Breeze.Server, is Termite's real parent; it just
  # forwards WINCH to the server. Sending straight to the server after the
  # handoff is equivalent.
  defp restore_winch(reader, server), do: redirect_winch(reader, server)
end
