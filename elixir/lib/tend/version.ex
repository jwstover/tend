defmodule Tend.Version do
  @moduledoc """
  Reports the build version of `tend`.

  The Go tree stamps its version in at release time via ldflags and falls back
  to the module version in the binary's build info. Nothing releases the Elixir
  build yet, so what it prints is the mix project's version, read at compile
  time because `Mix.Project` is not available inside an escript at runtime.

  That version is a placeholder, not the project's version. release-please
  owns the released version (AGENTS.md §11) and never touches `mix.exs`, so
  `tend version` prints `v0.1.0` here and whatever was last tagged from a
  released Go build of the same commit. Phase 5, which cuts the Elixir build
  alongside the Go one, is where the two have to be wired to one source.
  """

  @version Mix.Project.config()[:version]

  @doc """
  The version string, `v`-prefixed the way the Go binary prints it.
  """
  @spec string() :: String.t()
  def string, do: "v" <> @version
end
