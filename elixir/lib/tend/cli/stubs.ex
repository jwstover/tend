defmodule Tend.CLI.Stubs do
  @moduledoc """
  One stub per entry point, and nothing else.

  This is the seam the rest of Phase 1 grows into: a sub-task that implements
  an entry point replaces its stub here with a call into the module that does
  the work. Keeping one function per command -- rather than one shared
  "not implemented" branch -- means those sub-tasks never have to touch the
  dispatcher, only the function named after the command they are building.

  Every stub returns the process exit status.
  """

  alias Tend.CLI.Invocation
  alias Tend.Version

  @doc """
  Runs the entry point an invocation names.
  """
  @spec dispatch(Invocation.t()) :: non_neg_integer()
  def dispatch(%Invocation{command: :tui} = invocation), do: tui(invocation)
  def dispatch(%Invocation{command: :mcp} = invocation), do: mcp(invocation)
  def dispatch(%Invocation{command: :workflow_run} = invocation), do: workflow_run(invocation)
  def dispatch(%Invocation{command: :add} = invocation), do: add(invocation)
  def dispatch(%Invocation{command: :ls} = invocation), do: ls(invocation)
  def dispatch(%Invocation{command: :log} = invocation), do: log(invocation)
  def dispatch(%Invocation{command: :standup} = invocation), do: standup(invocation)
  def dispatch(%Invocation{command: :projects} = invocation), do: projects(invocation)
  def dispatch(%Invocation{command: :version} = invocation), do: version(invocation)
  def dispatch(%Invocation{command: :auth_jira} = invocation), do: auth_jira(invocation)
  def dispatch(%Invocation{command: :agent_hook} = invocation), do: agent_hook(invocation)

  @doc "Bare `tend`: the TUI."
  @spec tui(Invocation.t()) :: non_neg_integer()
  def tui(invocation), do: pending(invocation, "tui", "open the TUI")

  @doc "`tend mcp`: the per-session MCP server."
  @spec mcp(Invocation.t()) :: non_neg_integer()
  def mcp(invocation), do: pending(invocation, "mcp", "serve the MCP tool surface over stdio")

  @doc "`tend workflow run`: the per-run runner process."
  @spec workflow_run(Invocation.t()) :: non_neg_integer()
  def workflow_run(invocation), do: pending(invocation, "workflow run", "drive one workflow run")

  @doc "`tend add`: capture a task."
  @spec add(Invocation.t()) :: non_neg_integer()
  def add(invocation), do: pending(invocation, "add", "capture a task")

  @doc "`tend ls`: dump the live view."
  @spec ls(Invocation.t()) :: non_neg_integer()
  def ls(invocation), do: pending(invocation, "ls", "dump the live view")

  @doc "`tend log`: capture a standup note."
  @spec log(Invocation.t()) :: non_neg_integer()
  def log(invocation), do: pending(invocation, "log", "capture a standup note")

  @doc "`tend standup`: print the standup summary."
  @spec standup(Invocation.t()) :: non_neg_integer()
  def standup(invocation), do: pending(invocation, "standup", "print a standup summary")

  @doc "`tend projects`: list and manage projects."
  @spec projects(Invocation.t()) :: non_neg_integer()
  def projects(invocation), do: pending(invocation, "projects", "list projects")

  @doc "`tend auth jira`: manage Jira credentials."
  @spec auth_jira(Invocation.t()) :: non_neg_integer()
  def auth_jira(invocation), do: pending(invocation, "auth jira", "manage Jira credentials")

  @doc "`tend agent-hook`: record a Claude Code hook event."
  @spec agent_hook(Invocation.t()) :: non_neg_integer()
  def agent_hook(invocation), do: pending(invocation, "agent-hook", "record a hook event")

  @doc """
  `tend version`: print the build version.

  Implemented rather than stubbed -- there is nothing behind it to defer.
  """
  @spec version(Invocation.t()) :: non_neg_integer()
  def version(%Invocation{}) do
    IO.puts(Version.string())
    0
  end

  # A stub announces which entry point it is and which database it would have
  # opened, so `mix run` shows dispatch working end to end.
  defp pending(%Invocation{db_path: db_path}, name, what) do
    IO.puts("tend #{name}: would #{what} (db: #{db_path}); not implemented yet")
    0
  end
end
