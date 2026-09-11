defmodule Spike do
  @moduledoc """
  Entry point: `mix run -e Spike.main` (or `bin/spike`).

  Starts one Breeze session on the real tty and blocks until the view
  returns `{:stop, term}` (the `q` key). Mirrors what `Breeze.Example.run/1`
  does, minus its example-mode guards.
  """

  def main do
    # `logger: :replace` silences the default handler while the session
    # runs so a stray Logger call cannot paint over the alt screen (or,
    # worse, over the child's screen during a handoff).
    opts = [view: Spike.View, logger: :replace, start_opts: [argv: System.argv()]]

    previous = Process.flag(:trap_exit, true)

    result =
      try do
        Breeze.Server.start_link(opts)
      after
        Process.flag(:trap_exit, previous)
      end

    case result do
      {:ok, pid} ->
        ref = Process.monitor(pid)

        receive do
          {:DOWN, ^ref, :process, ^pid, reason} when reason in [:normal, :shutdown] -> :ok
          {:DOWN, ^ref, :process, ^pid, {:shutdown, _}} -> :ok
          {:DOWN, ^ref, :process, ^pid, reason} -> exit(reason)
        end

      {:error, reason} ->
        exit(reason)
    end
  end
end
