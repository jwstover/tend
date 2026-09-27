defmodule Tend.Task.Priority do
  @moduledoc """
  A task's priority -- a port of the priority constants and `PriorityLetter`
  in `internal/task/task.go`.

  Stored values are `1` (highest, `"A"`) through `4` (`"D"`). `nil` means
  unprioritized and sorts last, which is how the column is stored: NULL, not
  a fifth level.

  The stored integer is what a `Tend.Task` carries; the letter is the display
  form, and `letter/1` and `parse_letter/1` are exact inverses over the four
  valid values.
  """

  @highest 1
  @lowest 4

  @typedoc "A stored priority: 1 (highest) through 4 (lowest)."
  @type t :: 1..4

  @letters for p <- @highest..@lowest, into: %{}, do: {<<?A + p - 1>>, p}

  @doc "The highest (most urgent) stored priority, `1`."
  @spec highest() :: t()
  def highest, do: @highest

  @doc "The lowest stored priority, `4`."
  @spec lowest() :: t()
  def lowest, do: @lowest

  @doc """
  Every stored priority, highest first.
  """
  @spec all() :: [t()]
  def all, do: Enum.to_list(@highest..@lowest)

  @doc """
  Whether `priority` is a stored priority. `nil` -- unprioritized -- is not:
  it is the absence of one.
  """
  @spec valid?(term()) :: boolean()
  def valid?(priority) when is_integer(priority),
    do: priority >= @highest and priority <= @lowest

  def valid?(_priority), do: false

  @doc """
  `"A"`..`"D"` for stored values 1..4, `""` for `nil` and for anything out of
  range.

  The counterpart of Go's `PriorityLetter`, including its habit of rendering
  an unprioritized task and a corrupt one the same way: the display has
  nowhere to put an error, and an empty cell is the honest answer for both.
  """
  @spec letter(term()) :: String.t()
  def letter(priority) do
    if valid?(priority), do: <<?A + priority - 1>>, else: ""
  end

  @doc """
  The stored value `letter` stands for, or `:error`.

  The inverse of `letter/1`, and the only way back from the display form.
  Nothing calls it yet -- the CLI port is where a user types a priority --
  but it is what makes the round trip testable in both directions.
  """
  @spec parse_letter(String.t()) :: {:ok, t()} | :error
  def parse_letter(letter) when is_binary(letter), do: Map.fetch(@letters, letter)
end
