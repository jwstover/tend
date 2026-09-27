defmodule Tend.Workflow.Graph.Problem do
  @moduledoc """
  One thing `Tend.Workflow.Graph.validate/3` found wrong -- a port of
  `workflow.Problem` in `internal/workflow/graph.go`.

  It names the step it is about (by id and name) and says what is wrong.
  `step_id` is `0` and `step` is `""` for a problem about the workflow as a
  whole, which today is only "no steps".

  Go's `Problem` has no severity: every problem is a problem, and the TUI
  renders `format/1` verbatim. The message text is therefore part of the port's
  contract, not a detail -- see the module doc of `Tend.Workflow.Graph`.
  """

  @typedoc "A problem, field for field the Go struct."
  @type t :: %__MODULE__{
          step_id: integer(),
          step: String.t(),
          msg: String.t()
        }

  defstruct step_id: 0,
            step: "",
            msg: ""

  @doc """
  The problem as one line: `"review: prompt mentions \\"approve\\" but no edge
  routes it"`, or just the message when the problem is about no step in
  particular.

  The port of Go's `Problem.String`. Named `format/1` for the same reason
  `Tend.Workflow.StepKind.format/1` is: this tree spells a Go `String()` method
  as a `format/1` function rather than a `String.Chars` implementation, so the
  rendering is called explicitly wherever it matters.
  """
  @spec format(t()) :: String.t()
  def format(%__MODULE__{step: "", msg: msg}), do: msg
  def format(%__MODULE__{step: step, msg: msg}), do: step <> ": " <> msg
end
