defmodule Tend.Store.WatcherTest do
  @moduledoc """
  The port of `internal/store/watch_test.go`, against a real temp database.

  Go's tests pull (`w.Changed(ctx)`); this one is pushed to, so every Go
  assertion becomes either `assert_receive` or `refute_receive`. The claims
  are the same ones: a commit by anybody else is seen exactly once, the
  watcher's own reads and an idle database are seen not at all.

  Timings are the one thing that is not a port. The watcher polls, so an
  assertion has to wait; `@interval` is turned right down and every wait is
  expressed as a multiple of it rather than as a bare number of milliseconds.
  """

  use ExUnit.Case, async: true

  alias Tend.Store
  alias Tend.Store.Watcher
  alias Tend.Test.Go
  alias Tend.Test.SQL

  @moduletag :tmp_dir

  # Short enough to keep the suite quick, long enough that a loaded CI box
  # still gets a poll in. Production is 500 ms.
  @interval 25

  # One poll interval plus generous slack: a change must arrive in a poll, not
  # eventually. Deliberately not `@interval` exactly -- BEAM timers are allowed
  # to be late, and a flaky watcher test would teach nobody anything.
  @grace @interval * 16

  # How long "nothing happened" is given to fail to happen. Many intervals, so
  # a watcher that broadcast on its own reads would be caught every time.
  @quiet @interval * 10

  defp open!(dir, name \\ "tend.db") do
    {:ok, store} = Store.open(Path.join(dir, name))
    on_exit(fn -> Store.close(store) end)
    store
  end

  defp watch!(path) do
    start_supervised!({Watcher, path: path, interval: @interval})
  end

  # A subscriber in its own process, relaying everything it receives back to
  # the test so `assert_receive` can speak for it. Plain `spawn`: one test
  # kills its subscriber on purpose, which a link would pass on to the test.
  defp start_subscriber(watcher) do
    test = self()

    pid =
      spawn(fn ->
        :ok = Watcher.subscribe(watcher)
        send(test, {:subscribed, self()})
        relay(test)
      end)

    on_exit(fn -> Process.exit(pid, :kill) end)
    assert_receive {:subscribed, ^pid}, @grace

    pid
  end

  defp relay(test) do
    receive do
      message -> send(test, {:relayed, self(), message})
    end

    relay(test)
  end

  # Retries `fun` until it returns true, for the one assertion that is about a
  # monitor firing rather than about a broadcast.
  defp wait_until(fun, attempts \\ 100) do
    cond do
      fun.() ->
        true

      attempts == 0 ->
        false

      true ->
        Process.sleep(10)
        wait_until(fun, attempts - 1)
    end
  end

  describe "a commit from another connection" do
    # The reason the watcher exists: a commit from a completely separate
    # Store -- standing in for another tend process -- is noticed, exactly
    # once, and an idle database reports nothing.
    test "is broadcast once, and only once", %{tmp_dir: dir} do
      store = open!(dir)
      watcher = watch!(store.path)
      :ok = Watcher.subscribe(watcher)

      refute_receive {:tend_store_changed, _}, @quiet

      other = open!(dir)
      SQL.exec!(other.conn, "INSERT INTO tasks (title) VALUES ('written elsewhere')")

      assert_receive {:tend_store_changed, ^watcher}, @grace
      refute_receive {:tend_store_changed, _}, @quiet

      # A second, different kind of write is a second change.
      SQL.exec!(other.conn, "UPDATE tasks SET state = 'doing' WHERE title = 'written elsewhere'")
      assert_receive {:tend_store_changed, ^watcher}, @grace
      refute_receive {:tend_store_changed, _}, @quiet
    end

    # The behaviour a UI leans on: writes through the store that started the
    # watcher are "another connection" too, so the watcher fires for them.
    # That means a UI's own mutations also trigger a (redundant, harmless)
    # live reload -- asserted here so nobody later "fixes" it by trying to
    # filter them out at this layer, which the pragma cannot do.
    test "includes writes through the store that started the watcher", %{tmp_dir: dir} do
      store = open!(dir)
      watcher = watch!(store.path)
      :ok = Watcher.subscribe(watcher)

      refute_receive {:tend_store_changed, _}, @quiet
      SQL.exec!(store.conn, "INSERT INTO tasks (title) VALUES ('written through the same store')")

      assert_receive {:tend_store_changed, ^watcher}, @grace
      refute_receive {:tend_store_changed, _}, @quiet
    end

    # Tags live in their own table and setting them does not bump
    # tasks.updated_at -- one of the reasons data_version, not a timestamp
    # column, is the change signal. Make sure such a write is seen.
    test "includes a write to a table other than tasks", %{tmp_dir: dir} do
      store = open!(dir)
      SQL.exec!(store.conn, "INSERT INTO tasks (id, title) VALUES (1, 'tag me')")

      watcher = watch!(store.path)
      :ok = Watcher.subscribe(watcher)

      SQL.exec!(store.conn, "INSERT INTO tags (id, name) VALUES (1, 'live')")
      SQL.exec!(store.conn, "INSERT INTO task_tags (task_id, tag_id) VALUES (1, 1)")

      assert_receive {:tend_store_changed, ^watcher}, @grace
    end
  end

  describe "a quiet database" do
    test "produces no broadcast, however many times the watcher reads it",
         %{tmp_dir: dir} do
      store = open!(dir)
      watcher = watch!(store.path)
      :ok = Watcher.subscribe(watcher)

      # Long enough for many polls: every one of them is a read on the pinned
      # connection, and a read must never be what moves data_version.
      refute_receive {:tend_store_changed, _}, @quiet
      assert Process.alive?(watcher)
    end

    test "is still quiet when the watcher was started on an already-written database",
         %{tmp_dir: dir} do
      store = open!(dir)
      SQL.exec!(store.conn, "INSERT INTO tasks (title) VALUES ('before the watcher existed')")

      watcher = watch!(store.path)
      :ok = Watcher.subscribe(watcher)

      # Primed, the way Go's Watch primes its Watcher: history is not news.
      refute_receive {:tend_store_changed, _}, @quiet
    end
  end

  describe "subscribers" do
    test "every one of them gets exactly one message per commit", %{tmp_dir: dir} do
      store = open!(dir)
      watcher = watch!(store.path)

      :ok = Watcher.subscribe(watcher)
      first = start_subscriber(watcher)
      second = start_subscriber(watcher)

      SQL.exec!(store.conn, "INSERT INTO tasks (title) VALUES ('one commit')")

      assert_receive {:tend_store_changed, ^watcher}, @grace
      assert_receive {:relayed, ^first, {:tend_store_changed, ^watcher}}, @grace
      assert_receive {:relayed, ^second, {:tend_store_changed, ^watcher}}, @grace

      refute_receive {:tend_store_changed, _}, @quiet
      refute_receive {:relayed, _, _}, @quiet
    end

    test "subscribing twice does not double-deliver", %{tmp_dir: dir} do
      store = open!(dir)
      watcher = watch!(store.path)

      :ok = Watcher.subscribe(watcher)
      :ok = Watcher.subscribe(watcher)
      assert Watcher.subscribers(watcher) == [self()]

      SQL.exec!(store.conn, "INSERT INTO tasks (title) VALUES ('once, please')")

      assert_receive {:tend_store_changed, ^watcher}, @grace
      refute_receive {:tend_store_changed, _}, @quiet
    end

    test "unsubscribing stops delivery", %{tmp_dir: dir} do
      store = open!(dir)
      watcher = watch!(store.path)

      :ok = Watcher.subscribe(watcher)
      :ok = Watcher.unsubscribe(watcher)
      assert Watcher.subscribers(watcher) == []

      SQL.exec!(store.conn, "INSERT INTO tasks (title) VALUES ('not for you')")
      refute_receive {:tend_store_changed, _}, @quiet
    end

    test "one that dies is dropped, and the watcher carries on", %{tmp_dir: dir} do
      store = open!(dir)
      watcher = watch!(store.path)

      :ok = Watcher.subscribe(watcher)
      doomed = start_subscriber(watcher)
      assert length(Watcher.subscribers(watcher)) == 2

      Process.exit(doomed, :kill)
      assert wait_until(fn -> Watcher.subscribers(watcher) == [self()] end)
      assert Process.alive?(watcher)

      # ...and the survivor is still served.
      SQL.exec!(store.conn, "INSERT INTO tasks (title) VALUES ('after the funeral')")
      assert_receive {:tend_store_changed, ^watcher}, @grace
      refute_receive {:relayed, _, _}, @quiet
    end
  end

  describe "lifecycle" do
    test "the pinned connection is its own, not the store's", %{tmp_dir: dir} do
      store = open!(dir)
      {:ok, watcher} = Store.watch(store, interval: @interval)

      # Closing the watcher must leave the store usable, and vice versa: two
      # handles on one file, sharing nothing.
      assert :ok = Watcher.stop(watcher)
      assert SQL.scalar!(store.conn, "SELECT COUNT(*) FROM tasks") == 0
    end

    # Go's Close is explicitly a no-op the second time; this is that contract
    # spelled the BEAM's way.
    test "stopping twice is a no-op", %{tmp_dir: dir} do
      store = open!(dir)
      {:ok, watcher} = Store.watch(store, interval: @interval)

      assert :ok = Watcher.stop(watcher)
      refute Process.alive?(watcher)
      assert :ok = Watcher.stop(watcher)
    end

    test "a name can be given, and subscribing works through it", %{tmp_dir: dir} do
      store = open!(dir)
      name = :"watcher_#{System.unique_integer([:positive])}"

      watcher = start_supervised!({Watcher, path: store.path, interval: @interval, name: name})
      :ok = Watcher.subscribe(name)

      SQL.exec!(store.conn, "INSERT INTO tasks (title) VALUES ('by name')")
      assert_receive {:tend_store_changed, ^watcher}, @grace
    end

    @tag :capture_log
    test "starting on an unopenable path fails rather than half-starts", %{tmp_dir: dir} do
      # A directory is not a database file, and SQLite says so on first use.
      Process.flag(:trap_exit, true)
      assert {:error, _reason} = Watcher.start_link(path: dir, interval: @interval)
    end
  end

  # The acceptance criterion in full: not a second connection in this VM but a
  # second *OS process*, which is the case the watcher exists for. The real Go
  # binary is used rather than a spawned BEAM, because the Go binary is the
  # other process a running `tend` actually shares its database with; the
  # Elixir CI job already installs a Go toolchain for `Tend.Store.GoParityTest`
  # and builds it the same way. Without a toolchain this skips rather than
  # silently passing.
  describe "a write from a second OS process" do
    @describetag :go_parity

    if not Go.available?() do
      @describetag skip: "the Go toolchain is not installed; `go build ./cmd/tend` cannot run"
    end

    # `setup`, not `setup_all`: ExUnit only allows the latter at the module
    # level, where it would build the binary for every test in the file.
    setup do
      {:ok, tend: Go.build!("watcher")}
    end

    test "reaches every subscriber within a poll interval", context do
      %{tmp_dir: dir, tend: tend} = context
      store = open!(dir)
      watcher = watch!(store.path)

      :ok = Watcher.subscribe(watcher)
      other = start_subscriber(watcher)

      # The Go binary opens the same file, migrates nothing (the ladder left
      # it current) and commits one task before exiting.
      assert Go.run!(tend, store.path, ["add", "written by go"]) =~ "written by go"

      assert_receive {:tend_store_changed, ^watcher}, @grace
      assert_receive {:relayed, ^other, {:tend_store_changed, ^watcher}}, @grace

      # Exactly one broadcast: `tend add` is a single commit, and the watcher
      # consumes a change rather than re-reporting it every poll.
      refute_receive {:tend_store_changed, _}, @quiet
      refute_receive {:relayed, _, _}, @quiet

      # ...and the row really is there, so it was a commit that was seen.
      assert SQL.scalar!(store.conn, "SELECT COUNT(*) FROM tasks WHERE title = 'written by go'") ==
               1
    end
  end
end
