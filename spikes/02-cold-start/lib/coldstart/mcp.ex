defmodule Coldstart.Mcp do
  @moduledoc """
  The smallest MCP stdio server that a client can complete a handshake
  with: newline-delimited JSON-RPC, `initialize`, `tools/list`, `ping`,
  and `tools/call` for `get_current_task`, reading the bound task from the
  store the way `tend mcp` does. Halts when stdin closes.

  Enough to time "spawn to initialize response", which is what claude
  waits on at session start. No library: the Go side uses the official
  SDK, but the boot cost being measured is the VM's, not the framework's.
  """

  alias Coldstart.Cmd
  alias Coldstart.Db

  def serve(args) do
    {opts, _} = Cmd.split_opts(args)
    task_id = String.to_integer(opts["task-id"] || raise("--task-id is required"))
    db = Db.open!(Cmd.db_path(opts))
    loop(db, task_id)
  end

  defp loop(db, task_id) do
    case IO.binread(:stdio, :line) do
      :eof ->
        Db.close(db)
        0

      {:error, reason} ->
        IO.puts(:stderr, "mcp: stdin #{inspect(reason)}")
        1

      line ->
        line
        |> String.trim()
        |> case do
          "" -> :ok
          json -> json |> :json.decode() |> handle(db, task_id) |> reply()
        end

        loop(db, task_id)
    end
  end

  defp reply(nil), do: :ok
  defp reply(msg), do: IO.binwrite(:stdio, [:json.encode(msg), "\n"])

  defp handle(%{"method" => "initialize", "id" => id}, _db, _task_id) do
    result(id, %{
      "protocolVersion" => "2025-06-18",
      "capabilities" => %{"tools" => %{}},
      "serverInfo" => %{"name" => "tend", "version" => "spike"}
    })
  end

  defp handle(%{"method" => "ping", "id" => id}, _db, _task_id), do: result(id, %{})

  defp handle(%{"method" => "tools/list", "id" => id}, _db, _task_id) do
    result(id, %{
      "tools" => [
        %{
          "name" => "get_current_task",
          "description" => "Get the task this session is bound to.",
          "inputSchema" => %{"type" => "object", "properties" => %{}}
        }
      ]
    })
  end

  defp handle(
         %{"method" => "tools/call", "id" => id, "params" => %{"name" => "get_current_task"}},
         db,
         task_id
       ) do
    case Db.one(db, "SELECT id, title, body_md, state, project_id FROM tasks WHERE id = ?1", [
           task_id
         ]) do
      [tid, title, body, state, project_id] ->
        out = %{
          "id" => tid,
          "title" => title,
          "body_md" => body,
          "state" => state,
          "project_id" => project_id
        }

        result(id, %{
          "content" => [%{"type" => "text", "text" => :json.encode(out) |> IO.iodata_to_binary()}],
          "structuredContent" => out
        })

      nil ->
        error(id, -32602, "task #{task_id} not found")
    end
  end

  # Notifications carry no id and get no reply.
  defp handle(%{"method" => _} = msg, _db, _task_id) when not is_map_key(msg, "id"), do: nil

  defp handle(%{"method" => method, "id" => id}, _db, _task_id),
    do: error(id, -32601, "method not found: #{method}")

  defp result(id, result), do: %{"jsonrpc" => "2.0", "id" => id, "result" => result}

  defp error(id, code, msg),
    do: %{"jsonrpc" => "2.0", "id" => id, "error" => %{"code" => code, "message" => msg}}
end
