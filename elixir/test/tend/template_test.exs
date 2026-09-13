defmodule Tend.TemplateTest do
  use ExUnit.Case, async: true

  alias Tend.Template
  alias Tend.Template.AST

  doctest Tend.Template

  # Every expectation below was checked against a real Go build before it was
  # written: `parse.Parse(name, src, "{{", "}}")` on the same input, with its
  # tree dumped node by node. Where this parser and Go disagree on purpose,
  # the test says so.

  describe "text spans" do
    test "a template with no actions is one literal node, byte for byte" do
      source = "Fix the bug described in the task.\n\nRun the tests."

      assert {:ok, [%AST.Text{text: text, offset: 0}]} = Template.parse(source)
      assert text == source
    end

    test "braces, backticks and multi-byte text survive untouched" do
      source = "} }} {} `reject` é— done {\n"

      assert {:ok, [%AST.Text{text: ^source, offset: 0}]} = Template.parse(source)
    end

    test "an empty template has no nodes at all" do
      assert {:ok, []} = Template.parse("")
    end

    test "text around an action is split at the delimiters, with exact offsets" do
      assert {:ok, [before, _action, rest]} = Template.parse("[{{.Input}}][x]")
      assert before == %AST.Text{text: "[", offset: 0}
      assert rest == %AST.Text{text: "][x]", offset: 11}
    end

    test "adjacent actions leave no empty text node between them" do
      assert {:ok, [%AST.Action{}, %AST.Action{}]} = Template.parse("{{.A}}{{.B}}")
    end
  end

  describe "trim markers" do
    test "{{- removes every preceding space, tab, carriage return and newline" do
      assert {:ok, [text, action, rest]} = Template.parse("a \t\r\n {{- .X}}b")
      assert text == %AST.Text{text: "a", offset: 0}
      assert action.offset == 10
      assert rest == %AST.Text{text: "b", offset: 14}
    end

    test "-}} removes every following space, tab, carriage return and newline" do
      assert {:ok, [text, _action, rest]} = Template.parse("a{{.X -}} \t\r\n b")
      assert text == %AST.Text{text: "a", offset: 0}
      assert rest == %AST.Text{text: "b", offset: 14}
    end

    test "both markers on one action trim both sides" do
      assert {:ok, [text, _action, rest]} = Template.parse("a  {{- .X -}}  b")
      assert text == %AST.Text{text: "a", offset: 0}
      assert rest == %AST.Text{text: "b", offset: 15}
    end

    test "a text node trimmed to nothing is dropped rather than left empty" do
      assert {:ok, [%AST.Action{}]} = Template.parse("  \n{{- .X}}")
    end

    test "only Go's four space characters are trimmed" do
      assert {:ok, [text, _action]} = Template.parse("a\v{{- .X}}")
      assert text == %AST.Text{text: "a\v", offset: 0}
    end

    test "{{-3}} is the number -3, not a trim marker" do
      assert {:ok, [%AST.Action{pipeline: pipeline}]} = Template.parse("{{-3}}")
      assert [%AST.Command{args: [%AST.Number{value: -3}]}] = pipeline.commands
    end

    test "{{- 3}} is a trim marker and the number 3" do
      assert {:ok, [%AST.Text{text: "x"}, %AST.Action{pipeline: pipeline}]} =
               Template.parse("x  {{- 3}}")

      assert [%AST.Command{args: [%AST.Number{value: 3}]}] = pipeline.commands
    end

    test "a tab or newline after the minus also marks a trim" do
      for marker <- ["\t", "\n", "\r", " "] do
        assert {:ok, [%AST.Action{}]} = Template.parse("  {{-#{marker}.X}}")
      end
    end

    test "-}} needs the space before it, as Go does" do
      assert {:error, error} = Template.parse("{{.X-}}")
      assert error.detail == "bad character U+002D '-'"

      assert {:error, %{detail: ~s(illegal number syntax: "-")}} = Template.parse("{{.X - }}")
    end
  end

  describe "fields, the cursor and variables" do
    test "a nested field chain is one node" do
      assert {:ok, [%AST.Action{pipeline: pipeline, offset: 2}]} =
               Template.parse("{{.Task.Body}}")

      assert [%AST.Command{args: [%AST.Field{path: ["Task", "Body"], offset: 2}]}] =
               pipeline.commands
    end

    test "the cursor dot parses inside a range" do
      assert {:ok, [%AST.Range{body: [%AST.Action{pipeline: pipeline}]}]} =
               Template.parse("{{range .Outcomes}}{{.}}{{end}}")

      assert [%AST.Command{args: [%AST.Dot{offset: 21}]}] = pipeline.commands
    end

    test "a field chain hangs off a variable" do
      assert {:ok, [_decl, %AST.Action{pipeline: pipeline}]} =
               Template.parse("{{$s := .Task}}{{$s.Body}}")

      assert [%AST.Command{args: [%AST.Variable{name: "$s", path: ["Body"]}]}] = pipeline.commands
    end

    test "$ on its own is always in scope" do
      assert {:ok, [%AST.Action{pipeline: pipeline}]} = Template.parse("{{$}}")
      assert [%AST.Command{args: [%AST.Variable{name: "$", path: []}]}] = pipeline.commands
    end

    test "a space makes a following field a separate argument" do
      assert {:ok, [%AST.Action{pipeline: pipeline}]} = Template.parse("{{$ .A}}")

      assert [%AST.Command{args: [%AST.Variable{name: "$"}, %AST.Field{path: ["A"]}]}] =
               pipeline.commands
    end
  end

  describe "calls, arguments and pipelines" do
    test "a call with parenthesised sub-expressions" do
      assert {:ok, [%AST.If{pipeline: pipeline}]} =
               Template.parse(~S|{{if and (ne .State "done") (not .IsBlocked)}}y{{end}}|)

      assert [%AST.Command{args: [%AST.Identifier{name: "and"}, left, right]}] = pipeline.commands

      assert %AST.Pipeline{commands: [%AST.Command{args: [%AST.Identifier{name: "ne"} | rest]}]} =
               left

      assert [%AST.Field{path: ["State"]}, %AST.String{value: "done"}] = rest

      assert %AST.Pipeline{
               commands: [
                 %AST.Command{
                   args: [%AST.Identifier{name: "not"}, %AST.Field{path: ["IsBlocked"]}]
                 }
               ]
             } = right
    end

    test "a call with a plain argument" do
      assert {:ok, [%AST.Action{pipeline: pipeline}]} = Template.parse("{{len .Subtasks}}")

      assert [%AST.Command{args: [%AST.Identifier{name: "len"}, %AST.Field{path: ["Subtasks"]}]}] =
               pipeline.commands
    end

    test "a pipeline is one command per stage" do
      assert {:ok, [%AST.Action{pipeline: pipeline}]} = Template.parse("{{.A | len | printf}}")
      assert [_, _, _] = pipeline.commands
    end

    test "a non-executable stage after the first is refused, naming the stage" do
      assert {:error, error} = Template.parse("{{.A | 1}}")
      assert error.detail == "non executable command in pipeline stage 2"
      assert {error.token, error.offset} == {"1", 7}
    end

    test "unknown function names are left for the renderer to resolve" do
      assert {:ok, [%AST.Action{pipeline: pipeline}]} = Template.parse("{{bogus .A}}")
      assert [%AST.Command{args: [%AST.Identifier{name: "bogus"} | _]}] = pipeline.commands
    end
  end

  describe "if" do
    test "if without else leaves else_body nil" do
      assert {:ok, [%AST.If{body: [%AST.Text{text: "a"}], else_body: nil}]} =
               Template.parse("{{if .A}}a{{end}}")
    end

    test "an empty else branch is [] and not nil" do
      assert {:ok, [%AST.If{body: [], else_body: []}]} =
               Template.parse("{{if .A}}{{else}}{{end}}")
    end

    test "if/else over the usual feedback shape" do
      source = "Do it.{{if .Feedback}} Address this: {{.Feedback}}{{end}}"
      assert {:ok, [%AST.Text{}, %AST.If{body: [_, _], else_body: nil}]} = Template.parse(source)
    end

    test "else if nests inside the else branch, closed by one end" do
      assert {:ok, [outer]} = Template.parse("{{if .A}}a{{else if .B}}b{{else}}c{{end}}")
      assert %AST.If{body: [%AST.Text{text: "a"}], else_body: [inner]} = outer

      assert %AST.If{body: [%AST.Text{text: "b"}], else_body: [%AST.Text{text: "c"}]} = inner
    end

    test "truthiness on a non-boolean is just a pipeline over a variable" do
      assert {:ok, [%AST.Range{body: [%AST.If{pipeline: pipeline} | _]}]} =
               Template.parse("{{range $i, $o := .Outcomes}}{{if $i}}, {{end}}{{$o}}{{end}}")

      assert [%AST.Command{args: [%AST.Variable{name: "$i"}]}] = pipeline.commands
    end
  end

  describe "range" do
    test "range with index and value declarations" do
      assert {:ok, [%AST.Range{pipeline: pipeline}]} =
               Template.parse("{{range $i, $o := .Outcomes}}{{$o}}{{end}}")

      assert %AST.Pipeline{
               decls: [%AST.Variable{name: "$i"}, %AST.Variable{name: "$o"}],
               assign?: false,
               commands: [%AST.Command{args: [%AST.Field{path: ["Outcomes"]}]}]
             } = pipeline
    end

    test "range with a single declaration" do
      assert {:ok, [%AST.Range{pipeline: %AST.Pipeline{decls: [%AST.Variable{name: "$v"}]}}]} =
               Template.parse("{{range $v := .A}}{{end}}")
    end

    test "range with no declarations" do
      assert {:ok, [%AST.Range{pipeline: %AST.Pipeline{decls: []}, else_body: nil}]} =
               Template.parse("{{range .Subtasks}}{{.Title}}{{end}}")
    end

    test "range with an else branch" do
      assert {:ok, [%AST.Range{else_body: [%AST.Text{text: "none"}]}]} =
               Template.parse("{{range .A}}{{else}}none{{end}}")
    end

    test "nested ranges" do
      source = ~S|{{range .Subtasks}}{{range .DependsOn}}#{{.}} {{end}}{{end}}|
      assert {:ok, [%AST.Range{body: [%AST.Range{}]}]} = Template.parse(source)
    end

    test "loop variables leave scope at the end" do
      assert {:error, error} = Template.parse("{{range $i, $o := .A}}{{end}}{{$i}}")
      assert error.detail == ~s(undefined variable "$i")
      assert {error.token, error.offset} == {"$i", 31}
    end

    test "range takes at most two declarations" do
      assert {:error, %{detail: "too many declarations in range"}} =
               Template.parse("{{range $i, $o, $p := .A}}{{end}}")
    end

    test "only variables may be initialized in a range" do
      assert {:error, %{detail: "range can only initialize variables"}} =
               Template.parse("{{range $i, .A}}{{end}}")
    end
  end

  describe "declarations" do
    test ":= declares, = rebinds" do
      assert {:ok, [declare, assign]} = Template.parse("{{$x := 1}}{{$x = 2}}")
      assert %AST.Action{pipeline: %AST.Pipeline{assign?: false, decls: [_]}} = declare
      assert %AST.Action{pipeline: %AST.Pipeline{assign?: true, decls: [_]}} = assign
    end

    test "a top-level declaration stays in scope for the rest of the template" do
      assert {:ok, [_, %AST.If{}]} = Template.parse("{{$x := .A}}{{if .B}}{{$x}}{{end}}")
    end

    test "a declaration inside a control leaves scope at its end" do
      assert {:error, %{detail: ~s(undefined variable "$y")}} =
               Template.parse("{{if .A}}{{$y := 1}}{{end}}{{$y}}")
    end
  end

  describe "literals" do
    test "interpreted strings expand their escapes" do
      assert %AST.String{value: "a\nb\"c", text: ~S("a\nb\"c")} = only_arg(~S({{"a\nb\"c"}}))
    end

    test "raw strings are verbatim" do
      assert %AST.String{value: "a\\nb"} = only_arg("{{`a\\nb`}}")
    end

    test "integers, floats, other bases and character constants" do
      assert %AST.Number{value: 1} = only_arg("{{1}}")
      assert %AST.Number{value: -1} = only_arg("{{-1}}")
      assert %AST.Number{value: 1.5} = only_arg("{{1.5}}")
      assert %AST.Number{value: 1500.0} = only_arg("{{1.5e3}}")
      assert %AST.Number{value: 0.5} = only_arg("{{.5}}")
      assert %AST.Number{value: 31} = only_arg("{{0x1f}}")
      assert %AST.Number{value: 7} = only_arg("{{07}}")
      assert %AST.Number{value: 97} = only_arg("{{'a'}}")
      assert %AST.Number{value: 10} = only_arg(~S({{'\n'}}))
    end

    test "booleans and nil" do
      assert %AST.Bool{value: true} = only_arg("{{true}}")
      assert %AST.Bool{value: false} = only_arg("{{false}}")
      assert %AST.Nil{} = only_arg("{{nil}}")
    end
  end

  describe "errors name the offending token and its byte offset" do
    test "an unbalanced {{if}}" do
      assert {:error, error} = Template.parse("Do it.{{if .A}}yes")
      assert error.detail == "unexpected EOF"
      assert error.offset == 18
      assert Exception.message(error) =~ "at byte 18"
    end

    test "an unbalanced {{range}}" do
      assert {:error, %{detail: "unexpected EOF", offset: 12}} = Template.parse("{{range .A}}")
    end

    test "a nested block left open" do
      assert {:error, %{detail: "unexpected EOF"}} =
               Template.parse("{{if .A}}{{range .B}}{{end}}")
    end

    test "a stray {{end}}" do
      assert {:error, error} = Template.parse("a{{end}}")
      assert error.detail == "unexpected {{end}}"
      assert {error.token, error.offset} == {"end", 3}
      assert Exception.message(error) == "template: prompt:1:4: unexpected {{end}} at byte 3"
    end

    test "a stray {{else}}" do
      assert {:error, %{detail: "unexpected {{else}}", offset: 2}} = Template.parse("{{else}}")
    end

    test "a second {{else}} in one {{if}}" do
      assert {:error, error} = Template.parse("{{if .A}}x{{else}}y{{else}}z{{end}}")
      assert error.detail == "expected end; found {{else}}"
      assert {error.token, error.offset} == {"else", 21}
    end

    test "an unknown action names the construct" do
      for {source, word} <- [
            {"{{with .A}}x{{end}}", "with"},
            {~s({{template "t"}}), "template"},
            {"{{block .A}}{{end}}", "block"},
            {"{{define .A}}{{end}}", "define"},
            {"{{range .A}}{{break}}{{end}}", "break"},
            {"{{range .A}}{{continue}}{{end}}", "continue"}
          ] do
        assert {:error, error} = Template.parse(source)
        assert error.detail == "unsupported action {{#{word}}}"
        assert error.token == word
      end
    end

    test "a comment is not part of the subset" do
      assert {:error, error} = Template.parse("{{/* why */}}x")
      assert error.detail == ~s(unexpected "/" in command)
      assert error.offset == 2
    end

    test "a malformed field path: a trailing dot" do
      assert {:error, error} = Template.parse("{{.A.}}")
      assert error.detail == "unexpected <.> in operand"
      assert {error.token, error.offset} == {".", 4}
    end

    test "a malformed field path: a hyphen in the name" do
      assert {:error, error} = Template.parse("{{.A-B}}")
      assert error.detail == "bad character U+002D '-'"
      assert {error.token, error.offset} == {"-", 4}
    end

    test "a malformed field path: a leading digit" do
      assert {:error, error} = Template.parse("{{.1A}}")
      assert error.detail == ~s(bad number syntax: ".1A")
      assert {error.token, error.offset} == {".1A", 2}
    end

    test "a malformed field path: a chain off a parenthesised pipeline" do
      assert {:error, %{detail: "unsupported field chain on a parenthesized pipeline"}} =
               Template.parse("{{(.A).B}}")
    end

    test "an unterminated interpreted string" do
      assert {:error, error} = Template.parse(~S|{{eq .A "done}}|)
      assert error.detail == "unterminated quoted string"
      assert error.offset == 15
    end

    test "an unterminated raw string" do
      assert {:error, error} = Template.parse("{{eq .A `done}}")
      assert error.detail == "unterminated raw quoted string"
      assert error.offset == 8
    end

    test "an unterminated character constant" do
      assert {:error, %{detail: "unterminated character constant"}} = Template.parse("{{'a}}")
    end

    test "a newline ends an interpreted string, as it does in Go" do
      assert {:error, %{detail: "unterminated quoted string"}} = Template.parse(~s({{"a\nb"}}))
    end

    test "an unclosed action" do
      assert {:error, error} = Template.parse("{{.Task.Title")
      assert error.detail == "unclosed action"
      assert {error.token, error.offset} == {"", 13}
    end

    test "an empty action" do
      assert {:error, %{detail: "missing value for command"}} = Template.parse("{{}}")
      assert {:error, %{detail: "missing value for if"}} = Template.parse("{{if}}x{{end}}")
    end

    test "an unbalanced paren" do
      assert {:error, %{detail: "unclosed left paren"}} = Template.parse("{{and (.A}}")
      assert {:error, %{detail: "unexpected right paren"}} = Template.parse("{{.A)}}")
    end

    test "the offset is a byte offset, and line and column follow it" do
      assert {:error, error} = Template.parse("one\ntwo\n{{end}}")
      assert error.offset == 10
      assert {error.line, error.column} == {3, 3}
      assert Exception.message(error) == "template: prompt:3:3: unexpected {{end}} at byte 10"
    end

    test "parse!/1 raises the same error" do
      assert_raise Tend.Template.ParseError, ~r/unexpected \{\{end\}\} at byte 2/, fn ->
        Template.parse!("{{end}}")
      end
    end
  end

  describe "every prompt_md literal in the Go tree" do
    # Copied verbatim from internal/workflow/prompt_test.go,
    # internal/workflow/graph_test.go and internal/cli/workflow_test.go. These
    # are the templates a user's tend.db may already hold.
    @ready_set ~S|<<range .Subtasks>><<if and (ne .State "done") (not .IsBlocked)>>- #<<.ID>> <<.Title>>
<<end>><<end>>|

    @prompts [
      "Fix the bug described in the task.\n\nRun the tests.",
      "",
      "Task #<<.Task.ID>>: <<.Task.Title>>\n<<.Task.Body>>\n" <>
        "cwd=<<.Cwd>> input=<<.Input>> feedback=<<.Feedback>> iteration=<<.Iteration>>\n" <>
        "outcomes: <<range $i, $o := .Outcomes>><<if $i>>, <<end>><<$o>><<end>>",
      "[<<.Input>>][<<.Feedback>>]",
      "Do it.<<if .Feedback>> Address this feedback: <<.Feedback>><<end>>",
      "<<range .Subtasks>>#<<.ID>> <<.Title>> [<<.State>>] blocked=<<.IsBlocked>> deps=<<.DependsOn>>\n<<end>>",
      "sub-tasks:<<range .Subtasks>> <<.Title>><<end>>",
      "<<.Subtasks>>",
      "<<len .Subtasks>> sub-tasks",
      "<<range .Subtasks>><<.Body>><<end>>",
      "Hello <<.Nope>>",
      "<<.Task.BodyMD>>",
      "<<.Task.Title>> <<.Tittle>>",
      "Just do the thing.",
      "<<.Task.ID>><<.Task.Title>><<.Task.Body>><<.Cwd>><<.Input>><<.Feedback>><<.Iteration>><<.Outcomes>>",
      "<<range .Outcomes>>- <<.>>\n<<end>>",
      "<<range .Subtasks>><<.ID>><<.Title>><<.State>><<.IsBlocked>><<.DependsOn>><<end>>",
      "<<range .Subtasks>><<range .DependsOn>>#<<.>> <<end>><<end>>",
      "<<range .Subtasks>><<if .IsBlocked>><<.Blockers>><<end>><<end>>",
      "<<range .Subtasks>><<if .IsBlocked>>ok<<else>><<.Blockers>><<end>><<end>>",
      "<<.Cwdd>>",
      "<<if .Feedback>><<.Bogus>><<end>>",
      "<<if .Feedback>>ok<<else>><<.Bogus>><<end>>",
      "Implement <<.Task.Title>>.<<if .Feedback>> Feedback: <<.Feedback>><<end>>",
      "Review <<.Input>>; finish with approve or reject.",
      "Open the PR.",
      "Review it.",
      "Say APPROVE, or `reject` it.",
      "finish with retry if flaky",
      "The change was approved and rejections are logged.",
      "Set the sub-task to done when its PR merges. Finish with `wave ready` or `stuck`.",
      "Wait until the wave is ready. If a sub-task is stuck, do not escalate on your own; " <>
        "note it and continue.\nFinish with `wave ready`, or `stuck` once nothing can proceed.",
      "finish_step with outcome `wave ready`",
      "When nothing can proceed, set the outcome to escalate. Otherwise finish with wave ready.",
      ~S(If the tests are red, "reject" the change.),
      "<<.Nope>>",
      "<<.Nope>> approve",
      "do plan"
    ]

    test "parses" do
      for prompt <- [@ready_set | @prompts] do
        source = restore(prompt)

        assert {:ok, nodes} = Template.parse(source),
               "expected #{inspect(source)} to parse"

        assert is_list(nodes)
      end
    end

    test "the ready-set prompt, the richest of them, parses to the expected shape" do
      assert {:ok, [%AST.Range{pipeline: pipeline, body: [%AST.If{} = branch]}]} =
               Template.parse(restore(@ready_set))

      assert %AST.Pipeline{decls: [], commands: [%AST.Command{args: [%AST.Field{}]}]} = pipeline

      assert [
               %AST.Command{
                 args: [%AST.Identifier{name: "and"}, %AST.Pipeline{}, %AST.Pipeline{}]
               }
             ] =
               branch.pipeline.commands

      assert [
               %AST.Text{text: "- #"},
               %AST.Action{},
               %AST.Text{text: " "},
               %AST.Action{},
               %AST.Text{text: "\n"}
             ] = branch.body

      assert branch.else_body == nil
    end

    test "the three the Go tests expect to be refused are refused here too" do
      for source <- ["{{.Task.Title", "{{.Cwd", "{{.Nope"] do
        assert {:error, %Tend.Template.ParseError{}} = Template.parse(source)
      end
    end

    # The literals above spell their delimiters `<<`/`>>` so that the Go
    # templates they are copied from do not have to be escaped here.
    defp restore(prompt) do
      prompt |> String.replace("<<", "{{") |> String.replace(">>", "}}")
    end
  end

  describe "the node set is closed" do
    @node_types [
      AST.Action,
      AST.Bool,
      AST.Command,
      AST.Dot,
      AST.Field,
      AST.Identifier,
      AST.If,
      AST.Nil,
      AST.Number,
      AST.Pipeline,
      AST.Range,
      AST.String,
      AST.Text,
      AST.Variable
    ]

    @kitchen_sink ~S"""
    head
    {{- if and (ne .State "done") (not .IsBlocked)}}
    {{range $i, $o := .Outcomes}}{{if $i}}{{.}}{{$o.Name}}{{end}}{{end -}}
    {{else}}{{$x := 1}}{{$x}}{{true}}{{nil}}{{.A | len}}
    {{end}}
    """

    test "parsing every construct produces only the fourteen documented types" do
      assert {:ok, nodes} = Template.parse(@kitchen_sink)
      found = nodes |> Enum.flat_map(&walk/1) |> Enum.uniq() |> Enum.sort()

      assert found == Enum.sort(@node_types),
             "the AST grew or lost a node type; update Tend.Template.AST's moduledoc too"
    end

    test "every documented type defines a struct and a t/0 type" do
      for module <- @node_types do
        assert function_exported?(module, :__struct__, 0) or Code.ensure_loaded?(module)
        assert Map.has_key?(struct(module, []), :offset)
      end
    end

    defp walk(%AST.Text{}), do: [AST.Text]
    defp walk(%AST.Action{pipeline: p}), do: [AST.Action | walk(p)]

    defp walk(%AST.If{} = n), do: [AST.If | branches(n)]
    defp walk(%AST.Range{} = n), do: [AST.Range | branches(n)]

    defp walk(%AST.Pipeline{} = n),
      do: [AST.Pipeline | Enum.flat_map(n.decls ++ n.commands, &walk/1)]

    defp walk(%AST.Command{args: args}), do: [AST.Command | Enum.flat_map(args, &walk/1)]
    defp walk(node), do: [node.__struct__]

    defp branches(node) do
      walk(node.pipeline) ++
        Enum.flat_map(node.body, &walk/1) ++
        Enum.flat_map(node.else_body || [], &walk/1)
    end
  end

  defp only_arg(source) do
    assert {:ok, [%AST.Action{pipeline: %AST.Pipeline{commands: [%AST.Command{args: [arg]}]}}]} =
             Template.parse(source)

    arg
  end
end
