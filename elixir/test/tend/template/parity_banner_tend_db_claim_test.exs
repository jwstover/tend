defmodule Tend.Template.Parity.BannerTendDbClaimTest do
  @moduledoc """
  The `tend.db` half of the banner, along the axis the `:live_go` fix opened.

  `Tend.Template.Parity.Banner` was taught to report what *ran* rather than
  what the machine *could* run, but at first only for live Go. The `tend.db`
  line still reported a capability: it printed "every prompt_md row in PATH."
  whenever the plan said `{:run, path}` and `:tend_db` was in ExUnit's
  `:include`, without asking whether the `:tend_db`-tagged test in
  `Tend.Template.ParityTest` had executed. Reproduced, on a machine with Go,
  sqlite3 and a real database, with

      cd elixir && mix test --include tend_db test/tend/template/renderer_test.exs

  which runs no parity case at all and still printed

      tend.db       : every prompt_md row in /Users/.../tend.db.

  `plan/0` now carries `tend_db_compared`, a count the tagged test records
  only on the branch that really rendered rows, and the banner makes the
  claim from that. This test holds the banner's `tend.db` line to that count
  whichever way the suite was invoked.
  """

  # Not async: it reconfigures ExUnit's global tag filters.
  use ExUnit.Case, async: false

  import ExUnit.CaptureIO

  alias Tend.Template.Parity
  alias Tend.Template.Parity.Banner

  test "claims the tend.db half only when a test really rendered its rows" do
    original = Keyword.get(ExUnit.configuration(), :include, [])

    if :tend_db in original do
      # The tagged test is async and has already run, whether it compared rows
      # or skipped for want of a database. Either way the line has to agree
      # with the count it recorded.
      assert_agrees_with_recorded_count(Parity.plan())
    else
      # The tagged test was excluded, so nothing rendered a row. Ask for the
      # half anyway and the banner still must not claim it.
      try do
        ExUnit.configure(include: [:tend_db | original])
        assert_agrees_with_recorded_count(Parity.plan())
      after
        ExUnit.configure(include: original)
      end
    end
  end

  defp assert_agrees_with_recorded_count(plan) do
    banner = capture_io(:stderr, fn -> Banner.print(:ignored) end)

    case plan do
      %{tend_db: {:run, path}, tend_db_compared: compared} when is_integer(compared) ->
        assert banner =~ "#{compared} comparison(s) over every prompt_md row in #{path}",
               "the tend.db half compared #{compared} case(s) and the banner did not " <>
                 "say so:\n\n#{banner}"

      %{tend_db: {:run, path}} ->
        refute banner =~ "every prompt_md row in #{path}",
               """
               Nothing in this run rendered a tend.db row, but the banner reported \
               the database as compared:

               #{banner}
               """

      %{tend_db: {:skip, _reason}} ->
        # No database to claim coverage of on this machine.
        refute banner =~ "every prompt_md row in"
    end
  end
end
