defmodule Tend.Template.ParseError do
  @moduledoc """
  A `prompt_md` template that is not a valid Go `text/template`.

  ## The message deliberately differs from Go's

  The Go tree surfaces `text/template`'s own message verbatim: `parsePrompt`
  in `internal/workflow/prompt.go` wraps it as

      invalid prompt template: template: prompt:1: unexpected {{end}}

  and the TUI's validate action, the CLI pre-flight and the MCP writes all
  show that string as-is. Go gives a line and no more -- no column, no offset.

  The sub-task asks for the offending token *and its byte offset*, so this
  port does not reproduce that string. It keeps Go's prefix and its wording
  for the failure itself and appends the position, which is strictly more
  than Go says and in the same shape:

      template: prompt:1:4: unexpected {{end}} at byte 3

  That is the decided contract, not an accident: a user reading a validate
  error wants to find the character, and `line` alone does not locate it in a
  long single-line prompt. Anything that renders one of these -- including the
  renderer and the built-ins sub-tasks -- should expect the richer string, and
  a test that pins a `text/template` message byte for byte will not match.

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
  struct, and the wrapper prepends `invalid prompt template: ` to
  `Exception.message/1` here. The composed string is

      invalid prompt template: template: prompt:1:4: unexpected {{end}} at byte 3

  where Go's is

      invalid prompt template: template: prompt:1: unexpected {{end}}

  so the wrapper needs no change, but whoever writes it should know it is
  inheriting the richer message above on purpose.
  """

  alias Tend.Template.Position

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
    offset = Position.clamp(source, offset)
    {line, column} = Position.line_and_column(source, offset)

    %__MODULE__{detail: detail, token: token, offset: offset, line: line, column: column}
  end
end
