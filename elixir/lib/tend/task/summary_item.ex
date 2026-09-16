defmodule Tend.Task.SummaryItem do
  @moduledoc """
  One task's line in a standup summary.

  A port of `task.SummaryItem` in `internal/task/event.go`. The title is the
  one snapshotted on the events the item was derived from, so a task deleted
  inside the window still reads by name.
  """

  @type t :: %__MODULE__{task_id: integer(), title: String.t()}

  defstruct task_id: 0, title: ""
end
