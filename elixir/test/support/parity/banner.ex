defmodule Tend.Template.Parity.Banner do
  @moduledoc """
  Says, at the end of every suite, what the parity harness actually compared.

  This exists for one failure mode. Half of the parity corpus can only be
  checked with a Go toolchain on the `PATH`, and a runner without one still
  goes green -- so without something shouting at the end of the run, "the
  Elixir suite passed" and "`Tend.Template` matches Go" become the same
  sentence, when on that runner the second was never tested at all.

  So the banner prints on every `mix test`, whether the news is good or bad,
  and unless live Go really did render the corpus it says in as many words
  that the run was **not** a parity check. `Tend.Template.Parity.run/1` prints
  the same text when the `mix tend.parity` task finds no Go to render against.

  It reports what *ran*, not what this machine *could* run: a toolchain on the
  `PATH` is not a comparison, and `mix test --exclude go` is otherwise a green
  run that claims one. The same holds one axis over -- a corpus the scanner
  found is not a corpus this run rendered (`mix test --only go`), and a
  readable database plus `--include tend_db` is not a database this run
  compared (`mix test --include tend_db SOME_OTHER_FILE`).
  `Tend.Template.Parity.plan/0` carries each capability and the matching fact
  separately (`:go`/`:live_go`, `:repo_cases`/`:repo_compared`,
  `:tend_db`/`:tend_db_compared`) and this module only ever makes the strong
  claim for the facts.

  `text/2` is pure -- it takes a `Tend.Template.Parity.plan/0` and whether the
  `tend.db` half was asked for -- so the wording is itself under test in
  `Tend.Template.Parity.BannerTest`.
  """

  alias Tend.Template.Parity

  @rule String.duplicate("=", 78)

  @doc """
  Registers `print/1` as an `ExUnit.after_suite/1` callback. Called from
  `test/test_helper.exs`, before the suite runs.
  """
  @spec install() :: :ok
  def install do
    ExUnit.after_suite(&print/1)
    :ok
  end

  @doc "Prints the banner for this machine to standard error."
  @spec print(term()) :: :ok
  def print(_results) do
    IO.puts(:stderr, text(Parity.plan(), tend_db_requested?()))
  end

  defp tend_db_requested? do
    :tend_db in Keyword.get(ExUnit.configuration(), :include, [])
  end

  @doc """
  The banner text for `plan`, given whether the `tend.db` half was requested.
  """
  @spec text(Parity.plan(), boolean()) :: binary()
  def text(plan, tend_db_requested?) do
    Enum.join([@rule, headline(plan), body(plan, tend_db_requested?), @rule], "\n")
  end

  # Both halves have to have happened: live Go has to have rendered the
  # recording, and `Tend.Template` has to have rendered the corpus against it.
  # Either one alone is a run that compared nothing to something.
  defp parity_run?(%{go: :available, live_go: :ran, repo_compared: compared})
       when is_integer(compared) and compared > 0,
       do: true

  defp parity_run?(_plan), do: false

  # Worded for the filter rather than the cause, because `--exclude go`,
  # `--only some_other_tag`, `--only go` and any single-file `mix test` all
  # reach one of these.
  defp headline(plan) do
    cond do
      parity_run?(plan) ->
        " Tend.Template <-> Go text/template parity"

      plan.go == :missing ->
        " !! NOT A PARITY RUN: no Go toolchain, so nothing here executed Go !!"

      plan.live_go == :not_run ->
        " !! NOT A PARITY RUN: the live-Go check is not part of this run !!"

      true ->
        " !! NOT A PARITY RUN: nothing in this run rendered the corpus " <>
          "through Tend.Template !!"
    end
  end

  defp body(plan, tend_db_requested?) do
    if parity_run?(plan) do
      Enum.join(
        repo_lines(plan) ++
          [
            "  recording     : checked against live Go by the :go-tagged test.",
            "  tend.db       : #{tend_db_body(plan, tend_db_requested?)}"
          ],
        "\n"
      )
    else
      Enum.join(
        repo_lines(plan) ++
          [
            "  tend.db       : #{tend_db_body(plan, tend_db_requested?)}",
            "",
            "  A green suite on this run does NOT mean Tend.Template matches Go.",
            "  " <> remedy(plan)
          ],
        "\n"
      )
    end
  end

  # `repo_cases` is how many cases the scanner found; `repo_compared` is how
  # many the byte-equality test rendered. Only the second is reported as
  # coverage, and when it is missing the first is named as what was *available*
  # so the line still says how big the gap is.
  defp repo_lines(%{repo_compared: :not_run} = plan) do
    [
      "  repo fixtures : did not run -- nothing in this run compared the corpus",
      "                  (#{plan.repo_cases} case(s) were available)."
    ]
  end

  defp repo_lines(%{live_go: :ran} = plan) do
    [
      "  repo fixtures : #{plan.repo_compared} case(s) vs the recording (#{plan.recorded} entries)."
    ]
  end

  defp repo_lines(plan) do
    [
      "  repo fixtures : #{plan.repo_compared} case(s) compared against RECORDED Go output",
      "                  only (test/fixtures/parity/repo_prompts.jsonl, " <>
        "#{plan.recorded} entries).",
      "                  A recording that has drifted from Go cannot be caught here."
    ]
  end

  defp remedy(%{go: :missing}),
    do: "Re-run where Go is installed:  MIX_ENV=test mix tend.parity --db PATH"

  defp remedy(%{go: :available}),
    do: "Go is installed here. Get the live check:  MIX_ENV=test mix tend.parity"

  # `{:run, path}` plus `--include tend_db` only says the half *could* have
  # run; `tend_db_compared` is the count the tagged test recorded after it
  # rendered the rows. `mix test --include tend_db test/.../renderer_test.exs`
  # satisfies the first and never touches a row.
  defp tend_db_body(%{tend_db: {:run, path}, tend_db_compared: compared}, true)
       when is_integer(compared) and compared > 0 do
    "#{compared} comparison(s) over every prompt_md row in #{path}."
  end

  defp tend_db_body(plan, tend_db_requested?) do
    "did not run -- #{tend_db_note(plan, tend_db_requested?)}"
  end

  defp tend_db_note(plan, tend_db_requested?) do
    case {plan.tend_db, tend_db_requested?} do
      {_any, false} -> "not asked for (mix test --include tend_db)"
      {{:skip, reason}, true} -> reason
      {{:run, path}, true} -> "no test in this run rendered a row from #{path}"
    end
  end
end
