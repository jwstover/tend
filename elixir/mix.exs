defmodule Tend.MixProject do
  use Mix.Project

  @version "0.1.0"

  def project do
    [
      app: :tend,
      version: @version,
      elixir: "~> 1.18",
      start_permanent: Mix.env() == :prod,
      escript: escript(),
      deps: deps()
    ]
  end

  def application do
    [extra_applications: [:logger]]
  end

  # `mix escript.build` produces ./tend, the Elixir counterpart of the Go
  # binary. Nothing releases it yet -- the Go binary is still the shipped
  # one until the Elixir build is cut alongside it.
  defp escript do
    [main_module: Tend.CLI, name: "tend"]
  end

  # No dependencies on purpose: CLI dispatch is hand-rolled on the standard
  # library. Keep it that way unless a dependency earns its place the way the
  # Go tree's few do.
  defp deps do
    []
  end
end
