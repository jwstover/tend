defmodule Tend.Template do
  @moduledoc """
  The Go `text/template` subset that `prompt_md` is written in.

  Workflow step prompts in a user's `tend.db` are Go templates -- they were
  written against `internal/workflow/prompt.go`, which parses them with
  `template.New("prompt").Option("missingkey=error").Parse/1` and renders them
  against a `PromptData` struct. Stored rows must keep working untouched, so
  the Elixir port speaks Go's template language rather than migrating anyone
  to EEx.

  The subset is complete: the lexer and parser, the renderer, and the
  built-in functions the prompts call. `render/2` has no caller in the tree
  yet -- the workflow step prompts that will call it land with their own
  sub-task -- so this is still a pure internal API with its own tests and no
  runtime side effects.

  Its claim to be Go's language is pinned rather than asserted: every `want`
  in `test/tend/template/` was captured from Go's own `text/template` and is
  asserted byte for byte. On top of that, `Tend.Template.Parity` renders every
  `prompt_md` in the repo's Go tests -- and, on demand, every row in a real
  user's `tend.db` -- through both engines and diffs the bytes:

      cd elixir && MIX_ENV=test mix tend.parity --db ~/.local/share/tend/tend.db

  `mix test` runs the same corpus against a recording of Go's output, and
  prints at the end of every suite which halves of it actually ran. What that
  corpus can and cannot reach is in `Tend.Template.Parity.Data`.

  ## What is supported

  Everything the stored prompts and the Go test suite actually use, plus the
  rest of the language's core grammar:

      Hello {{.Task.Title}}                      field chains
      {{.}}                                      the cursor, inside a range
      {{if .Feedback}}...{{else}}...{{end}}      conditionals, incl. {{else if}}
      {{range .Subtasks}}...{{end}}              iteration, with an optional {{else}}
      {{range $i, $o := .Outcomes}}{{$o}}{{end}} index and value variables
      {{$x := .Task.Body}}{{$x}}                 declarations and references
      {{if and (ne .State "done") (not .IsBlocked)}}
                                                 calls and parenthesised sub-expressions
      {{.Outcomes | len}}                        pipelines
      {{- .Input -}}                             trim markers
      1  -1.5  0x1f  'a'  "s"  `raw`  true  nil  literals

  Everything in that table renders, and renders Go's bytes -- with one
  exception, which is a float literal large or small enough that Go's `%v`
  switches to an exponent. That happens at a million, not at some absurd
  magnitude: `{{1e6}}` is `1e+06` in Go and `1000000` here.
  `Tend.Template.Value`'s `format_float/1` has the table and the reason it is
  not worth porting `strconv` for; no field of the prompt data is a float, so
  only a literal written into a prompt can reach it.

  The built-ins are `and`, `or`, `not`, `eq`, `ne` and `len` --
  `Tend.Template.Funcs` has them, and has the warning that Go's `and` and
  `or` return the argument that decided rather than a boolean. Variables
  follow Go's stack discipline, including an inner `{{$x := ...}}` shadowing
  an outer one for the length of its block; `Tend.Template.Renderer`
  describes it.

  ## What is not, and why

    * `{{template}}`, `{{define}}` and `{{block}}` -- a prompt is one
      anonymous template; nothing in the tree composes them.
    * `{{with}}` -- unused by every prompt and fixture in the repo.
    * `{{break}}` and `{{continue}}` -- likewise.
    * `{{/* comments */}}` -- the sub-task says to add these only if a stored
      prompt or fixture uses one. Nothing in the repo does, so they are out,
      and `{{/*` gets the same "unrecognized character in action" that Go
      would give a stray `/` anywhere else.
    * A field chain whose head is neither a field nor a variable -- Go's
      `ChainNode`. `{{.A.B}}` and `{{$x.A}}` are in; `{{len.A}}` (a chain off
      a function name) and `{{(.A).B}}` (a chain off a parenthesised
      pipeline) are out. These are the only *syntax* Go accepts and this
      refuses; a repo-wide sweep found zero of either, and supporting them
      would add a node type to the closed set in `Tend.Template.AST` for the
      renderer and the built-ins to carry.
    * Imaginary and hex-float literals.

  Each of these is a parse error that names the construct, so a prompt using
  one is refused loudly rather than rendered wrongly.

  One more thing is out, and it fails at render rather than at parse: Go's
  other built-in functions -- the four comparisons `lt`, `le`, `gt` and `ge`,
  plus `index`, `slice`, `printf`, `print`, `println`, `html`, `js`,
  `urlquery` and `call`. No template in the Go tree calls one -- a sweep of
  every `{{...}}` under `internal/` finds `and`, `ne`, `not` and `len` and
  nothing else -- and the sub-task that added the built-ins said to implement
  only what the prompts use. A template that calls one gets
  `function "printf" not defined`, which is Go's wording for a name *it* does
  not have; adding one is a clause in `Tend.Template.Funcs`.

  The comparisons are the ones to watch, because they are the plausible
  ones: `{{if gt .Iteration 1}}` is a `prompt_md` somebody may already have
  stored, and it is refused here. `eq` and `ne` are implemented; their four
  ordering siblings are not.

  Everything else is *more* permissive than Go, never less, so no prompt Go
  parses is refused here. `{{99999999999999999999}}` is an arbitrary-precision
  integer where Go reports `integer overflow`; `{{1_}}` and `{{-.}}` parse
  where Go reports `illegal number syntax`; a non-ASCII byte counts as a
  letter in an identifier, where Go asks `unicode.IsLetter`.

  ## Errors

  `parse/1` returns `{:error, %Tend.Template.ParseError{}}`, carrying the
  offending token and its byte offset. `Exception.message/1` keeps the shape
  the Go tree's `ErrInvalidPrompt` surfaces -- the string the TUI's validate
  action shows verbatim -- and adds the column and the offset, which Go does
  not report. `Tend.Template.ParseError` says why:

      iex> {:error, error} = Tend.Template.parse("{{end}}")
      iex> Exception.message(error)
      "template: prompt:1:3: unexpected {{end}} at byte 2"

  `render/2` returns either of the two, since it parses first:
  `Tend.Template.ParseError` for a template that never had a chance, and
  `Tend.Template.RenderError` for one that does not fit its data. The Go tree
  wraps both in the same `ErrInvalidPrompt`, and the sub-task that ports
  `RenderPrompt` will do the same with one `Tend.Error`.

  ## Rendering against Elixir data

  Go renders against a `PromptData` struct; this renders against any struct
  or map, resolving `{{.Task.Title}}` to a `"Title"` string key, a `:Title`
  atom key or an underscored `:title` one, first hit winning.
  `Tend.Template.Renderer` documents the rule and the two shapes the data has
  to keep for the output to stay Go's: an empty string is `""` and never
  `nil`, an empty list is `[]` and never `nil`. It is *printing* that tells
  the two apart, not `{{range}}` -- `{{range}}` over a `nil` takes the
  `{{else}}` arm, as it does in Go.
  """

  alias Tend.Template.AST
  alias Tend.Template.Lexer
  alias Tend.Template.ParseError
  alias Tend.Template.Parser
  alias Tend.Template.RenderError
  alias Tend.Template.Renderer

  @doc """
  Parses a `prompt_md` template into a `Tend.Template.AST` node list.

  A template with no actions parses to a single `Tend.Template.AST.Text`
  equal to the input, byte for byte; an empty template parses to `[]`.

      iex> Tend.Template.parse("Run the tests.")
      {:ok, [%Tend.Template.AST.Text{text: "Run the tests.", offset: 0}]}

      iex> {:error, error} = Tend.Template.parse("{{.Task.Title")
      iex> {error.token, error.offset}
      {"", 13}
  """
  @spec parse(binary()) :: {:ok, [AST.tree_node()]} | {:error, ParseError.t()}
  def parse(source) when is_binary(source) do
    {:ok, parse!(source)}
  rescue
    error in ParseError -> {:error, error}
  end

  @doc """
  Same as `parse/1`, but raises `Tend.Template.ParseError` instead of
  returning it.
  """
  @spec parse!(binary()) :: [AST.tree_node()]
  def parse!(source) when is_binary(source) do
    Parser.parse(source, Lexer.tokenize(source))
  end

  @doc """
  Parses `source` and renders it against `data`.

  This is `internal/workflow`'s `RenderPrompt` without the `PromptData`: any
  struct or map will do, and a name the data does not have is an error rather
  than an empty string.

      iex> Tend.Template.render("Task {{.Task.ID}}: {{.Task.Title}}", %{task: %{id: 42, title: "Fix it"}})
      {:ok, "Task 42: Fix it"}

      iex> {:error, error} = Tend.Template.render("Hello {{.Nope}}", %{name: "me"})
      iex> Exception.message(error)
      ~s(template: prompt:1:9: executing "prompt" at <.Nope>: map has no entry for key "Nope")
  """
  @spec render(binary(), term()) ::
          {:ok, binary()} | {:error, ParseError.t() | RenderError.t()}
  def render(source, data) when is_binary(source) do
    {:ok, render!(source, data)}
  rescue
    error in [ParseError, RenderError] -> {:error, error}
  end

  @doc """
  Renders an already-parsed `nodes`, which came from `source`, against `data`.

  `internal/workflow`'s `ValidatePrompt` parses a prompt once and executes it
  *twice* -- against a zero `PromptData` and a full one, so that a field
  missing from either branch is caught. This is the entry point that lets the
  Elixir port do the same without parsing twice or reaching past this module
  into `Tend.Template.Renderer`. `source` is only used to position an error.

      iex> {:ok, nodes} = Tend.Template.parse("{{.Cwd}}")
      iex> Tend.Template.render("{{.Cwd}}", nodes, %{cwd: "/tmp"})
      {:ok, "/tmp"}
  """
  @spec render(binary(), [AST.tree_node()], term()) ::
          {:ok, binary()} | {:error, RenderError.t()}
  def render(source, nodes, data) when is_binary(source) and is_list(nodes) do
    {:ok, render!(source, nodes, data)}
  rescue
    error in RenderError -> {:error, error}
  end

  @doc """
  Same as `render/2`, but raises `Tend.Template.ParseError` or
  `Tend.Template.RenderError` instead of returning it.
  """
  @spec render!(binary(), term()) :: binary()
  def render!(source, data) when is_binary(source) do
    render!(source, parse!(source), data)
  end

  @doc """
  Same as `render/3`, but raises `Tend.Template.RenderError` instead of
  returning it.
  """
  @spec render!(binary(), [AST.tree_node()], term()) :: binary()
  def render!(source, nodes, data) when is_binary(source) and is_list(nodes) do
    Renderer.render(source, nodes, data)
  end
end
