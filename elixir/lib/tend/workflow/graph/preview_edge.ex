defmodule Tend.Workflow.Graph.PreviewEdge do
  @moduledoc """
  An edge as the preview shows it -- a port of `workflow.PreviewEdge` in
  `internal/workflow/graph.go`.

  `to` is the 1-based number of the step the edge leads to, `0` when that step
  is not among the workflow's steps. The schema's cascade should make that
  impossible, but a preview should never crash over it.
  """

  alias Tend.Workflow.Edge

  @typedoc "A previewed edge, field for field the Go struct."
  @type t :: %__MODULE__{
          edge: Edge.t(),
          to: integer()
        }

  defstruct edge: %Edge{},
            to: 0

  @doc """
  The annotation: `"reject -> 2 (max 3)"`, or `"reject -> ?"` when the target
  is unknown.

  The port of Go's `PreviewEdge.String`.
  """
  @spec format(t()) :: String.t()
  def format(%__MODULE__{edge: edge, to: to}) do
    target = if to > 0, do: Integer.to_string(to), else: "?"
    annotation = "#{edge.outcome} -> #{target}"

    case edge.max_iterations do
      nil -> annotation
      max -> annotation <> " (max #{max})"
    end
  end
end
