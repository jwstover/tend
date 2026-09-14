defmodule Tend.Template.Parity.BannerTest do
  @moduledoc """
  The banner is the only thing standing between "the Elixir suite is green"
  and "`Tend.Template` matches Go" on a runner with no Go toolchain, so its
  wording is tested rather than eyeballed.
  """

  use ExUnit.Case, async: true

  alias Tend.Template.Parity
  alias Tend.Template.Parity.Banner

  @with_go %{
    go: :available,
    repo_cases: 230,
    recorded: 230,
    tend_db: {:run, "/home/me/.local/share/tend/tend.db"}
  }

  @without_go %{
    go: :missing,
    repo_cases: 230,
    recorded: 230,
    tend_db: {:skip, "no Go toolchain on the PATH"}
  }

  describe "with no Go toolchain" do
    test "refuses to let the run be read as a passing parity check" do
      text = Banner.text(@without_go, false)

      assert text =~ "NOT A PARITY RUN"
      assert text =~ "nothing here executed Go"
      assert text =~ "does NOT mean Tend.Template matches Go"
      assert text =~ "RECORDED Go output"
      assert text =~ "mix tend.parity"
    end

    test "still says how much the recorded half did compare" do
      assert Banner.text(@without_go, false) =~ "230 case(s) compared against RECORDED"
    end

    test "names why the tend.db half did not run, even when it was asked for" do
      assert Banner.text(@without_go, true) =~ "did not run -- no Go toolchain on the PATH"
      assert Banner.text(@without_go, false) =~ "did not run -- not asked for"
    end
  end

  describe "with a Go toolchain" do
    test "does not claim the run failed" do
      text = Banner.text(@with_go, true)

      refute text =~ "NOT A PARITY RUN"
      assert text =~ "Tend.Template <-> Go text/template parity"
      assert text =~ "checked against live Go"
    end

    test "distinguishes a tend.db half that ran from one that was not asked for" do
      assert Banner.text(@with_go, true) =~ "every prompt_md row in /home/me"
      assert Banner.text(@with_go, false) =~ "did not run -- not asked for"
    end

    test "reports a tend.db that cannot be read as a skip, not as coverage" do
      plan = %{@with_go | tend_db: {:skip, "no database at /nope/tend.db"}}

      assert Banner.text(plan, true) =~ "did not run -- no database at /nope/tend.db"
    end
  end

  describe "the plan the banner reports on" do
    test "describes this machine, and agrees with itself" do
      plan = Parity.plan()

      assert plan.go in [:available, :missing]
      assert plan.repo_cases > 0, "the corpus scanner found no templates in the repo's Go tests"
      assert plan.recorded == plan.repo_cases

      case plan do
        %{go: :missing, tend_db: tend_db} ->
          assert tend_db == {:skip, "no Go toolchain on the PATH"}

        %{go: :available} ->
          assert match?({:run, _path}, plan.tend_db) or match?({:skip, _r}, plan.tend_db)
      end
    end
  end
end
