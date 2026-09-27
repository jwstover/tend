defmodule Tend.Template.AST.Text do
  @moduledoc """
  A literal span of the template, reproduced byte for byte.

  `text` is a slice of the source, never re-encoded and never normalised: a
  template with no actions parses to exactly one of these, equal to the whole
  input. The only thing that ever shortens it is a trim marker
  (`{{-` / `-}}`), which removes whitespace the way Go does.
  """

  @type t :: %__MODULE__{text: binary(), offset: non_neg_integer()}

  @enforce_keys [:text, :offset]
  defstruct [:text, :offset]
end

defmodule Tend.Template.AST.Action do
  @moduledoc """
  `{{pipeline}}` -- evaluate the pipeline and write its value.
  """

  @type t :: %__MODULE__{
          pipeline: Tend.Template.AST.Pipeline.t(),
          offset: non_neg_integer()
        }

  @enforce_keys [:pipeline, :offset]
  defstruct [:pipeline, :offset]
end

defmodule Tend.Template.AST.If do
  @moduledoc """
  `{{if pipeline}}body{{else}}else_body{{end}}`.

  `else_body` is `nil` when the template has no `{{else}}`, which is not the
  same as an empty body: `{{if .A}}x{{else}}{{end}}` has `else_body: []`.

  `{{else if .B}}` is desugared the way Go desugars it, into an `If` whose
  `else_body` holds a single nested `If`. One `{{end}}` closes the chain.
  """

  @type t :: %__MODULE__{
          pipeline: Tend.Template.AST.Pipeline.t(),
          body: [Tend.Template.AST.tree_node()],
          else_body: [Tend.Template.AST.tree_node()] | nil,
          offset: non_neg_integer()
        }

  @enforce_keys [:pipeline, :body, :else_body, :offset]
  defstruct [:pipeline, :body, :else_body, :offset]
end

defmodule Tend.Template.AST.Range do
  @moduledoc """
  `{{range pipeline}}body{{else}}else_body{{end}}`.

  The loop variables, if any, live on the `pipeline`'s `decls`, exactly as
  they do in Go: `{{range $i, $o := .Outcomes}}` is a pipeline with two
  declarations and one command. `else_body` is `nil` when there is no
  `{{else}}`; Go runs it when the ranged value is empty.
  """

  @type t :: %__MODULE__{
          pipeline: Tend.Template.AST.Pipeline.t(),
          body: [Tend.Template.AST.tree_node()],
          else_body: [Tend.Template.AST.tree_node()] | nil,
          offset: non_neg_integer()
        }

  @enforce_keys [:pipeline, :body, :else_body, :offset]
  defstruct [:pipeline, :body, :else_body, :offset]
end

defmodule Tend.Template.AST.Pipeline do
  @moduledoc """
  A `|`-separated chain of commands, optionally preceded by declarations.

  `commands` is never empty -- an empty pipeline is a parse error
  ("missing value for ..."). Each command after the first is fed the previous
  one's value as its last argument.

  `decls` holds the variables a `:=` (or `=`) clause binds, in source order:
  `[]` for `{{.A}}`, `[$x]` for `{{$x := .A}}`, `[$i, $o]` for
  `{{range $i, $o := .Outcomes}}`. `assign?` is `true` for `=` (rebind an
  existing variable) and `false` for `:=` (declare a new one).

  A `Pipeline` is also the node for a parenthesised sub-expression, so
  `(ne .State "done")` appears as a `Pipeline` in the argument list of the
  enclosing command -- again, exactly as in Go.
  """

  @type t :: %__MODULE__{
          decls: [Tend.Template.AST.Variable.t()],
          assign?: boolean(),
          commands: [Tend.Template.AST.Command.t()],
          offset: non_neg_integer()
        }

  @enforce_keys [:decls, :assign?, :commands, :offset]
  defstruct [:decls, :assign?, :commands, :offset]
end

