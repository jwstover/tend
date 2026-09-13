defmodule Tend.CLI.Help do
  @moduledoc """
  Renders `--help`, laid out the way cobra lays it out in the Go binary.

  The root help is the contract: same commands, same short descriptions, same
  `--db` flag line as `tend --help` from the Go binary, with one stated
  exception -- cobra's two injected built-ins, `completion` and `help`, which
  are cobra's surface rather than tend's. The exception is written into the
  acceptance criterion on sub-task 262 and into `Tend.CLI.HelpTest`, which
  derives its expectation from the Go help by deleting exactly those two rows
  by name, so no other divergence can hide behind it.

  `tend help` still works as a synonym for `tend --help`; `tend completion`
  has no counterpart here and reports itself unknown.
  """

  alias Tend.CLI.Command

  @db_flag "      --db string   path to the SQLite database (default $TEND_DB, then $XDG_DATA_HOME/tend/tend.db)"
  @root_short "tend is a terminal-native personal task tracker"

  # cobra pads a command name to the longest name plus two, with a floor of 11.
  @min_name_padding 11

  @doc """
  The short description of the root command.
  """
  @spec root_short() :: String.t()
  def root_short, do: @root_short

  @doc """
  Help text for the whole command tree.
  """
  @spec root() :: String.t()
  def root do
    commands = Tend.CLI.Commands.all()

    [
      @root_short,
      "",
      "Usage:",
      "  tend [flags]",
      "  tend [command]",
      available_commands(commands),
      "",
      "Flags:",
      @db_flag,
      "  -h, --help        help for tend",
      "  -v, --version     version for tend",
      "",
      ~s(Use "tend [command] --help" for more information about a command.)
    ]
    |> render()
  end

  @doc """
  Help text for one command, given the path of nodes that reached it.
  """
  @spec command([Command.t()]) :: String.t()
  def command([]), do: root()

  def command(path) do
    node = List.last(path)
    names = Enum.map(path, & &1.name)
    line = Enum.join(["tend" | names], " ")

    [
      node.short,
      "",
      "Usage:",
      usage_lines(node, line),
      aliases(node),
      available_commands(node.subcommands),
      "",
      "Flags:",
      "  -h, --help   help for #{node.name}",
      "",
      "Global Flags:",
      @db_flag,
      subcommand_hint(node, line)
    ]
    |> render()
  end

  defp render(parts) do
    parts
    |> List.flatten()
    |> Enum.join("\n")
    |> String.trim_trailing()
    |> Kernel.<>("\n")
  end

  defp usage_lines(node, line) do
    runnable =
      if node.subcommands == [] or node.stub do
        ["  " <> String.trim("#{line} #{node.args}") <> " [flags]"]
      else
        []
      end

    grouped = if node.subcommands == [], do: [], else: ["  #{line} [command]"]

    runnable ++ grouped
  end

  defp aliases(%Command{aliases: []}), do: []

  defp aliases(%Command{} = node) do
    ["", "Aliases:", "  " <> Enum.join([node.name | node.aliases], ", ")]
  end

  defp available_commands(commands) do
    case Enum.reject(commands, & &1.hidden) do
      [] ->
        []

      visible ->
        # cobra pads against every sibling, hidden ones included.
        padding = name_padding(commands)

        ["", "Available Commands:"] ++
          Enum.map(visible, &("  " <> String.pad_trailing(&1.name, padding) <> " " <> &1.short))
    end
  end

  defp name_padding(commands) do
    commands
    |> Enum.map(&String.length(&1.name))
    |> Enum.max()
    |> max(@min_name_padding)
  end

  defp subcommand_hint(%Command{subcommands: []}, _line), do: []

  defp subcommand_hint(%Command{} = node, line) do
    if Enum.all?(node.subcommands, & &1.hidden) do
      []
    else
      ["", ~s(Use "#{line} [command] --help" for more information about a command.)]
    end
  end
end
