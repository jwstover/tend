defmodule Tend.Template.Parity.BannerExclusionTest do
  @moduledoc """
  The banner's whole job is that "the Elixir suite is green" cannot be
  mistaken for "`Tend.Template` matches Go", so the one word that could
  falsify it has its own test.

  Review pass 1 for sub-task #318 found that the banner decided what to say
  from a `Tend.Template.Parity.plan/0` that asked only whether a Go toolchain
  was on the `PATH` -- never whether the `:go`-tagged test that actually
  executes Go had run. `mix test --exclude go` on a machine that has Go
  therefore exited 0 while printing

      recording     : checked against live Go by the :go-tagged test.

  which is the precise failure mode the banner exists to prevent. `plan/0` now
  reports `:live_go`, a fact the `:go` test records, and the banner makes the
  strong claim only for that.

  Check it outside this file with:

      cd elixir && mix test --exclude go test/tend/template/parity_test.exs

  which is green and must not print the "checked against live Go" line.
  """

  # Not async: it reconfigures ExUnit's global tag filters.
  use ExUnit.Case, async: false

  import ExUnit.CaptureIO

  alias Tend.Template.Parity.Banner
  alias Tend.Template.Parity.Go

  test "does not claim live Go checked the recording when the :go tag is excluded" do
    if Go.available?() do
      original = Keyword.get(ExUnit.configuration(), :exclude, [])

      try do
        ExUnit.configure(exclude: Enum.uniq([:go | original]))
        banner = capture_io(:stderr, fn -> Banner.print(:ignored) end)

        refute banner =~ "checked against live Go",
               """
               The :go tag was excluded, so nothing executed Go, but the banner \
               still reported the recording as checked against live Go:

               #{banner}
               """
      after
        ExUnit.configure(exclude: original)
      end
    else
      # Without Go the banner already says NOT A PARITY RUN; nothing to prove.
      :ok
    end
  end
end