defmodule Tend.Template.AST.Command do
  @moduledoc """
  One stage of a pipeline: a head term and its arguments.

  `args` is never empty. `hd(args)` is what gets called or evaluated and the
  rest are its arguments, so `len .Subtasks` is a command with an
  `Identifier` head and one `Field` argument, while `.Task.Body` is a command
  with a single `Field` and no arguments.
  """

  @type t :: %__MODULE__{
          args: [Tend.Template.AST.operand()],
          offset: non_neg_integer()
        }

  @enforce_keys [:args, :offset]
  defstruct [:args, :offset]
end

defmodule Tend.Template.AST.Dot do
  @moduledoc """
  The cursor, `{{.}}` -- whatever the enclosing `range` (or the top-level
  data) currently points at.
  """

  @type t :: %__MODULE__{offset: non_neg_integer()}

  @enforce_keys [:offset]
  defstruct [:offset]
end

defmodule Tend.Template.AST.Field do
  @moduledoc """
  A field chain rooted at the cursor: `{{.Task.Body}}` is `path: ["Task", "Body"]`.

  `path` is never empty -- a bare `.` is `Tend.Template.AST.Dot`.
  """

  @type t :: %__MODULE__{path: [String.t()], offset: non_neg_integer()}

  @enforce_keys [:path, :offset]
  defstruct [:path, :offset]
end

defmodule Tend.Template.AST.Variable do
  @moduledoc """
  A variable reference, with any field chain hanging off it.

  `name` keeps Go's leading `$`, so `{{$o}}` is `name: "$o", path: []` and
  `{{$o.Title}}` is `name: "$o", path: ["Title"]`. `$` on its own is the
  always-in-scope root variable, `name: "$"`.

  Referring to a variable that is not in scope is a parse error, as it is in
  Go -- the parser tracks declarations rather than deferring to the renderer.
  """

  @type t :: %__MODULE__{
          name: String.t(),
          path: [String.t()],
          offset: non_neg_integer()
        }

  @enforce_keys [:name, :path, :offset]
  defstruct [:name, :path, :offset]
end

defmodule Tend.Template.AST.Identifier do
  @moduledoc """
  A bare word: the name of a function, such as `and`, `ne`, `not` or `len`.

  The parser does not check that the function exists -- it has no function
  table, and the built-ins land with the builtins sub-task. Go resolves names
  at parse time and says `function "bogus" not defined`; here an unknown name
  parses into an `Identifier` and is rejected later.
  """

  @type t :: %__MODULE__{name: String.t(), offset: non_neg_integer()}

  @enforce_keys [:name, :offset]
  defstruct [:name, :offset]
end

defmodule Tend.Template.AST.String do
  @moduledoc """
  A string literal. `value` is the *unquoted* text, so `"done"` carries
  `value: "done"` and `"a\\nb"` carries a real newline. `text` keeps the
  literal as written, quotes and all, for error messages.

  Both Go spellings parse to this: interpreted `"..."` literals, whose
  escapes are expanded, and raw `` `...` `` literals, which are taken
  verbatim except that carriage returns are dropped, as the Go spec requires.
  """

  @type t :: %__MODULE__{
          value: binary(),
          text: binary(),
          offset: non_neg_integer()
        }

  @enforce_keys [:value, :text, :offset]
  defstruct [:value, :text, :offset]
end

defmodule Tend.Template.AST.Number do
  @moduledoc """
  A numeric literal. `value` is an integer or a float; `text` is the literal
  as written, for error messages.

  Character constants (`{{'a'}}`) are numbers here too, as in Go: their value
  is the code point.

  `value` does not have Go's ranges. Go's `NumberNode` holds an `int64`, a
  `uint64` and a `float64` and falls back to the float when a literal does not
  fit an integer, so `{{9223372036854775808}}` is `9.223372036854776e18` there
  and an exact integer here, and `{{99999999999999999999}}`, which Go refuses
  outright with `integer overflow`, is an exact integer too. The renderer has
  to decide what those print as; nothing in the repo's prompts uses a literal
  anywhere near that size.
  """

  @type t :: %__MODULE__{
          value: integer() | float(),
          text: binary(),
          offset: non_neg_integer()
        }

  @enforce_keys [:value, :text, :offset]
  defstruct [:value, :text, :offset]
