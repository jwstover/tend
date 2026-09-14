defmodule Tend.Task.Brief do
  @moduledoc """
  Everything a Claude Code session launched from a task should know about it
  up front, and the markdown block that says it.

  A port of `internal/task/brief.go` (Go's `SessionBrief` and
  `SessionSystemPrompt`). Pure text assembly, zero I/O, like
  `Tend.Task.LogEntry.standup_markdown/4`.

  The struct is the task itself, where it sits (project, parent, sub-tasks),
  what it waits on and what waits on it, and the notes logged against it. The
  TUI assembles it from the store right before a launch or resume and renders
  it into `claude`'s `--append-system-prompt`, so a session starts with the
  task's context instead of the user having to open with "review the current
  tend task".

  Every field but `task` is optional: a brief with nothing else renders the
  task alone. `children` and `log` come in the order the store lists them
  (sub-tasks oldest first, log newest first). `child_blockers` is the batch
  blocker map (`Store.BlockerCounts` in Go), so a sub-task can be marked as
  still waiting on something without a query per child.

  ## No caller yet

  Nothing calls `render/1`: the TUI session launcher arrives in a later phase
  of the port. It is a pure function over values, fully covered by tests, with
  no runtime side effects, so carrying it unused costs nothing.

  ## Local time

  Dates and log stamps render in *local* time, because Go's do (`.Local()`
  before `Format`). The zone is the machine's, read through `Tend.LocalTime`,
  so `render/1` is pure in its arguments but reads that zone, exactly as its
  Go counterpart does.

  ## Divergences from Go

  Go's timestamps are `time.Time`, whose zero value still formats, and its
  `State` is a string, whose zero value still has a label. Here both are nil,
  and nil renders as *nothing* rather than as Go's zero: a missing state is a
  blank state, a missing log stamp is a blank stamp, and a missing date drops
  its line the way Go drops a zero one. `render/1` never raises over a value
  the struct's own defaults allow.

  A `DateTime` before roughly 1970 raises out of `Tend.LocalTime.to_naive/1`,
  where Go would format it; this is `Tend.LocalTime`'s behaviour, shared with
  `Tend.Task.LogEntry` and `Tend.Task.Event`, and no task carries such a date.

  Titles print through `Tend.Error.quote_go/1`, this port of Go's `%q`. It
  agrees with `strconv.Quote` on every rule but one: whether a code point is
  *printable* is read from the BEAM's Unicode tables, a newer edition than the
  one Go's are built from, so the 5,812 code points the two editions disagree
  on print as themselves here where Go still writes `\\uNNNN`. See
  `Tend.Error.quote_go/1`, which documents the gap and carries the test that
  measures it; no title a user types falls in it.
  """

  alias Tend.Error
  alias Tend.LocalTime
  alias Tend.Task
  alias Tend.Task.BlockerCount
  alias Tend.Task.LogEntry
  alias Tend.Task.Priority
  alias Tend.Task.Project
  alias Tend.Task.State

  @typedoc "A brief, field for field Go's `task.SessionBrief`."
  @type t :: %__MODULE__{
          task: Task.t(),
          project: Project.t(),
          tags: [String.t()],
          parent: Task.t() | nil,
          children: [Task.t()],
          child_blockers: %{optional(integer()) => BlockerCount.t()},
          blockers: [Task.t()],
          blocking: [Task.t()],
          log: [LogEntry.t()]
        }

  defstruct task: %Task{},
            project: %Project{},
            tags: [],
            # The tasks this one waits on, open or done, and the tasks waiting
            # on it. Go's nil slices and nil map are the empty ones here.
            parent: nil,
            children: [],
            child_blockers: %{},
            blockers: [],
            blocking: [],
            log: []

  # How many log entries the brief carries. The log is newest first, so the cap
  # keeps the recent recaps -- the ones that say where the work stands -- and
  # drops the deep history, which a session that needs it can still read over
  # MCP.
  @log_limit 10

  @doc """
  The cap on how many log entries `render/1` carries, Go's `briefLogLimit`.
  """
  @spec log_limit() :: pos_integer()
  def log_limit, do: @log_limit

  @doc """
  Renders a brief as the system prompt block an interactive session gets
  (`agent.LaunchOpts.AppendSystemPrompt` in Go).

  It says what the session is bound to, that the details are a launch-time
  snapshot the tend MCP tools can refresh and change, and then lays the task
  out as markdown: a facts list, the parent, the sub-tasks with their states,
  both dependency directions, the body verbatim, and the recent log. Sections
  with nothing in them are left out rather than rendered empty, so a bare task
  reads as a short block.
  """
  @spec render(t()) :: String.t()
  def render(%__MODULE__{} = brief) do
    IO.iodata_to_binary([
      preamble(brief.task),
      facts(brief),
      children_section(brief),
      blockers_section(brief),
      blocking_section(brief),
      description(brief.task),
      log_section(brief)
    ])
  end

  defp preamble(%Task{} = task) do
    [
      "This Claude Code session was launched from tend, a terminal task tracker, ",
      "and is bound to tend task ##{task.id}: #{quoted(task.title)}. ",
      "The task is laid out below; treat it as the context for this session and do not ask the user to restate it. ",
      "The details are a snapshot from when the session started. The tend MCP tools (mcp__tend__get_current_task, mcp__tend__list_subtasks, ",
      "mcp__tend__append_task_body, mcp__tend__set_task_state, mcp__tend__create_subtask, ...) read and change the live task; ",
      "record progress and decisions by appending to the task body, since log entries are the user's.\n\n"
    ]
  end

  defp facts(%__MODULE__{task: task} = brief) do
    [
      "## Task ##{task.id}: #{task.title}\n\n",
      "- State: #{state_label(task.state)}\n",
      project_line(brief.project),
      priority_line(task.priority),
      text_line("Due", task.due),
      text_line("Snoozed until", task.snooze_until),
      tags_line(brief.tags),
      date_line("Created", task.created_at),
      date_line("Updated", task.updated_at),
      date_line("Completed", task.completed_at),
      parent_line(brief)
    ]
  end

  defp project_line(%Project{name: ""}), do: []
  defp project_line(%Project{name: name, cwd: ""}), do: "- Project: #{name}\n"

  defp project_line(%Project{name: name, cwd: cwd}),
    do: "- Project: #{name} (default cwd #{cwd})\n"

  defp priority_line(priority) do
    case Priority.letter(priority) do
      "" -> []
      letter -> "- Priority: #{letter}\n"
    end
  end

  # Go's optional strings are *string, and it renders neither a nil pointer nor
  # an empty one.
  defp text_line(_label, nil), do: []
  defp text_line(_label, ""), do: []
  defp text_line(label, value), do: "- #{label}: #{value}\n"

  defp tags_line([]), do: []
  defp tags_line(tags), do: "- Tags: #{Enum.join(tags, ", ")}\n"

  # Go skips a zero timestamp; nil is the zero here.
  defp date_line(_label, nil), do: []

  defp date_line(label, %DateTime{} = at),
    do: "- #{label}: #{Calendar.strftime(LocalTime.to_naive(at), "%Y-%m-%d")}\n"

  # A parent id with no loaded parent still names the parent by id, so a failed
  # parent lookup degrades to less detail rather than a wrong claim.
  defp parent_line(%__MODULE__{parent: %Task{} = parent}),
    do: "- Parent task: #{ref(parent)}\n"

  defp parent_line(%__MODULE__{task: %Task{parent_id: nil}}),
    do: "- Parent task: none (a top-level task)\n"

  defp parent_line(%__MODULE__{task: %Task{parent_id: parent_id}}),
    do: "- Parent task: ##{parent_id}\n"

  defp children_section(%__MODULE__{children: []}), do: []

  defp children_section(%__MODULE__{children: children, child_blockers: child_blockers}) do
    done = Enum.count(children, &(&1.state == :done))

    [
      "\n### Sub-tasks (#{done} of #{length(children)} done)\n\n",
      Enum.map(children, &["- #{box(&1)} #{ref(&1)}", waiting_on(child_blockers, &1), "\n"])
    ]
  end

  defp waiting_on(child_blockers, %Task{id: id}) do
    case child_blockers do
      %{^id => %BlockerCount{open: open} = count} ->
        if BlockerCount.blocked?(count), do: ", waiting on #{open} open task(s)", else: []

      _no_entry ->
        []
    end
  end

  defp blockers_section(%__MODULE__{blockers: []}), do: []

  defp blockers_section(%__MODULE__{blockers: blockers}) do
    [
      "\n### Blocked by (#{length(Task.open_blockers(blockers))} open)\n\n",
      Enum.map(blockers, &"- #{box(&1)} #{ref(&1)}\n")
    ]
  end

  defp blocking_section(%__MODULE__{blocking: []}), do: []

  defp blocking_section(%__MODULE__{blocking: blocking}),
    do: ["\n### Blocks\n\n", Enum.map(blocking, &"- #{ref(&1)}\n")]

  defp description(%Task{body_md: body_md}) do
    case String.trim(body_md) do
      "" -> "\n### Description\n\n(no description)\n"
      body -> "\n### Description\n\n#{body}\n"
    end
  end

  defp log_section(%__MODULE__{log: []}), do: []

  defp log_section(%__MODULE__{log: log}) do
    total = length(log)

    {heading, shown} =
      if total > @log_limit do
        {"\n### Log (newest first; the #{@log_limit} most recent of #{total} entries)\n\n",
         Enum.take(log, @log_limit)}
      else
        {"\n### Log (newest first)\n\n", log}
      end

    [heading, Enum.map(shown, &"- #{stamp(&1.created_at)}: #{String.trim(&1.body)}\n")]
  end

  # `%LogEntry{}`'s own default `created_at` is nil, so an entry built by hand
  # rather than read from the store has no instant to stamp. Blank the stamp
  # and keep the entry, the way `state_label/1` blanks a missing state: losing
  # the note's text -- or the whole brief -- over a missing timestamp is worse
  # than a line that reads `- : note`.
  defp stamp(nil), do: ""

  defp stamp(%DateTime{} = at),
    do: Calendar.strftime(LocalTime.to_naive(at), "%Y-%m-%d %H:%M")

  defp box(%Task{state: :done}), do: "[x]"
  defp box(%Task{}), do: "[ ]"

  # A related task, rendered `#12 "title" (state)`.
  defp ref(%Task{} = task), do: "##{task.id} #{quoted(task.title)} (#{state_label(task.state)})"

  # Go's zero State is the empty string, and Label leaves it alone; a task that
  # never came from the store renders a blank state rather than blowing up the
  # whole brief. See "Divergences from Go" above.
  defp state_label(nil), do: ""
  defp state_label(state), do: State.label(state)

  # Go prints titles with %q, and `Tend.Error.quote_go/1` is this port's
  # `strconv.Quote` -- the same verb, reached from the one place that carries
  # it, rather than a second copy that would drift from it. Its one residual
  # divergence, over which code points count as printable, is recorded under
  # "Divergences from Go" above.
  defp quoted(title), do: Error.quote_go(title)
end
