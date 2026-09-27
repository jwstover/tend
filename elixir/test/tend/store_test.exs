defmodule Tend.StoreTest do
  use ExUnit.Case, async: true

  alias Tend.Store
  alias Tend.Store.Migrator
  alias Tend.Test.SQL

  @moduletag :tmp_dir

  # The tables a fully migrated database holds. Spelled out rather than
  # counted: a count tells you something changed, a list tells you what.
  # `sqlite_sequence` is SQLite's own, created by goose_db_version's
  # AUTOINCREMENT, and `goose_db_version` is the bookkeeping table the Go
  # binary reads; the other fifteen come from priv/migrations.
  @tables ~w(
    agent_sessions goose_db_version log_entries projects settings sqlite_sequence
    states tags task_dependencies task_events task_tags tasks
    workflow_edges workflow_runs workflow_step_runs workflow_steps workflows
  )

  @triggers ~w(trg_events_task_created trg_events_task_deleted trg_events_task_state)

  defp open!(dir, name \\ "tend.db") do
    {:ok, store} = Store.open(Path.join(dir, name))
    on_exit(fn -> Store.close(store) end)
    store
  end

  describe "open/1 pragmas (dsnFor in internal/store/store.go)" do
    test "WAL, a busy timeout and foreign keys are all on", %{tmp_dir: dir} do
      store = open!(dir)

      assert SQL.scalar!(store.conn, "PRAGMA journal_mode") == "wal"
      assert SQL.scalar!(store.conn, "PRAGMA busy_timeout") == 5000
      assert SQL.scalar!(store.conn, "PRAGMA foreign_keys") == 1
    end

    test "foreign keys are enforced, not merely reported on", %{tmp_dir: dir} do
      store = open!(dir)

      SQL.exec!(store.conn, "INSERT INTO tasks (id, title) VALUES (1, 'tagged')")
      SQL.exec!(store.conn, "INSERT INTO tags (id, name) VALUES (1, 'home')")
      SQL.exec!(store.conn, "INSERT INTO task_tags (task_id, tag_id) VALUES (1, 1)")

      # A dangling tag_id is refused...
      assert {:error, _} =
               SQL.exec(store.conn, "INSERT INTO task_tags (task_id, tag_id) VALUES (1, 99)")

      # ...and deleting the task cascades the link away.
      SQL.exec!(store.conn, "DELETE FROM tasks WHERE id = 1")
      assert SQL.scalar!(store.conn, "SELECT COUNT(*) FROM task_tags") == 0
    end
  end

  describe "open/1 schema" do
    test "a fresh database carries every table and the task_events triggers", %{tmp_dir: dir} do
      store = open!(dir)

      assert SQL.names!(store.conn, "table") == Enum.sort(@tables)
      assert SQL.names!(store.conn, "trigger") == @triggers
    end

    test "the ladder writes both version records", %{tmp_dir: dir} do
      store = open!(dir)

      assert SQL.scalar!(store.conn, "PRAGMA user_version") == Migrator.latest_version()

      # goose's own shape: a version-0 row from creating the table, then one
      # per migration. This is what the Go binary reads to decide it has
      # nothing to do.
      assert SQL.goose_rows!(store.conn) ==
               Enum.map(0..Migrator.latest_version(), &[&1 + 1, &1, 1])
    end

    test "the parent directory is created if it is missing", %{tmp_dir: dir} do
      path = Path.join([dir, "deeply", "nested", "tend.db"])

      assert {:ok, store} = Store.open(path)
      on_exit(fn -> Store.close(store) end)
      assert File.exists?(path)
    end
  end

  describe "open/1 idempotence" do
    test "a second open runs no migration", %{tmp_dir: dir} do
      path = Path.join(dir, "tend.db")
      first = open!(dir)
      before = SQL.goose_rows!(first.conn)
      schema = SQL.schema!(first.conn)
      :ok = Store.close(first)

      {:ok, second} = Store.open(path)
      on_exit(fn -> Store.close(second) end)

      # Had anything re-run, goose_db_version would have grown rows and the
      # DDL would have been recreated.
      assert SQL.goose_rows!(second.conn) == before
      assert SQL.schema!(second.conn) == schema
      assert Migrator.version(second.conn) == {:ok, Migrator.latest_version()}
    end
  end

  describe "open/1 failures" do
    test "a path whose parent is a file comes back as an error", %{tmp_dir: dir} do
      blocker = Path.join(dir, "not-a-directory")
      File.write!(blocker, "")

      assert {:error, {:db_directory_failed, _dir, _reason}} =
               Store.open(Path.join(blocker, "tend.db"))
    end
  end
end
