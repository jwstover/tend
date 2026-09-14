defmodule Tend.Template.Parity.Banner do
  @moduledoc """
  Says, at the end of every suite, what the parity harness actually compared.

  This exists for one failure mode. Half of the parity corpus can only be
  checked with a Go toolchain on the `PATH`, and a runner without one still
  goes green -- so without something shouting at the end of the run, "the
  Elixir suite passed" and "`Tend.Template` matches Go" become the same
  sentence, when on that runner the second was never tested at all.

  So the banner prints on every `mix test`, whether the news is good or bad,
  and when Go is missing it says in as many words that the run was **not** a
  parity check. `Tend.Template.Parity.run/1` prints the same text when the
  `mix tend.parity` task finds no Go to render against.

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

  defp headline(%{go: :available}), do: " Tend.Template <-> Go text/template parity"

  defp body(%{go: :missing} = plan, tend_db_requested?) do
    Enum.join(
      [
        "  repo fixtures : #{plan.repo_cases} case(s) compared against RECORDED Go output",
        "                  only (test/fixtures/parity/repo_prompts.jsonl, " <>
          "#{plan.recorded} entries).",
        "                  A recording that has drifted from Go cannot be caught here.",
        "  tend.db       : did not run -- #{tend_db_note(plan, tend_db_requested?)}",
        "",
        "  A green suite on this machine does NOT mean Tend.Template matches Go.",
        "  Re-run where Go is installed:  MIX_ENV=test mix tend.parity --db PATH"
      ],
      "\n"
    )
  end

  defp body(%{go: :available} = plan, tend_db_requested?) do
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
