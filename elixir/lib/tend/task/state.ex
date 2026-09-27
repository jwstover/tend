defmodule Tend.Task.State do
  @moduledoc """
  The workflow state of a task -- a port of `task.State` in
  `internal/task/task.go`.

  The canonical set lives in the `states` table; these atoms mirror the seed
  rows. Go models a state as a `string` newtype, so its stored form and its
  in-memory form are the same value. Here a state is an **atom**, and the
  stored string is what `format/1` returns and `parse/1` accepts. Round
  tripping is exact: `format/1` never produces a name `parse/1` rejects, and
  the atom set is closed, so a value the database does not recognise cannot
  slip into a struct field.

  Any code that reads or writes the `tasks.state` column -- the store port --
  must go through `parse/1` and `format/1`.
  """

  @typedoc "One of the seeded workflow states."
  @type t :: :inbox | :todo | :doing | :review | :blocked | :done | :someday

  # Declaration order matches the Go constants.
  @states [
    :inbox,
    :todo,
    :doing,
    # finished on our side, waiting on someone else's eyes
    :review,
    :blocked,
    :done,
    :someday
  ]

  @by_name Map.new(@states, &{Atom.to_string(&1), &1})

  @doc """
  Every seeded workflow state, in the order the Go constants declare them.
  """
  @spec all() :: [t()]
  def all, do: @states

  @doc """
  Whether `state` is one of the seeded workflow states.

  The counterpart of Go's `State.Valid`. Accepts any term, so it can guard a
  value that came from outside.
  """
  @spec valid?(term()) :: boolean()
  def valid?(state), do: state in @states

  @doc """
  The state's stored name, the string the `tasks.state` column holds.
  """
  @spec format(t()) :: String.t()
  def format(state) when state in @states, do: Atom.to_string(state)

  @doc """
  The state named by `name`, or `:error`.

  Rejects everything that is not a stored name exactly: the empty string,
  a differently cased name (`"DONE"`), a state that does not exist
  (`"archived"`), and the display label `"in review"` -- labels are for
  reading, not for storing.
  """
  @spec parse(String.t()) :: {:ok, t()} | :error
  def parse(name) when is_binary(name), do: Map.fetch(@by_name, name)

  @doc """
  The state's name as the UI shows it: the stored name, except where a bare
  word would read oddly as a heading.
  """
  @spec label(t()) :: String.t()
  def label(:review), do: "in review"
  def label(state) when state in @states, do: format(state)
end
