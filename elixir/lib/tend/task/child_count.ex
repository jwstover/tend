defmodule Tend.Task.ChildCount do
  @moduledoc """
  A task's sub-tasks summarized for progress display -- the N/M indicator in
  the list and detail pane.

  A port of `task.ChildCount` in `internal/task/task.go`. Loaded as a batch
  map keyed by task id, never per row.
  """

  @type t :: %__MODULE__{done: integer(), total: integer()}

  defstruct done: 0, total: 0
end
