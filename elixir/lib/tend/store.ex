defmodule Tend.Store do
  @moduledoc """
  The persistence layer: the only module that touches SQL.

  A port of `internal/store`, starting with the part every other part needs --
  opening the file and getting the schema onto it. Query functions land on top
  of this; `open/1` has no caller yet.

  Built on the raw `Exqlite.Sqlite3` API rather than Ecto. The Go store is
  hand-written SQL against a schema that two binaries have to agree on
  byte for byte, and a port is a port: an Ecto schema per table would be a
  redesign wearing a port's clothes, and `Ecto.Migration` cannot express the
  goose files at all.

  ## The pragmas are load-bearing

  `open/1` sets the same three the Go DSN does (`dsnFor` in
  `internal/store/store.go`):

    * `journal_mode = WAL`, so a reader in one process and a writer in another
      do not block each other -- the TUI, `tend add` from a shell, an MCP tool
      call and a workflow runner are routinely all live at once.
    * `busy_timeout = 5000`, so a briefly contended write waits instead of
      failing.
    * `foreign_keys = ON`, so the schema's cascades actually fire. Several
      migrations are written on the assumption that they do.

  A connection that skipped them would still work, right up until it silently
  did not, so they are asserted in the tests rather than trusted.
  """

  alias Exqlite.Sqlite3
  alias Tend.Store.Migrator
  alias Tend.Store.Watcher

  @typedoc """
  An open store: the connection, and the path it was opened on.

  The path is kept for the same reason `store.Store` keeps its DSN -- the
  change watcher opens a second handle on the same file.
  """
  @type t :: %__MODULE__{conn: Sqlite3.db(), path: String.t()}

  defstruct [:conn, :path]

  @doc """
  Opens the database at `path`, creating it and its parent directory if
  needed, and brings the schema up to date.

  Idempotent: opening a database that is already current runs no migration.

  Errors come back as `{:error, reason}` with a descriptive term. They will be
  folded into `Tend.Error` once that module exists on this branch's siblings.
  """
  @spec open(String.t()) :: {:ok, t()} | {:error, term()}
  def open(path) do
    with :ok <- ensure_directory(path),
         {:ok, conn} <- open_connection(path) do
      case Migrator.migrate(conn) do
        :ok ->
          {:ok, %__MODULE__{conn: conn, path: path}}

        {:error, reason} ->
          Sqlite3.close(conn)
          {:error, reason}
      end
    end
  end

  @doc """
  Opens a connection on `path` with the pragmas above and nothing else: no
  directory creation, no migration ladder.

  This is the Elixir counterpart of Go's `dsnFor` -- the one place the pragma
  set is written down, shared by `open/1` and by the change watcher, whose
  pinned connection has to be configured exactly like a store's but must never
  migrate. Everything else should go through `open/1`.
  """
  @spec open_connection(String.t()) :: {:ok, Sqlite3.db()} | {:error, term()}
  def open_connection(path) do
    with {:ok, conn} <- connect(path) do
      case pragmas(conn) do
        :ok ->
          {:ok, conn}

        {:error, reason} ->
          Sqlite3.close(conn)
          {:error, reason}
      end
    end
  end

  @doc """
  Starts a `Tend.Store.Watcher` on the file this store was opened on.

  The port of Go's `(*Store).Watch`. The watcher gets its own connection, so
  it shares nothing with this one and neither closing ends the other; `opts`
  are `Tend.Store.Watcher.start_link/1`'s, minus `:path`.

  Nothing in the port calls this yet -- there is no UI to reload.
  """
  @spec watch(t(), keyword()) :: GenServer.on_start()
  def watch(%__MODULE__{path: path}, opts \\ []) do
    opts |> Keyword.put(:path, path) |> Watcher.start_link()
  end

  @doc """
  Closes the underlying connection.
  """
  @spec close(t()) :: :ok | {:error, term()}
  def close(%__MODULE__{conn: conn}), do: Sqlite3.close(conn)

  defp ensure_directory(path) do
    case path |> Path.dirname() |> File.mkdir_p() do
      :ok -> :ok
      {:error, reason} -> {:error, {:db_directory_failed, Path.dirname(path), reason}}
    end
  end

  defp connect(path) do
    case Sqlite3.open(path) do
      {:ok, conn} -> {:ok, conn}
      {:error, reason} -> {:error, {:db_open_failed, path, reason}}
    end
  end

  defp pragmas(conn) do
    Enum.reduce_while(
      [
        "PRAGMA journal_mode = WAL",
        "PRAGMA busy_timeout = 5000",
        "PRAGMA foreign_keys = ON"
      ],
      :ok,
      fn sql, :ok ->
        case Sqlite3.execute(conn, sql) do
          :ok -> {:cont, :ok}
          {:error, reason} -> {:halt, {:error, {:pragma_failed, sql, reason}}}
        end
      end
    )
  end
end
