defmodule Tend.CLI.Commands do
  @moduledoc """
  The command surface, mirrored from the Go binary's cobra tree.

  Names, aliases, short descriptions and hidden-ness are copied from
  `internal/cli/*.go` so `tend --help` reads the same from either binary. The
  table is the extension point for the rest of the port: a sub-task that
  implements a command fills in its `stub` and adds the function behind it.

  Only the four entry points this scaffold is built around carry a stub today
  -- the TUI (bare `tend`), `mcp`, `workflow run`, and the one-shots. Every
  other node is table data with no behaviour yet.
  """

  alias Tend.CLI.Command

  @commands [
    %Command{
      name: "add",
      aliases: ["a"],
      args: "<text>...",
      short: "Capture a task instantly (no TUI)",
      stub: :add
    },
    %Command{
      name: "agent-hook",
      args: "<event>",
      hidden: true,
      short: "Record a Claude Code hook event against its session",
      stub: :agent_hook
    },
    %Command{
      name: "auth",
      short: "Manage credentials for external services",
      subcommands: [
        %Command{
          name: "jira",
          short: "Manage Jira credentials (stored in the system keychain)",
          stub: :auth_jira,
          subcommands: [
            %Command{
              name: "login",
              short: "Store Jira site, email, and API token in the system keychain"
            },
            %Command{name: "logout", short: "Remove Jira credentials from the system keychain"},
            %Command{name: "status", short: "Show which Jira site and account are authenticated"}
          ]
        }
      ]
    },
    %Command{
      name: "log",
      args: "<text>...",
      short: "Capture a standup note instantly (no TUI)",
      stub: :log
    },
    %Command{name: "ls", short: "Dump the live view as plain text", stub: :ls},
    %Command{
      name: "mcp",
      hidden: true,
      short: "Run tend's MCP server, bound to one task",
      stub: :mcp
    },
    %Command{
      name: "projects",
      aliases: ["project", "proj"],
      short: "List and manage projects",
      stub: :projects,
      subcommands: [
        %Command{name: "add", args: "<name>", short: "Create a project"},
        %Command{
          name: "archive",
          args: "<name>",
          short: "Hide a project from the projects column"
        },
        %Command{
          name: "cwd",
          args: "<name> [path]",
          short: "Show or set a project's default working directory for new sessions"
        },
        %Command{name: "ls", short: "List projects with their live task counts"},
        %Command{name: "rename", args: "<name> <new-name>", short: "Rename a project"},
        %Command{
          name: "rm",
          aliases: ["delete"],
          args: "<name>",
          short: "Delete a project; its tasks move to Unsorted"
        },
        %Command{name: "unarchive", args: "<name>", short: "Restore an archived project"}
      ]
    },
    %Command{
      name: "standup",
      short: "Print a standup summary of recent task activity as markdown",
      stub: :standup
    },
    %Command{name: "version", short: "Print the tend version", stub: :version},
    %Command{
      name: "workflow",
      aliases: ["wf", "workflows"],
      short: "List, start, watch and steer agent workflow runs",
      subcommands: [
        %Command{
          name: "approve",
          args: "<run-id>",
          short: "Approve the gate a run is waiting at"
        },
        %Command{
          name: "cancel",
          args: "<run-id>",
          short: "Cancel a run; its runner kills the current step and exits"
        },
        %Command{
          name: "decide",
          args: "<run-id> <outcome>",
          short: "Decide the gate a run is waiting at on any outcome its edges route"
        },
        %Command{
          name: "logs",
          args: "<run-id> [--step N] [-f]",
          short:
            "Print a run's step log (rendered, raw, or the runner's own), optionally following it"
        },
        %Command{name: "ls", short: "List workflows with their step counts"},
        %Command{
          name: "pause",
          args: "<run-id>",
          short: "Pause a live run; its runner stops the current step and exits"
        },
        %Command{
          name: "reject",
          args: "<run-id>",
          short:
            "Reject the gate a run is waiting at, with feedback for the step it loops back to"
        },
        %Command{
          name: "resume",
          args: "<run-id>",
          short: "Re-enter a run whose runner died or was paused"
        },
        %Command{
          name: "run",
          args: "<run-id>",
          hidden: true,
          short: "Drive one workflow run (the runner; hosted in tmux by `start` and the TUI)",
          stub: :workflow_run
        },
        %Command{
          name: "start",
          args: "<workflow> --task <id> [--cwd <dir>]",
          short: "Run a workflow on a task: create the run and launch its runner in tmux"
        },
        %Command{name: "status", args: "[<run-id>]", short: "Show live runs, or one run in full"}
      ]
    }
  ]

  @doc """
  The root commands, ordered the way cobra lists them: by name.
  """
  @spec all() :: [Command.t()]
  def all, do: @commands

  @doc """
  The stub every entry point dispatches to, keyed by the path that reaches it.

  Used by the tests to assert one distinct stub per entry point; the bare
  `tend` (the TUI) is the empty path.
  """
  @spec entry_points() :: [{[String.t()], atom()}]
  def entry_points do
    [{[], :tui} | collect_stubs(@commands, [])]
  end

  defp collect_stubs(commands, prefix) do
    Enum.flat_map(commands, fn command ->
      path = prefix ++ [command.name]
      here = if command.stub, do: [{path, command.stub}], else: []
      here ++ collect_stubs(command.subcommands, path)
    end)
  end
end
