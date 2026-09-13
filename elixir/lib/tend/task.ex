defmodule Tend.Task do
  @moduledoc """
  The domain representation of a row in the `tasks` table, and the rules that
  guard what goes into one.

  A port of `internal/task/task.go`. Like it, this module and everything under
  `Tend.Task.*` has zero I/O and depends on nothing else in the project: it is
  values and pure functions, and the store is what reads and writes them.

  `due` and `snooze_until` stay as ISO 8601 date strings (`YYYY-MM-DD`); the
  database compares them lexically and v1 has no date arithmetic to justify
  parsing them into `Date`s.

  Tags are deliberately absent: the list view needs them for every visible
  row, and a per-row query would be N+1. They load as a batch map instead
  (`Store.TagsByTask` in Go), the same idiom child counts and session statuses
  use.

  Struct fields default to their Go zero values, except the two timestamps
  Go cannot leave unset -- `created_at` and `updated_at` -- which are `nil`
  here, since `time.Time`'s zero has no Elixir counterpart and an unsaved task
  has no honest value to put there.
  """

  alias Tend.Task.State

  @typedoc """
  A task. Field for field the Go struct, with `state` an atom (see
  `Tend.Task.State`) and the timestamps `DateTime`s in UTC.
  """
  @type t :: %__MODULE__{
          id: integer(),
          title: String.t(),
          body_md: String.t(),
          state: State.t() | nil,
          parent_id: integer() | nil,
          project_id: integer(),
          priority: Tend.Task.Priority.t() | nil,
          due: String.t() | nil,
          snooze_until: String.t() | nil,
          created_at: DateTime.t() | nil,
          updated_at: DateTime.t() | nil,
          completed_at: DateTime.t() | nil
        }

  defstruct id: 0,
            title: "",
            body_md: "",
            state: nil,
            parent_id: nil,
            project_id: 0,
            priority: nil,
            due: nil,
            snooze_until: nil,
            created_at: nil,
            updated_at: nil,
            completed_at: nil

  @date_format "YYYY-MM-DD"

  @doc """
  Parses and canonicalizes an ISO 8601 date (`#{@date_format}`), the only date
  format the schema stores.

  Surrounding whitespace is trimmed. Anything else -- another format, a word,
  a signed year, a day that does not exist -- is
  `{:error, {:invalid_date, original}}`, which `Tend.Error.message/1` renders
  the way the Go error does. The reason carries the string as given, untrimmed,
  because Go's does.
  """
  @spec normalize_date(String.t()) :: {:ok, String.t()} | {:error, {:invalid_date, String.t()}}
  def normalize_date(s) when is_binary(s) do
    trimmed = String.trim(s)

    with true <- four_digit_year?(trimmed),
         {:ok, date} <- Date.from_iso8601(trimmed) do
      {:ok, Date.to_iso8601(date)}
    else
      _not_a_date -> {:error, {:invalid_date, s}}
    end
  end

  # Go reads the year with time.Parse("2006-01-02", ...), which takes exactly
  # four ASCII digits and nothing else. Date.from_iso8601/1 additionally
  # accepts ISO 8601's extended-form sign, so on its own it would quietly
  # rewrite "+2026-09-13" to "2026-09-13" and accept "-2026-09-13" as a
  # negative year -- a value that would reach tasks.due, which the database
  # compares lexically, and sort before every real date.
  defp four_digit_year?(<<a, b, c, d, ?-, _rest::binary>>)
       when a in ?0..?9 and b in ?0..?9 and c in ?0..?9 and d in ?0..?9,
       do: true

  defp four_digit_year?(_s), do: false

  @doc """
  Trims surrounding whitespace and rejects blank titles.

  A bare title is the only thing capture requires, so this is the entire
  validation surface for `tend add`.
  """
  @spec normalize_title(String.t()) :: {:ok, String.t()} | {:error, :empty_title}
  def normalize_title(s) when is_binary(s) do
    case String.trim(s) do
      "" -> {:error, :empty_title}
      title -> {:ok, title}
    end
  end

  @doc """
  Narrows a task's dependencies to the ones not yet done -- the tasks actually
  holding it up.

  Order is preserved. Go returns a nil slice when nothing is open; the empty
  list is the same thing here.
  """
  @spec open_blockers([t()]) :: [t()]
  def open_blockers(blockers) when is_list(blockers) do
    Enum.reject(blockers, &(&1.state == :done))
  end
end
