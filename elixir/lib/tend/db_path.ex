defmodule Tend.DBPath do
  @moduledoc """
  Resolves where the SQLite database lives.

  A port of `resolveDBPath` in `internal/cli/root.go`, and deliberately
  identical to it: both binaries have to agree on the file, or the Elixir port
  would quietly open an empty database next to the real one.

  Order of precedence:

    1. the `--db` flag
    2. `$TEND_DB`
    3. `$XDG_DATA_HOME/tend/tend.db`
    4. `~/.local/share/tend/tend.db`
    5. `tend.db`, relative to the working directory, when there is no home
       directory to anchor to -- capture must not fail for want of a `$HOME`.
  """

  @doc """
  Resolves the database path from `flag_value` and the environment.

  `flag_value` is the `--db` flag, `nil` (or `""`) when it was not given.
  `env` and `home` exist so the tests can drive every branch; production calls
  pass neither. Both an unset variable and one set to the empty string count as
  absent, matching Go's `os.Getenv`.
  """
  @spec resolve(String.t() | nil, (String.t() -> String.t() | nil), (-> String.t() | nil)) ::
          String.t()
  def resolve(flag_value, env \\ &System.get_env/1, home \\ &System.user_home/0) do
    cond do
      present?(flag_value) ->
        flag_value

      present?(env.("TEND_DB")) ->
        env.("TEND_DB")

      present?(env.("XDG_DATA_HOME")) ->
        Path.join([env.("XDG_DATA_HOME"), "tend", "tend.db"])

      present?(home.()) ->
        Path.join([home.(), ".local", "share", "tend", "tend.db"])

      # No home directory to anchor to; fall back to the working directory
      # rather than failing capture.
      true ->
        "tend.db"
    end
  end

  defp present?(nil), do: false
  defp present?(""), do: false
  defp present?(value) when is_binary(value), do: true
end
