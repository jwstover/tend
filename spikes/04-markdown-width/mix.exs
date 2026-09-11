defmodule Spike4.MixProject do
  use Mix.Project

  def project do
    [
      app: :spike4,
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
      # Pinned exactly, as in spikes 1 to 3; the spike calls Breeze.Markdown
      # and BackBreeze.String directly, both @moduledoc false.
      {:breeze, "0.5.1"},
      # Reads gocheck's JSON.
      {:jason, "~> 1.4"},
      # Only for the fail-path estimate: does a pure-Elixir CommonMark parser
      # give an AST with the constructs Breeze.Markdown drops?
      {:earmark_parser, "~> 1.4"}
    ]
  end
end
