defmodule Tend.Template.Position do
  @moduledoc """
  Byte offset to 1-based line and column, shared by the template tree's two
  error structs.

  `Tend.Template.ParseError` and `Tend.Template.RenderError` both point at a
  character in the prompt source, and they have to agree on how to count it,
  or the same offset would read as two different places depending on which
  phase failed.

  The column is **1-based**, in bytes from the start of the line. Go's
  `text/template` reports a 0-based one for render errors -- `ErrorContext`
  hands `fmt.Sprintf` the raw byte offset as the column -- so this port's
  columns are one greater than Go's. That divergence is deliberate and is
  spelled out in both error structs.
  """

  @doc """
  The 1-based line and column of `offset` in `source`.

  `offset` is clamped to the source, so an error raised at EOF still reports
  a position inside the file.

      iex> Tend.Template.Position.line_and_column("a\\nbc", 3)
      {2, 2}
  """
  @spec line_and_column(binary(), integer()) :: {pos_integer(), pos_integer()}
  def line_and_column(source, offset) when is_binary(source) do
    offset = clamp(source, offset)
    before = binary_part(source, 0, offset)

    case :binary.matches(before, "\n") do
      [] -> {1, offset + 1}
      matches -> {length(matches) + 1, offset - elem(List.last(matches), 0)}
    end
  end

  @doc """
  Clamps `offset` into `0..byte_size(source)`.

      iex> Tend.Template.Position.clamp("abc", 99)
      3
  """
  @spec clamp(binary(), integer()) :: non_neg_integer()
  def clamp(source, offset) when is_binary(source) and is_integer(offset) do
    offset |> max(0) |> min(byte_size(source))
  end
end
