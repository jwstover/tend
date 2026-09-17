defmodule Tend.Template.VariablesTest.PromptTask do
  @moduledoc false
  # internal/workflow/prompt.go's PromptTask, field for field and in
  # declaration order. Same convention as Tend.Template.RendererTest.
  defstruct id: 0, title: "", body: ""
end

defmodule Tend.Template.VariablesTest.PromptSubtask do
  @moduledoc false
  defstruct id: 0, title: "", state: "", is_blocked: false, depends_on: []
end

defmodule Tend.Template.VariablesTest.PromptData do
  @moduledoc false
  defstruct task: %Tend.Template.VariablesTest.PromptTask{},
            cwd: "",
            input: "",
            feedback: "",
            iteration: 0,
            outcomes: [],
            subtasks: []
end

defmodule Tend.Template.VariablesTest do
  @moduledoc """
  Variables, `{{range}}`'s loop variables and `|` pipelines, pinned against
  Go.

  Every `want` was captured from Go's `text/template` with
  `Option("missingkey=error")` against the data set the test names.
  """

  use ExUnit.Case, async: true

  alias Tend.Template
  alias Tend.Template.RenderError
  alias Tend.Template.VariablesTest.PromptData
  alias Tend.Template.VariablesTest.PromptSubtask
  alias Tend.Template.VariablesTest.PromptTask

  @zero %PromptData{}

  # `one` and `three` differ from the zero value only in `Outcomes`, so
  # `{{range $i, $o := .Outcomes}}` can be pinned at one and at several.
  @one %PromptData{outcomes: ["approve"]}
  @three %PromptData{outcomes: ["a", "b", "c"]}

  # internal/workflow/prompt_test.go's fullData.
  @full %PromptData{
    task: %PromptTask{id: 42, title: "Fix the flaky test", body: "It fails on CI only."},
    cwd: "/home/me/proj",
    input: "previous deliverable",
    feedback: "reviewer said no",
    iteration: 3,
    outcomes: ["approve", "reject"],
    subtasks: [
      %PromptSubtask{id: 43, title: "write the migration", state: "done"},
      %PromptSubtask{id: 44, title: "wire the store", state: "todo", depends_on: [43]},
      %PromptSubtask{
        id: 45,
        title: "expose over MCP",
        state: "todo",
        is_blocked: true,
        depends_on: [44]
      }
    ]
  }

  describe "{{range $i, $o := .Outcomes}} -- the template prompt_test.go asserts on" do
    # The comma-separated outcome list is the one prompt in the Go tests that
    # needs index *and* element, and it is the one `{{if $i}}` case where the
    # index being 0 is load-bearing: Go's truthiness makes the first pass take
    # the {{else}} arm, which is what keeps the comma off the front.
    @outcomes "{{range $i, $o := .Outcomes}}{{if $i}}, {{end}}{{$o}}{{end}}"

    test "no outcomes render nothing" do
      assert Template.render(@outcomes, @zero) == {:ok, ""}
    end

    test "one outcome renders with no separator" do
      assert Template.render(@outcomes, @one) == {:ok, "approve"}
    end

    test "several outcomes are comma-separated" do
      assert Template.render(@outcomes, @three) == {:ok, "a, b, c"}
    end

    test "in the whole prompt prompt_test.go renders, byte for byte" do
      # "#" and "{{" are spelled apart so Elixir does not read `#{` as an
      # interpolation; the template is prompt_test.go's, byte for byte.
      source =
        "Task #" <>
          "{{.Task.ID}}: {{.Task.Title}}\n{{.Task.Body}}\n" <>
          "cwd={{.Cwd}} input={{.Input}} feedback={{.Feedback}} iteration={{.Iteration}}\n" <>
          "outcomes: " <> @outcomes

      assert Template.render(source, @full) ==
               {:ok,
                "Task #42: Fix the flaky test\nIt fails on CI only.\n" <>
                  "cwd=/home/me/proj input=previous deliverable " <>
                  "feedback=reviewer said no iteration=3\n" <>
                  "outcomes: approve, reject"}
    end
  end

  describe "which variable gets the index and which gets the element" do
    test "with two variables the index comes first" do
      assert Template.render("{{range $i, $o := .Outcomes}}{{$i}}={{$o}} {{end}}", @three) ==
               {:ok, "0=a 1=b 2=c "}
    end

    test "with one variable it is the element, not the index" do
      assert Template.render("{{range $o := .Outcomes}}[{{$o}}]{{end}}", @three) ==
               {:ok, "[a][b][c]"}
    end

    test "a field chain hangs off the element variable" do
      assert Template.render("{{range $i, $s := .Subtasks}}{{$i}}:{{$s.Title}} {{end}}", @full) ==
               {:ok, "0:write the migration 1:wire the store 2:expose over MCP "}
    end

    test "ranging an integer binds the element, and refuses a second variable" do
      # Go 1.22's integer range has no index to give, so one variable is the
      # count and two are an error.
      assert Template.render("{{range $v := 3}}{{$v}}{{end}}", @full) == {:ok, "012"}

      assert {:error, %RenderError{reason: :not_iterable} = error} =
               Template.render("{{range $i, $v := .Iteration}}x{{end}}", @full)

      assert error.detail == "can't use 3 to iterate over more than one variable"
    end

    test "and refuses the second variable before it looks at the count" do
      # Go's walkRange checks len(Pipe.Decl) first, so a zero count is an
      # error too rather than quietly taking the {{else}} arm.
      assert {:error, %RenderError{reason: :not_iterable} = error} =
               Template.render("{{range $i, $v := .Iteration}}x{{else}}y{{end}}", @zero)

      assert error.detail == "can't use 0 to iterate over more than one variable"
    end
  end

  describe "scoping and shadowing" do
    test "a nested range sees both loops' variables" do
      assert Template.render(
               "{{range $i, $s := .Subtasks}}{{range $j, $d := $s.DependsOn}}" <>
                 "{{$i}}-{{$j}}-{{$d}} {{end}}{{end}}",
               @full
             ) == {:ok, "1-0-43 2-0-44 "}
    end

    test "nested ranges may reuse the outer loop's variable names" do
      # The inner loop's `$i` and `$o` are pushed above the outer loop's and
      # popped with the inner {{end}}, so the outer pair is intact after it.
      assert Template.render(
               "{{range $i, $o := .Outcomes}}{{range $i, $o := $.Outcomes}}" <>
                 "{{$i}}{{$o}}{{end}}|{{$i}}{{$o}}{{end}}",
               @three
             ) == {:ok, "0a1b2c|0a0a1b2c|1b0a1b2c|2c"}
    end

    test "a loop variable shadows an outer variable of the same name" do
      assert Template.render(
               "{{$i := 9}}{{range $i, $o := .Outcomes}}{{$i}}{{end}}-{{$i}}",
               @three
             ) ==
               {:ok, "012-9"}
    end

    test "an assignment and a declaration of one name in the same block do not collide" do
      # `=` reaches the outer `$x`; the `:=` right after it pushes a second,
      # inner `$x` that every later reference in the block sees and that the
      # {{end}} pops, leaving the assigned 1 behind.
      assert Template.render(
               "{{$x := 0}}{{range .Outcomes}}{{$x = 1}}{{$x := 2}}{{$x}}{{end}}{{$x}}",
               @three
             ) == {:ok, "2221"}
    end

    test "an inner declaration shadows an outer one for the length of the block" do
      assert Template.render(
               ~S({{$x := "outer"}}{{range .Outcomes}}{{$x := "inner"}}{{$x}}{{end}}{{$x}}),
               @three
             ) == {:ok, "innerinnerinnerouter"}
    end

    test "the same inside an {{if}}" do
      assert Template.render("{{$x := 1}}{{if true}}{{$x := 2}}{{$x}}{{end}}{{$x}}", @full) ==
               {:ok, "21"}
    end

    test "an assignment reaches the outer variable and outlives the block" do
      # `=` overwrites the nearest binding rather than declaring a new one, so
      # unlike `:=` above this one is still 2 after the {{end}}.
      assert Template.render("{{$x := 1}}{{if true}}{{$x = 2}}{{end}}{{$x}}", @full) ==
               {:ok, "2"}

      assert Template.render("{{$x := 1}}{{range .Outcomes}}{{$x = 2}}{{end}}{{$x}}", @three) ==
               {:ok, "2"}
    end

    test "an assignment from a loop variable keeps the last iteration's value" do
      assert Template.render(
               "{{$x := 1}}{{range $i, $o := .Outcomes}}{{$x = $i}}{{end}}{{$x}}",
               @three
             ) == {:ok, "2"}
    end

    test "$ is the whole data value, whatever the cursor is" do
      assert Template.render("{{range $i, $o := .Outcomes}}{{$.Cwd}}|{{end}}", @one) ==
               {:ok, "|"}

      assert Template.render("{{range .Subtasks}}{{$.Task.ID}} {{end}}", @full) ==
               {:ok, "42 42 42 "}
    end

    test "an assignment to a variable that was never declared is refused" do
      # Go's setVar walks the stack and calls s.errorf when the name is not
      # on it, so this is a *render* error and not a silent declaration:
      #
      #   template: prompt:1:7: executing "prompt" at <1>: undefined variable: $y
      #
      # The parser cannot catch it. It tracks declarations and refuses an
      # undeclared *read*, but an assignment's left-hand side looks exactly
      # like a declaration's to the grammar, so `{{$y = 1}}` parses.
      assert {:ok, _nodes} = Template.parse("{{$y = 1}}{{$y}}")

      assert {:error, %RenderError{reason: :undefined_variable} = error} =
               Template.render("{{$y = 1}}{{$y}}", @full)

      assert error.detail == "undefined variable: $y"
      # Go's `at` is the head of the pipeline's last command, which is `1`.
      assert error.context == "1"
    end

    test "the same inside a {{range}} body" do
      # Go: template: prompt:1:26: executing "prompt" at <5>: undefined variable: $q
      assert {:error, %RenderError{reason: :undefined_variable} = error} =
               Template.render("{{range .Outcomes}}{{$q = 5}}{{end}}", @full)

      assert error.detail == "undefined variable: $q"
      assert error.context == "5"
    end

    test "the same inside an {{if}}, beside an assignment that is fine" do
      # `$x` is declared and `$w` is not, so the block fails on `$w` rather
      # than rendering "2".
      assert {:error, %RenderError{reason: :undefined_variable} = error} =
               Template.render("{{$x := 1}}{{if true}}{{$x = 2}}{{$w = 3}}{{end}}{{$x}}", @full)

      assert error.detail == "undefined variable: $w"
    end

    test "and for a {{range}}'s own loop variables" do
      # `=` on loop variables assigns to names that must already exist, and
      # Go refuses before the first iteration -- the assignment happens in
      # evalPipeline, against the ranged expression.
      #
      #   template: prompt:1:17: executing "prompt" at <.Outcomes>:
      #       undefined variable: $i
      assert {:error, %RenderError{reason: :undefined_variable} = error} =
               Template.render("{{range $i, $o = .Outcomes}}x{{end}}", @full)

      assert error.detail == "undefined variable: $i"
      assert error.context == ".Outcomes"
    end

    test "declared first, `=` loop variables work and outlive the loop" do
      # The other half of the rule: `=` is fine once the names exist, and the
      # last iteration's values survive the {{end}}.
      assert Template.render(
               "{{$i := 0}}{{$o := \"\"}}{{range $i, $o = .Outcomes}}{{$i}}{{$o}} {{end}}|{{$i}}{{$o}}",
               @three
             ) == {:ok, "0a 1b 2c |2c"}
    end

    test "a variable out of scope is a parse error, before any data is involved" do
      assert {:error, %Tend.Template.ParseError{} = error} =
               Template.parse("{{range $i, $o := .Outcomes}}{{$o}}{{end}}{{$o}}")

      assert error.detail == ~s(undefined variable "$o")
    end
  end

  describe "declarations" do
    @declarations [
      {"{{$x := .Cwd}}{{$x}}", "/home/me/proj"},
      # An action that declares writes nothing of its own.
      {"[{{$x := .Cwd}}]", "[]"},
      {"{{$x := .Outcomes | len}}{{$x}}", "2"},
      {"{{if $x := .Cwd}}{{$x}}{{end}}", "/home/me/proj"},
      {"{{$x := .Task}}{{$x.Title}}", "Fix the flaky test"},
      {"{{$x := .Subtasks}}{{len $x}}", "3"}
    ]

    for {source, want} <- @declarations do
      test "#{inspect(source)} is #{inspect(want)}" do
        assert Template.render(unquote(source), @full) == {:ok, unquote(want)}
      end
    end

    test "an unknown field off a variable names the variable's type" do
      assert {:error, %RenderError{reason: :missing_field} = error} =
               Template.render("{{$x := .Task}}{{$x.Nope}}", @full)

      assert error.context == "$x.Nope"

      assert error.detail ==
               "can't evaluate field Nope in type Tend.Template.VariablesTest.PromptTask"
    end

    test "a variable is not a function, and neither is a field off one" do
      assert {:error, %RenderError{reason: :bad_command} = error} =
               Template.render("{{$x := .Cwd}}{{$x .Input}}", @full)

      assert error.detail == "can't give argument to non-function $x"

      assert {:error, %RenderError{} = chained} =
               Template.render("{{$x := .Task}}{{$x.Title .Cwd}}", @full)

      assert chained.detail == "Title has arguments but cannot be invoked as function"
    end
  end

  describe "pipelines" do
    @pipelines [
      {"{{.Outcomes | len}}", "2"},
      {"{{.Cwd | len}}", "13"},
      {~S({{"a" | eq "a"}}), "true"},
      {"{{.Iteration | eq 3}}", "true"},
      {"{{.Cwd | not | not}}", "true"},
      {"{{len .Subtasks | eq 3}}", "true"},
      {"{{(and 1 2)}}", "2"}
    ]

    for {source, want} <- @pipelines do
      test "#{inspect(source)} is #{inspect(want)}" do
        assert Template.render(unquote(source), @full) == {:ok, unquote(want)}
      end
    end

    test "a piped value handed to a field is the same error as an argument" do
      assert {:error, %RenderError{reason: :bad_command} = error} =
               Template.render("{{.Cwd | .Input}}", @full)

      assert error.detail == "Input has arguments but cannot be invoked as function"
      assert error.context == ".Input"
    end

    test "a piped value handed to $ is Go's non-function message" do
      assert {:error, %RenderError{reason: :bad_command} = error} =
               Template.render("{{.Cwd | $}}", @full)

      assert error.detail == "can't give argument to non-function $"
    end

    test "an argument beside a parenthesised sub-expression names the whole pipeline" do
      # Go's notAFunction formats args[0], which here is the whole PipeNode,
      # so the message keeps the pipeline's arguments:
      #
      #   template: prompt:1:2: executing "prompt" at <(and 1 2) 3>:
      #       can't give argument to non-function and 1 2
      #
      # The parentheses belong to the *command*, not to the pipeline, which
      # is why PipeNode.String() leaves them off in the message and the
      # command keeps them in the context.
      assert {:error, %RenderError{reason: :bad_command} = error} =
               Template.render("{{(and 1 2) 3}}", @full)

      assert error.detail == "can't give argument to non-function and 1 2"
      assert error.context == "(and 1 2) 3"
    end

    test "a one-word sub-expression is the same rule, not a different one" do
      # Go: at <(.Cwd) .Input>: can't give argument to non-function .Cwd
      assert {:error, %RenderError{} = error} = Template.render("{{(.Cwd) .Input}}", @full)

      assert error.detail == "can't give argument to non-function .Cwd"
      assert error.context == "(.Cwd) .Input"
    end

    test "a piped value into a sub-expression is refused too" do
      # One known drift, and this port is the better of the two: Go reports
      # `at <.Cwd>`, a stale s.at left over from the previous stage, where
      # this reports the stage that actually failed. The detail is Go's.
      assert {:error, %RenderError{reason: :bad_command} = error} =
               Template.render("{{.Cwd | (.Input)}}", @full)

      assert error.detail == "can't give argument to non-function .Input"
      assert error.context == "(.Input)"
    end
  end

  describe "the ready-set prompt, which needs all of it at once" do
    test "renders the sub-tasks that are neither done nor blocked" do
      # internal/workflow/prompt_test.go's readySetTpl, the Dispatch step's
      # job in template form: a range, an {{if}}, a call to `and` over two
      # parenthesised sub-expressions, and the cursor's fields.
      source =
        "{{range .Subtasks}}{{if and (ne .State \"done\") (not .IsBlocked)}}- #" <>
          "{{.ID}} {{.Title}}\n{{end}}{{end}}"

      assert Template.render(source, @full) == {:ok, "- #44 wire the store\n"}
      assert Template.render(source, @zero) == {:ok, ""}
    end
  end
end
