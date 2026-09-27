defmodule Tend.Workflow.Graph.PreviewRow do
  @moduledoc """
  One step of the text graph preview -- a port of `workflow.PreviewRow` in
  `internal/workflow/graph.go`.

  It carries the step's 1-based position in authoring order and the edges worth
  annotating. An edge routing `done` to the very next step is what a linear
  workflow has by default, so it is left implicit; every other edge is listed.
  `end?` is set when no edge leaves the step, in which case its one outcome
  (`done`) ends the run and the row is annotated `"done -> end"`.

  Go's field is `End`; `end` is a reserved word here, so the port spells it
  `end?`, which is also how Elixir names a boolean.
  """

  alias Tend.Workflow
  alias Tend.Workflow.Graph.PreviewEdge
  alias Tend.Workflow.Step

  @typedoc "A previewed step, field for field the Go struct."
  @type t :: %__MODULE__{
          number: integer(),
          step: Step.t(),
          edges: [PreviewEdge.t()],
          end?: boolean()
        }

  defstruct number: 0,
            step: %Step{},
            edges: [],
            end?: false

  @doc """
  Every bracketed note the row carries: one per listed edge, or `"done -> end"`
  for a step nothing leaves.

  Empty for a step whose only edge is the implicit `done -> next`. The port of
  Go's `PreviewRow.Annotations`.
  """
  @spec annotations(t()) :: [String.t()]
  def annotations(%__MODULE__{end?: true}), do: [Workflow.outcome_done() <> " -> end"]
  def annotations(%__MODULE__{edges: edges}), do: Enum.map(edges, &PreviewEdge.format/1)
end
