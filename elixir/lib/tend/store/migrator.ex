defmodule Tend.Store.Migrator do
  @moduledoc """
  Applies `priv/migrations/*.sql` to an open SQLite connection.

  The Go tree drives the same files through
  [goose](https://github.com/pressly/goose) (`migrate` in
  `internal/store/store.go`). There is no goose for Elixir and Ecto's migrator
  wants its own DSL, so the ladder is hand-rolled here: sixteen files, applied
  in order, each inside a transaction. The files themselves are copied
  **verbatim** from `internal/store/migrations/` -- the DDL is the contract
  between the two binaries, and retyping 777 lines of it would be seventeen
  chances to diverge by a comma.

  ## Two bookkeeping tables, on purpose

  Version is tracked in `PRAGMA user_version`, which is a single integer in the
  database header and costs nothing to read. But the Go binary does not look at
  it: goose keeps its own `goose_db_version` table, and a database without one
  is a database goose will happily re-run every migration against. Both binaries
  have to be able to open the same file during the transition, so this module
  maintains both:

    * `PRAGMA user_version` is what *this* ladder reads and writes.
    * `goose_db_version` is written alongside it, in goose's own shape, so the
      Go binary opens an Elixir-created database and finds nothing to do.

  The asymmetry runs the other way too. A `tend.db` created by the Go binary
  carries `goose_db_version` at version 16 with `PRAGMA user_version` still 0,
  which read naively would mean "fresh database, replay everything" -- against a
  schema that already exists. So the first thing `migrate/1` does is reconcile:
  when the header says 0 but a `goose_db_version` table is present, the header
  is *seeded* from it rather than believed.

  ## Down

  `rollback_to/2` exists for the ported migration tests, which drive 00009,
  00014 and 00015 down and back up -- a `Down` that cannot run is a trap found
  only when it is needed. Nothing in the application calls it.
  """

  alias Exqlite.Sqlite3

  @typedoc """
  One migration file: its number, its name, and its two halves of SQL.
  """
  @type migration :: %{
          version: pos_integer(),
          name: String.t(),
          up: String.t(),
          down: String.t()
        }

  # goose's own DDL for its bookkeeping table, byte for byte (tabs included).
  # Reproducing it exactly is what makes an Elixir-created `sqlite_master`
  # identical to a Go-created one rather than merely equivalent.
  @goose_table "CREATE TABLE goose_db_version (\n" <>
                 "\t\tid INTEGER PRIMARY KEY AUTOINCREMENT,\n" <>
                 "\t\tversion_id INTEGER NOT NULL,\n" <>
                 "\t\tis_applied INTEGER NOT NULL,\n" <>
                 "\t\ttstamp TIMESTAMP DEFAULT (datetime('now'))\n" <>
                 "\t)"

  # `-- +goose Up` / `-- +goose Down`, and nothing else on the line. The
  # `StatementBegin`/`StatementEnd` pairs that wrap the trigger bodies are
  # deliberately *not* matched: they exist because goose splits statements on
  # semicolons and a trigger body is full of them. sqlite3_exec (what
  # `Exqlite.Sqlite3.execute/2` calls) parses the SQL properly, so the whole
  # section goes over in one call and the markers ride along as SQL comments.
  @section ~r/^\s*--\s*\+goose\s+(up|down)\s*$/i

  @doc """
  Brings `conn` up to the newest migration on disk.
  """
  @spec migrate(Sqlite3.db()) :: :ok | {:error, term()}
  def migrate(conn), do: migrate_to(conn, latest_version())

  @doc """
  Brings `conn` up to `target`, leaving anything newer unapplied.

  Exposed for the migration tests, which seed rows in an older schema shape
  before letting `Tend.Store.open/1` finish the job.
  """
  @spec migrate_to(Sqlite3.db(), non_neg_integer()) :: :ok | {:error, term()}
  def migrate_to(conn, target) when is_integer(target) and target >= 0 do
    with {:ok, current} <- reconcile(conn) do
      migrations()
      |> Enum.filter(&(&1.version > current and &1.version <= target))
      |> step_through(conn, &up/2)
    end
  end

  @doc """
  Rolls `conn` back down to `target`, newest migration first.
  """
  @spec rollback_to(Sqlite3.db(), non_neg_integer()) :: :ok | {:error, term()}
  def rollback_to(conn, target) when is_integer(target) and target >= 0 do
    with {:ok, current} <- reconcile(conn) do
      migrations()
      |> Enum.filter(&(&1.version > target and &1.version <= current))
      |> Enum.reverse()
      |> step_through(conn, &down/2)
    end
  end

  @doc """
  The version `conn` is at: the number of the newest applied migration, or 0
  for a database that has never been migrated.

  Not a pure read. This is the reconciling step `migrate/1` runs first, so
  calling it can write `PRAGMA user_version` (seeded from `goose_db_version`)
  and create the `goose_db_version` table if it is missing. Reading the header
  without reconciling would answer 0 for every database the Go binary made.
  """
  @spec version(Sqlite3.db()) :: {:ok, non_neg_integer()} | {:error, term()}
  def version(conn), do: reconcile(conn)

  @doc """
  Every migration on disk, in ascending version order.
  """
  @spec migrations() :: [migration()]
  def migrations do
    directory()
    |> Path.join("*.sql")
    |> Path.wildcard()
    |> Enum.map(&read/1)
    |> Enum.sort_by(& &1.version)
  end

  @doc """
  The newest migration version on disk.
  """
  @spec latest_version() :: non_neg_integer()
  def latest_version do
    case migrations() do
      [] -> 0
      list -> list |> List.last() |> Map.fetch!(:version)
    end
  end

  @doc """
  Where the migration files live.
  """
  @spec directory() :: String.t()
  def directory, do: Application.app_dir(:tend, ["priv", "migrations"])

  # -- the ladder ------------------------------------------------------------

  defp step_through(migrations, conn, apply_one) do
    Enum.reduce_while(migrations, :ok, fn migration, :ok ->
      case apply_one.(conn, migration) do
        :ok -> {:cont, :ok}
        {:error, reason} -> {:halt, {:error, reason}}
      end
    end)
  end

  defp up(conn, migration) do
    transaction(conn, migration, :up, fn ->
      with :ok <- exec(conn, migration.up),
           :ok <- record(conn, migration.version) do
        set_version(conn, migration.version)
      end
    end)
  end

  defp down(conn, migration) do
    transaction(conn, migration, :down, fn ->
      with :ok <- exec(conn, migration.down),
           :ok <-
             exec(conn, "DELETE FROM goose_db_version WHERE version_id = #{migration.version}") do
        set_version(conn, migration.version - 1)
      end
    end)
  end

  # Each migration is one transaction, the way goose runs them: a file that
  # fails halfway leaves the schema where it was rather than somewhere new.
  defp transaction(conn, migration, direction, body) do
    with :ok <- exec(conn, "BEGIN") do
      case body.() do
        :ok ->
          exec(conn, "COMMIT")

        {:error, reason} ->
          exec(conn, "ROLLBACK")
          {:error, {:migration_failed, direction, migration.version, migration.name, reason}}
      end
    end
  end

  # -- version bookkeeping ---------------------------------------------------

  # Agrees `PRAGMA user_version` with `goose_db_version` before anything is
  # applied, and makes sure the goose table exists for the Go binary's benefit.
  defp reconcile(conn) do
    with {:ok, declared} <- read_version(conn),
         {:ok, goose?} <- goose_table?(conn) do
      cond do
        # A database the Go binary made: the header was never written, but
        # goose's table knows exactly how far the schema got.
        declared == 0 and goose? -> seed_from_goose(conn)
        goose? -> {:ok, declared}
        # Fresh, or a database whose goose table went missing.
        true -> with :ok <- create_goose_table(conn, declared), do: {:ok, declared}
      end
    end
  end

  defp seed_from_goose(conn) do
    sql = "SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied = 1"

    with {:ok, applied} <- scalar(conn, sql),
         :ok <- set_version(conn, applied) do
      {:ok, applied}
    end
  end

  # goose seeds a version-0 row when it creates the table, then one row per
  # migration; `through` backfills the rows for a schema that is already
  # ahead of a missing table, which is the only way the two can disagree.
  defp create_goose_table(conn, through) do
    with :ok <- exec(conn, @goose_table) do
      Enum.reduce_while(0..through//1, :ok, fn version, :ok ->
        case record(conn, version) do
          :ok -> {:cont, :ok}
          {:error, reason} -> {:halt, {:error, reason}}
        end
      end)
    end
  end

  defp record(conn, version) do
    exec(conn, "INSERT INTO goose_db_version (version_id, is_applied) VALUES (#{version}, 1)")
  end

  defp read_version(conn), do: scalar(conn, "PRAGMA user_version")

  # `PRAGMA user_version` takes no bind parameters; the value is always an
  # integer this module derived from a filename or read back out of SQLite.
  defp set_version(conn, version), do: exec(conn, "PRAGMA user_version = #{version}")

  defp goose_table?(conn) do
    sql = "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'"

    with {:ok, count} <- scalar(conn, sql), do: {:ok, count > 0}
  end

  # -- reading the files -----------------------------------------------------

  defp read(path) do
    [number, name] = path |> Path.basename(".sql") |> String.split("_", parts: 2)
    {up, down} = split(File.read!(path))

    %{version: String.to_integer(number), name: name, up: up, down: down}
  end

  defp split(sql) do
    {up, down} =
      sql
      |> String.split("\n")
      |> Enum.reduce({nil, [], []}, &collect/2)
      |> then(fn {_section, up, down} -> {up, down} end)

    {join(up), join(down)}
  end

  defp collect(line, {section, up, down}) do
    case section(line) do
      :up -> {:up, up, down}
      :down -> {:down, up, down}
      nil when section == :up -> {:up, [line | up], down}
      nil when section == :down -> {:down, up, [line | down]}
      # Anything before the first `-- +goose Up` is preamble, not SQL.
      nil -> {section, up, down}
    end
  end

  defp section(line) do
    case Regex.run(@section, line) do
      [_, word] -> if String.downcase(word) == "up", do: :up, else: :down
      nil -> nil
    end
  end

  defp join(lines), do: lines |> Enum.reverse() |> Enum.join("\n")

  # -- SQLite plumbing -------------------------------------------------------

  # `Exqlite.Sqlite3.execute/2` is sqlite3_exec, which parses and runs a whole
  # script. That is what lets a migration's Up section go over in one call,
  # trigger bodies and their internal semicolons included.
  defp exec(conn, sql), do: Sqlite3.execute(conn, sql)

  defp scalar(conn, sql) do
    case Sqlite3.prepare(conn, sql) do
      {:ok, statement} ->
        result = Sqlite3.fetch_all(conn, statement)
        Sqlite3.release(conn, statement)

        case result do
          {:ok, [[value] | _]} -> {:ok, value}
          {:ok, []} -> {:ok, nil}
          {:error, reason} -> {:error, {:query_failed, sql, reason}}
        end

      {:error, reason} ->
        {:error, {:query_failed, sql, reason}}
    end
  end
end
