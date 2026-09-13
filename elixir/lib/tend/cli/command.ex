defmodule Tend.CLI.Command do
  @moduledoc """
  One node of the command tree.

  `stub` names the entry point the node dispatches to -- an atom that
  `Tend.CLI.Stubs` has exactly one function for. A node with no `stub` is
  either a group (it has subcommands, so it prints its help) or a command the
  Elixir port has not reached yet.
  """

  @type t :: %__MODULE__{
          name: String.t(),
          short: String.t(),
          args: String.t() | nil,
          aliases: [String.t()],
          hidden: boolean(),
          stub: atom() | nil,
          subcommands: [t()]
        }

  @enforce_keys [:name, :short]
  defstruct [:name, :short, :args, :stub, aliases: [], hidden: false, subcommands: []]

  @doc """
  Whether `token` names this command, by name or by alias.
  """
  @spec matches?(t(), String.t()) :: boolean()
  def matches?(%__MODULE__{} = command, token) do
    token == command.name or token in command.aliases
  end

  @doc """
  The node in `commands` that `token` names, or `nil`.
  """
  @spec find([t()], String.t()) :: t() | nil
  def find(commands, token) do
    Enum.find(commands, &matches?(&1, token))
  end
end
