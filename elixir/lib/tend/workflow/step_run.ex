defmodule Tend.Workflow.StepRun do
  @moduledoc """
  One execution of one step within a run -- a port of `workflow.StepRun` in
  `internal/workflow/workflow.go`.

  The step definition is read live, so `prompt_rendered`, `model` and
  `permission_mode` record what actually ran; `system_prompt` is the hand-off
  block the runner appended to `claude`'s system prompt for an agent step,
  `""` for a gate.

  `input` is the previous step's deliverable; `feedback` is the hand-off of the
  step that routed here over a loop-back edge (a reject-style outcome), `""`
  when the step was reached going forward. `outcome` and `deliverable` are what
  this step handed off, empty until it is `finished?/1`.

  `session_external_id` is the `claude --session-id` of the agent session that
  ran the step, `""` for a gate. `log_path` is the step's stream-json log file,
  `""` if none was written.

  There is no state column: a step run is either finished or it is not, and
  `ended_at` is what says so.
  """

  @typedoc "A step run, field for field the Go struct."
  @type t :: %__MODULE__{
          id: integer(),
          run_id: integer(),
          step_id: integer(),
          iteration: integer(),
          session_external_id: String.t(),
          prompt_rendered: String.t(),
          system_prompt: String.t(),
          model: String.t(),
          permission_mode: String.t(),
          input: String.t(),
          feedback: String.t(),
          outcome: String.t(),
          deliverable: String.t(),
          log_path: String.t(),
          started_at: DateTime.t() | nil,
          ended_at: DateTime.t() | nil
        }

  defstruct id: 0,
            run_id: 0,
            step_id: 0,
            iteration: 0,
            session_external_id: "",
            prompt_rendered: "",
            system_prompt: "",
            model: "",
            permission_mode: "",
            input: "",
            feedback: "",
            outcome: "",
            deliverable: "",
            log_path: "",
            started_at: nil,
            ended_at: nil

  @doc """
  Whether the step run has handed off an outcome.

  The counterpart of Go's `StepRun.Finished`, which is `EndedAt != nil` --
  the end time is the record of the hand-off, not the outcome text, because a
  step may legitimately finish with an empty deliverable.
  """
  @spec finished?(t()) :: boolean()
  def finished?(%__MODULE__{ended_at: ended_at}), do: ended_at != nil
end
