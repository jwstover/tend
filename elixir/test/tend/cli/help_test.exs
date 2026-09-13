defmodule Tend.CLI.HelpTest do
  use ExUnit.Case, async: true

  alias Tend.CLI.Command
  alias Tend.CLI.Commands
  alias Tend.CLI.Help

  # `tend --help` from the Go binary, verbatim, minus cobra's two built-ins
  # (`completion` and `help`), which are cobra's surface rather than tend's.
  # If the Go command surface changes, this is the test that says so.
  @go_help """
  tend is a terminal-native personal task tracker

  Usage:
    tend [flags]
    tend [command]

  Available Commands:
    add         Capture a task instantly (no TUI)
    auth        Manage credentials for external services
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

  test "the root help matches the Go binary's, line for line" do
    assert Help.root() == @go_help
  end

  test "the --db flag is documented as the Go binary documents it" do
    assert Help.root() =~
             "      --db string   path to the SQLite database " <>
               "(default $TEND_DB, then $XDG_DATA_HOME/tend/tend.db)"
  end

  test "hidden commands stay hidden, as they are in cobra" do
    refute Help.root() =~ "mcp"
    refute Help.root() =~ "agent-hook"
    refute Help.command([Command.find(Commands.all(), "workflow")]) =~ "\n  run "
  end

  test "every visible command in the table appears with its short description" do
    for command <- Commands.all(), not command.hidden do
      assert Help.root() =~ command.name
      assert Help.root() =~ command.short
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
