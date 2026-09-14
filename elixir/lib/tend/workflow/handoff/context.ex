defmodule Tend.Workflow.Handoff.Context do
  @moduledoc """
  What `Tend.Workflow.Handoff.step_system_prompt/1` needs to tell an agent
  where it is and how it must finish -- a port of `HandoffContext` in
  `internal/workflow/handoff.go`.

  The workflow and step names, the iteration, and the outcomes `finish_step`
  accepts: the step's edge outcomes, or just `done` when it has none, which is
  the same set `get_workflow_step` reports. An empty `outcomes` is filled in
  with `done` by the functions that read it rather than here, exactly as Go
  does it, so a context built from a step with no edges is the zero value it
  looks like.
  """

  @typedoc "A hand-off context, field for field the Go struct."
  @type t :: %__MODULE__{
          workflow: String.t(),
          step: String.t(),
          iteration: integer(),
          outcomes: [String.t()]
        }

  defstruct workflow: "", step: "", iteration: 0, outcomes: []
end
