defmodule Tend.CLI.HelpTest do
  use ExUnit.Case, async: true

  alias Tend.CLI.Command
  alias Tend.CLI.Commands
  alias Tend.CLI.Help

  # `tend --help` from the Go binary at ac26902, verbatim and complete. If the
  # Go command surface changes, this is the test that says so.
  @go_help """
  tend is a terminal-native personal task tracker

  Usage:
    tend [flags]
    tend [command]

  Available Commands:
    add         Capture a task instantly (no TUI)
    auth        Manage credentials for external services
    completion  Generate the autocompletion script for the specified shell
    help        Help about any command
    log         Capture a standup note instantly (no TUI)
    ls          Dump the live view as plain text
    projects    List and manage projects
    standup     Print a standup summary of recent task activity as markdown
    version     Print the tend version
    workflow    List, start, watch and steer agent workflow runs

  Flags:
        --db string   path to the SQLite database (default $TEND_DB, then $XDG_DATA_HOME/tend/tend.db)
    -h, --help        help for tend
    -v, --version     version for tend

  Use "tend [command] --help" for more information about a command.
  """

  # The acceptance criterion on sub-task 262 says the root help lists the same
  # commands as the Go binary's, "excluding cobra's two injected built-ins,
  # `completion` and `help`". So the expectation is derived rather than
  # hand-copied: take the Go help and delete exactly those two rows, by name.
  # Anything else that drifts -- a short description, the order, the padding,
  # the `--db` line -- still fails, and the exception cannot widen silently.
  @cobra_builtins ["completion", "help"]

  defp builtin_row?(line) do
    Enum.any?(@cobra_builtins, &String.starts_with?(line, "  " <> &1 <> " "))
  end

  defp go_help_lines, do: String.split(@go_help, "\n")

  defp expected_help do
    go_help_lines() |> Enum.reject(&builtin_row?/1) |> Enum.join("\n")
  end

  test "the root help is the Go binary's, minus cobra's two built-ins" do
    assert Help.root() == expected_help()
  end

  test "the only rows the expectation drops are cobra's two built-ins" do
    assert go_help_lines() -- String.split(expected_help(), "\n") == [
             "  completion  Generate the autocompletion script for the specified shell",
             "  help        Help about any command"
           ]
  end

  test "the --db flag is documented as the Go binary documents it" do
    assert Help.root() =~
             "      --db string   path to the SQLite database " <>
               "(default $TEND_DB, then $XDG_DATA_HOME/tend/tend.db)"
  end

  test "hidden commands stay hidden, as they are in cobra" do
    root = Help.root()

    # Assert the list is there before asserting what is not in it, so an empty
    # help can't pass this test.
    assert root =~ ~r/^Available Commands:$/m
    refute root =~ ~r/^  mcp\s/m
    refute root =~ ~r/^  agent-hook\s/m

    workflow = Help.command([Command.find(Commands.all(), "workflow")])

    assert workflow =~ ~r/^Available Commands:$/m
    refute workflow =~ ~r/^  run\s/m
  end

  test "every visible command in the table gets a row, padded the way cobra pads" do
    # cobra's minNamePadding is 11 and no command name in the tree is longer,
    # so every row is a name padded to 11 columns, a space, then the short.
    for command <- Commands.all(), not command.hidden do
      row = "  " <> String.pad_trailing(command.name, 11) <> " " <> command.short

      assert Help.root() =~ "\n" <> row <> "\n"
    end
  end

  test "help for a nested command names the full path" do
    path = [
      Command.find(Commands.all(), "auth"),
      Command.find(Command.find(Commands.all(), "auth").subcommands, "jira")
    ]

    text = Help.command(path)

    assert text =~ "Usage:\n  tend auth jira [flags]\n  tend auth jira [command]"
    assert text =~ "  -h, --help   help for jira"
    assert text =~ "Global Flags:\n      --db string"
  end
end
