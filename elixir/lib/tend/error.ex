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
  `message/1` and a member to `t/0`. `Tend.GoParityTest` reads the Go sources
  and fails if a sentinel defined there has no atom here, so a missed one is
  a test failure rather than a surprise at runtime. (`Tend.ErrorTest` covers
  this module's own rules; the cross-language guard is the parity test.)

  Only the errors of `internal/task/task.go`, `internal/task/project.go`,
  `internal/task/session.go` and `internal/task/log.go` are listed today; the
  store and template ports add theirs.
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
    project_not_found: "project not found",

    # internal/task/log.go
    empty_note: "log entry is empty"
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
          | :empty_note

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

  A reason that interpolates a value quotes it the way Go's `%q` does, with
  `quote_go/1` below -- not with `inspect/1`, which disagrees with it on
  interpolation markers, on escape spellings and on any binary it cannot read
  as text.

  Raises `ArgumentError` for anything not listed above -- a reason with no
  message is a reason that was invented instead of being added here.
  """
  @spec message(t()) :: String.t()
  def message(reason) when is_atom(reason) and is_map_key(@sentinels, reason) do
    Map.fetch!(@sentinels, reason)
  end

  # fmt.Errorf("invalid date %q (want YYYY-MM-DD)", s) in
  # internal/task/task.go.
  def message({:invalid_date, value}) when is_binary(value) do
    "invalid date #{quote_go(value)} (want YYYY-MM-DD)"
  end

  def message(reason) do
    raise ArgumentError,
          "unknown error reason #{inspect(reason)}; add it to Tend.Error " <>
            "with the message text its Go counterpart prints"
  end

  @doc """
  A port of Go's `strconv.Quote`, which is what fmt's `%q` verb applies to a
  string.

  Public because a message built with `%q` is not always an error's:
  `Tend.Workflow.Graph`'s problems quote an outcome with this verb too, and
  their text is rendered verbatim by the TUI just as an error's is.

  Elixir's `inspect/1` is NOT a stand-in for it: it escapes `\#{}`, spells
  escape `\\e` where Go spells it `\\x1b`, and abandons the quoted form
  entirely for a binary it cannot read as text (`"<<0>>"` rather than
  `"\\x00"`), which turns the message into something that is no longer a
  quoted string at all.

  The rules, from `strconv.appendEscapedRune`: `"` and `\\` are always
  backslashed; a printable rune is emitted as itself; the seven characters Go
  names get their name (`\\a \\b \\f \\n \\r \\t \\v`); anything else below
  U+0020, plus U+007F, is `\\xNN`; anything else below U+10000 is `\\uNNNN`;
  the rest is `\\UNNNNNNNN`. A byte that is not part of a valid UTF-8 sequence
  is `\\xNN` on its own, one byte at a time, exactly as Go's decoder yields it.
  """
  @spec quote_go(String.t()) :: String.t()
  def quote_go(s) when is_binary(s), do: IO.iodata_to_binary([?", escape(s), ?"])

  defp escape(<<>>), do: []
  defp escape(<<?", rest::binary>>), do: [~S(\") | escape(rest)]
  defp escape(<<?\\, rest::binary>>), do: [~S(\\) | escape(rest)]
  defp escape(<<cp::utf8, rest::binary>>), do: [escape_rune(cp) | escape(rest)]
  defp escape(<<byte, rest::binary>>), do: [hex(?x, byte, 2) | escape(rest)]

  # The seven escapes Go spells with a letter, by codepoint.
  @named %{
    0x07 => ~S(\a),
    0x08 => ~S(\b),
    0x09 => ~S(\t),
    0x0A => ~S(\n),
    0x0B => ~S(\v),
    0x0C => ~S(\f),
    0x0D => ~S(\r)
  }

  defp escape_rune(cp) do
    cond do
      printable?(cp) -> <<cp::utf8>>
      is_map_key(@named, cp) -> Map.fetch!(@named, cp)
      cp < 0x20 or cp == 0x7F -> hex(?x, cp, 2)
      cp < 0x10000 -> hex(?u, cp, 4)
      true -> hex(?U, cp, 8)
    end
  end

  defp hex(verb, value, width) do
    digits = value |> Integer.to_string(16) |> String.downcase() |> String.pad_leading(width, "0")
    <<?\\, verb>> <> digits
  end

  # Go's unicode.IsPrint: the letter, mark, number, punctuation and symbol
  # categories, plus the ASCII space. `:unicode_util.lookup/1` is the only
  # general-category table on the BEAM, and it tracks a newer Unicode edition
  # than Go's tables do (16.0 against 15.0 as of go1.26), so the two can
  # disagree on a codepoint assigned in between -- a rune Go escapes as
  # \uNNNN because it knows nothing about it. `test/tend/error_test.exs`
  # measures that gap against the Go binary; everything a user can plausibly
  # type into a date prompt is outside it.
  defp printable?(0x20), do: true

  defp printable?(cp) do
    {class, _subclass} = Map.fetch!(:unicode_util.lookup(cp), :category)
    class in [:letter, :mark, :number, :punctuation, :symbol]
  end
end
