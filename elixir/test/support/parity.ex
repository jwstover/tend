defmodule Tend.Template.Parity do
  @moduledoc """
  The Go parity harness: render the same `prompt_md` through Go's
  `text/template` and through `Tend.Template`, and compare the bytes.

  `Tend.Template` exists to render prompts a user already has stored, so the
  only test that really settles it is the one that runs both engines over the
  real corpus. This is that test, kept in the repo so later phases can re-run
  it after every change to the renderer.

  ## Running it

  From `elixir/`:

      MIX_ENV=test mix tend.parity                 # the repo's test fixtures
      MIX_ENV=test mix tend.parity --db ~/.local/share/tend/tend.db
      MIX_ENV=test mix tend.parity --update        # re-record the corpus

  The first two need a Go toolchain on the `PATH`; `--db` also needs
  `sqlite3`. The database is copied to a temporary directory and the copy is
  opened read-only, so a run cannot touch the file a user is working in.

  ## What `mix test` runs

  Three pieces, and they need different things of the machine, so they are
  gated separately and `Tend.Template.Parity.Banner` prints at the end of
  every suite which of them actually ran:

    * **the repo fixtures against the recording** always runs, and needs no
      Go. It compares `Tend.Template`'s bytes to the Go output recorded in
      `test/fixtures/parity/repo_prompts.jsonl`, which is Go's own bytes
      captured by `--update`. It also re-scans the Go test files and fails if
      a template has appeared that the recording has no output for, which is
      the signal to re-run `--update`. The corpus is those templates plus
      `Tend.Template.Parity.Corpus.extra_templates/0`, a short list of
      constructs a stored prompt can use and the Go tests do not --
      `{{range}}` with an `{{else}}` arm, mostly. What a corpus of typed data
      still cannot reach is spelled out in `Tend.Template.Parity.Data`.
    * **the recording against live Go** is tagged `:go` and is excluded when
      no Go toolchain is on the `PATH` -- so it runs by default on a machine
      that has Go. It is what stops the recording from quietly drifting away
      from the Go it claims to be, and it is what calls `record_live_check/0`
      -- that call, and not the presence of a `go` binary, is what licenses
      the banner to say the recording was checked. `mix test --exclude go` is
      green on a machine with Go, and the banner has to say so.
    * **a real user `tend.db`** cannot be recorded -- the file is one person's
      machine -- so it is tagged `:tend_db` and excluded by default. To run
      it:

          MIX_ENV=test mix test --include tend_db

      It renders every `prompt_md` row in `$TEND_DB`, or in the database
      `Tend.DBPath` resolves to, through both engines. With no such file, or
      no Go, the test skips rather than fails -- and the banner says so.
  """

  alias Tend.DBPath
  alias Tend.Template
  alias Tend.Template.Parity.Banner
  alias Tend.Template.Parity.Corpus
  alias Tend.Template.Parity.Data
  alias Tend.Template.Parity.Go

  @typedoc "What one engine did with one case."
  @type result :: {:ok, binary()} | {:error, binary()}

  @typedoc """
  What this machine can and cannot check, decided once and shared by the
  tests, the mix task and the banner so the three cannot disagree.

  `go` is a *capability* -- is there a toolchain to render against -- and
  `live_go` is a *fact*: did the `:go`-tagged test that renders through it
  actually execute in this run. The two are not the same thing, and only the
  second one licenses saying the recording was checked.
  """
  @type plan :: %{
          go: :available | :missing,
          live_go: :ran | :not_run,
          repo_cases: non_neg_integer(),
          recorded: non_neg_integer(),
          tend_db: {:run, binary()} | {:skip, binary()}
        }

  # Set by `record_live_check/0` from the `:go`-tagged test, read back by
  # `plan/0`. `:persistent_term` rather than a process: the banner runs in
  # `ExUnit.after_suite/1`, after the test process that recorded it is gone,
  # and the value is written at most once per VM.
  @live_check_key {__MODULE__, :live_check_ran}

  @doc """
  Records that the recording really was rendered against live Go in this run.

  Called by the `:go`-tagged test in `Tend.Template.ParityTest`, which is the
  only thing that does the comparison. Without it the banner can only report
  that Go is *installed*, which is a claim `mix test --exclude go` falsifies
  in one word.
  """
  @spec record_live_check() :: :ok
  def record_live_check, do: :persistent_term.put(@live_check_key, true)

  @doc """
  What a parity run on this machine would cover.

  Cheap enough to call from a test, a mix task and the end-of-suite banner
  alike; they all call it rather than each deciding for themselves whether Go
  is present or a `tend.db` is readable.
  """
  @spec plan() :: plan()
  def plan do
    %{
      go: if(Go.available?(), do: :available, else: :missing),
      live_go: live_go(),
      repo_cases: length(Corpus.repo_cases()),
      recorded: map_size(Corpus.golden()),
      tend_db: tend_db_plan()
    }
  end

  # Two conditions, and both are needed. The recorded fact alone would still
  # let `Banner.print/1` claim live Go when it is called from a test that has
  # itself excluded the tag -- the `:go` test is async and has already run by
  # then -- and the tag filter alone would still claim it under
  # `--only some_other_tag`, where `:go` never appears in `:exclude` and the
  # test never runs either.
  defp live_go do
    if live_check_ran?() and not go_tag_excluded?(), do: :ran, else: :not_run
  end

  defp live_check_ran?, do: :persistent_term.get(@live_check_key, false)

  defp go_tag_excluded?, do: :go in Keyword.get(ExUnit.configuration(), :exclude, [])

  defp tend_db_plan do
    path = DBPath.resolve(nil)

    cond do
      not Go.available?() -> {:skip, "no Go toolchain on the PATH"}
      not File.exists?(path) -> {:skip, "no database at #{path}"}
      System.find_executable("sqlite3") == nil -> {:skip, "sqlite3 is not on the PATH"}
      true -> {:run, path}
    end
  end

  @doc """
  Renders `cases` through both engines and returns one comparison per case.

  Each comparison is `%{name:, template:, data:, go:, elixir:, status:}` where
  `status` is `:match` or `:mismatch`. Two errors count as a match: Go names
  Go types in its messages and this port cannot, by design -- what matters is
  that a template Go refuses is refused here too, and that every template Go
  renders renders to the same bytes.
  """
  @spec compare([map()]) :: [map()]
  def compare(cases), do: compare_against(cases, Go.render(cases))

  @doc """
  Compares `cases` against results `Tend.Template.Parity.Go.render/1` already
  produced, so one Go invocation can serve several callers.
  """
  @spec compare_against([map()], [map()]) :: [map()]
  def compare_against(cases, go_results) do
    go = Map.new(go_results, &{{&1["template"], &1["data"]}, go_result(&1)})

    Enum.map(cases, fn one ->
      comparison(one, Map.fetch!(go, {one.template, one.data}))
    end)
  end

  @doc """
  Compares `cases` against a recorded golden map, the way `mix test` does.

  Returns `{comparisons, missing}`: the cases the recording covers, and the
  cases it does not.
  """
  @spec compare_recorded([map()], map()) :: {[map()], [map()]}
  def compare_recorded(cases, golden) do
    {known, missing} =
      Enum.split_with(cases, &Map.has_key?(golden, {&1.template, &1.data}))

    comparisons =
      Enum.map(known, fn one ->
        comparison(one, golden |> Map.fetch!({one.template, one.data}) |> go_result())
      end)

    {comparisons, missing}
  end

  @doc """
  Every case where the checked-in recording is not what Go produces today.

  This is the check that keeps the recorded half honest: `mix test` compares
  `Tend.Template` to a file, and a file only means anything for as long as it
  is still Go's bytes. Needs a Go toolchain.

  Drift is a change in *what parity asserts* -- whether Go rendered the
  template, and the bytes it rendered. The wording of a Go error is not part
  of it, because it is not part of the comparison either: `compare/1` counts
  any two errors as a match, since this port cannot name Go types. Holding
  the recording to Go's exact error strings would only turn a Go upgrade into
  a red build over a message nothing reads.

  Returns a list of `%{template:, data:, recorded:, live:}`.
  """
  @spec recording_drift([map()]) :: [map()]
  def recording_drift(cases), do: recording_drift(Go.render(cases), Corpus.golden())

  @doc "As `recording_drift/1`, against Go results and a golden map already read."
  @spec recording_drift([map()], map()) :: [map()]
  def recording_drift(go_results, golden) do
    go_results
    |> Enum.filter(&drifted?(recorded(golden, &1), &1))
    |> Enum.map(fn live ->
      %{
        template: live["template"],
        data: live["data"],
        recorded: go_result(recorded(golden, live)),
        live: go_result(live)
      }
    end)
  end

  defp recorded(golden, live), do: Map.get(golden, {live["template"], live["data"]})

  # A missing recording is not drift -- `compare_recorded/2` reports that as a
  # missing case, with the command that fixes it.
  defp drifted?(nil, _live), do: false

  defp drifted?(recorded, live) do
    case {go_result(recorded), go_result(live)} do
      {{:ok, same}, {:ok, same}} -> false
      {{:error, _recorded}, {:error, _live}} -> false
      _differ -> true
    end
  end

  defp comparison(one, go) do
    elixir = elixir_result(one)

    status =
      case {go, elixir} do
        {{:ok, same}, {:ok, same}} -> :match
        {{:error, _go}, {:error, _elixir}} -> :match
        _differ -> :mismatch
      end

    %{
      name: one.name,
      template: one.template,
      data: one.data,
      go: go,
      elixir: elixir,
      status: status
    }
  end

  @doc "Renders one case through `Tend.Template`."
  @spec elixir_result(map()) :: result()
  def elixir_result(%{template: template, data: data}) do
    case Template.render(template, Data.fetch!(data)) do
      {:ok, rendered} -> {:ok, rendered}
      {:error, error} -> {:error, Exception.message(error)}
    end
  end

  defp go_result(%{"ok" => true} = result), do: {:ok, Map.get(result, "output", "")}
  defp go_result(result), do: {:error, Map.get(result, "error", "")}

  @doc """
  Runs the harness. `opts` takes `:db` (a `tend.db` path to add) and
  `:update` (re-record the repo corpus instead of checking it).

  Returns `{:ok, report}` when everything matched, `{:error, report}` when
  anything did not, and `{:skipped, report}` when there is no Go toolchain to
  render against -- which is not a pass, and the report says so at length.
  `report` is a printable string in all three cases.
  """
  @spec run(keyword()) :: {:ok, binary()} | {:skipped, binary()} | {:error, binary()}
  def run(opts \\ []) do
    if Go.available?() do
      compare_everything(opts)
    else
      {:skipped, Banner.text(plan(), opts[:db] != nil)}
    end
  end

  defp compare_everything(opts) do
    repo = Corpus.repo_cases()
    go_repo = Go.render(repo)

    if opts[:update], do: Corpus.write_golden(go_repo)

    drift = recording_drift(go_repo, Corpus.golden())
    {db, db_note} = db_cases(opts[:db])
    comparisons = compare_against(repo, go_repo) ++ compare(db)
    report = report(comparisons, length(repo), db_note, drift, opts)

    case {Enum.filter(comparisons, &(&1.status == :mismatch)), drift} do
      {[], []} -> {:ok, report}
      {mismatches, drifted} -> {:error, failure(report, mismatches, drifted)}
    end
  end

  defp failure(report, mismatches, drifted) do
    Enum.join(
      [report | Enum.map(mismatches, &describe/1) ++ Enum.map(drifted, &describe_drift/1)],
      "\n"
    )
  end

  defp db_cases(nil), do: {[], "tend.db: not asked for (pass --db PATH)"}

  defp db_cases(path) do
    case Corpus.db_cases(path) do
      {:ok, [], 0} -> {[], "tend.db: NOTHING COMPARED -- #{path} has no prompt_md rows"}
      {:ok, cases, rows} -> {cases, "tend.db: #{rows} prompt_md rows from #{path}"}
      {:error, reason} -> {[], "tend.db: SKIPPED -- #{reason}"}
    end
  end

  defp report(comparisons, repo_case_count, db_note, drift, opts) do
    matched = Enum.count(comparisons, &(&1.status == :match))
    rendered = Enum.count(comparisons, &match?({:ok, _output}, &1.go))

    """
    parity: #{matched}/#{length(comparisons)} cases match \
    (#{rendered} rendered by Go, #{length(comparisons) - rendered} refused by it)
    fixtures: #{repo_case_count} cases from the repo's Go test files\
    #{if opts[:update], do: " (re-recorded)", else: ""}
    recording: #{recording_note(drift, opts)}
    #{db_note}\
    """
  end

  defp recording_note(drift, opts) do
    cond do
      opts[:update] -> "rewritten from this run"
      drift == [] -> "matches this Go toolchain byte for byte"
      true -> "#{length(drift)} case(s) DRIFTED from this Go toolchain -- re-record"
    end
  end

  defp describe(comparison) do
    """
    MISMATCH #{comparison.name}
      template: #{inspect(comparison.template)}
      go:       #{inspect(comparison.go)}
      elixir:   #{inspect(comparison.elixir)}
    """
  end

  defp describe_drift(drift) do
    """
    RECORDING DRIFT #{inspect(drift.template)} [#{drift.data}]
      recorded: #{inspect(drift.recorded)}
      live go:  #{inspect(drift.live)}
    """
  end
end
