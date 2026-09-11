defmodule Spike.MixProject do
  use Mix.Project

  def project do
    [
      app: :spike,
      version: "0.1.0",
      elixir: "~> 1.18",
      start_permanent: false,
      deps: deps()
    ]
  end

  def application do
    [extra_applications: [:logger]]
  end

  defp deps do
    [
      # Pinned exactly: the handoff reaches into Termite's signal handler
      # state and Breeze's Term struct, both private surfaces.
      {:breeze, "0.5.1"}
    ]
  end
end
