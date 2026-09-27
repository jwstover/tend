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

  The sentinels listed today are those of `internal/task/task.go`,
  `internal/task/project.go`, `internal/task/session.go`,
  `internal/task/log.go`, `internal/workflow/workflow.go` and
  `internal/workflow/prompt.go`.

  The store and the template engine have no Go sentinel to name -- Go builds
  those failures with `fmt.Errorf` wrappings -- so they fold in as `message/1`
  clauses and `t/0` members rather than as atoms:

    * the interpolating errors the task surface of the store port raises
      (`:unknown_state`, `:priority_out_of_range`, `:task_not_found`,
      `:invalid_timestamp`);
    * the store's descriptive tuples, raised by `Tend.Store.open/1`,
      `Tend.Store.Migrator` and `Tend.Store.Watcher` (`:db_directory_failed`,
      `:db_open_failed`, `:pragma_failed`, `:migration_failed`,
      `:query_failed`, `:data_version_failed`);
    * the template engine's `Tend.Template.ParseError` and
      `Tend.Template.RenderError`, which `{:invalid_prompt, cause}` carries
      exactly as Go's `prompt.go` wraps `text/template`'s error in
      `ErrInvalidPrompt`.
  """

  alias Tend.Task.Priority

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
    empty_note: "log entry is empty",

    # internal/workflow/workflow.go
    empty_name: "name is empty",
    empty_outcome: "outcome is empty",
    workflow_not_found: "workflow not found",
    step_not_found: "step not found",
    run_not_found: "run not found",
    step_run_not_found: "step run not found",
    in_use: "referenced by an active run",
    cross_workflow_edge: "edge joins steps of different workflows",
    run_ended: "run has already ended",
    run_not_failed: "run has not failed",
    step_run_finished: "step run already finished",

    # internal/workflow/prompt.go
    invalid_prompt: "invalid prompt template"
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
          | :empty_name
          | :empty_outcome
          | :workflow_not_found
          | :step_not_found
          | :run_not_found
          | :step_run_not_found
          | :in_use
          | :cross_workflow_edge
          | :run_ended
          | :run_not_failed
          | :step_run_finished
          | :invalid_prompt

  @typedoc """
  Any reason a `{:error, reason}` in this port can carry: a sentinel, or a
  tagged tuple standing in for one of Go's `fmt.Errorf` errors.

  The last two groups are the folded-in ones. `{:invalid_prompt, cause}` is
  `Tend.Workflow.Prompt`'s, carrying the template engine's own exception; the
  five store tuples are `Tend.Store`'s and `Tend.Store.Migrator`'s, and carry
  whatever term SQLite or the file system handed back.
  """
  @type t ::
          sentinel()
          | {:invalid_date, String.t()}
          | {:in_use, String.t(), integer()}
          | {:unknown_state, String.t()}
          | {:priority_out_of_range, term()}
          | {:task_not_found, integer()}
          | {:invalid_timestamp, String.t(), String.t()}
          | {:invalid_prompt, Exception.t()}
          | {:db_directory_failed, String.t(), term()}
          | {:db_open_failed, String.t(), term()}
          | {:pragma_failed, String.t(), term()}
          | {:migration_failed, :up | :down, non_neg_integer(), String.t(), term()}
          | {:query_failed, String.t(), term()}
          | {:data_version_failed, term()}

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

  # fmt.Errorf("%s is %w %d", what, ErrInUse, runID) in
  # internal/workflow/workflow.go, built by Tend.Workflow.in_use_error/2. Go
  # wraps :in_use rather than restating it, so the sentinel's own message is
  # spliced in here rather than duplicated.
  def message({:in_use, what, run_id}) when is_binary(what) and is_integer(run_id) do
    "#{what} is #{Map.fetch!(@sentinels, :in_use)} #{run_id}"
  end

  # fmt.Errorf("unknown state %q", st) in Store.SetState. Go compares a string
  # newtype, this port an atom, so the value is rendered back into the stored
  # spelling before it is quoted.
  def message({:unknown_state, value}) do
    "unknown state #{quote_go(to_string(value))}"
  end

  # fmt.Errorf("priority %d out of range %d..%d", *p, task.PriorityHighest,
  # task.PriorityLowest) in Store.SetPriority.
  def message({:priority_out_of_range, value}) do
    "priority #{value} out of range #{Priority.highest()}..#{Priority.lowest()}"
  end

  # Go's Store.GetTask has no sentinel for this: it wraps whatever
  # database/sql returned, and a missing row arrives as sql.ErrNoRows, whose
  # message is "sql: no rows in result set". exqlite reports no rows as an
  # empty list rather than an error, so the port has to name the condition
  # itself and reproduce the sentence Go would have printed around it.
  def message({:task_not_found, id}) do
    "loading task #{id}: no rows in result set"
  end

  # fmt.Errorf("parsing %q: %w", s, err) in store.parseTime, wrapped by
  # toDomain's fmt.Errorf("task %d created_at: %w", ...). Go's innermost layer
  # is time.ParseError's own prose ("cannot parse ... as ..."), which is the
  # time package's to write and not worth reconstructing; everything the
  # reader needs -- which column, and the value that would not parse -- is
  # here.
  def message({:invalid_timestamp, context, value}) do
    "#{context}: parsing #{quote_go(value)}"
  end

  # The generic wrap every store call site puts around a driver failure:
  # fmt.Errorf("inserting task: %w", err) and its ~forty siblings. `context`
  # is that call site's label, verbatim.
  #
  # Tend.Store.Migrator raises the same tuple with the offending SQL in
  # `context` rather than a label, so a migration failure reads as the
  # statement followed by the driver's complaint. The two shapes were ported
  # independently and share one clause here; unifying them is a reviewer's
  # call, not the join's.
  def message({:query_failed, context, reason}) do
    "#{context}: #{render(reason)}"
  end

  # fmt.Errorf("%w: %w", ErrInvalidPrompt, err) in internal/workflow/prompt.go,
  # built by Tend.Workflow.Prompt. `cause` is the template engine's own
  # exception -- Tend.Template.ParseError or Tend.Template.RenderError -- and
  # its message is spliced in verbatim, because that is the part naming the
  # offending variable and its position, and the TUI's validate action shows
  # the whole string as-is. Both exceptions say so in their own "Folded into
  # Tend.Error" sections; this clause is what they were waiting for.
  def message({:invalid_prompt, cause}) when is_exception(cause) do
    "#{Map.fetch!(@sentinels, :invalid_prompt)}: #{Exception.message(cause)}"
  end

  # The store's remaining reasons, from Tend.Store.open/1 and
  # Tend.Store.Migrator. Go builds this path's errors with fmt.Errorf
  # wrappings rather than sentinels -- internal/store/store.go's Open says
  # "creating db directory: %w" and "opening db %s: %w" -- so these keep Go's
  # wording where the arguments line up. Two deliberate departures:
  #
  #   * the directory is named, where Go's "creating db directory: %w" leaves
  #     it out. Same call as Tend.Template.ParseError's richer position: a
  #     message a user reads should locate the thing it is about.
  #   * a pragma and a single migration have no Go message at all to copy. Go
  #     sets its pragmas in the DSN, where a failure is the driver's own error,
  #     and runs its migrations through goose, which words its own failures and
  #     is wrapped only as "applying migrations: %w". Those two are this port's
  #     wording, in Go's shape: what was being done, then the cause.
  def message({:db_directory_failed, dir, cause}) when is_binary(dir) do
    "creating db directory #{dir}: #{cause_text(cause)}"
  end

  def message({:db_open_failed, path, cause}) when is_binary(path) do
    "opening db #{path}: #{cause_text(cause)}"
  end

  def message({:pragma_failed, sql, cause}) when is_binary(sql) do
    "applying #{sql}: #{cause_text(cause)}"
  end

  def message({:migration_failed, direction, version, name, cause})
      when direction in [:up, :down] and is_integer(version) and is_binary(name) do
    "migrating #{direction} #{version}_#{name}: #{cause_text(cause)}"
  end

  # Tend.Store.Watcher's own reason, folded in with the rest so the store tree
  # is total: every `{:error, reason}` it can return has a message here. The
  # sub-task naming the store reasons predates the watcher, which is why it is
  # not on that list; `Tend.ErrorTest` scans the store modules and fails if
  # another appears.
  def message({:data_version_failed, cause}) do
    "reading PRAGMA data_version: #{cause_text(cause)}"
  end

  def message(reason) do
    raise ArgumentError,
          "unknown error reason #{inspect(reason)}; add it to Tend.Error " <>
            "with the message text its Go counterpart prints"
  end

  # A driver failure is a string from SQLite most of the time ("UNIQUE
  # constraint failed: projects.name") and an atom or a tuple the rest of it.
  # The string form is passed through, because Go's %w on the driver error
  # prints exactly that; anything else is inspected rather than mangled.
  defp render(reason) when is_binary(reason), do: reason
  defp render(reason), do: inspect(reason)

  # What the layer underneath handed back, as text. Exqlite reports a string,
  # `File` a POSIX atom, and anything else is a term nobody promised a shape
  # for -- so it is inspected rather than guessed at.
  defp cause_text(cause) when is_binary(cause), do: cause
  defp cause_text(cause) when is_atom(cause), do: Atom.to_string(cause)
  defp cause_text(cause) when is_exception(cause), do: Exception.message(cause)
  defp cause_text(cause), do: inspect(cause)

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
