defmodule Tend.Store.Row do
  @moduledoc """
  Turns a SQLite result row into a domain value.

  A port of the mappers and null helpers at the bottom of
  `internal/store/store.go` (`toDomain`, `toDomainSlice`, `parseTime`,
  `parseStatusTime`). Every part of the store port reads rows through here, so
  the timestamp rules live in exactly one place.

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
  read and compares the text, so a parser that rounded to the second would
  make every CAS spuriously succeed.

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
  alias Tend.Task.State

  # Anchored, and deliberately not built on NaiveDateTime.from_iso8601/1: that
  # function accepts a `T` separator, a trailing offset and a fractional
  # second of any length, none of which Go's layouts do.
  @time ~r/\A(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?\z/
  @status_time ~r/\A(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2})\.(\d{3})\z/

  @typedoc """
  A reason `to_task/1` can fail with.

  Both stand for a column whose stored value is not one the schema can
  produce, so both mean corruption rather than a user mistake.
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
    with {:ok, created} <- stamp(created_at, id, "created_at"),
         {:ok, updated} <- stamp(updated_at, id, "updated_at"),
         {:ok, completed} <- optional_stamp(completed_at, id, "completed_at"),
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
  def to_tasks(rows) when is_list(rows) do
    rows
    |> Enum.reduce_while({:ok, []}, fn row, {:ok, acc} ->
      case to_task(row) do
        {:ok, task} -> {:cont, {:ok, [task | acc]}}
        {:error, reason} -> {:halt, {:error, reason}}
      end
    end)
    |> case do
      {:ok, tasks} -> {:ok, Enum.reverse(tasks)}
      {:error, reason} -> {:error, reason}
    end
  end

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

  The counterpart of Go's `parseStatusTime`. Nothing in the task surface reads
  that column -- the session functions arrive in a later part of the store
  port -- so it has no caller here yet; it lives with its sibling because the
  two layouts are one decision, and it is tested directly.
  """
  @spec parse_status_time(term()) :: {:ok, DateTime.t()} | :error
  def parse_status_time(value) when is_binary(value), do: parse(@status_time, value)
  def parse_status_time(_value), do: :error

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

  defp stamp(value, id, column) do
    case parse_time(value) do
      {:ok, at} -> {:ok, at}
      :error -> {:error, {:invalid_timestamp, "task #{id} #{column}", to_string(value)}}
    end
  end

  defp optional_stamp(nil, _id, _column), do: {:ok, nil}
  defp optional_stamp(value, id, column), do: stamp(value, id, column)

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
