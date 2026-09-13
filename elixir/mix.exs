defmodule Tend.MixProject do
  use Mix.Project

  @version "0.1.0"

  def project do
    [
      app: :tend,
      version: @version,
      elixir: "~> 1.18",
      start_permanent: Mix.env() == :prod,
      elixirc_paths: elixirc_paths(Mix.env()),
      escript: escript(),
      deps: deps()
    ]
  end

  # test/support holds helpers shared by more than one test file; it is
  # compiled only under MIX_ENV=test so none of it can reach the release.
  defp elixirc_paths(:test), do: ["lib", "test/support"]
  defp elixirc_paths(_env), do: ["lib"]

  def application do
    [extra_applications: [:logger]]
  end

  # `mix escript.build` produces ./tend, the Elixir counterpart of the Go
  # binary. Nothing releases it yet -- the Go binary is still the shipped
  # one until the Elixir build is cut alongside it.
  #
  # The escript is a scaffold-era convenience, not the packaging plan: that
  # was settled on Burrito in Phase 0 spike 2 (33e8476). An escript carries
  # neither `priv/` nor a NIF, so once the store's SQLite driver and
  # priv/migrations/*.sql landed, ./tend stopped being runnable even though
  # `mix escript.build` still succeeds -- `:code.priv_dir(:tend)` resolves
  # inside the archive and Exqlite.Sqlite3NIF fails `on_load` with a dlopen
  # error. Reach for `mix run`/`mix test` until Burrito lands in Phase 5.
  defp escript do
    [main_module: Tend.CLI, name: "tend"]
  end

  # Dependencies stay few and earned, the way the Go tree's are: CLI dispatch,
  # for instance, is hand-rolled on the standard library rather than pulling an
  # options parser. The store's SQLite driver is the planned exception -- there
  # is no standard-library sqlite3, and CI already caches deps/ and _build/ on
  # the expectation that it is a NIF compiled from C.
  #
  # exqlite is that exception, and only its raw `Exqlite.Sqlite3` API is used:
  # no Ecto, because the Go store is hand-written SQL and the port is a port,
  # not a redesign.
  defp deps do
    [
      {:exqlite, "~> 0.40"}
    ]
  end
end
