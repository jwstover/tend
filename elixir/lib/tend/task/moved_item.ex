defmodule Tend.Task.MovedItem do
  @moduledoc """
  One task's project move in a standup summary.

  A port of `task.MovedItem` in `internal/task/event.go`. `to` is where the
  task ended up, so a task moved twice inside the window reports its
  destination, not its route.
  """

  @type t :: %__MODULE__{task_id: integer(), title: String.t(), to: String.t()}

  defstruct task_id: 0, title: "", to: ""
end
