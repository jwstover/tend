defmodule Coldstart do
  @moduledoc """
  Spike 2 entry point. Three one-shots shaped like the Go commands they
  stand in for (`tend add`, `tend agent-hook`, `tend mcp`), dispatched from
  argv in the application start callback and finished with a hard halt so
  the measurement is "process start to process exit" and nothing else.

  Throwaway code, per task #207.
  """

  use Application

  @impl true
  def start(_type, _args) do
    # Runs in the application master's context; run/1 never returns.
    run(argv())
  end

  @doc "Dispatch one command and halt with its exit code."
  def run(args) do
    code =
      case args do
        ["add" | rest] -> Coldstart.Cmd.add(rest)
        ["agent-hook" | rest] -> Coldstart.Cmd.agent_hook(rest)
        ["mcp" | rest] -> Coldstart.Mcp.serve(rest)
        ["noop" | _] -> 0
        ["stats" | _] -> stats()
        _ -> usage()
      end

    # :erlang.halt/1 flushes stdout (OTP 26+ default) and skips the
    # application shutdown walk System.stop/1 would do.
    System.halt(code)
  end

  # Burrito hands argv through its wrapper; a plain `mix release` or
  # `mix run` sees them as init plain arguments. Same list either way.
  defp argv do
    if Code.ensure_loaded?(Burrito.Util.Args) do
      Burrito.Util.Args.argv()
    else
      :init.get_plain_arguments() |> Enum.map(&to_string/1)
    end
  end

  # Where the boot time goes: wall clock since the VM started (excludes
  # the wrapper and ERTS exec before that), how many modules embedded
  # mode loaded, and which emulator flavor ran them.
  defp stats do
    {since_start_ms, _} = :erlang.statistics(:wall_clock)

    IO.puts(
      "vm_start_to_app_start_ms=#{since_start_ms} loaded_modules=#{length(:erlang.loaded())} " <>
        "flavor=#{:erlang.system_info(:emu_flavor)} mode=#{:code.get_mode()} " <>
        "schedulers=#{:erlang.system_info(:schedulers_online)} " <>
        "apps=#{inspect(Enum.map(Application.loaded_applications(), &elem(&1, 0)))}"
    )

    0
  end

  defp usage do
    IO.puts(
      :stderr,
      "usage: coldstart (add <text>... | agent-hook <event> | mcp --task-id N | noop) --db <path>"
    )

    2
  end
end
