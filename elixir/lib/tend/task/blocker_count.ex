defmodule Tend.Task.BlockerCount do
  @moduledoc """
  What a task waits on, summarized for the list row's dependency cell: how
  many tasks it depends on, and how many of those are still open.

  A port of `task.BlockerCount` in `internal/task/task.go`. Like
  `Tend.Task.ChildCount` it loads as a batch map rather than per row.
  """

  @type t :: %__MODULE__{open: integer(), total: integer()}

  defstruct open: 0, total: 0

  @doc """
  Whether the task still waits on something.
  """
  @spec blocked?(t()) :: boolean()
  def blocked?(%__MODULE__{open: open}), do: open > 0
end
