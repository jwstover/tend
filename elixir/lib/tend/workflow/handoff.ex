defmodule Tend.Workflow.Handoff do
  @moduledoc """
  How a step hands off: the rule for settling one that did not, and the text
  that tells an agent it has to -- a port of `internal/workflow/handoff.go`.

  Pure text assembly and one predicate, no I/O, so the block that ran can be
  recorded on the step run verbatim and a test can pin it byte for byte.

  ## The tool ids are spelled out

  `finish_step_tool/0` and `get_workflow_step_tool/0` are the exact tool ids a
  step's session sees for the step tools registered by
  `tend mcp --step-run-id`: the MCP server is named `tend` in the per-session
  `--mcp-config`, and Claude Code exposes a server's tools as
  `mcp__<server>__<tool>`. Written out in full so a session whose tool list is
  deferred can load them by name rather than guess.

  ## The text is the contract

  Every sentence in `step_system_prompt/1`, `nudge_prompt/2` and
  `retry_prompt/3` is there because a real run went wrong without it -- the
  deferred-tool loop of tend task #197, the direct `tend.db` write of task #252
  -- so the strings are ported verbatim rather than rephrased, and
  `Tend.Workflow.GoDriverTest` renders the real Go functions and compares the
  bytes.

  Nothing calls this module: the runner that appends the system prompt and
  sends the nudge or the retry turn is Phase 3.
  """

  alias Tend.Error
  alias Tend.Workflow
  alias Tend.Workflow.Handoff.Context

  @finish_step_tool "mcp__tend__finish_step"
  @get_workflow_step_tool "mcp__tend__get_workflow_step"

  # Covers the one way a session with many MCP servers has been seen to miss
  # the hand-off (tend task #197, haiku): tend's tools are deferred behind a
  # tool search, the model loads them, and then searches again and again
  # instead of calling what it loaded. The tool ids are exact, so one load is
  # enough, and the hint says so.
  @deferred_tools_hint "These tool ids are exact. If they are not in your tool list yet, load them once by name " <>
                         "(a deferred-tool search for `select:" <>
                         @finish_step_tool <>
                         "," <>
                         @get_workflow_step_tool <>
                         "`); " <>
                         "a search result that lists a tool means it is now callable, so call it directly. " <>
                         "Never search for the same tool twice."

  # Closes the way a step with bypassPermissions was seen to route around the
  # MCP surface (tend task #252, the Complete Breakdown dry run): needing to
  # edit one table inside a large task body, the Ship step found no tool for
  # it, read tend.db's schema with sqlite3, and ran an UPDATE on the tasks
  # table itself. The write happened to be correct, but nothing checked it and
  # no event recorded it. The tools are the one write path; a step that cannot
  # express an edit through them stops and says so in its deliverable instead.
  @db_write_rule "tend's MCP tools are the only way to read or change tend's tasks and workflows from this session. " <>
                   "Never open, copy or write tend's SQLite database (tend.db) with sqlite3, a script or any other means, " <>
                   "even to make an edit the tools cannot express: put what you could not record into your deliverable instead."

  @doc """
  The exact tool id of the `finish_step` tool a step's session sees.

      iex> Tend.Workflow.Handoff.finish_step_tool()
      "mcp__tend__finish_step"
  """
  @spec finish_step_tool() :: String.t()
  def finish_step_tool, do: @finish_step_tool

  @doc """
  The exact tool id of the `get_workflow_step` tool a step's session sees.

      iex> Tend.Workflow.Handoff.get_workflow_step_tool()
      "mcp__tend__get_workflow_step"
  """
  @spec get_workflow_step_tool() :: String.t()
  def get_workflow_step_tool, do: @get_workflow_step_tool

  @doc """
  Whether a step that exits without calling `finish_step` may still be settled
  from its final text as a `done` deliverable.

  That is safe only when `done` is the one outcome the step can have: no edges
  at all (the end of a linear workflow) or a single `done` edge. A step that
  routes anything else -- approve/reject, a verdict, a choice -- must hand off
  through `finish_step`, because a guessed `done` would silently take an edge
  the agent never chose, or end the run with the verdict lost.

      iex> Tend.Workflow.Handoff.fallback_allowed?([])
      true
      iex> Tend.Workflow.Handoff.fallback_allowed?(["done"])
      true
      iex> Tend.Workflow.Handoff.fallback_allowed?(["approve", "reject"])
      false
  """
  @spec fallback_allowed?([String.t()]) :: boolean()
  def fallback_allowed?([]), do: true
  def fallback_allowed?([outcome]), do: outcome == Workflow.outcome_done()
  def fallback_allowed?(outcomes) when is_list(outcomes), do: false

  @doc """
  The block the runner appends to claude's system prompt
  (`--append-system-prompt`) for every agent step.

  It is here rather than in each step's `prompt_md` so the hand-off contract
  does not depend on every prompt author remembering to spell it out: it names
  the step, the run's dependence on `finish_step`, the exact tool ids and the
  allowed outcomes, and it says the tool does not end the session, since a
  model that expects it to may stall waiting.

  A context with no outcomes gets `done`, the same set `get_workflow_step`
  reports for a step with no edges.
  """
  @spec step_system_prompt(Context.t()) :: String.t()
  def step_system_prompt(%Context{} = context) do
    outcomes = outcomes_or_done(context.outcomes)

    "You are executing step #{Error.quote_go(context.step)} of the tend workflow " <>
      "#{Error.quote_go(context.workflow)} (iteration #{context.iteration}) as a headless " <>
      "session driven by tend's workflow runner.\n\n" <>
      "The run cannot continue until you call the MCP tool #{@finish_step_tool} with " <>
      "`outcome` set to one of: #{quote_all(outcomes)}. " <>
      "Pass what the next step should receive as `deliverable` " <>
      "(a PR link, a summary, a review verdict with its reasons). " <>
      "Call #{@get_workflow_step_tool} first if you need this step's input, feedback, " <>
      "or outcomes.\n\n" <>
      @deferred_tools_hint <>
      "\n\n" <>
      "#{@finish_step_tool} does not end your session; call it once, when the work is done, " <>
      "then finish your turn normally. " <>
      "Do not skip it, and do not substitute a shell command or a message for it: " <>
      "a step that exits without calling it is treated as not having done its job.\n\n" <>
      @db_write_rule
  end

  @doc """
  The one follow-up turn the runner sends to a step's session when it exited
  without calling `finish_step`.

  Short, because the session already holds the step and its work; it only has
  to name what is missing and what is allowed. An empty `outcomes` gets `done`,
  as in `step_system_prompt/1`.
  """
  @spec nudge_prompt(String.t(), [String.t()]) :: String.t()
  def nudge_prompt(step, outcomes) when is_binary(step) and is_list(outcomes) do
    outcomes = outcomes_or_done(outcomes)

    "Your previous turn ended without handing off step #{Error.quote_go(step)}: " <>
      "the workflow run is stopped until you call the MCP tool #{@finish_step_tool} " <>
      "with `outcome` set to one of #{quote_all(outcomes)} and the step's result as " <>
      "`deliverable`. Call it now with the verdict you already reached, then finish your " <>
      "turn. Do not redo the work. " <> @deferred_tools_hint
  end

  @doc """
  The turn sent to a failed step's existing session when the user retries the
  run (`runner.Retry` without a fresh start).

  The session already holds the step and whatever it did, so it only has to
  learn that the run stopped, why, and that the hand-off still stands. `reason`
  is the failure the runner recorded on the run (`Tend.Workflow.Run`'s
  `error`); it is quoted back, trimmed as Go's `strings.TrimSpace` trims it, so an agent that ended without
  `finish_step`, hit a denied tool call, or reported an error knows which of
  those to fix rather than redoing the step from the top.
  """
  @spec retry_prompt(String.t(), String.t(), [String.t()]) :: String.t()
  def retry_prompt(step, reason, outcomes)
      when is_binary(step) and is_binary(reason) and is_list(outcomes) do
    outcomes = outcomes_or_done(outcomes)

    "The workflow run failed at step #{Error.quote_go(step)} and has been retried. " <>
      "The runner recorded the failure as: #{String.trim(reason)}\n\n" <>
      "Review where you left off, fix what caused the failure, and complete the step as " <>
      "originally instructed. " <>
      "Then hand off by calling the MCP tool #{@finish_step_tool} with `outcome` set to " <>
      "one of #{quote_all(outcomes)} and the step's result as `deliverable`; " <>
      "do not end your turn without it. " <> @deferred_tools_hint
  end

  defp outcomes_or_done([]), do: [Workflow.outcome_done()]
  defp outcomes_or_done(outcomes), do: outcomes

  # Go's quoteAll: `"approve", "reject"`. The quoting is strconv.Quote, which
  # is what Tend.Error.quote_go/1 ports, so an outcome carrying a quote or a
  # control character is escaped Go's way here too.
  defp quote_all(outcomes), do: Enum.map_join(outcomes, ", ", &Error.quote_go/1)
end
