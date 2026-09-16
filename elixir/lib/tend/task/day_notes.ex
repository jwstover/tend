defmodule Tend.Task.DayNotes do
  @moduledoc """
  One local calendar day's slice of a notes window.

  A port of `task.DayNotes` in `internal/task/log.go`. Go carries the day as a
  `time.Time` at local midnight because that is the only "date" it has; here it
  is a `Date`, which is what local midnight was standing in for -- everything
  Go does with the field (compare two of them, label one relative to today,
  format it `"Mon Jan 2"`) reads only the calendar day.
  """

  alias Tend.Task.LogEntry

  @type t :: %__MODULE__{day: Date.t() | nil, notes: [LogEntry.t()]}

  defstruct day: nil, notes: []
end
