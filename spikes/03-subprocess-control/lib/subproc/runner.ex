defmodule Subproc.Runner do
  @moduledoc """
  One headless `claude -p` (or a stand-in) under the `muontrap` port program.

  This is the shape a production step runner would take. It opens the
  muontrap executable that ships with the `:muontrap` hex package as an
  Erlang port *directly* rather than through `MuonTrap.Daemon`, for two
  reasons found while writing the spike (both recorded in README.md):

    * `MuonTrap.Daemon` is a line logger: it splits captured output on
      newlines and hands each *line* to `:logger_fun`, but it caps the
      partial-line buffer at 256 bytes (`@max_data_to_buffer`). A
      stream-json line is routinely several KB, and muontrap's stdio window
      is 10 KB, so a long line is guaranteed to straddle port messages and
      come out truncated. The port protocol itself is fine; only the Daemon's
      line splitting is unusable here.
    * Cancelling needs the child's OS pid (to signal the whole process
      group) and the exit status afterwards. The Daemon exposes muontrap's
      pid but stops as soon as the port closes.

  What muontrap gives that a bare `Port.open` does not: when the port
  closes for *any* reason (our `cancel/2`, this GenServer crashing, the
  whole BEAM dying), muontrap sees EOF on its stdin and SIGTERMs the child,
  then SIGKILLs it `:delay_to_sigkill` ms later. A bare port leaves the
  child running (see the note in README.md).

  Output flow control: muontrap only forwards up to `--stdio-window`
  (default 10 KB) bytes before it needs an acknowledgement from the Erlang
  side, one byte per 256 bytes handled, written to its stdin. We ack every
  chunk as soon as it is on disk.

  Stdin: muontrap does not redirect the child's stdin, so the child
  inherits muontrap's own stdin, which is the pipe carrying those acks from
  the BEAM. A child that reads its stdin (claude does: `claude -p` appends
  piped stdin to the prompt) would race muontrap for the ack bytes. The
  `:stdin` option (default `:devnull`) wraps the command in
  `sh -c 'exec "$0" "$@" </dev/null'` so the child never sees that pipe.
  Pass `stdin: :inherit` to reproduce the hazard.
  """
  use GenServer

  require Logger

  @type mode :: :port_close | :group_term

  defstruct [
    :owner,
    :ref,
    :port,
    :file,
    :log_path,
    :delay_to_sigkill,
    :started_at,
    :cancel_mode,
    :cancel_timer,
    buffer: "",
    lines: 0,
    bytes: 0,
    exit_status: nil
  ]

  @doc """
  Options:

    * `:cmd` (required) program, resolved on $PATH
    * `:args` argument list
    * `:cd`, `:env` as for `Port.open/2` (`:env` is `[{"K", "v" | nil}]`)
    * `:log_path` (required) the jsonl file lines are appended to
    * `:delay_to_sigkill` ms muontrap waits after SIGTERM (default 5000, the
      Go runner's WaitDelay)
    * `:stdin` `:devnull` (default) or `:inherit`
    * `:owner` pid that receives `{:runner, ref, event}` messages:
      `{:line, n, byte_size, t_ms}`, `{:exit, status}`, `{:closed, reason}`
  """
  def start_link(opts) do
    GenServer.start_link(__MODULE__, Keyword.put_new(opts, :owner, self()))
  end

  @doc "muontrap's OS pid. Its process group is the whole step's process group."
  def os_pid(pid), do: GenServer.call(pid, :os_pid)

  @doc "Reference the owner receives events under."
  def ref(pid), do: GenServer.call(pid, :ref)

  @doc """
  Cancel the step.

    * `:port_close` -- MuonTrap's own mechanism: close the port, muontrap
      SIGTERMs its immediate child and SIGKILLs it after `:delay_to_sigkill`.
      Nothing signals grandchildren; they live or die by the child's own
      signal handling. No exit status comes back (the port is gone).
    * `:group_term` -- what the Go runner does today: SIGTERM the process
      group (muontrap, claude, and every descendant that did not leave the
      group), wait `:delay_to_sigkill`, SIGKILL the group if anything is
      left. muontrap reports the child's exit status through the port as
      usual, so the stream is drained and `{:exit, status}` still arrives.
      muontrap itself takes the SIGTERM as "shut down" and runs its own
      SIGTERM-then-SIGKILL on the child too, so the two escalations overlap
      rather than conflict.
  """
  def cancel(pid, mode \\ :group_term) when mode in [:port_close, :group_term] do
    GenServer.call(pid, {:cancel, mode})
  end

  @impl true
  def init(opts) do
    cmd = Keyword.fetch!(opts, :cmd)
    args = Keyword.get(opts, :args, [])
    log_path = Keyword.fetch!(opts, :log_path)
    delay = Keyword.get(opts, :delay_to_sigkill, 5_000)

    exe = System.find_executable(cmd) || raise ArgumentError, "#{cmd} not on $PATH"

    {exe, args} =
      case Keyword.get(opts, :stdin, :devnull) do
        :devnull -> {"/bin/sh", ["-c", ~s(exec "$0" "$@" </dev/null), exe | args]}
        :inherit -> {exe, args}
      end

    muontrap_args = [
      "--capture-output",
      "--delay-to-sigkill",
      Integer.to_string(delay),
      "--",
      exe | args
    ]

    port_opts =
      [:use_stdio, :exit_status, :binary, :hide, :stream, {:args, muontrap_args}] ++
        case Keyword.get(opts, :cd) do
          nil -> []
          cd -> [{:cd, to_charlist(cd)}]
        end ++
        case Keyword.get(opts, :env) do
          nil ->
            []

          env ->
            [
              {:env,
               Enum.map(env, fn {k, v} ->
                 {to_charlist(k), if(v, do: to_charlist(v), else: false)}
               end)}
            ]
        end

    Process.flag(:trap_exit, true)
    {:ok, file} = :file.open(log_path, [:write, :binary, :raw])
    port = Port.open({:spawn_executable, to_charlist(MuonTrap.muontrap_path())}, port_opts)

    {:ok,
     %__MODULE__{
       owner: Keyword.fetch!(opts, :owner),
       ref: make_ref(),
       port: port,
       file: file,
       log_path: log_path,
       delay_to_sigkill: delay,
       started_at: System.monotonic_time(:millisecond)
     }}
  end

  @impl true
  def handle_call(:os_pid, _from, state) do
    {:reply, port_os_pid(state.port), state}
  end

  def handle_call(:ref, _from, state), do: {:reply, state.ref, state}

  def handle_call({:cancel, :port_close}, _from, state) do
    # EOF on muontrap's stdin: it SIGTERMs the child, waits, SIGKILLs.
    safe_close(state.port)
    {:reply, :ok, %{state | cancel_mode: :port_close}}
  end

  def handle_call({:cancel, :group_term}, _from, state) do
    pgid = port_os_pid(state.port)
    signal_group(pgid, "TERM")
    timer = Process.send_after(self(), {:escalate, pgid}, state.delay_to_sigkill)
    {:reply, :ok, %{state | cancel_mode: :group_term, cancel_timer: timer}}
  end

  @impl true
  def handle_info({port, {:data, data}}, %{port: port} = state) do
    {lines, rest} = split_lines(state.buffer <> data)
    now = System.monotonic_time(:millisecond) - state.started_at

    state =
      Enum.reduce(lines, state, fn line, st ->
        :ok = :file.write(st.file, [line, "\n"])
        n = st.lines + 1
        send(st.owner, {:runner, st.ref, {:line, n, byte_size(line), now}})
        %{st | lines: n}
      end)

    # Acknowledge to muontrap that these bytes are handled so it opens the
    # window again. One ack byte per up-to-256 bytes, value = count - 1.
    Port.command(port, encode_acks(byte_size(data)))

    {:noreply, %{state | buffer: rest, bytes: state.bytes + byte_size(data)}}
  end

  def handle_info({port, {:exit_status, status}}, %{port: port} = state) do
    state = flush_partial(state)
    send(state.owner, {:runner, state.ref, {:exit, status}})
    {:stop, :normal, %{state | exit_status: status}}
  end

  def handle_info({:EXIT, port, reason}, %{port: port} = state) do
    # Port closed (our cancel, or an abnormal port death). With :port_close
    # there is no :exit_status coming.
    state = flush_partial(state)
    send(state.owner, {:runner, state.ref, {:closed, reason}})
    {:stop, :normal, state}
  end

  def handle_info({:escalate, pgid}, state) do
    if Subproc.Procs.alive?(pgid) or Subproc.Procs.group_alive?(pgid) do
      signal_group(pgid, "KILL")
    end

    {:noreply, %{state | cancel_timer: nil}}
  end

  def handle_info(_other, state), do: {:noreply, state}

  @impl true
  def terminate(_reason, state) do
    :file.close(state.file)
    safe_close(state.port)
  end

  defp flush_partial(%{buffer: ""} = state), do: state

  defp flush_partial(state) do
    :ok = :file.write(state.file, [state.buffer, "\n"])
    %{state | buffer: "", lines: state.lines + 1}
  end

  defp split_lines(bin) do
    parts = :binary.split(bin, "\n", [:global])
    {lines, [rest]} = Enum.split(parts, length(parts) - 1)
    {lines, rest}
  end

  defp encode_acks(count) do
    full = div(count, 256)
    part = rem(count, 256)

    case {full, part} do
      {0, p} -> <<p - 1>>
      {f, 0} -> :binary.copy(<<255>>, f)
      {f, p} -> [:binary.copy(<<255>>, f), <<p - 1>>]
    end
  end

  defp port_os_pid(port) do
    case Port.info(port, :os_pid) do
      {:os_pid, p} -> p
      nil -> nil
    end
  end

  defp safe_close(port) do
    Port.close(port)
  rescue
    ArgumentError -> :ok
  end

  # Erlang spawns port programs into their own process group (erl_child_setup
  # calls setsid), so -pgid is muontrap's pid and never reaches the BEAM.
  # There is no kill(2) in OTP; shell out.
  defp signal_group(nil, _sig), do: :ok

  defp signal_group(pgid, sig) do
    {_, _} = System.cmd("kill", ["-#{sig}", "--", "-#{pgid}"], stderr_to_stdout: true)
    :ok
  end
end
