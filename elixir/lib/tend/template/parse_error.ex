defmodule Tend.Template.ParseError do
  @moduledoc """
  A `prompt_md` template that is not a valid Go `text/template`.

  The Go tree surfaces `text/template`'s own message verbatim: `parsePrompt`
  in `internal/workflow/prompt.go` wraps it as
  `invalid prompt template: template: prompt:1: unexpected {{end}}`, and the
  TUI's validate action, the CLI pre-flight and the MCP writes all show that
  string as-is. So the message shape is part of the contract, not decoration.

  This struct keeps Go's prefix and phrasing and adds what the sub-task asks
  for -- the offending token and its byte offset:

      template: prompt:2:7: unexpected {{end}} at byte 19

  Fields:

    * `detail` -- Go's own wording for the failure, e.g. `unexpected {{end}}`
    * `token` -- the offending lexeme, exactly as it appears in the source
    * `offset` -- its 0-based byte offset into the template source
    * `line` -- its 1-based line, counting `\\n`
    * `column` -- its 1-based column, in bytes from the start of the line

  ## Folding into `Tend.Error`

  `Tend.Error` does not exist on this branch (it arrives with the task-core
  sub-task), so this is a free-standing exception rather than a `Tend.Error`
  variant. When the two meet, `ErrInvalidPrompt`'s Elixir counterpart becomes
  a `Tend.Error` whose `reason` is `:invalid_prompt` and whose cause is this
  struct; `Exception.message/1` here already produces the tail that the
  wrapper prepends `invalid prompt template: ` to, so nothing about this
  struct has to change -- only the code that raises it one layer up.
  """

  @type t :: %__MODULE__{
          detail: String.t(),
          token: String.t(),
          offset: non_neg_integer(),
          line: pos_integer(),
          column: pos_integer()
        }

  @enforce_keys [:detail, :token, :offset, :line, :column]
  defexception [:detail, :token, :offset, :line, :column]

  @impl true
  def message(%__MODULE__{} = error) do
    "template: prompt:#{error.line}:#{error.column}: #{error.detail} at byte #{error.offset}"
  end

  @doc """
  Builds an error for `detail` about `token` at `offset` bytes into `source`.

  `offset` is clamped to the source length so an error raised at EOF -- an
  unclosed action, say -- still reports a position inside the file.
  """
  @spec new(binary(), non_neg_integer(), String.t(), String.t()) :: t()
  def new(source, offset, token, detail) when is_binary(source) do
    offset = offset |> max(0) |> min(byte_size(source))
    {line, column} = line_and_column(source, offset)

    %__MODULE__{detail: detail, token: token, offset: offset, line: line, column: column}
  end

  defp line_and_column(source, offset) do
    before = binary_part(source, 0, offset)

    case :binary.matches(before, "\n") do
      [] -> {1, offset + 1}
      matches -> {length(matches) + 1, offset - elem(List.last(matches), 0)}
    end
  end
end
