defmodule Tend.CLI do
  @moduledoc """
  The command line: parse `argv`, pick an entry point, run it.

  A hand-rolled parser rather than a dependency, because the surface is a fixed
  tree of names plus one persistent flag. The shape it keeps is the Go
  binary's: four kinds of entry point -- the TUI (bare `tend`), the MCP server
  (`tend mcp`), the workflow runner (`tend workflow run`) and the one-shots --
  each reached by a path through `Tend.CLI.Commands`.

  Parsing stops at the command: each entry point owns its own flags, so
  everything after the path is handed to it untouched. Only `--db` is lifted
  out wherever it appears, because it is persistent in the Go tree too.
  """

  alias Tend.CLI.Command
  alias Tend.CLI.Commands
  alias Tend.CLI.Help
  alias Tend.CLI.Invocation
  alias Tend.CLI.Stubs
  alias Tend.DBPath
  alias Tend.Version

  @typedoc """
  What a command line asked for, before anything is printed.
  """
  @type plan ::
          {:run, Invocation.t()}
          | {:help, [Command.t()]}
          | :version
          | {:not_implemented, [Command.t()]}
          | {:error, String.t(), [Command.t()]}

  @doc """
  The escript entry point. Runs `argv` and halts with its exit status.
  """
  @spec main([String.t()]) :: no_return()
  def main(argv) do
    argv |> run() |> System.halt()
  end

  @doc """
  Runs `argv` and returns the exit status, printing whatever it has to print.
  """
  @spec run([String.t()]) :: non_neg_integer()
  def run(argv) do
    case plan(argv) do
      {:run, invocation} ->
        Stubs.dispatch(invocation)

      {:help, path} ->
        IO.write(Help.command(path))
        0

      :version ->
        IO.puts("tend version " <> Version.string())
        0

      {:not_implemented, path} ->
        IO.write(:stderr, "tend #{join(path)}: not implemented in the Elixir port yet\n")
        1

      {:error, message, path} ->
        # cobra silences usage on this path and prints the bare error; the
        # usage block goes to stderr here so an unknown command still tells
        # the reader what the commands actually are.
        IO.write(:stderr, "tend: #{message}\n\n")
        IO.write(:stderr, Help.command(path))
        1
    end
  end

  @doc """
  Resolves `argv` to an entry point without running it or printing anything.
  """
  @spec plan([String.t()]) :: plan()
  def plan(argv) do
    case extract_db(argv) do
      {:ok, db_flag, rest} -> resolve(rest, db_flag)
      {:error, message} -> {:error, message, []}
    end
  end

  # `tend help [command]` is a synonym for `--help`, the one cobra built-in
  # worth keeping: without it `tend help` would read as an unknown command.
  defp resolve(["help" | rest], _db_flag) do
    {path, _argv} = walk(Commands.all(), rest, [])
    {:help, path}
  end

  defp resolve(argv, db_flag) do
    {path, rest} = walk(Commands.all(), argv, [])
    finish(path, rest, db_flag)
  end

  defp walk(_commands, [], path), do: {Enum.reverse(path), []}

  defp walk(commands, [token | rest] = argv, path) do
    case Command.find(commands, token) do
      nil -> {Enum.reverse(path), argv}
      node -> walk(node.subcommands, rest, [node | path])
    end
  end

  defp finish([], rest, db_flag) do
    cond do
      help_flag?(rest) -> {:help, []}
      version_flag?(rest) -> :version
      rest == [] -> {:run, invocation(:tui, [], [], db_flag)}
      flag?(hd(rest)) -> {:error, "unknown flag: #{hd(rest)}", []}
      true -> {:error, ~s(unknown command "#{hd(rest)}" for "tend"), []}
    end
  end

  defp finish(path, rest, db_flag) do
    node = List.last(path)

    cond do
      help_flag?(rest) ->
        {:help, path}

      node.stub ->
        {:run, invocation(node.stub, Enum.map(path, & &1.name), rest, db_flag)}

      node.subcommands != [] and rest != [] and not flag?(hd(rest)) ->
        {:error, ~s(unknown command "#{hd(rest)}" for "tend #{join(path)}"), path}

      node.subcommands != [] ->
        {:help, path}

      true ->
        {:not_implemented, path}
    end
  end

  defp invocation(command, path, argv, db_flag) do
    %Invocation{
      command: command,
      path: path,
      argv: argv,
      db_path: DBPath.resolve(db_flag)
    }
  end

  # --db is persistent: it may appear anywhere ahead of the `--` separator,
  # before or after the command, as either `--db path` or `--db=path`.
  defp extract_db(argv, db \\ nil, acc \\ [])

  defp extract_db([], db, acc), do: {:ok, db, Enum.reverse(acc)}

  defp extract_db(["--" | rest], db, acc), do: {:ok, db, Enum.reverse(acc) ++ ["--" | rest]}

  defp extract_db(["--db"], _db, _acc), do: {:error, "flag needs an argument: --db"}

  defp extract_db(["--db", value | rest], _db, acc), do: extract_db(rest, value, acc)

  defp extract_db(["--db=" <> value | rest], _db, acc), do: extract_db(rest, value, acc)

  defp extract_db([token | rest], db, acc), do: extract_db(rest, db, [token | acc])

  defp help_flag?(argv), do: flag_given?(argv, ["-h", "--help"])

  defp version_flag?(argv), do: flag_given?(argv, ["-v", "--version"])

  defp flag_given?(argv, names) do
    argv
    |> Enum.take_while(&(&1 != "--"))
    |> Enum.any?(&(&1 in names))
  end

  defp flag?("-"), do: false
  defp flag?(token), do: String.starts_with?(token, "-")

  defp join(path), do: Enum.map_join(path, " ", & &1.name)
end
