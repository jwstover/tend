defmodule Tend.Workflow.Run do
  @moduledoc """
  One execution of a workflow against a task -- a port of `workflow.Run` in
  `internal/workflow/workflow.go`.

  `current_step_run_id` is the step run the runner is on (or paused at), `nil`
  before the first step starts. `tmux_session` names the runner's tmux session,
  `""` when none has been started. `ended_at` is set exactly when `state`
  becomes terminal. `error` is why the run failed -- the runner's message, kept
  on the row because the runner's own output dies with its tmux session -- and
  `""` unless `state` is `:failed`.

  `state` defaults to `nil` rather than `""`: Go's zero is `RunState("")`,
  which is not a valid state and so has no atom here. See
  `Tend.Workflow.RunState`.
  """

  alias Tend.Workflow.RunState

  @typedoc "A run, field for field the Go struct."
  @type t :: %__MODULE__{
          id: integer(),
          workflow_id: integer(),
          task_id: integer(),
          cwd: String.t(),
          state: RunState.t() | nil,
          current_step_run_id: integer() | nil,
          tmux_session: String.t(),
          error: String.t(),
          started_at: DateTime.t() | nil,
          ended_at: DateTime.t() | nil
        }

  defstruct id: 0,
            workflow_id: 0,
            task_id: 0,
            cwd: "",
            state: nil,
            current_step_run_id: nil,
            tmux_session: "",
            error: "",
            started_at: nil,
            ended_at: nil
end
