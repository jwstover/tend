defmodule Tend.Version do
  @moduledoc """
  Reports the build version of `tend`.

  The Go tree stamps its version in at release time via ldflags and falls back
  to the module version in the binary's build info. Nothing releases the Elixir
  build yet, so the mix project version -- read at compile time, because
  `Mix.Project` is not available inside an escript at runtime -- is the single
  source of truth.
  """

  @version Mix.Project.config()[:version]

  @doc """
  The version string, `v`-prefixed the way the Go binary prints it.
  """
  @spec string() :: String.t()
  def string, do: "v" <> @version
end
