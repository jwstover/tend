defmodule Tend.Template do
  @moduledoc """
  The Go `text/template` subset that `prompt_md` is written in.

  Workflow step prompts in a user's `tend.db` are Go templates -- they were
  written against `internal/workflow/prompt.go`, which parses them with
  `template.New("prompt").Option("missingkey=error").Parse/1` and renders them
  against a `PromptData` struct. Stored rows must keep working untouched, so
  the Elixir port speaks Go's template language rather than migrating anyone
  to EEx.

  This module is part one of three: the lexer and the parser. Nothing
  evaluates a template yet, so `parse/1` has no caller in the tree -- it is a
  pure internal API with its own tests and no runtime side effects. The
  renderer and the built-in functions land in their own sub-tasks and
  pattern-match on the node set in `Tend.Template.AST`, which is closed and
  documented for exactly that reason.

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
      pipeline) are out. These are the only constructs Go accepts and this
      refuses; a repo-wide sweep found zero of either, and supporting them
      would add a node type to the closed set in `Tend.Template.AST` for the
      renderer and the built-ins to carry.
    * Imaginary and hex-float literals.

  Each of these is a parse error that names the construct, so a prompt using
  one is refused loudly rather than rendered wrongly.

  Everything else is *more* permissive than Go, never less, so no prompt Go
  accepts is refused here. `{{99999999999999999999}}` is an arbitrary-precision
  integer where Go reports `integer overflow`; `{{1_}}` and `{{-.}}` parse
  where Go reports `illegal number syntax`; a non-ASCII byte counts as a
  letter in an identifier, where Go asks `unicode.IsLetter`.

  ## Errors

  `parse/1` returns `{:error, %Tend.Template.ParseError{}}`, carrying the
  offending token and its byte offset. `Exception.message/1` formats it the
  way the Go tree's `ErrInvalidPrompt` surfaces `text/template`'s own
  message, which the TUI's validate action shows verbatim:

      iex> {:error, error} = Tend.Template.parse("{{end}}")
      iex> Exception.message(error)
      "template: prompt:1:3: unexpected {{end}} at byte 2"
  """

  alias Tend.Template.AST
  alias Tend.Template.Lexer
  alias Tend.Template.ParseError
  alias Tend.Template.Parser

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
end
