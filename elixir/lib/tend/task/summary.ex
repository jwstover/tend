defmodule Tend.Task.Summary do
  @moduledoc """
  A window of events aggregated for standup rendering -- a port of
  `task.Summary` in `internal/task/event.go`.

  Each task appears in at most one of `completed`/`blocked`/`started`, chosen
  by that precedence. `triaged` counts the tasks that left the inbox during the
  window.

  `moved` is independent of the three above: a task can be completed *and* have
  changed project in the same window, and both are worth reporting.

  Go returns nil slices for the four lists when nothing landed in them; the
  empty list is the same thing here. `Tend.Task.Event.summarize/1` is what
  builds one.
  """

  alias Tend.Task.MovedItem
  alias Tend.Task.SummaryItem

  @type t :: %__MODULE__{
          completed: [SummaryItem.t()],
          blocked: [SummaryItem.t()],
          started: [SummaryItem.t()],
          moved: [MovedItem.t()],
          triaged: non_neg_integer()
        }

  defstruct completed: [], blocked: [], started: [], moved: [], triaged: 0

  @doc """
  Whether the summary has nothing worth printing.

  A window holding only a move is not empty: it has something to report.
  """
  @spec empty?(t()) :: boolean()
  def empty?(%__MODULE__{} = summary) do
    summary.completed == [] and summary.blocked == [] and summary.started == [] and
      summary.moved == [] and summary.triaged == 0
  end
end
