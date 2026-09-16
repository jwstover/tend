defmodule Tend.Workflow do
  @moduledoc """
  The domain layer for agent workflows: reusable multi-step agent procedures a
  task can run -- a port of `internal/workflow/workflow.go`.

  A workflow is a set of steps (`Tend.Workflow.Step`) joined by edges
  (`Tend.Workflow.Edge`) keyed on outcome. A run (`Tend.Workflow.Run`) binds a
  workflow to a task; each step run (`Tend.Workflow.StepRun`) is one execution
  of one step within a run, and for an agent step that is exactly one Claude
  Code session.

  Like the Go package, this module and everything under `Tend.Workflow.*` is
  types and rules with zero I/O. It is a **sibling** of `Tend.Task` rather than
  a tenant of it, because a workflow has its own vocabulary (Step, Edge, Run)
  that would collide with or crowd the task tree's. The one edge between them
  is `Tend.Workflow.RunState.session_status/1`, the port of
  `internal/workflow/status.go`, which Go's own package takes the same
  dependency for.

  Struct fields default to their Go zero values wherever Go's zero has an
  Elixir counterpart, the same caveat `Tend.Task` carries: `time.Time{}` is
  `nil` here, and so is a string newtype's `""` where that string is not a
  valid member of the set (`kind` on a step, `state` on a run).

  Nothing calls this module yet. The store port is what will read and write
  these values, exactly as it does for `Tend.Task`.
  """

  @typedoc """
  A named, reusable procedure: an ordered set of steps plus the edges joining
  them. Field for field the Go struct, with the timestamps `DateTime`s in UTC.

  Deliberately not bound to a project or a task; binding happens per
  `Tend.Workflow.Run`.
  """
  @type t :: %__MODULE__{
          id: integer(),
          name: String.t(),
          description: String.t(),
          created_at: DateTime.t() | nil,
          updated_at: DateTime.t() | nil,
          step_count: integer()
        }

  # step_count is the number of steps in the workflow. Zero unless the value
  # came from a listing that counts them.
  defstruct id: 0,
            name: "",
            description: "",
            created_at: nil,
            updated_at: nil,
            step_count: 0

  @outcome_done "done"
  @outcome_approve "approve"
  @outcome_reject "reject"

  @doc """
  The conventional "it went fine, move on" outcome: what a linear workflow's
  edges are keyed on, and the fallback a headless step reports when it exits
  without calling `finish_step`.
  """
  @spec outcome_done() :: String.t()
  def outcome_done, do: @outcome_done

  @doc """
  The conventional gate approval outcome.

  `outcome_approve/0` and `outcome_reject/0` are what the TUI's `a` and `x`
  keys write, and a reject is the one that carries the reviewer's feedback as
  its deliverable. They are conventions, not a rule -- a gate routes whatever
  outcomes its edges name.
  """
  @spec outcome_approve() :: String.t()
  def outcome_approve, do: @outcome_approve

  @doc """
  The conventional gate rejection outcome. See `outcome_approve/0`.
  """
  @spec outcome_reject() :: String.t()
  def outcome_reject, do: @outcome_reject

  @doc """
  Trims surrounding whitespace from a workflow or step name and rejects blanks.

  Case is preserved as typed; the schema's `NOCASE` collation is what makes
  `"Ship"` and `"ship"` the same workflow.
  """
  @spec normalize_name(String.t()) :: {:ok, String.t()} | {:error, :empty_name}
  def normalize_name(s) when is_binary(s) do
    case String.trim(s) do
      "" -> {:error, :empty_name}
      name -> {:ok, name}
    end
  end

  @doc """
  Canonicalizes an outcome name: trimmed and lower-cased, so the `"Approve"` an
  agent reports matches the `"approve"` an edge was authored with.

  Rejects anything that normalizes to nothing. Lower-casing folds the way Go's
  `strings.ToLower` folds (see the note on `lower/1` below), because the result
  is the outcome as stored and compared, not just a display form.
  """
  @spec normalize_outcome(String.t()) :: {:ok, String.t()} | {:error, :empty_outcome}
  def normalize_outcome(s) when is_binary(s) do
    case s |> String.trim() |> lower() do
      "" -> {:error, :empty_outcome}
      outcome -> {:ok, outcome}
    end
  end

  # Go lowers with strings.ToLower, which walks runes applying Unicode's
  # *simple* mapping. String.downcase/1 applies the full mapping, and the two
  # differ on U+0130 (dotted capital I) -- Go lowers it to "i", Elixir to "i" +
  # U+0307 -- plus any character cased after Go's `unicode.Version`, which
  # trails OTP's `:unicode_util.spec_version/0` by a release or more. Only the
  # U+0130 divergence is a mapping difference rather than data-version skew,
  # and only it is foldable without reimplementing the table.
  #
  # Invalid UTF-8 also diverges, and is left alone deliberately: Go's ToLower
  # goes through strings.Map, which substitutes U+FFFD per invalid *byte*,
  # while String.downcase/1 passes the byte through. String.replace_invalid/1
  # would not close the gap -- it collapses a truncated multi-byte sequence to
  # one U+FFFD where Go emits one per byte -- so it would trade a visible
  # divergence for a hidden one. Outcomes reaching here are valid UTF-8.
  #
  # `Tend.Task.Project` folds the same way for the same reason; the fold is
  # duplicated rather than shared because this tree depends on nothing, exactly
  # as Go calls strings.ToLower separately in each package.
  defp lower(s), do: s |> String.replace("İ", "i") |> String.downcase()

  @doc """
  The reason deleting a workflow or a step returns when a live run stands in
  the way, naming the run so the user can go deal with it.

  A port of Go's `InUseError`, which wraps `ErrInUse` so callers can still
  match it with `errors.Is`. Here the sentinel is the tuple's tag: a caller
  that wants "is this the in-use error?" matches `{:in_use, _what, _run_id}`,
  and `Tend.Error.message/1` renders the wrapped message the way Go's does.
  """
  @spec in_use_error(String.t(), integer()) :: {:in_use, String.t(), integer()}
  def in_use_error(what, run_id) when is_binary(what) and is_integer(run_id) do
    {:in_use, what, run_id}
  end
end
