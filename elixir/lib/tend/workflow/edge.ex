defmodule Tend.Workflow.Edge do
  @moduledoc """
  A route from a step's outcome to the next step -- a port of `workflow.Edge`
  in `internal/workflow/workflow.go`.

  The edges leaving a step define its allowed outcomes; an outcome with no edge
  ends the run. `max_iterations` bounds how many times `to_step_id` may run
  within one run when reached over this edge, `nil` meaning unbounded -- the
  guard on a review-reject loop.
  """

  @typedoc "An edge, field for field the Go struct."
  @type t :: %__MODULE__{
          id: integer(),
          from_step_id: integer(),
          outcome: String.t(),
          to_step_id: integer(),
          max_iterations: integer() | nil
        }

  defstruct id: 0,
            from_step_id: 0,
            outcome: "",
            to_step_id: 0,
            max_iterations: nil
end
