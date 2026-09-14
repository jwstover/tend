defmodule Tend.Task.Event do
  @moduledoc """
  One row of the append-only `task_events` activity log, plus the standup
  aggregation and the reporting-window helpers that live beside it -- a port of
  `internal/task/event.go`.

  Events record raw facts (a state went from `old` to `new`); standup verbs
  like "started" are derived at render time by `summarize/1`. `task_title` is a
  snapshot taken when the event was written, so events still render after their
  task is gone -- the same convention `Tend.Task.Session`'s `label` follows.

  ## Kinds

  A kind is an atom (see `kinds/0`), stored as the string `format_kind/1`
  returns. Go models it as a `string` newtype, so anything the `task_events.kind`
  column happens to hold is a legal value there and simply matches none of the
  switches that read it. `parse_kind/1` keeps that property by handing back the
  **raw string** for a name it does not recognize, rather than erroring: a row
  written by a future migration is ignored by `summarize/1`, exactly as Go
  ignores it, and its kind survives a round trip for anything that wants to
  echo it.

  That is also why the struct's `kind` defaults to `""`: Go's zero value is
  `EventKind("")`, and unlike the atom-only enumerations (`Tend.Task.State`,
  `Tend.Task.SessionStatus`) this type can hold it faithfully.

  ## Local time

  The reporting window is a *local* one: `last_workday_start/1` opens it at
  local midnight and `window_label/2` names it by the local day it opens on,
  because Go's callers hand `LastWorkdayStart` a `time.Now()` in `time.Local`
  and it answers in the same zone. Both read the machine's zone through
  `Tend.LocalTime`, as `Tend.Task.LogEntry` does for the notes they sit beside.
  """

  alias Tend.LocalTime
  alias Tend.Task.MovedItem
  alias Tend.Task.State
  alias Tend.Task.Summary
  alias Tend.Task.SummaryItem

  @typedoc "A kind of event tend writes."
  @type kind :: :created | :state | :deleted | :project | :parent

  @typedoc """
  What an event's `kind` field can hold: a kind tend knows, or the raw string
  of one it does not.
  """
  @type stored_kind :: kind() | String.t()

  @typedoc "An activity-log row, field for field the Go struct."
  @type t :: %__MODULE__{
          id: integer(),
          task_id: integer(),
          task_title: String.t(),
          kind: stored_kind(),
          old: String.t() | nil,
          new: String.t() | nil,
          created_at: DateTime.t() | nil
        }

  defstruct id: 0,
            task_id: 0,
            task_title: "",
            kind: "",
            old: nil,
            new: nil,
            created_at: nil

  # Declaration order matches the Go constants.
  @kinds [
    :created,
    :state,
    :deleted,
    # A task moving between projects. `old` and `new` hold project *names*,
    # snapshotted like `task_title`, so the log stays readable after a project
    # is renamed or deleted. Added by migration 8.
    :project,
    # A task moving to a different parent: promoted to the top level, demoted
    # under another task, or shifted between parents. `old` and `new` hold the
    # parent *titles*, snapshotted like `task_title`, with `top_level_label/0`
    # standing in for "no parent". `summarize/1` ignores it; a re-parent is
    # bookkeeping, not standup news. Added by migration 15.
    :parent
  ]

  @by_name Map.new(@kinds, &{Atom.to_string(&1), &1})

  @top_level_label "(top level)"

  @doc """
  Every kind tend writes, in the order the Go constants declare them.
  """
  @spec kinds() :: [kind()]
  def kinds, do: @kinds

  @doc """
  Whether `kind` is one of the kinds tend writes.
  """
  @spec valid_kind?(term()) :: boolean()
  def valid_kind?(kind), do: kind in @kinds

  @doc """
  The kind's stored name, the string the `task_events.kind` column holds.

  A raw string -- a kind `parse_kind/1` did not recognize -- is returned
  unchanged, so the round trip is exact for those too.
  """
  @spec format_kind(stored_kind()) :: String.t()
  def format_kind(kind) when kind in @kinds, do: Atom.to_string(kind)
  def format_kind(kind) when is_binary(kind), do: kind

  @doc """
  The kind named by `name`, or `name` itself when tend does not recognize it.

  Deliberately total, mirroring Go's `EventKind(row.Kind)` conversion: a kind
  from the future is carried as written and matched by nothing.
  """
  @spec parse_kind(String.t()) :: stored_kind()
  def parse_kind(name) when is_binary(name), do: Map.get(@by_name, name, name)

  @doc """
  What a `:parent` event records in `old` or `new` when the task had, or now
  has, no parent.
  """
  @spec top_level_label() :: String.t()
  def top_level_label, do: @top_level_label

  @doc """
  Collapses a window of events (oldest first) into one line per task.

  Transitions are replayed in order so a task that bounced around lands where
  it ended up: done then reopened is not completed, blocked then unblocked is
  not blocked. Started is sticky -- touching `:doing` at all counts, even if
  the task moved on. Tasks keep the order they first appear in the window.
  """
  @spec summarize([t()]) :: Summary.t()
  def summarize(events) when is_list(events) do
    {accs, reversed_order} = Enum.reduce(events, {%{}, []}, &replay/2)

    reversed_order
    |> Enum.reverse()
    |> Enum.reduce(%Summary{}, &collect(&1, Map.fetch!(accs, &1), &2))
    |> finish()
  end

  # A project event with no destination says nothing about where the task
  # landed, so it is not even a touch.
  defp replay(%__MODULE__{kind: :project, new: nil}, state), do: state

  defp replay(%__MODULE__{kind: :project, new: new} = event, state) do
    # Last move wins: the window reports where a task ended up.
    touch(state, event, &%{&1 | moved: true, moved_to: new})
  end

  defp replay(%__MODULE__{kind: :state, old: old, new: new} = event, state)
       when is_binary(old) and is_binary(new) do
    touch(state, event, &(&1 |> arrived_at(new) |> left(old)))
  end

  defp replay(%__MODULE__{}, state), do: state

  defp touch({accs, order}, %__MODULE__{task_id: id, task_title: title}, update) do
    {acc, order} =
      case accs do
        %{^id => acc} -> {acc, order}
        _first_sighting -> {blank_acc(), [id | order]}
      end

    {Map.put(accs, id, update.(%{acc | title: title})), order}
  end

  defp blank_acc do
    %{
      title: "",
      completed: false,
      blocked: false,
      started: false,
      triaged: false,
      moved: false,
      moved_to: ""
    }
  end

  defp arrived_at(acc, new) do
    case State.parse(new) do
      {:ok, :done} -> %{acc | completed: true, blocked: false}
      {:ok, :blocked} -> %{acc | blocked: true}
      {:ok, :doing} -> %{acc | started: true}
      _other -> acc
    end
  end

  defp left(acc, old) do
    case State.parse(old) do
      {:ok, :done} -> %{acc | completed: false}
      {:ok, :blocked} -> %{acc | blocked: false}
      {:ok, :inbox} -> %{acc | triaged: true}
      _other -> acc
    end
  end

  defp collect(id, acc, summary) do
    summary
    |> count_triage(acc)
    |> record_move(id, acc)
    |> place(%SummaryItem{task_id: id, title: acc.title}, acc)
  end

  defp count_triage(summary, %{triaged: true}), do: %{summary | triaged: summary.triaged + 1}
  defp count_triage(summary, _acc), do: summary

  defp record_move(summary, id, %{moved: true} = acc) do
    item = %MovedItem{task_id: id, title: acc.title, to: acc.moved_to}
    %{summary | moved: [item | summary.moved]}
  end

  defp record_move(summary, _id, _acc), do: summary

  defp place(summary, item, acc) do
    cond do
      acc.completed -> %{summary | completed: [item | summary.completed]}
      acc.blocked -> %{summary | blocked: [item | summary.blocked]}
      acc.started -> %{summary | started: [item | summary.started]}
      true -> summary
    end
  end

  # The four lists were built head-first; put them back in window order.
  defp finish(%Summary{} = summary) do
    %{
      summary
      | completed: Enum.reverse(summary.completed),
        blocked: Enum.reverse(summary.blocked),
        started: Enum.reverse(summary.started),
        moved: Enum.reverse(summary.moved)
    }
  end

  @doc """
  When *local* midnight of the most recent weekday before `t` began, so a
  Monday standup reports Friday.

  Both Go callers pass `time.Now()` -- a local wall clock -- and Go answers in
  that same zone, so the day walked back from, and the midnight returned, are
  local. `t` is an instant here (`DateTime.utc_now/0` is what a caller has, the
  port carrying no time zone database), read as the machine's wall clock the
  way notes are bucketed; see `Tend.LocalTime`. The result comes back in UTC,
  which is the form the store and `window_label/2` want.
  """
  @spec last_workday_start(DateTime.t()) :: DateTime.t()
  def last_workday_start(%DateTime{} = t) do
    t
    |> LocalTime.to_naive()
    |> NaiveDateTime.to_date()
    |> Date.add(-1)
    |> skip_weekend()
    |> NaiveDateTime.new!(~T[00:00:00])
    |> LocalTime.from_naive()
  end

  defp skip_weekend(day) do
    # Date.day_of_week/1 counts Monday as 1, so 6 and 7 are the weekend.
    if Date.day_of_week(day) in [6, 7], do: skip_weekend(Date.add(day, -1)), else: day
  end

  @doc """
  Names a reporting window for display: `"Yesterday"` when it starts there, the
  weekday when it starts within the past week (`"Since Friday"` on a Monday),
  and the date otherwise.

  The elapsed days are whole days truncated toward zero, as Go's
  `int(now.Sub(from).Hours() / 24)` is. The weekday and the date name the
  *local* day `from` falls on -- Go formats it in its own zone, which for the
  window `last_workday_start/1` opens is the local one.
  """
  @spec window_label(DateTime.t(), DateTime.t()) :: String.t()
  def window_label(%DateTime{} = from, %DateTime{} = now) do
    days = div(DateTime.diff(now, from, :second), 86_400)
    local = LocalTime.to_naive(from)

    cond do
      days <= 1 -> "Yesterday"
      days < 7 -> "Since " <> Calendar.strftime(local, "%A")
      true -> "Since " <> Date.to_iso8601(NaiveDateTime.to_date(local))
    end
  end
end
