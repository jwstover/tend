defmodule Tend.Workflow.StepKind do
  @moduledoc """
  What a step *is* -- a port of `workflow.StepKind` in
  `internal/workflow/workflow.go`.

  It distinguishes a step the runner executes as a headless `claude` session
  from one where it stops and waits for a human. Go models it as a `string`
  newtype with a `Valid` check and no fallback, so the set is closed: an
  unrecognised kind is an error, not a value. `parse/1` is strict for the same
  reason, unlike `Tend.Task.SessionStatus.parse/1`, which folds the unknown
  because a status is an observation rather than a decision the runner acts on.
  """

  @typedoc "One of the two step kinds."
  @type t :: :agent | :gate

  # Declaration order matches the Go constants.
  @kinds [
    # Runs prompt_md as a headless Claude Code session.
    :agent,
    # Pauses the run for a manual decision; no claude process is launched. Its
    # outcomes come from its edges like any other step.
    :gate
  ]

  @by_name Map.new(@kinds, &{Atom.to_string(&1), &1})

  @doc """
  Both step kinds, in the order the Go constants declare them.
  """
  @spec all() :: [t()]
  def all, do: @kinds

  @doc """
  Whether `kind` is a known step kind.

  The counterpart of Go's `StepKind.Valid`. Accepts any term, so it can guard a
  value that came from outside.
  """
  @spec valid?(term()) :: boolean()
  def valid?(kind), do: kind in @kinds

  @doc """
  The kind's stored name, the string the `workflow_steps.kind` column holds.
  """
  @spec format(t()) :: String.t()
  def format(kind) when kind in @kinds, do: Atom.to_string(kind)

  @doc """
  The kind named by `name`, or `:error`.

  Rejects everything Go's `Valid` rejects: the empty string, a differently
  cased name, and a kind that does not exist (`"human"`).
  """
  @spec parse(String.t()) :: {:ok, t()} | :error
  def parse(name) when is_binary(name), do: Map.fetch(@by_name, name)
end
