defmodule Subproc.Procs do
  @moduledoc """
  Process-table inspection via `ps`, portable across macOS and Linux.
  Everything the spike asserts about orphans is scoped to the tree under
  one pid, never to global `pgrep` patterns: this machine has a dozen other
  `claude` processes running.
  """

  @type proc :: %{
          pid: pos_integer(),
          ppid: pos_integer(),
          pgid: pos_integer(),
          command: String.t()
        }

  @spec table() :: [proc()]
  def table do
    {out, 0} = System.cmd("ps", ["-axo", "pid=,ppid=,pgid=,command="])

    out
    |> String.split("\n", trim: true)
    |> Enum.flat_map(fn line ->
      case String.split(String.trim_leading(line), " ", parts: 4, trim: true) do
        [pid, ppid, pgid, cmd] ->
          [
            %{
              pid: String.to_integer(pid),
              ppid: String.to_integer(ppid),
              pgid: String.to_integer(pgid),
              command: cmd
            }
          ]

        [pid, ppid, pgid] ->
          [
            %{
              pid: String.to_integer(pid),
              ppid: String.to_integer(ppid),
              pgid: String.to_integer(pgid),
              command: ""
            }
          ]

        _ ->
          []
      end
    end)
  end

  @doc "All descendants of `root` (not including root), depth-first, from one ps snapshot."
  @spec descendants(pos_integer(), [proc()]) :: [proc()]
  def descendants(root, table \\ table()) do
    by_parent = Enum.group_by(table, & &1.ppid)
    walk(root, by_parent)
  end

  defp walk(pid, by_parent) do
    Enum.flat_map(Map.get(by_parent, pid, []), fn child ->
      [child | walk(child.pid, by_parent)]
    end)
  end

  @doc "The direct child of pid (muontrap has exactly one)."
  def child_of(pid, table \\ table()) do
    Enum.find(table, &(&1.ppid == pid))
  end

  @doc "Every process in process group pgid, from one snapshot."
  def group(pgid, table \\ table()), do: Enum.filter(table, &(&1.pgid == pgid))

  def alive?(pid),
    do: match?({_, 0}, System.cmd("kill", ["-0", Integer.to_string(pid)], stderr_to_stdout: true))

  def group_alive?(pgid), do: group(pgid) != []

  @doc """
  Poll until `fun.(table)` is truthy or `timeout_ms` elapses. Returns
  `{:ok, value}` or `:timeout`.
  """
  def wait_for(fun, timeout_ms, every \\ 100) do
    deadline = System.monotonic_time(:millisecond) + timeout_ms
    do_wait(fun, deadline, every)
  end

  defp do_wait(fun, deadline, every) do
    case fun.(table()) do
      nil ->
        wait_more(fun, deadline, every)

      false ->
        wait_more(fun, deadline, every)

      value ->
        {:ok, value}
    end
  end

  defp wait_more(fun, deadline, every) do
    if System.monotonic_time(:millisecond) >= deadline do
      :timeout
    else
      Process.sleep(every)
      do_wait(fun, deadline, every)
    end
  end

  def fmt(procs) do
    procs
    |> Enum.map(fn p ->
      "    #{p.pid} ppid=#{p.ppid} pgid=#{p.pgid} #{String.slice(p.command, 0, 90)}"
    end)
    |> Enum.join("\n")
  end

  @doc "Best effort cleanup of anything the spike left behind, so a failing scenario does not leak."
  def reap(pids) do
    for pid <- pids, alive?(pid) do
      System.cmd("kill", ["-KILL", Integer.to_string(pid)], stderr_to_stdout: true)
    end

    :ok
  end
end
