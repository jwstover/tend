defmodule Tend.Store.Row do
  @moduledoc """
  Turns a SQLite result row into a domain value.

  A port of the mappers and null helpers at the bottom of
  `internal/store/store.go` (`toDomain`, `toDomainSlice`, `sessionToDomain`,
  `parseTime`, `parseStatusTime`, and the formatting half of
  `statusUpdatedAtParam`), plus `workflowToDomain` from the bottom of
  `internal/store/workflows.go`. Every part of the store port reads rows
  through here, so the timestamp rules live in exactly one place.

  Rows arrive as plain lists, in the column order the query's `SELECT` names
  them. That order is the contract: the sqlc-generated queries spell every
  column out rather than using `*`, and `Tend.Store` copies them verbatim, so
  `to_task/1`'s twelve-element pattern is the same positional agreement the Go
  `row.Scan(&i.ID, &i.Title, ...)` calls make.

  ## The null helpers are gone, on purpose

  Go needs four of them -- `toNullString`/`nullString`,
  `toNullInt64`/`nullInt64` -- because `database/sql` models a nullable column
  as a `sql.NullString`/`sql.NullInt64` struct with a `Valid` flag while the
  domain types use `*string`/`*int64`. Both directions are pure adapter code.

  exqlite has no such wrapper: it reads SQL NULL as `nil` and binds `nil` as
  SQL NULL, which is already the domain representation. So all four helpers
  port to the identity function and are deliberately *not* written -- a
  `def null_string(v), do: v` would be four chances to typo something that
  cannot otherwise go wrong. Where Go calls `nullInt64(row.ParentID)` this
  module simply uses the value, and where Go calls `toNullInt64(p)`
  `Tend.Store` simply binds it.

  ## Timestamps

  Two layouts, for the same reason Go has two constants:

    * `"2006-01-02 15:04:05"` -- what `datetime('now')` writes, and what every
      timestamp column but one holds.
    * `"2006-01-02 15:04:05.000"` -- what `strftime('%Y-%m-%d %H:%M:%f', 'now')`
      writes into `agent_sessions.status_updated_at`, whose millisecond
      precision is load-bearing: that column doubles as the freshness token
      the session poller's compare-and-swap compares against, and whole-second
      resolution is provably too coarse for it (see `statusTimeLayout` in
      `store.go`).

  Both parsers are as strict as Go's `time.Parse` is with those layouts, which
  is stricter than `NaiveDateTime.from_iso8601/1`:

    * the separator is a space, never `T`;
    * every field is zero-padded to its exact width;
    * nothing may follow the seconds but a fractional part, and no timezone
      suffix is accepted;
    * `parse_time/1` accepts an *optional* fractional second even though its
      layout has none, because Go's parser does ("the input may contain a
      fractional second field immediately after the seconds field, even if the
      layout does not signify its presence");
    * `parse_status_time/1` requires a fractional second of exactly three
      digits, because Go's `.000` is a fixed-width chunk -- two digits or four
      are both errors there.

  The fractional second is kept, not discarded. It is what makes
  `parse_status_time/1` worth having: the poller's CAS reformats the value it
  read -- with `format_status_time/1`, the exact inverse -- and compares the
  text, so a parser that rounded to the second would make every CAS
  spuriously succeed.

  The result is a `DateTime` in UTC. Go's `time.Parse` with a zone-less layout
  yields a `time.Time` in UTC too, so the two agree without either side
  guessing a local zone.

  > #### One divergence from Go {: .info}
  >
  > Go's `time.Time` counts nanoseconds and its parser takes up to nine
  > fractional digits; a `DateTime` counts microseconds. A fractional second
  > longer than six digits is therefore truncated rather than refused. Neither
  > layout the schema writes produces more than three, so nothing in the port
  > can reach the gap.
  """

  alias Tend.Task
  alias Tend.Task.Project
  alias Tend.Task.Session
  alias Tend.Task.SessionStatus
  alias Tend.Task.State
  alias Tend.Task.TaskSession
  alias Tend.Workflow

  # Anchored, and deliberately not built on NaiveDateTime.from_iso8601/1: that
  # function accepts a `T` separator, a trailing offset and a fractional
  # second of any length, none of which Go's layouts do.
  @time ~r/\A(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?\z/
  @status_time ~r/\A(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2})\.(\d{3})\z/

  @typedoc """
  A reason a mapper here can fail with.

  Both stand for a column whose stored value is not one the schema can
  produce, so both mean corruption rather than a user mistake.
  `to_workflow/2` can only raise the first: `workflows` has no enumerated
  column.
  """
  @type error ::
          {:invalid_timestamp, String.t(), String.t()}
          | {:unknown_state, String.t()}

  @doc """
  Maps one `tasks` row -- the twelve columns the task queries select, in order
  -- into a `Tend.Task`.

  The counterpart of Go's `toDomain`.
  """
  @spec to_task(list()) :: {:ok, Task.t()} | {:error, error()}
  def to_task([
        id,
        title,
        body_md,
        state,
        parent_id,
        priority,
        due,
        snooze_until,
        created_at,
        updated_at,
        completed_at,
        project_id
      ]) do
    with {:ok, created} <- stamp(created_at, "task #{id} created_at"),
         {:ok, updated} <- stamp(updated_at, "task #{id} updated_at"),
         {:ok, completed} <- optional_stamp(completed_at, "task #{id} completed_at"),
         {:ok, parsed_state} <- state(state) do
      {:ok,
       %Task{
         id: id,
         title: title,
         body_md: body_md,
         state: parsed_state,
         parent_id: parent_id,
         project_id: project_id,
         priority: priority,
         due: due,
         snooze_until: snooze_until,
         created_at: created,
         updated_at: updated,
         completed_at: completed
       }}
    end
  end

  @doc """
  Maps every row of a task query, stopping at the first that will not map.

  The counterpart of Go's `toDomainSlice`, including its all-or-nothing
  behaviour: one unreadable row fails the whole read rather than being
  silently dropped from a list the caller will treat as complete.
  """
  @spec to_tasks([list()]) :: {:ok, [Task.t()]} | {:error, error()}
  def to_tasks(rows) when is_list(rows), do: all(rows, &to_task/1)

  @doc """
  Maps one `workflows` row -- the five columns every workflow query selects, in
  order -- into a `Tend.Workflow`, with `step_count` supplied separately.

  The counterpart of Go's `workflowToDomain`, whose second argument is the
  count too. The count is not a column of `workflows`: only `ListWorkflows`
  knows it, from the `LEFT JOIN` that groups `workflow_steps` by workflow, and
  every other caller passes `0` -- a `Tend.Workflow` whose `step_count` is zero
  means "nobody counted", exactly as the Go struct's does.
  """
  @spec to_workflow(list(), integer()) :: {:ok, Workflow.t()} | {:error, error()}
  def to_workflow([id, name, description, created_at, updated_at], step_count)
      when is_integer(step_count) do
    with {:ok, created} <- stamp(created_at, "workflow #{id} created_at"),
         {:ok, updated} <- stamp(updated_at, "workflow #{id} updated_at") do
      {:ok,
       %Workflow{
         id: id,
         name: name,
         description: description,
         created_at: created,
         updated_at: updated,
         step_count: step_count
       }}
    end
  end

  @doc """
  Maps every row of the `ListWorkflows` statement, stopping at the first that
  will not map.

  Those rows carry a sixth column, the joined step count, which Go splits back
  off into `workflowToDomain`'s second argument; this does the same split. The
  all-or-nothing behaviour is `to_tasks/1`'s, for the same reason.
  """
  @spec to_workflows([list()]) :: {:ok, [Workflow.t()]} | {:error, error()}
  def to_workflows(rows) when is_list(rows) do
    all(rows, fn [id, name, description, created_at, updated_at, step_count] ->
      to_workflow([id, name, description, created_at, updated_at], step_count)
    end)
  end

  @doc """
  Maps one `projects` row -- the seven columns every project query selects, in
  order -- into a `Tend.Task.Project`, with `live_count` supplied separately.

  The counterpart of Go's `projectToDomain`. `live_count` is not a column of
  `projects`: only `@list_projects`' `LEFT JOIN` knows it, and every other
  caller passes `0` -- the same "nobody counted" zero `to_workflow/2`'s
  `step_count` uses.
  """
  @spec to_project(list(), integer()) :: {:ok, Project.t()} | {:error, error()}
  def to_project([id, name, sort_order, archived_at, created_at, updated_at, cwd], live_count)
      when is_integer(live_count) do
    with {:ok, created} <- stamp(created_at, "project #{id} created_at"),
         {:ok, updated} <- stamp(updated_at, "project #{id} updated_at"),
         {:ok, archived} <- optional_stamp(archived_at, "project #{id} archived_at") do
      {:ok,
       %Project{
         id: id,
         name: name,
         sort_order: sort_order,
         archived_at: archived,
         created_at: created,
         updated_at: updated,
         cwd: cwd,
         live_count: live_count
       }}
    end
  end

  @doc """
  Maps every row of `@list_projects`, stopping at the first that will not map.

  Those rows carry an eighth column, the joined live top-level task count,
  which Go splits back off into `projectToDomain`'s second argument; this does
  the same split. The all-or-nothing behaviour is `to_tasks/1`'s, for the same
  reason.
  """
  @spec to_projects([list()]) :: {:ok, [Project.t()]} | {:error, error()}
  def to_projects(rows) when is_list(rows) do
    all(rows, fn [id, name, sort_order, archived_at, created_at, updated_at, cwd, live_count] ->
      to_project([id, name, sort_order, archived_at, created_at, updated_at, cwd], live_count)
    end)
  end

  # The shared body of to_tasks/1, to_workflows/1, to_sessions/1 and
  # to_projects/1: map every row, stop at the first that will not map, keep
  # the query's order.
  defp all(rows, mapper) do
    rows
    |> Enum.reduce_while({:ok, []}, fn row, {:ok, acc} ->
      case mapper.(row) do
        {:ok, value} -> {:cont, {:ok, [value | acc]}}
        {:error, reason} -> {:halt, {:error, reason}}
      end
    end)
    |> case do
      {:ok, values} -> {:ok, Enum.reverse(values)}
      {:error, reason} -> {:error, reason}
    end
  end

  @doc """
  Maps one `agent_sessions` row -- the twelve columns the session queries
  select, in order -- into a `Tend.Task.Session`.

  The counterpart of Go's `sessionToDomain`, including its split treatment of
  the three timestamps. `started_at` and `last_active_at` are `NOT NULL`, and
  an unreadable one fails the whole read; `status_updated_at` is nullable and
  degrades to `nil` instead, for the reason Go's comment gives -- it is a
  display timestamp on a cached observation, not worth refusing a session
  over.

  `status` goes through `Tend.Task.SessionStatus.parse/1`, which is total, so
  a value tend does not recognize reads as `:unknown` rather than erroring --
  the same latitude Go's `task.SessionStatus(row.Status)` takes.
  """
  @spec to_session(list()) :: {:ok, Session.t()} | {:error, error()}
  def to_session([
        id,
        task_id,
        external_id,
        cwd,
        label,
        started_at,
        last_active_at,
        tmux_session,
        needs_recap,
        status,
        status_updated_at,
        workflow_step_run_id
      ]) do
    with {:ok, started} <- stamp(started_at, "session #{id} started_at"),
         {:ok, last_active} <- stamp(last_active_at, "session #{id} last_active_at") do
      {:ok,
       %Session{
         id: id,
         task_id: task_id,
         external_id: external_id,
         cwd: cwd,
         label: label,
         tmux_session: tmux_session,
         needs_recap: needs_recap != 0,
         status: SessionStatus.parse(status),
         status_updated_at: lenient_status_time(status_updated_at),
         started_at: started,
         last_active_at: last_active,
         step_run_id: workflow_step_run_id
       }}
    end
  end

  @doc """
  Maps every row of a session query, stopping at the first that will not map.

  Go writes the loop out at each of its four call sites; the port keeps one,
  with the same all-or-nothing behaviour `to_tasks/1` has.
  """
  @spec to_sessions([list()]) :: {:ok, [Session.t()]} | {:error, error()}
  def to_sessions(rows) when is_list(rows), do: all(rows, &to_session/1)

  @doc """
  Maps one `ListSessionsForProject` row -- a session's twelve columns followed
  by the owning task's title and state -- into a `Tend.Task.TaskSession`.

  Go reads the state as `task.State(row.TaskState)` and lets an unrecognized
  name ride along; `state/1` here is closed for the same reason `to_task/1`'s
  is, so such a row is `{:error, {:unknown_state, name}}`. Only a write that
  went around the `states` foreign key can produce one.
  """
  @spec to_task_session(list()) :: {:ok, TaskSession.t()} | {:error, error()}
  def to_task_session(row) when is_list(row) and length(row) == 14 do
    {session_columns, [task_title, task_state]} = Enum.split(row, 12)

    with {:ok, session} <- to_session(session_columns),
         {:ok, parsed_state} <- state(task_state) do
      {:ok, %TaskSession{session: session, task_title: task_title, task_state: parsed_state}}
    end
  end

  @doc """
  Maps every row of `ListSessionsForProject`, stopping at the first that will
  not map.
  """
  @spec to_task_sessions([list()]) :: {:ok, [TaskSession.t()]} | {:error, error()}
  def to_task_sessions(rows) when is_list(rows), do: all(rows, &to_task_session/1)

  @doc """
  Parses a timestamp written by `datetime('now')`.

  The counterpart of Go's `parseTime`. `:error` rather than a reason term: the
  caller knows which column it was reading and builds the message, the way
  `toDomain` wraps `parseTime`'s error with the field name.
  """
  @spec parse_time(term()) :: {:ok, DateTime.t()} | :error
  def parse_time(value) when is_binary(value), do: parse(@time, value)
  def parse_time(_value), do: :error

  @doc """
  Parses `agent_sessions.status_updated_at`, written by
  `strftime('%Y-%m-%d %H:%M:%f', 'now')` at millisecond precision.

  The counterpart of Go's `parseStatusTime`, and the read half of the poller's
  compare-and-swap token: `to_session/1` parses the column with this, and
  `format_status_time/1` renders the value back out for the CAS to compare
  against.
  """
  @spec parse_status_time(term()) :: {:ok, DateTime.t()} | :error
  def parse_status_time(value) when is_binary(value), do: parse(@status_time, value)
  def parse_status_time(_value), do: :error

  @doc """
  Renders a `DateTime` back into the exact text
  `strftime('%Y-%m-%d %H:%M:%f', 'now')` would have written for that instant.

  The formatting half of Go's `statusUpdatedAtParam`
  (`t.UTC().Format(statusTimeLayout)`), and the reason it has to be exact
  rather than merely correct: the CAS compares this string against the column
  with SQL `IS`, so a fourth digit, a missing zero pad or a rounded
  millisecond does not read as a *different* time, it reads as *no match at
  all*. Two racing writers would then both lose instead of one winning, and
  the poller could never write again.

  Sub-millisecond precision is truncated rather than rounded, which is what
  Go's `.000` layout chunk does. `Calendar.strftime/2`'s own `%f` is
  deliberately avoided: its width follows the `DateTime`'s precision rather
  than the layout's, so a whole-second `DateTime` would render the wrong
  number of digits.
  """
  @spec format_status_time(DateTime.t()) :: String.t()
  def format_status_time(%DateTime{} = at) do
    utc = DateTime.shift_zone!(at, "Etc/UTC")
    {microsecond, _precision} = utc.microsecond
    millis = microsecond |> div(1000) |> Integer.to_string() |> String.pad_leading(3, "0")

    Calendar.strftime(utc, "%Y-%m-%d %H:%M:%S") <> "." <> millis
  end

  @doc """
  Renders a `DateTime` in the layout `datetime('now')` writes: whole seconds,
  no fractional part.

  The formatting half of `parse_time/1` -- Go has no named function for it
  either, just the inline `time.Now().UTC().Format(sqliteTimeLayout)` that
  `Store.SetProjectArchived` uses to stamp `archived_at`.
  """
  @spec format_time(DateTime.t()) :: String.t()
  def format_time(%DateTime{} = at) do
    at |> DateTime.shift_zone!("Etc/UTC") |> Calendar.strftime("%Y-%m-%d %H:%M:%S")
  end

  # Go's sessionToDomain discards parseStatusTime's error and keeps the zero
  # time; nil is this port's zero time.
  defp lenient_status_time(value) do
    case parse_status_time(value) do
      {:ok, at} -> at
      :error -> nil
    end
  end

  defp parse(pattern, value) do
    case Regex.run(pattern, value, capture: :all_but_first) do
      nil ->
        :error

      [year, month, day, hour, minute, second | fraction] ->
        [y, mo, d, h, mi, s] =
          Enum.map([year, month, day, hour, minute, second], &String.to_integer/1)

        # NaiveDateTime.new/7 is what rejects a day the month does not have,
        # and an hour, minute or second out of range -- all of which Go's
        # time.Parse rejects too, as a "day out of range" style error.
        case NaiveDateTime.new(y, mo, d, h, mi, s, microsecond(fraction)) do
          {:ok, naive} -> {:ok, DateTime.from_naive!(naive, "Etc/UTC")}
          {:error, _reason} -> :error
        end
    end
  end

  # An optional group that did not participate comes back as "", which is the
  # "no fractional second at all" case and Go's zero nanoseconds.
  defp microsecond([]), do: {0, 0}
  defp microsecond([""]), do: {0, 0}

  defp microsecond([digits]) do
    precision = min(byte_size(digits), 6)

    value =
      digits
      |> binary_part(0, precision)
      |> String.pad_trailing(6, "0")
      |> String.to_integer()

    {value, precision}
  end

  # `context` is the "task 7 created_at" / "workflow 3 updated_at" /
  # "session 4 started_at" label Go's toDomain, workflowToDomain and
  # sessionToDomain each build with fmt.Errorf before wrapping parseTime's
  # error.
  defp stamp(value, context) do
    case parse_time(value) do
      {:ok, at} -> {:ok, at}
      :error -> {:error, {:invalid_timestamp, context, to_string(value)}}
    end
  end

  defp optional_stamp(nil, _context), do: {:ok, nil}
  defp optional_stamp(value, context), do: stamp(value, context)

  # Go writes task.State(row.State) and lets an unrecognised name ride along
  # as an invalid State. The atom set here is closed on purpose (see
  # Tend.Task.State), so the same row is an error instead. Only a write that
  # went around the states foreign key can produce one.
  defp state(name) when is_binary(name) do
    case State.parse(name) do
      {:ok, state} -> {:ok, state}
      :error -> {:error, {:unknown_state, name}}
    end
  end

  defp state(name), do: {:error, {:unknown_state, to_string(name)}}
end
