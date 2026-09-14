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
    live_go: :ran,
    repo_cases: 230,
    recorded: 230,
    tend_db: {:run, "/home/me/.local/share/tend/tend.db"}
  }

  @without_go %{
    go: :missing,
    live_go: :not_run,
    repo_cases: 230,
    recorded: 230,
    tend_db: {:skip, "no Go toolchain on the PATH"}
  }

  # Go on the PATH, and nothing in the run rendered anything through it:
  # `mix test --exclude go`, `--only some_other_tag`, or any `mix test FILE`
  # that does not include the `:go`-tagged test.
  @go_not_run %{@with_go | live_go: :not_run, tend_db: {:run, "/home/me/tend.db"}}

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

  describe "with a Go toolchain nothing in the run used" do
    test "does not claim the recording was checked against live Go" do
      text = Banner.text(@go_not_run, false)

      refute text =~ "checked against live Go"
      assert text =~ "NOT A PARITY RUN"
      assert text =~ "the live-Go check is not part of this run"
      assert text =~ "RECORDED Go output"
      assert text =~ "does NOT mean Tend.Template matches Go"
    end

    test "says Go is installed rather than telling the reader to install it" do
      text = Banner.text(@go_not_run, false)

      assert text =~ "Go is installed here"
      refute text =~ "Re-run where Go is installed"
    end

    test "still reports a tend.db half that did run, because it did" do
      assert Banner.text(@go_not_run, true) =~ "every prompt_md row in /home/me/tend.db"
    end
  end

  describe "the plan the banner reports on" do
    test "describes this machine, and agrees with itself" do
      plan = Parity.plan()

      assert plan.go in [:available, :missing]
      assert plan.live_go in [:ran, :not_run]
      assert plan.repo_cases > 0, "the corpus scanner found no templates in the repo's Go tests"
      assert plan.recorded == plan.repo_cases

      case plan do
        %{go: :missing, tend_db: tend_db} ->
          assert tend_db == {:skip, "no Go toolchain on the PATH"}

        %{go: :available, tend_db: {:run, path}} ->
          # Only claimed when everything the tend.db half needs is here.
          assert File.exists?(path)
          assert System.find_executable("sqlite3")

        %{go: :available, tend_db: {:skip, reason}} ->
          # Whatever the reason is, it cannot be the missing toolchain.
          refute reason =~ "Go toolchain"
      end
    end

    # There is deliberately no "plan/0 does not report live Go unless the
    # :go test ran" test here. Every version of it has to ask whether the
    # tag is excluded in *this* invocation, and on any machine with Go --
    # including CI, which pins a toolchain -- that is false, so the body
    # never executes and the test asserts nothing where it matters. The
    # discriminating coverage lives in
    # `Tend.Template.Parity.BannerExclusionTest`, which is `async: false`
    # and reconfigures the filter itself rather than hoping for one.
  end
end
