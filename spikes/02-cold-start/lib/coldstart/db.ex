defmodule Coldstart.Db do
  @moduledoc """
  Raw `Exqlite.Sqlite3` over the same DSN pragmas the Go store sets
  (WAL, busy_timeout 5000, foreign_keys ON), plus the schema-version check
  that stands in for goose's `Up`: read `goose_db_version`, compare to the
  version this build was written against. A real port would run its
  migration ladder here; the cost that matters for the spike is the open,
  the pragmas, and one read, which is exactly what goose pays when nothing
  is pending.
  """

  alias Exqlite.Sqlite3

  # The Go tree's migration count at the time of the spike.
  @schema_version 15

  def open!(path) do
    File.mkdir_p!(Path.dirname(path))
    {:ok, db} = Sqlite3.open(path)
    :ok = Sqlite3.execute(db, "PRAGMA journal_mode = WAL")
    :ok = Sqlite3.execute(db, "PRAGMA busy_timeout = 5000")
    :ok = Sqlite3.execute(db, "PRAGMA foreign_keys = ON")

    case one(db, "SELECT max(version_id) FROM goose_db_version", []) do
      [@schema_version] ->
        db

      [v] ->
        raise "schema at version #{inspect(v)}, this build expects #{@schema_version}"
    end
  end

  def close(db), do: Sqlite3.close(db)

  def exec!(db, sql, params) do
    {:ok, stmt} = Sqlite3.prepare(db, sql)
    :ok = Sqlite3.bind(stmt, params)
    :done = Sqlite3.step(db, stmt)
    :ok = Sqlite3.release(db, stmt)
  end

  def one!(db, sql, params) do
    case one(db, sql, params) do
      nil -> raise "no row from #{sql}"
      row -> row
    end
  end

  def one(db, sql, params) do
    {:ok, stmt} = Sqlite3.prepare(db, sql)
    :ok = Sqlite3.bind(stmt, params)

    row =
      case Sqlite3.step(db, stmt) do
        {:row, row} -> row
        :done -> nil
      end

    :ok = Sqlite3.release(db, stmt)
    row
  end
end
