defmodule Tend.CLI.Invocation do
  @moduledoc """
  A parsed command line: which entry point to run, and with what.

  `argv` is everything left after the command path and the persistent `--db`
  flag were taken off it -- each entry point parses its own flags, the way each
  cobra command does.
  """

  @type t :: %__MODULE__{
          command: atom(),
          path: [String.t()],
          argv: [String.t()],
          db_path: String.t()
        }

  @enforce_keys [:command, :path, :argv, :db_path]
  defstruct [:command, :path, :argv, :db_path]
end
