defmodule Coldstart.Cmd do
  @moduledoc """
  The two one-shots, doing the same database work the Go commands do.

  `add`: open the store, verify the schema is at the version this build
  knows (the cheap half of what goose's `Up` does on every open), insert
  one task, print the confirmation with the project name.

  `agent-hook`: decode the hook payload from stdin, map the event to a
  status, then and only then open the store and run the same UPDATE
  `SetSessionStatus` does. Never exits non-zero, same as Go.
  """

  alias Coldstart.Db

  @hook_events %{
    "SessionStart" => "starting",
    "Stop" => "idle",
    "Notification" => "blocked",
    "SessionEnd" => "ended"
  }

  def add(args) do
    {opts, words} = split_opts(args)
    title = words |> Enum.join(" ") |> String.trim()

    if title == "" do
      IO.puts(:stderr, "nothing to add: pass a title")
      1
    else
      db = Db.open!(db_path(opts))

      try do
        project_id = 1

        [id, project_id, title] =
          Db.one!(
            db,
            "INSERT INTO tasks (title, project_id) VALUES (?1, ?2) RETURNING id, project_id, title",
            [
              title,
              project_id
            ]
          )

        suffix =
          case Db.one(db, "SELECT name FROM projects WHERE id = ?1", [project_id]) do
            [name] -> " to " <> name
            _ -> ""
          end

        IO.puts("added ##{id}#{suffix}: #{title}")
        0
      after
        Db.close(db)
      end
    end
  end

  def agent_hook(args) do
    {opts, words} = split_opts(args)

    case run_hook(opts, words) do
      :ok -> :ok
      {:error, reason} -> IO.puts(:stderr, "coldstart agent-hook: #{reason}")
    end

    0
  end

  defp run_hook(opts, [event]) do
    with {:ok, status} <-
           Map.fetch(@hook_events, event) |> ok_or("unsubscribed hook event #{inspect(event)}"),
         {:ok, payload} <- read_payload(),
         {:ok, session_id} <-
           Map.fetch(payload, "session_id") |> ok_or("hook event #{event} carried no session_id") do
      db = Db.open!(db_path(opts))

      try do
        Db.exec!(
          db,
          "UPDATE agent_sessions SET status = ?1, status_updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now'), last_active_at = datetime('now') WHERE external_id = ?2",
          [status, session_id]
        )

        :ok
      after
        Db.close(db)
      end
    end
  end

  defp run_hook(_opts, _), do: {:error, "expected exactly one event argument"}

  defp read_payload do
    case IO.read(:stdio, :eof) do
      data when is_binary(data) ->
        try do
          {:ok, :json.decode(data)}
        rescue
          e -> {:error, "decoding hook payload: #{Exception.message(e)}"}
        end

      other ->
        {:error, "reading stdin: #{inspect(other)}"}
    end
  end

  defp ok_or({:ok, v}, _msg), do: {:ok, v}
  defp ok_or(:error, msg), do: {:error, msg}

  @doc "Pull `--db PATH` and `--task-id N` style pairs out of argv."
  def split_opts(args, opts \\ %{}, words \\ [])
  def split_opts([], opts, words), do: {opts, Enum.reverse(words)}

  def split_opts(["--" <> key, val | rest], opts, words),
    do: split_opts(rest, Map.put(opts, key, val), words)

  def split_opts([w | rest], opts, words), do: split_opts(rest, opts, [w | words])

  def db_path(opts) do
    opts["db"] || System.get_env("TEND_DB") ||
      raise "no database: pass --db or set TEND_DB"
  end
end
