defmodule Tend.Test.Go do
  @moduledoc """
  Building and driving the real Go `tend` binary from a test.

  Two test modules need a second OS process holding the same `tend.db` --
  `Tend.Store.GoParityTest`, which checks both binaries agree on the schema,
  and `Tend.Store.WatcherTest`, whose whole subject is noticing a write made
  by somebody else. The binary they need is the same one, built the same way
  from this worktree, so it is built here rather than twice.

  Where the Go toolchain is absent the caller should skip itself; see
  `available?/0`.
  """

  @doc """
  Whether a Go toolchain is on `PATH`.

  Callers use this in a module body to skip rather than fail:

      if not Tend.Test.Go.available?() do
        @moduletag skip: "the Go toolchain is not installed"
      end
  """
  @spec available?() :: boolean()
  def available?, do: not is_nil(System.find_executable("go"))

  @doc """
  Builds `./cmd/tend` from this worktree and returns the binary's path.

  Call it from `setup_all`: the build is registered for cleanup with
  `ExUnit.Callbacks.on_exit/1`, and `tag` keeps two modules building
  concurrently from colliding on the output name.
  """
  @spec build!(String.t()) :: String.t()
  def build!(tag) do
    root = Path.expand("..", File.cwd!())
    binary = Path.join(System.tmp_dir!(), "tend-#{tag}-#{System.pid()}")

    {output, status} =
      System.cmd("go", ["build", "-o", binary, "./cmd/tend"], cd: root, stderr_to_stdout: true)

    if status != 0, do: raise("go build failed:\n#{output}")
    ExUnit.Callbacks.on_exit(fn -> File.rm(binary) end)

    binary
  end

  @doc """
  Runs the binary against the database at `path` and returns its combined
  output, raising if it exits non-zero.
  """
  @spec run!(String.t(), String.t(), [String.t()]) :: String.t()
  def run!(binary, path, args) do
    {output, status} = System.cmd(binary, ["--db", path | args], stderr_to_stdout: true)
    if status != 0, do: raise("tend #{Enum.join(args, " ")} failed:\n#{output}")

    output
  end
end
