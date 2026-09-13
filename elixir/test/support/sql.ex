defmodule Tend.Test.SQL do
  @moduledoc """
  Raw SQLite helpers for the store tests.

  `Tend.Store` is the connection and the schema only -- the query functions
  land in a later sub-task -- so the tests that check what the schema *does*
  have to speak SQL directly. These wrappers exist so they can do that without
  four lines of prepare/fetch/release per assertion.

  Every function raises on failure: a test that cannot run its own setup query
  should blow up where the query is, not three assertions later.
  """

  alias Exqlite.Sqlite3

  @doc """
  Runs `sql` for effect.
  """
  @spec exec!(Sqlite3.db(), String.t()) :: :ok
  def exec!(conn, sql) do
    case Sqlite3.execute(conn, sql) do
      :ok -> :ok
      {:error, reason} -> raise "executing #{inspect(sql)}: #{inspect(reason)}"
    end
  end

  @doc """
  Runs `sql` and returns whether it succeeded, for the cases that assert a
  constraint or a dropped column *refuses* something.
  """
  @spec exec(Sqlite3.db(), String.t()) :: :ok | {:error, term()}
  def exec(conn, sql), do: Sqlite3.execute(conn, sql)

  @doc """
  Runs `sql` and returns its rows as lists.
  """
  @spec rows!(Sqlite3.db(), String.t()) :: [list()]
  def rows!(conn, sql) do
    case Sqlite3.prepare(conn, sql) do
      {:ok, statement} ->
        result = Sqlite3.fetch_all(conn, statement)
        Sqlite3.release(conn, statement)

        case result do
          {:ok, rows} -> rows
          {:error, reason} -> raise "querying #{inspect(sql)}: #{inspect(reason)}"
        end

      {:error, reason} ->
        raise "preparing #{inspect(sql)}: #{inspect(reason)}"
    end
  end

  @doc """
  Runs `sql` and returns the first column of the first row.
  """
  @spec scalar!(Sqlite3.db(), String.t()) :: term()
  def scalar!(conn, sql) do
    case rows!(conn, sql) do
      [[value | _] | _] -> value
      [] -> nil
    end
  end

  @doc """
  Every object in `sqlite_master`, in a stable order.

  This is the comparison behind the Go-parity tests, so it deliberately
  includes `sql` -- the whole point is that the DDL text matches, not just the
  set of names.
  """
  @spec schema!(Sqlite3.db()) :: [list()]
  def schema!(conn) do
    rows!(conn, "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name")
  end

  @doc """
  The names of every `sqlite_master` object of `type`, sorted.
  """
  @spec names!(Sqlite3.db(), String.t()) :: [String.t()]
  def names!(conn, type) do
    conn
    |> rows!("SELECT name FROM sqlite_master WHERE type = '#{type}' ORDER BY name")
    |> Enum.map(&hd/1)
  end

  @doc """
  Every row of `goose_db_version`, the table the Go binary reads to decide
  whether it has any migrating to do.
  """
  @spec goose_rows!(Sqlite3.db()) :: [list()]
  def goose_rows!(conn) do
    rows!(conn, "SELECT id, version_id, is_applied FROM goose_db_version ORDER BY id")
  end
end