end

defmodule Tend.Template.AST.Bool do
  @moduledoc """
  The literals `true` and `false`.
  """

  @type t :: %__MODULE__{value: boolean(), offset: non_neg_integer()}

  @enforce_keys [:value, :offset]
  defstruct [:value, :offset]
end

defmodule Tend.Template.AST.Nil do
  @moduledoc """
  The literal `nil`. Go only allows it as a function argument; the renderer,
  not the parser, is where that matters.
  """

  @type t :: %__MODULE__{offset: non_neg_integer()}

  @enforce_keys [:offset]
  defstruct [:offset]
end

defmodule Tend.Template.AST do
  @moduledoc """
  The node set `Tend.Template.parse/1` produces. It is closed.

  Fourteen structs, and no others, can appear in a parsed template. The
  renderer and the built-in functions pattern-match on them exhaustively, so
  adding a fifteenth is a deliberate change to this list, not an accident:

  ### Tree nodes -- what a node list holds

    * `Tend.Template.AST.Text` -- literal source
    * `Tend.Template.AST.Action` -- `{{pipeline}}`
    * `Tend.Template.AST.If` -- `{{if}}` / `{{else}}` / `{{end}}`
    * `Tend.Template.AST.Range` -- `{{range}}` / `{{else}}` / `{{end}}`

  ### Structure

    * `Tend.Template.AST.Pipeline` -- declarations plus `|`-joined commands;
      also the node for a parenthesised sub-expression
    * `Tend.Template.AST.Command` -- a head term and its arguments

  ### Operands -- what a command's `args` holds

    * `Tend.Template.AST.Dot` -- `.`
    * `Tend.Template.AST.Field` -- `.Task.Body`
    * `Tend.Template.AST.Variable` -- `$o`, `$o.Title`, `$`
    * `Tend.Template.AST.Identifier` -- a function name
    * `Tend.Template.AST.String` -- `"done"`, `` `raw` ``
    * `Tend.Template.AST.Number` -- `1`, `-1.5`, `0x1f`, `'a'`
    * `Tend.Template.AST.Bool` -- `true`, `false`
    * `Tend.Template.AST.Nil` -- `nil`
    * `Tend.Template.AST.Pipeline` -- a parenthesised sub-expression

  Every node carries `offset`, the 0-based byte offset of the construct in
  the template source, so a render-time failure can point at the same place a
  parse failure would.

  ## Where this differs from Go's `text/template/parse`

    * Go's `parse.Tree` also has `ChainNode`, `WithNode`, `TemplateNode`,
      `CommentNode`, `BreakNode` and `ContinueNode`. None of them appear in
      any stored prompt or fixture, and `Tend.Template` rejects all of them
      with a parse error that names the construct -- including `ChainNode`,
      which covers both `{{len.A}}` and `{{(.A).B}}`. `Tend.Template`'s
      "What is not, and why" is the full list, with the reasoning.
    * Go's `FieldNode` for a chained field such as `.Task.Body` carries the
      position of the *last* `.` in the chain, an artefact of how it merges
      chains. `offset` here is the position of the first `.`, which is what
      an error message wants to underline.
  """

  @typedoc "A node that can appear in a template's node list."
  @type tree_node ::
          Tend.Template.AST.Text.t()
          | Tend.Template.AST.Action.t()
          | Tend.Template.AST.If.t()
          | Tend.Template.AST.Range.t()

  @typedoc "A node that can appear in a command's argument list."
  @type operand ::
          Tend.Template.AST.Dot.t()
          | Tend.Template.AST.Field.t()
          | Tend.Template.AST.Variable.t()
          | Tend.Template.AST.Identifier.t()
          | Tend.Template.AST.String.t()
          | Tend.Template.AST.Number.t()
          | Tend.Template.AST.Bool.t()
          | Tend.Template.AST.Nil.t()
          | Tend.Template.AST.Pipeline.t()

  @typedoc "Any node in the closed set. `Pipeline` arrives via `operand/0`."
  @type t :: tree_node() | operand() | Tend.Template.AST.Command.t()
end
