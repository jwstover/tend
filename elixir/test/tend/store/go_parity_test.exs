defmodule Tend.Store.GoParityTest do
  @moduledoc """
  Both binaries have to be able to open the same `tend.db` during the
  transition, so this drives the real Go binary against Elixir-created
  databases and vice versa rather than asserting parity by eye.

  The binary is built from this same worktree in `setup_all`. Where the Go
  toolchain is absent -- the Elixir CI job installs only the BEAM -- the whole
  module skips rather than silently passing.
  """

  use ExUnit.Case, async: false

  alias Exqlite.Sqlite3
  alias Tend.Store
  alias Tend.Store.Migrator
  alias Tend.Store.Row
  alias Tend.Test.Go
  alias Tend.Test.SQL

  @moduletag :tmp_dir
  @moduletag :go_parity

  if not Go.available?() do
    @moduletag skip: "the Go toolchain is not installed; `go build ./cmd/tend` cannot run"
  end

  setup_all do
    {:ok, tend: Go.build!("go-parity")}
  end

  # Local spelling of Tend.Test.Go.run!/3, kept because every test below reads
  # as `go! tend, path, [...]`.
  defp go!(binary, path, args), do: Go.run!(binary, path, args)

  # A read-only handle, so inspecting a database cannot be what changed it.
  defp inspect!(path, fun) do
    {:ok, conn} = Sqlite3.open(path, mode: :readonly)

    try do
      fun.(conn)
    after
      Sqlite3.close(conn)
    end
  end

  test "a fresh Elixir database has the same sqlite_master as a Go one", context do
    %{tmp_dir: dir, tend: tend} = context
    elixir_db = Path.join(dir, "elixir.db")
    go_db = Path.join(dir, "go.db")

    {:ok, store} = Store.open(elixir_db)
    ours = SQL.schema!(store.conn)
    our_goose = SQL.goose_rows!(store.conn)
    :ok = Store.close(store)

    go!(tend, go_db, ["add", "seed the schema"])

    {theirs, their_goose} =
      inspect!(go_db, fn conn -> {SQL.schema!(conn), SQL.goose_rows!(conn)} end)

    # Byte for byte, `sql` column included: the DDL text is the contract, not
    # just the set of object names.
    assert ours == theirs
    # And goose's own bookkeeping matches, which is why the Go binary below
    # finds nothing to migrate.
    assert our_goose == their_goose
  end

  test "the Go binary reads and writes an Elixir-created database", context do
    %{tmp_dir: dir, tend: tend} = context
    path = Path.join(dir, "tend.db")

    {:ok, store} = Store.open(path)
    before = inspect_state(store.conn)
    :ok = Store.close(store)

    assert go!(tend, path, ["add", "written by go"]) =~ "written by go"
    assert go!(tend, path, ["ls"]) =~ "written by go"

    # No migration ran: goose would have appended rows and recreated DDL.
    after_go = inspect!(path, &inspect_state/1)
    assert after_go.schema == before.schema
    assert after_go.goose == before.goose
  end

  test "a Go-created database opens through the ladder unchanged", context do
    %{tmp_dir: dir, tend: tend} = context
    path = Path.join(dir, "tend.db")

    go!(tend, path, ["add", "written by go"])
    before = inspect!(path, &inspect_state/1)
    # The state a Go-created file is really in: goose at 16, header at 0.
    assert before.user_version == 0

    {:ok, store} = Store.open(path)
    after_elixir = inspect_state(store.conn)
    :ok = Store.close(store)

    # The ladder seeded the header rather than replaying anything.
    assert after_elixir.user_version == Migrator.latest_version()
    assert after_elixir.schema == before.schema
    assert after_elixir.goose == before.goose
    assert after_elixir.titles == before.titles

    # ...and the Go binary still reads and writes it afterwards.
    assert go!(tend, path, ["ls"]) =~ "written by go"
    go!(tend, path, ["add", "written by go again"])

    final = inspect!(path, &inspect_state/1)
    assert final.schema == before.schema
    assert final.goose == before.goose
    assert final.titles == before.titles ++ ["written by go again"]
  end

  test "the Go binary carrying an Elixir-created database forward", context do
    %{tmp_dir: dir, tend: tend} = context
    path = Path.join(dir, "tend.db")

    # An older Elixir binary gets the schema to 8 and stamps the header.
    {:ok, conn} = Sqlite3.open(path)
    SQL.exec!(conn, "PRAGMA journal_mode = WAL")
    SQL.exec!(conn, "PRAGMA foreign_keys = ON")
    :ok = Migrator.migrate_to(conn, 8)
    assert SQL.scalar!(conn, "PRAGMA user_version") == 8
    :ok = Sqlite3.close(conn)

    # The Go binary finishes the ladder -- and leaves the header at 8, since
    # goose does not know the header exists.
    go!(tend, path, ["add", "written by go"])

    before =
      inspect!(path, fn conn ->
        assert SQL.scalar!(conn, "PRAGMA user_version") == 8

        assert SQL.scalar!(conn, "SELECT MAX(version_id) FROM goose_db_version") ==
                 Migrator.latest_version()

        inspect_state(conn)
      end)

    # Opening from Elixir must believe goose, not the stale header: replaying
    # 9..16 would fail on `CREATE TABLE workflows`.
    {:ok, store} = Store.open(path)
    assert SQL.scalar!(store.conn, "PRAGMA user_version") == Migrator.latest_version()
    after_elixir = inspect_state(store.conn)
    :ok = Store.close(store)

    assert after_elixir.schema == before.schema
    assert after_elixir.goose == before.goose
    assert after_elixir.titles == before.titles

    # ...and the Go binary still has it afterwards.
    assert go!(tend, path, ["ls"]) =~ "written by go"
  end

  # `tend agent-hook <event>` is the Go side of the session-status contract:
  # one short-lived process per Claude Code hook firing, finding the row by
  # the session id its payload carries and writing what the event implies.
  defp hook!(binary, path, event, session_id) do
    payload = ~s({"session_id":"#{session_id}","hook_event_name":"#{event}"})
    Go.run!(binary, path, ["agent-hook", event], input: payload)
  end

  test "the two binaries interoperate on session status against one file", context do
    %{tmp_dir: dir, tend: tend} = context
    path = Path.join(dir, "tend.db")

    {:ok, store} = Store.open(path)
    on_exit(fn -> Store.close(store) end)

    {:ok, task} = Store.add_task(store, "fix the bug")

    {:ok, _session} =
      Store.create_session(store, task.id, "ext-1", "/tmp/work", task.title, "tend-ext-1")

    :ok = Store.set_session_status(store, "ext-1", :blocked)
    {:ok, [by_elixir]} = Store.list_sessions_for_task(store, task.id)
    elixir_stamp = status_time!(path, "ext-1")

    # The Go binary, in its own OS process, finds the Elixir-written row by
    # external id and moves it on.
    hook!(tend, path, "Stop", "ext-1")

    {:ok, [by_go]} = Store.list_sessions_for_task(store, task.id)
    assert by_go.status == :idle
    go_stamp = status_time!(path, "ext-1")

    # Both wrote the same column through the same strftime, so the stored text
    # is the same shape either way -- and Elixir's parse/format round trip
    # returns each of them byte for byte, which is the whole CAS contract.
    layout = ~r/\A\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}\z/
    assert elixir_stamp =~ layout
    assert go_stamp =~ layout
    assert go_stamp != elixir_stamp

    for stamp <- [elixir_stamp, go_stamp] do
      assert {:ok, parsed} = Row.parse_status_time(stamp)
      assert Row.format_status_time(parsed) == stamp
    end

    # So a CAS token minted from what Go wrote wins, and one minted from the
    # pre-hook value Elixir wrote loses -- the poller-versus-hook race,
    # decided across two implementations of the store.
    assert {:ok, false} =
             Store.set_session_working_if_unchanged(store, "ext-1", by_elixir.status_updated_at)

    assert {:ok, true} =
             Store.set_session_working_if_unchanged(store, "ext-1", by_go.status_updated_at)

    # ...and the Go binary writes over the Elixir CAS's own stamp afterwards,
    # which it can only do having read the row Elixir left.
    hook!(tend, path, "SessionEnd", "ext-1")
    {:ok, [final]} = Store.list_sessions_for_task(store, task.id)
    assert final.status == :ended
    assert status_time!(path, "ext-1") =~ layout
  end

  # The raw column text, read on a handle of its own so the assertion cannot
  # be reading a cached parse.
  defp status_time!(path, external_id) do
    inspect!(path, fn conn ->
      SQL.scalar!(
        conn,
        "SELECT status_updated_at FROM agent_sessions WHERE external_id = '#{external_id}'"
      )
    end)
  end

  defp inspect_state(conn) do
    %{
      schema: SQL.schema!(conn),
      goose: SQL.goose_rows!(conn),
      user_version: SQL.scalar!(conn, "PRAGMA user_version"),
      titles: conn |> SQL.rows!("SELECT title FROM tasks ORDER BY id") |> Enum.map(&hd/1)
    }
  end
end
