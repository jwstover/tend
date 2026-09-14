defmodule Tend.Template.ParityTest do
  @moduledoc """
  The parity corpus: every `prompt_md` this repo knows about, rendered
  through Go and through `Tend.Template`, byte against byte.

  Three pieces, run under different conditions on purpose:

    * **the repo's test fixtures** always run. They are compared against the
      Go output recorded in `test/fixtures/parity/repo_prompts.jsonl`, so
      `mix test` needs no Go toolchain, and a template that appears in a Go
      test with no recording is a failure that names the command to fix it.
    * **the recording itself** is checked against live Go under the `:go`
      tag, which `test/test_helper.exs` excludes only when there is no Go on
      the `PATH`. A recording nobody re-checks is a file that agrees with
      itself, which is not parity.
    * **a real user `tend.db`** is tagged `:tend_db` and excluded by default,
      because the file is one person's machine and cannot be checked in. Run
      it with `MIX_ENV=test mix test --include tend_db`, or the whole harness
      with `MIX_ENV=test mix tend.parity --db PATH`.

  Whichever of the three a given machine can run, `Tend.Template.Parity.Banner`
  prints it after the suite, so a green run that skipped Go cannot be read as
  a passing parity check. `Tend.Template.Parity` documents all of it.
  """

  use ExUnit.Case, async: true

  alias Tend.Template.Parity
  alias Tend.Template.Parity.Corpus
  alias Tend.Template.Parity.Data

  describe "every prompt_md in the repo's test fixtures" do
    setup do
      cases = Corpus.repo_cases()
      {comparisons, missing} = Parity.compare_recorded(cases, Corpus.golden())
      %{cases: cases, comparisons: comparisons, missing: missing}
    end

    test "is covered by the recorded Go output", %{cases: cases, missing: missing} do
      assert cases != [], "the corpus scanner found no templates in the repo's Go tests"

      assert missing == [],
             """
             #{length(missing)} template/data pair(s) in the repo's Go tests have no \
             recorded Go output. Re-record them with:

                 MIX_ENV=test mix tend.parity --update

             First without a recording: #{inspect(List.first(missing))}
             """
    end

    test "renders to the same bytes Go rendered", %{comparisons: comparisons} do
      rendered = Enum.filter(comparisons, &match?({:ok, _output}, &1.go))

      # Without a floor this test passes on an empty corpus, which is the one
      # way a parity harness can be wrong and still be green.
      assert length(rendered) > 100,
             "only #{length(rendered)} of #{length(comparisons)} recorded cases were " <>
               "templates Go rendered; the corpus has shrunk unexpectedly"

      for comparison <- rendered do
        {:ok, want} = comparison.go

        assert comparison.elixir == {:ok, want},
               "#{comparison.name}: #{inspect(comparison.template)}\n" <>
                 "go:     #{inspect(want)}\nelixir: #{inspect(comparison.elixir)}"
      end
    end

    test "and refuses everything Go refused, missing keys included",
         %{comparisons: comparisons} do
      refused = Enum.filter(comparisons, &match?({:error, _message}, &1.go))
      assert length(refused) > 0

      for comparison <- refused do
        assert match?({:error, _message}, comparison.elixir),
               "#{comparison.name}: #{inspect(comparison.template)} renders here but Go " <>
                 "refused it with #{inspect(comparison.go)}"
      end
    end

    test "a missing key raises rather than rendering an empty string" do
      # The half of `missingkey=error` a corpus of valid prompts cannot show.
      assert {:error, %Tend.Template.RenderError{reason: :missing_key}} =
               Tend.Template.render("{{.Nope}}", %{"Cwd" => "/tmp"})
    end
  end

  describe "the recorded Go output" do
    @describetag :go

    test "is byte for byte what this Go toolchain produces today" do
      drift = Parity.recording_drift(Corpus.repo_cases())

      assert drift == [],
             """
             #{length(drift)} recorded case(s) no longer match the Go toolchain on this \
             machine, so the Go-less half of this suite is checking Tend.Template against \
             a stale file. Re-record with:

                 MIX_ENV=test mix tend.parity --update

             First drifted: #{inspect(List.first(drift))}
             """
    end
  end

  describe "every prompt_md row in a real tend.db" do
    @describetag :tend_db

    test "renders to the same bytes Go rendered" do
      # `$TEND_DB` first, then the XDG path -- the same order the binary
      # resolves `--db` in, so this is the database a user is actually using.
      case Parity.plan().tend_db do
        {:skip, reason} ->
          # Nothing to compare against on this machine. The end-of-suite
          # banner repeats this, so the skip cannot pass for a parity check.
          IO.puts(:stderr, "SKIPPED the tend.db parity half: #{reason}")

        {:run, path} ->
          assert {:ok, cases, rows} = Corpus.db_cases(path)
          assert rows > 0, "#{path} has no workflow_steps rows to compare"
          comparisons = Parity.compare(cases)
          assert length(comparisons) == rows * length(Data.names())

          for comparison <- comparisons do
            assert comparison.status == :match,
                   "#{comparison.name}: #{inspect(comparison.template)}\n" <>
                     "go:     #{inspect(comparison.go)}\nelixir: #{inspect(comparison.elixir)}"
          end
      end
    end
  end
end
