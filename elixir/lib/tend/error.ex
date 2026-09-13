defmodule Tend.Error do
  @moduledoc """
  Every error reason the port can return, in one place.

  ## The convention

  The Go tree signals expected failures with package-level sentinel values
  (`var ErrEmptyTitle = errors.New("task title is empty")`) that callers
  compare with `errors.Is`. This port keeps that shape, minus the values:

    * **one Go sentinel becomes exactly one atom**, named by dropping the
      `Err` prefix and underscoring the rest -- `ErrEmptyTitle` is
      `:empty_title`, `ErrProjectNotFound` is `:project_not_found`;
    * a function that can fail returns `{:error, reason}` with that atom as
      the reason, never a string and never an exception;
    * callers match the atom the way Go callers call `errors.Is`;
    * the atom is listed in `@sentinels` below, with the Go error's message
      text reproduced verbatim, so `message/1` can render it exactly as the
      Go binary would.

  The few Go errors built with `fmt.Errorf` rather than `errors.New`
  interpolate a value, so they cannot be a bare atom. They become a tagged
  tuple whose first element follows the same naming rule --
  `{:invalid_date, "06/09/2026"}` -- and `message/1` grows a clause that
  rebuilds the Go format string. See `@type t`.

  ## Extending this

  Later parts of the port add their own reasons. **Extend the lists here
  rather than inventing a name elsewhere**: add the atom to `@sentinels`
  with the Go message text, or, for an interpolating error, add a clause to
  `message/1` and a member to `t/0`. `Tend.ErrorTest` reads the Go sources
  and fails if a sentinel defined there has no atom here, so a missed one is
  a test failure rather than a surprise at runtime.

  Only the errors of `internal/task/task.go`, `internal/task/project.go` and
  `internal/task/session.go` are listed today; the store and template ports
  add theirs.
  """

  # Sentinel atom => the Go error's message, verbatim. Keep this sorted by
  # the Go file the sentinel comes from, then by declaration order.
  @sentinels %{
    # internal/task/task.go
    empty_title: "task title is empty",
    self_dependency: "a task cannot depend on itself",
    dependency_cycle: "dependency would form a cycle",

    # internal/task/project.go
    empty_project_name: "project name is empty",
    protected_project: "the default project cannot be deleted",
    project_not_found: "project not found"
  }

  @typedoc """
  An error reason that stands on its own, one per Go sentinel.
  """
  @type sentinel ::
          :empty_title
          | :self_dependency
          | :dependency_cycle
          | :empty_project_name
          | :protected_project
          | :project_not_found

  @typedoc """
  Any reason a `{:error, reason}` in this port can carry: a sentinel, or a
  tagged tuple standing in for one of Go's `fmt.Errorf` errors.
  """
  @type t :: sentinel() | {:invalid_date, String.t()}

  @doc """
  Every sentinel atom, sorted.

  The list is the port's half of the one-to-one mapping with Go's
  sentinels; the test suite compares it against the Go sources.
  """
  @spec sentinels() :: [sentinel()]
  def sentinels, do: @sentinels |> Map.keys() |> Enum.sort()

  @doc """
  Whether `reason` is one of the sentinel atoms.
  """
  @spec sentinel?(term()) :: boolean()
  def sentinel?(reason) when is_atom(reason), do: Map.has_key?(@sentinels, reason)
  def sentinel?(_reason), do: false

  @doc """
  The message `reason` would print, character for character as the Go error
  prints it.

  Raises `ArgumentError` for anything not listed above -- a reason with no
  message is a reason that was invented instead of being added here.
  """
  @spec message(t()) :: String.t()
  def message(reason) when is_atom(reason) and is_map_key(@sentinels, reason) do
    Map.fetch!(@sentinels, reason)
  end

  # fmt.Errorf("invalid date %q (want YYYY-MM-DD)", s) in
  # internal/task/task.go. Elixir's inspect/1 quotes a plain string the way
  # Go's %q does.
  def message({:invalid_date, value}) when is_binary(value) do
    "invalid date #{inspect(value)} (want YYYY-MM-DD)"
  end

  def message(reason) do
    raise ArgumentError,
          "unknown error reason #{inspect(reason)}; add it to Tend.Error " <>
            "with the message text its Go counterpart prints"
  end
end
