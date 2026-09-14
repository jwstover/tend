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
  run that claims one. `Tend.Template.Parity.plan/0` carries the two facts
  separately (`:go` and `:live_go`) and this module only makes the strong
  claim for the second.

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

  defp headline(%{go: :missing}),
    do: " !! NOT A PARITY RUN: no Go toolchain, so nothing here executed Go !!"

  # Go is installed and this run is still reporting nothing rendered through
  # it. Worded for the filter rather than the cause, because `--exclude go`,
  # `--only some_other_tag` and any single-file `mix test` all reach it.
  defp headline(%{go: :available, live_go: :not_run}),
    do: " !! NOT A PARITY RUN: the live-Go check is not part of this run !!"

  defp headline(%{go: :available, live_go: :ran}),
    do: " Tend.Template <-> Go text/template parity"

  defp body(%{live_go: :ran} = plan, tend_db_requested?) do
    Enum.join(
      [
        "  repo fixtures : #{plan.repo_cases} case(s) vs the recording " <>
          "(#{plan.recorded} entries).",
        "  recording     : checked against live Go by the :go-tagged test.",
        "  tend.db       : #{tend_db_body(plan, tend_db_requested?)}"
      ],
      "\n"
    )
  end

  defp body(plan, tend_db_requested?) do
    Enum.join(
      [
        "  repo fixtures : #{plan.repo_cases} case(s) compared against RECORDED Go output",
        "                  only (test/fixtures/parity/repo_prompts.jsonl, " <>
          "#{plan.recorded} entries).",
        "                  A recording that has drifted from Go cannot be caught here.",
        "  tend.db       : #{tend_db_body(plan, tend_db_requested?)}",
        "",
        "  A green suite on this run does NOT mean Tend.Template matches Go.",
        "  " <> remedy(plan)
      ],
      "\n"
    )
  end

  defp remedy(%{go: :missing}),
    do: "Re-run where Go is installed:  MIX_ENV=test mix tend.parity --db PATH"

  defp remedy(%{go: :available}),
    do: "Go is installed here. Get the live check:  MIX_ENV=test mix tend.parity"

  defp tend_db_body(plan, tend_db_requested?) do
    case plan.tend_db do
      {:run, path} when tend_db_requested? -> "every prompt_md row in #{path}."
      _other -> "did not run -- #{tend_db_note(plan, tend_db_requested?)}"
    end
  end

  defp tend_db_note(plan, tend_db_requested?) do
    case {plan.tend_db, tend_db_requested?} do
      {_any, false} -> "not asked for (mix test --include tend_db)"
      {{:skip, reason}, true} -> reason
      {{:run, path}, true} -> "ready at #{path}"
    end
  end
end
