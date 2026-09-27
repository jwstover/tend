defmodule Tend.Store.Watcher do
  @moduledoc """
  A GenServer that answers one question cheaply, over and over: has any other
  connection committed to the database since the last look?

  The port of `internal/store/watch.go`. It is what lets a UI notice writes
  made by other processes -- an MCP-backed agent session, `tend add` from
  another shell, the workflow runner, or the Go binary itself -- without a
  daemon, a socket, or any cooperation from the writer.

  ## `PRAGMA data_version`

  The whole mechanism is SQLite's `PRAGMA data_version`, which the SQLite docs
  describe for exactly this use ("interactive programs that display database
  content on-screen can use `PRAGMA data_version` to determine if they need to
  ... update the screen display"). The value is *per-connection*: it moves when
  a **different** connection commits, and never for the reading connection's
  own writes. So the watcher pins one dedicated connection that is never
  written through, and every commit anywhere else -- including one through the
  `Tend.Store` that started it, which is an "other" connection from the pinned
  one's point of view -- moves it.

  That last part is deliberate and inherited from Go: a UI's own mutations
  also trigger a (redundant, harmless) reload. Nobody should later "fix" it by
  filtering them out at this layer; the pragma cannot tell them apart.

  ## Shape of the port

  Go's `Watcher` is pulled: the caller loops and calls `Changed`. On the BEAM
  the loop belongs to the watcher, so this is a process that polls every
  500 ms and pushes. The two halves of Go's contract survive unchanged:

    * a change is reported once and then consumed -- one commit produces
      exactly one message per subscriber, not one per poll;
    * the watcher's own reads and an idle database produce nothing at all.

  Subscribers are a monitored `send` list rather than a `Registry`: a list
  that lives and dies with the watcher needs no supervision tree and no
  application to start, and no Phoenix.PubSub is involved. A subscriber that
  dies is simply dropped.

  Each change is delivered as:

      {:tend_store_changed, watcher_pid}

  ## No caller yet

  Nothing in Phase 1 subscribes -- there is no UI to reload. This is a
  complete, tested module waiting for one, and it starts nothing on its own:
  it is only ever running because someone called `start_link/1` or
  `Tend.Store.watch/2`.
  """

  use GenServer

  alias Exqlite.Sqlite3
  alias Tend.Store

  # Go's TUI polls on a timer of the same order. Short enough that a write in
  # another process feels immediate, long enough that a pragma read every
  # interval costs nothing. Tests override it.
  @default_interval 500

  @typedoc """
  The message a subscriber receives, once per commit by anyone else.
  """
  @type change :: {:tend_store_changed, pid()}

  @doc """
  Starts a watcher on the database file at `:path`.

  Options:

    * `:path` (required) -- the database file to watch. It is opened with the
      store's pragmas but **not** migrated; the file is expected to exist
      already, which it does whenever the watcher is started from a
      `Tend.Store` that opened it.
    * `:interval` -- milliseconds between polls, default `#{@default_interval}`.
      Tests turn it down; nothing else should need to.
    * `:name` -- a `GenServer` name, passed straight through.

  The returned watcher is primed the way Go's `Watch` primes its `Watcher`:
  the first reported change is one that landed after `start_link/1` returned,
  never the accumulated history of the file.
  """
  @spec start_link(keyword()) :: GenServer.on_start()
  def start_link(opts) do
    {name, opts} = Keyword.pop(opts, :name)
    GenServer.start_link(__MODULE__, opts, if(name, do: [name: name], else: []))
  end

  @doc """
  Subscribes `pid` (by default the caller) to this watcher's change messages.

  Subscribing twice is a no-op rather than a way to get two copies of every
  change: the acceptance is *exactly one* message per subscriber per commit.
  The subscriber is monitored, so it costs the watcher nothing to outlive it.
  """
  @spec subscribe(GenServer.server(), pid()) :: :ok
  def subscribe(watcher, pid \\ self()) do
    GenServer.call(watcher, {:subscribe, pid})
  end

  @doc """
  Stops delivering changes to `pid`. Unknown pids are ignored.
  """
  @spec unsubscribe(GenServer.server(), pid()) :: :ok
  def unsubscribe(watcher, pid \\ self()) do
    GenServer.call(watcher, {:unsubscribe, pid})
  end

  @doc """
  The pids currently subscribed, in no particular order.

  Introspection for tests and for a human at an `iex` prompt; the watcher does
  not need it to work.
  """
  @spec subscribers(GenServer.server()) :: [pid()]
  def subscribers(watcher), do: GenServer.call(watcher, :subscribers)

  @doc """
  Stops the watcher and releases its pinned connection.

  Idempotent, which is the point: Go's `Close` is explicitly a no-op the
  second time, and a caller tearing a watcher down should not have to prove it
  is still alive first.
  """
  @spec stop(GenServer.server()) :: :ok
  def stop(watcher) do
    GenServer.stop(watcher)
  catch
    # Already gone -- the Go `Close` contract, spelled the BEAM's way.
    :exit, _reason -> :ok
  end

  @impl true
  def init(opts) do
    path = Keyword.fetch!(opts, :path)
    interval = Keyword.get(opts, :interval, @default_interval)

    case Store.open_connection(path) do
      {:ok, conn} -> prime(conn, path, interval)
      {:error, reason} -> {:stop, reason}
    end
  end

  # Reads the starting data_version before anyone can subscribe, so the first
  # change reported is one that landed after start_link/1 returned.
  defp prime(conn, path, interval) do
    case read_data_version(conn) do
      {:ok, version} ->
        # Trapping exits so terminate/2 runs on a supervisor shutdown and the
        # pinned connection is released rather than left to the GC.
        Process.flag(:trap_exit, true)
        schedule(interval)

        {:ok, %{conn: conn, path: path, interval: interval, last: version, subscribers: %{}}}

      {:error, reason} ->
        Sqlite3.close(conn)
        {:stop, reason}
    end
  end

  @impl true
  def handle_call({:subscribe, pid}, _from, state) do
    if Map.has_key?(state.subscribers, pid) do
      {:reply, :ok, state}
    else
      ref = Process.monitor(pid)
      {:reply, :ok, put_in(state.subscribers[pid], ref)}
    end
  end

  def handle_call({:unsubscribe, pid}, _from, state) do
    {:reply, :ok, drop_subscriber(state, pid)}
  end

  def handle_call(:subscribers, _from, state) do
    {:reply, Map.keys(state.subscribers), state}
  end

  @impl true
  def handle_info(:poll, state) do
    case read_data_version(state.conn) do
      {:ok, version} when version == state.last ->
        schedule(state.interval)
        {:noreply, state}

      {:ok, version} ->
        broadcast(state)
        schedule(state.interval)
        {:noreply, %{state | last: version}}

      # The pragma read failing means the pinned connection is unusable, and
      # a watcher that cannot read is not a watcher. Go surfaces the error
      # from `Changed`; here it takes the process down where it is visible.
      {:error, reason} ->
        {:stop, reason, state}
    end
  end

  def handle_info({:DOWN, _ref, :process, pid, _reason}, state) do
    {:noreply, drop_subscriber(state, pid)}
  end

  # A trapped exit from whoever started us, and anything else the mailbox
  # collects, is not the watcher's business.
  def handle_info(_message, state), do: {:noreply, state}

  @impl true
  def terminate(_reason, state) do
    Sqlite3.close(state.conn)
    :ok
  end

  defp schedule(interval), do: Process.send_after(self(), :poll, interval)

  defp broadcast(state) do
    message = {:tend_store_changed, self()}

    for {pid, _ref} <- state.subscribers do
      send(pid, message)
    end

    :ok
  end

  defp drop_subscriber(state, pid) do
    case Map.pop(state.subscribers, pid) do
      {nil, _subscribers} ->
        state

      {ref, subscribers} ->
        Process.demonitor(ref, [:flush])
        %{state | subscribers: subscribers}
    end
  end

  # The raw pragma read on the pinned connection. A read, never a write, which
  # is the invariant the whole module rests on: this connection's own activity
  # must never be what moves the number.
  defp read_data_version(conn) do
    case Sqlite3.prepare(conn, "PRAGMA data_version") do
      {:ok, statement} ->
        try do
          case Sqlite3.step(conn, statement) do
            {:row, [version]} -> {:ok, version}
            other -> {:error, {:data_version_failed, other}}
          end
        after
          Sqlite3.release(conn, statement)
        end

      {:error, reason} ->
        {:error, {:data_version_failed, reason}}
    end
  end
end
