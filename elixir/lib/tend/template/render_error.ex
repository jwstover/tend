defmodule Tend.Template.RenderError do
  @moduledoc """
  A `prompt_md` template that parses but cannot be rendered against its data.

  Overwhelmingly this is the `missingkey=error` case: a field the data does
  not have. Go's engine refuses it rather than writing an empty string, and
  so does this, because a prompt with a typo in it has to fail loudly the
  first time it runs instead of quietly handing an agent a sentence with a
  hole in it.

  ## The message keeps Go's shape

  `internal/workflow/prompt.go` wraps whatever `Execute` returns in
  `ErrInvalidPrompt` and the TUI's validate action shows it verbatim, so
  `Exception.message/1` reproduces `text/template`'s own layout:

      template: prompt:1:3: executing "prompt" at <.Nope>: can't evaluate field Nope in type Tend.Workflow.PromptData

  Two things in it cannot be byte-identical to Go's, and both are on purpose:

    * **The type name is Elixir's.** Go names the Go type
      (`main.PromptData`, `string`, `[]main.PromptSubtask`); there is no such
      type here, so a struct is named by its module and everything else by
      its Elixir type (`binary`, `integer`, `list`, `map`, `nil`). This is
      also the whole of the difference in `nil pointer evaluating nil.Title`,
      where Go names the pointer's type (`*main.PromptTask.Title`) and an
      Elixir `nil` has none to name.
    * **The column is 1-based.** Go's `ErrorContext` passes the raw byte
      offset as the column, so Go says `prompt:1:2` where this says
      `prompt:1:3`. `Tend.Template.ParseError` already counts columns that
      way and the two have to agree -- see `Tend.Template.Position`.

  There is a third, smaller drift inherited from the parser: for a chained
  field, Go's position is the *last* dot in the chain and this port's is the
  first, which is the one an error wants to underline. `Tend.Template.AST`
  documents it.

  The field name itself -- the load-bearing part, the thing
  `TestRenderPromptErrorNamesTheVariable` asserts on -- is named exactly as
  Go names it, both in `detail` and in the `<...>` context.

  Fields:

    * `reason` -- the machine-readable cause, see below
    * `detail` -- Go's own wording for the failure, e.g.
      `can't evaluate field Nope in type Tend.Workflow.PromptData`
    * `context` -- the node being evaluated, spelled back out as source, e.g.
      `.Task.BodyMD`; this is what Go puts between `<` and `>`
    * `offset` -- the node's 0-based byte offset into the template source
    * `line` -- its 1-based line, counting `\\n`
    * `column` -- its 1-based column, in bytes from the start of the line

  ## Folding into `Tend.Error`

  `Tend.Error` does not exist on this branch (it arrives with the task-core
  sub-task), so this is a free-standing exception rather than a `Tend.Error`
  variant, exactly as `Tend.Template.ParseError` is. When the two meet, the
  workflow prompt sub-task wraps *both* of them the same way: as a
  `Tend.Error` whose reason is

      :invalid_prompt

  -- the Elixir counterpart of `ErrInvalidPrompt` -- carrying this struct as
  its cause and prepending `invalid prompt template: ` to
  `Exception.message/1` here. That wrapper is the only atom this folds into;
  `reason` below stays internal to the template tree and is there so a caller
  can tell the causes apart without matching on message text.

  The `reason` atoms, verbatim:

    * `:missing_field` -- a struct (or any non-map value) has no such field;
      `can't evaluate field X in type T`
    * `:missing_key` -- a map has no such key; `map has no entry for key "X"`.
      This is the case Go's `missingkey=error` option actually governs; a
      struct field is an error in Go whatever the option says
    * `:nil_data` -- a field was read off `nil`. Go splits this in two and so
      does this: `nil data; no entry for key "X"` when the *root* is nil, and
      `nil pointer evaluating nil.X` when the nil is partway down a chain
    * `:not_iterable` -- `{{range}}` over something that is not a list;
      `range can't iterate over V`
    * `:bad_command` -- a command that cannot be evaluated at all, such as
      `{{nil}}` or an argument given to something that is not a function.
      Go has three wordings for the second, chosen by what the head of the
      command is, and all three are reproduced: `X has arguments but cannot
      be invoked as function` for a field, `X is not a method but has
      arguments` for a map key, and `can't give argument to non-function X`
      for a literal, `$`, the cursor or a parenthesised pipeline
    * `:unsupported` -- a construct the renderer does not implement yet.
      Every one of these belongs to the builtins-and-pipelines sub-task
      (functions, multi-stage pipelines, variable declarations); when that
      lands, this reason should stop being reachable for anything a stored
      prompt contains
  """

  alias Tend.Template.AST
  alias Tend.Template.Position

  @typedoc "Why the render failed. See the module doc for the full list."
  @type reason ::
          :missing_field
          | :missing_key
          | :nil_data
          | :not_iterable
          | :bad_command
          | :unsupported

  @type t :: %__MODULE__{
          reason: reason(),
          detail: String.t(),
          context: String.t(),
          offset: non_neg_integer(),
          line: pos_integer(),
          column: pos_integer()
        }

  @enforce_keys [:reason, :detail, :context, :offset, :line, :column]
  defexception [:reason, :detail, :context, :offset, :line, :column]

  @impl true
  def message(%__MODULE__{} = error) do
    "template: prompt:#{error.line}:#{error.column}: " <>
      ~s(executing "prompt" at <#{error.context}>: #{error.detail})
  end

  @doc """
  Builds an error for `detail` about `node`, positioned by the node's offset
  into `source`.
  """
  @spec new(binary(), AST.operand() | AST.Command.t(), reason(), String.t()) :: t()
  def new(source, node, reason, detail) when is_binary(source) do
    offset = Position.clamp(source, node.offset)
    {line, column} = Position.line_and_column(source, offset)

    %__MODULE__{
      reason: reason,
      detail: detail,
      context: AST.to_source(node),
      offset: offset,
      line: line,
      column: column
    }
  end
end
