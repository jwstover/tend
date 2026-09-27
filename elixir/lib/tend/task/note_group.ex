defmodule Tend.Task.NoteGroup do
  @moduledoc """
  One task's notes -- or the freestanding notes when `task_id` is `nil` -- for
  the standup view's grouped rendering.

  A port of `task.NoteGroup` in `internal/task/log.go`. `title` is the group's
  first note's `Tend.Task.LogEntry.ref/1`: the task title, the `"#42"` fallback
  when only the id survives, or `""` for freestanding notes.
  """

  alias Tend.Task.LogEntry

  @type t :: %__MODULE__{
          task_id: integer() | nil,
          title: String.t(),
          notes: [LogEntry.t()]
        }

  defstruct task_id: nil, title: "", notes: []
end
