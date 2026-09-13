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
  alias Tend.Test.SQL

  @moduletag :tmp_dir
  @moduletag :go_parity

  if is_nil(System.find_executable("go")) do
    @moduletag skip: "the Go toolchain is not installed; `go build ./cmd/tend` cannot run"
  end

  setup_all do
    root = Path.expand("..", File.cwd!())
    binary = Path.join(System.tmp_dir!(), "tend-go-parity-#{System.pid()}")

    {output, status} =
      System.cmd("go", ["build", "-o", binary, "./cmd/tend"], cd: root, stderr_to_stdout: true)

    assert status == 0, "go build failed:\n#{output}"
    on_exit(fn -> File.rm(binary) end)

    {:ok, tend: binary}
  end

  # Runs the Go binary against `path` and returns its combined output.
  defp go!(binary, path, args) do
    {output, status} =
      System.cmd(binary, ["--db", path | args], stderr_to_stdout: true)

    assert status == 0, "tend #{Enum.join(args, " ")} failed:\n#{output}"
    output
  end

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

  defp inspect_state(conn) do
    %{
      schema: SQL.schema!(conn),
      goose: SQL.goose_rows!(conn),
      user_version: SQL.scalar!(conn, "PRAGMA user_version"),
      titles: conn |> SQL.rows!("SELECT title FROM tasks ORDER BY id") |> Enum.map(&hd/1)
    }
  end
end
