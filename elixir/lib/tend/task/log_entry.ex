defmodule Tend.Task.LogEntry do
  @moduledoc """
  A manual standup note -- a quick free-form line captured via the TUI or
  `tend log` -- and the bucketing, grouping and rendering the standup views
  read it through. A port of `internal/task/log.go`.

  `task_id` is optional context; the note stands on its own if the task is
  later deleted. `task_title` is joined in for display where the store query
  provides it -- empty for freestanding notes, deleted tasks, or queries that
  don't need it.

  ## Local time

  `created_at` is stored UTC, and the standup groups and stamps notes in *local*
  time, so a note written at 23:30 does not surface under tomorrow. The zone is
  the machine's, read through `Tend.LocalTime` — so `split_notes_by_day/1` and
  `standup_markdown/4` are pure in their arguments but read that zone, exactly
  as their Go counterparts do through `time.Local`.
  """

  alias Tend.LocalTime
  alias Tend.Task
  alias Tend.Task.DayNotes
  alias Tend.Task.NoteGroup
  alias Tend.Task.Summary

  @typedoc "A note, field for field the Go struct."
  @type t :: %__MODULE__{
          id: integer(),
          task_id: integer() | nil,
          task_title: String.t(),
          body: String.t(),
          created_at: DateTime.t() | nil
        }

  defstruct id: 0,
            task_id: nil,
            task_title: "",
            body: "",
            created_at: nil

  @doc """
  The note's task reference for display: the title when known, `"#42"` when
  only the id survives, `""` when freestanding.
  """
  @spec ref(t()) :: String.t()
  def ref(%__MODULE__{task_id: nil}), do: ""
  def ref(%__MODULE__{task_id: task_id, task_title: ""}), do: "##{task_id}"
  def ref(%__MODULE__{task_title: task_title}), do: task_title

  @doc """
  Buckets a chronological notes window into local calendar days, preserving
  order -- the standup view's top-level sections, so yesterday's notes never
  mix with today's.

  Only runs of notes on the same day merge, the way Go's "compare against the
  last bucket" does: a window that is not chronological gets a bucket per run.
  """
  @spec split_notes_by_day([t()]) :: [DayNotes.t()]
  def split_notes_by_day(notes) when is_list(notes) do
    notes
    |> Enum.reduce([], &bucket/2)
    |> Enum.reverse()
    |> Enum.map(&%{&1 | notes: Enum.reverse(&1.notes)})
  end

  defp bucket(note, days) do
    day = note.created_at |> LocalTime.to_naive() |> NaiveDateTime.to_date()

    case days do
      [%DayNotes{day: ^day} = last | rest] -> [%{last | notes: [note | last.notes]} | rest]
      _new_day -> [%DayNotes{day: day, notes: [note]} | days]
    end
  end

  @doc """
  Names a local calendar day relative to `now`: `"Today"`, `"Yesterday"`, or
  the date itself (`"Fri Jul 3"`).

  Go takes the current *instant* and reads only its calendar day; pass that
  day.
  """
  @spec day_label(Date.t(), Date.t()) :: String.t()
  def day_label(%Date{} = day, %Date{} = now) do
    cond do
      Date.compare(day, now) == :eq -> "Today"
      Date.compare(day, Date.add(now, -1)) == :eq -> "Yesterday"
      true -> Calendar.strftime(day, "%a %b %-d")
    end
  end

  @doc """
  Buckets a window of notes (oldest first) by task.

  Groups keep the order each task first appeared in the window, and notes stay
  chronological within their group, so every group reads as that workstream's
  narrative. Freestanding notes share one group.
  """
  @spec group_notes([t()]) :: [NoteGroup.t()]
  def group_notes(notes) when is_list(notes) do
    # Go keys the freestanding group by task id 0; `nil` is the same bucket
    # without borrowing an id a task could in principle hold.
    {groups, order} = Enum.reduce(notes, {%{}, []}, &add_to_group/2)

    order
    |> Enum.reverse()
    |> Enum.map(fn key ->
      group = Map.fetch!(groups, key)
      %{group | notes: Enum.reverse(group.notes)}
    end)
  end

  defp add_to_group(note, {groups, order}) do
    key = note.task_id

    {group, order} =
      case groups do
        %{^key => group} -> {group, order}
        _first_sighting -> {%NoteGroup{task_id: key, title: ref(note)}, [key | order]}
      end

    {Map.put(groups, key, %{group | notes: [note | group.notes]}), order}
  end

  @doc """
  Trims surrounding whitespace and rejects blank notes -- the entire validation
  surface, mirroring capture.
  """
  @spec normalize_note(String.t()) :: {:ok, String.t()} | {:error, :empty_note}
  def normalize_note(s) when is_binary(s) do
    case String.trim(s) do
      "" -> {:error, :empty_note}
      note -> {:ok, note}
    end
  end

  @doc """
  Renders the standup export shared by `tend standup` and the TUI's yank:
  manual notes first (the user's own words), then the report generated from the
  event log, then what is in flight and what is stuck.

  `label` names the reporting window (`"Yesterday"`, `"Since Friday"`) --
  `Tend.Task.Event.window_label/2` produces it. Note timestamps render in local
  time; `created_at` is stored UTC.
  """
  @spec standup_markdown(String.t(), [t()], Summary.t(), [Task.t()]) :: String.t()
  def standup_markdown(label, notes, %Summary{} = summary, live)
      when is_binary(label) and is_list(notes) and is_list(live) do
    IO.iodata_to_binary([
      Enum.map(split_notes_by_day(notes), &render_day/1),
      "**#{label}**\n",
      Enum.map(summary.completed, &"- Completed: #{&1.title} (##{&1.task_id})\n"),
      Enum.map(summary.blocked, &"- Blocked: #{&1.title} (##{&1.task_id})\n"),
      Enum.map(summary.started, &"- Started: #{&1.title} (##{&1.task_id})\n"),
      Enum.map(summary.moved, &"- Moved to #{&1.to}: #{&1.title} (##{&1.task_id})\n"),
      if(summary.triaged > 0, do: "- Triaged #{summary.triaged} inbox item(s)\n", else: []),
      if(Summary.empty?(summary), do: "- nothing logged\n", else: []),
      "\n**Today**\n",
      section(live, :doing, "- nothing in progress\n"),
      "\n**Blockers**\n",
      section(live, :blocked, "- none\n")
    ])
  end

  defp render_day(%DayNotes{} = day) do
    [
      "**Notes — #{Calendar.strftime(day.day, "%a %b %-d")}**\n",
      Enum.map(group_notes(day.notes), &render_group/1),
      "\n"
    ]
  end

  defp render_group(%NoteGroup{} = group) do
    [group_header(group), Enum.map(group.notes, &render_note/1)]
  end

  defp group_header(%NoteGroup{task_id: nil}), do: "- general\n"

  defp group_header(%NoteGroup{task_id: task_id, title: title}) do
    # The deleted-task fallback keeps the bare id as the header rather than
    # printing it twice.
    if title == "##{task_id}", do: "- #{title}\n", else: "- #{title} (##{task_id})\n"
  end

  defp render_note(%__MODULE__{} = note) do
    [first | rest] = String.split(note.body, "\n")
    at = Calendar.strftime(LocalTime.to_naive(note.created_at), "%H:%M")
    ["  - #{at} — #{first}\n", Enum.map(rest, &"    #{&1}\n")]
  end

  defp section(live, state, when_none) do
    case Enum.filter(live, &(&1.state == state)) do
      [] -> when_none
      tasks -> Enum.map(tasks, &"- #{&1.title} (##{&1.id})\n")
    end
  end
end
