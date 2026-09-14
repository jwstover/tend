defmodule Tend.Template.RendererTest.PromptTask do
  @moduledoc false
  # internal/workflow/prompt.go's PromptTask, field for field. The real
  # struct belongs to the workflow sub-task; this one exists so the renderer
  # can be pinned against Go today, including the zero values -- Go's zero
  # string is "", never nil, and the rendering depends on it.
  defstruct id: 0, title: "", body: ""
end

defmodule Tend.Template.RendererTest.PromptSubtask do
  @moduledoc false
  defstruct id: 0, title: "", state: "", is_blocked: false, depends_on: []
end

defmodule Tend.Template.RendererTest.PromptData do
  @moduledoc false
  defstruct task: %Tend.Template.RendererTest.PromptTask{},
            cwd: "",
            input: "",
            feedback: "",
            iteration: 0,
            outcomes: [],
            subtasks: []
end

defmodule Tend.Template.RendererTest do
  use ExUnit.Case, async: true

  alias Tend.Template
  alias Tend.Template.RenderError
  alias Tend.Template.RendererTest.PromptData
  alias Tend.Template.RendererTest.PromptSubtask
  alias Tend.Template.RendererTest.PromptTask

  # Every `want` below is Go's own output, captured by running the template
  # through internal/workflow.RenderPrompt against these exact two data sets
  # and printing the result with %q. Where this port diverges from Go on
  # purpose the test says so and says why.

  @zero %PromptData{}

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

  describe "every prompt_md literal in the Go tree that needs no builtin" do
    # {template, output against PromptData{}, output against fullData}. The
    # templates are spelled with `<<`/`>>` so a Go `#{{.ID}}` does not have to
    # be escaped against Elixir interpolation; `restore/1` puts the braces
    # back. Same convention as Tend.TemplateTest.
    @renders [
      {"Fix the bug described in the task.\n\nRun the tests.",
       "Fix the bug described in the task.\n\nRun the tests.",
       "Fix the bug described in the task.\n\nRun the tests."},
      {"", "", ""},
      {"Just do the thing.", "Just do the thing.", "Just do the thing."},
      {"[<<.Input>>][<<.Feedback>>]", "[][]", "[previous deliverable][reviewer said no]"},
      {"Do it.<<if .Feedback>> Address this feedback: <<.Feedback>><<end>>", "Do it.",
       "Do it. Address this feedback: reviewer said no"},
      {"Implement <<.Task.Title>>.<<if .Feedback>> Feedback: <<.Feedback>><<end>>", "Implement .",
       "Implement Fix the flaky test. Feedback: reviewer said no"},
      {"Review <<.Input>>; finish with approve or reject.",
       "Review ; finish with approve or reject.",
       "Review previous deliverable; finish with approve or reject."},
      {"<<.Task.ID>><<.Task.Title>><<.Task.Body>><<.Cwd>><<.Input>><<.Feedback>>" <>
         "<<.Iteration>><<.Outcomes>>", "00[]",
       "42Fix the flaky testIt fails on CI only./home/me/proj" <>
         "previous deliverablereviewer said no3[approve reject]"},
      {"<<range .Subtasks>>#<<.ID>> <<.Title>> [<<.State>>] blocked=<<.IsBlocked>> " <>
         "deps=<<.DependsOn>>\n<<end>>", "",
       "#43 write the migration [done] blocked=false deps=[]\n" <>
         "#44 wire the store [todo] blocked=false deps=[43]\n" <>
         "#45 expose over MCP [todo] blocked=true deps=[44]\n"},
      {"sub-tasks:<<range .Subtasks>> <<.Title>><<end>>", "sub-tasks:",
       "sub-tasks: write the migration wire the store expose over MCP"},
      {"<<range .Outcomes>>- <<.>>\n<<end>>", "", "- approve\n- reject\n"},
      {"<<range .Subtasks>><<.ID>><<.Title>><<.State>><<.IsBlocked>><<.DependsOn>><<end>>", "",
       "43write the migrationdonefalse[]44wire the storetodofalse[43]" <>
         "45expose over MCPtodotrue[44]"},
      {"<<range .Subtasks>><<range .DependsOn>>#<<.>> <<end>><<end>>", "", "#43 #44 "},
      {"<<range .Subtasks>><<if .IsBlocked>>blocked<<end>><<end>>", "", "blocked"},
      {"<<range .Subtasks>><<if .IsBlocked>>b<<else>>u<<end>><<end>>", "", "uub"}
    ]

    for {template, zero_want, full_want} <- @renders do
      test "renders #{inspect(template)} byte for byte" do
        source = restore(unquote(template))

        assert Template.render(source, @zero) == {:ok, unquote(zero_want)}
        assert Template.render(source, @full) == {:ok, unquote(full_want)}
      end
    end

    test "a bare {{.Subtasks}} over the empty list is Go's empty slice" do
      assert Template.render("{{.Subtasks}}", @zero) == {:ok, "[]"}
    end

    test "a bare {{.Subtasks}} over a populated list is Go's, field for field" do
      # Go prints a struct's fields in declaration order, and so does this:
      # `defstruct` records the order and `Module.__info__(:struct)` hands it
      # back in it. The expectation below is Go 1.26.4's own output for
      # "{{.Subtasks}}" against prompt_test.go's fullData.
      #
      # This is the one place the port could have drifted silently, since
      # ValidatePrompt only requires that the populated case not error.
      assert {:ok, rendered} = Template.render("{{.Subtasks}}", @full)

      assert rendered ==
               "[{43 write the migration done false []} " <>
                 "{44 wire the store todo false [43]} " <>
                 "{45 expose over MCP todo true [44]}]"
    end

    defp restore(template) do
      template |> String.replace("<<", "{{") |> String.replace(">>", "}}")
    end
  end

  describe "missingkey=error" do
    test "an unknown top-level field is an error, not an empty string" do
      assert {:error, %RenderError{reason: :missing_field} = error} =
               Template.render("{{.Cwdd}}", @full)

      assert error.detail ==
               "can't evaluate field Cwdd in type Tend.Template.RendererTest.PromptData"

      assert error.context == ".Cwdd"
    end

    test "the message names the offending variable, the way ErrInvalidPrompt surfaces it" do
      assert {:error, error} = Template.render("{{.Task.Title}} {{.Tittle}}", @full)
      assert Exception.message(error) =~ "Tittle"

      # Go's own message for this is byte for byte the same but for the
      # column, which it reports 0-based -- `prompt:1:18`, the raw byte
      # offset. `Tend.Template.Position` says why this port counts from 1.
      assert Exception.message(error) ==
               ~s(template: prompt:1:19: executing "prompt" at <.Tittle>: ) <>
                 "can't evaluate field Tittle in type Tend.Template.RendererTest.PromptData"
    end

    test "an unknown field partway down a chain names that field and its type" do
      assert {:error, %RenderError{reason: :missing_field} = error} =
               Template.render("{{.Task.BodyMD}}", @full)

      assert error.context == ".Task.BodyMD"

      assert error.detail ==
               "can't evaluate field BodyMD in type Tend.Template.RendererTest.PromptTask"
    end

    test "an unknown field inside the taken {{if}} arm is an error" do
      # {{if .Feedback}} is true only for the populated data, which is why
      # Go's ValidatePrompt renders against both.
      source = "{{if .Feedback}}{{.Bogus}}{{end}}"

      assert {:ok, ""} = Template.render(source, @zero)

      assert {:error, %RenderError{reason: :missing_field} = error} =
               Template.render(source, @full)

      assert error.detail =~ "can't evaluate field Bogus"
    end

    test "an unknown field inside the taken {{else}} arm is an error" do
      source = "{{if .Feedback}}ok{{else}}{{.Bogus}}{{end}}"

      assert {:ok, "ok"} = Template.render(source, @full)

      assert {:error, %RenderError{reason: :missing_field} = error} =
               Template.render(source, @zero)

      assert error.detail =~ "can't evaluate field Bogus"
    end

    test "an unknown field inside a {{range}} body is an error once the list is not empty" do
      # Over the empty list the body never runs, so the error needs the
      # populated data to surface -- exactly the pair ValidatePrompt uses.
      source = "{{range .Subtasks}}{{.Body}}{{end}}"

      assert {:ok, ""} = Template.render(source, @zero)

      assert {:error, %RenderError{reason: :missing_field} = error} =
               Template.render(source, @full)

      assert error.detail ==
               "can't evaluate field Body in type Tend.Template.RendererTest.PromptSubtask"
    end

    test "an unknown field inside an {{if}} inside a {{range}} is an error" do
      for source <- [
            "{{range .Subtasks}}{{if .IsBlocked}}{{.Blockers}}{{end}}{{end}}",
            "{{range .Subtasks}}{{if .IsBlocked}}ok{{else}}{{.Blockers}}{{end}}{{end}}"
          ] do
        assert {:ok, ""} = Template.render(source, @zero)
        assert {:error, %RenderError{} = error} = Template.render(source, @full)
        assert Exception.message(error) =~ "Blockers"
      end
    end

    test "a missing key on a plain map is Go's map wording, not a struct's" do
      assert {:error, %RenderError{reason: :missing_key} = error} =
               Template.render("{{.Nope}}", %{"Yes" => 1})

      assert error.detail == ~s(map has no entry for key "Nope")
    end

    test "a nil root is Go's nil data, naming the key it was asked for" do
      # Go 1.26.4, "{{.B}}" executed against a nil data value:
      # nil data; no entry for key "B"
      assert {:error, %RenderError{reason: :nil_data} = error} = Template.render("{{.B}}", nil)

      assert error.detail == ~s(nil data; no entry for key "B")
    end

    test "a nil partway down a chain is Go's nil pointer, not Go's nil data" do
      # Go reserves `nil data; no entry for key` for a nil root and says
      # `nil pointer evaluating T.Field` for a nil inside a chain -- for
      # `{{.A.B}}` with a nil `A`, `nil pointer evaluating *main.Inner.B` or
      # `interface {}.B` depending on the declared type. An Elixir nil has no
      # type, so the type slot reads `nil`; the rest is Go's.
      assert {:error, %RenderError{reason: :nil_data} = error} =
               Template.render("{{.Task.Title}}", %{"Task" => nil})

      assert error.detail == "nil pointer evaluating nil.Title"
    end

    test "an Elixir internal is not a field, however much it looks like one" do
      # `.__struct__` resolves to a module in Elixir and to nothing at all in
      # Go: "{{.__struct__}}" is `can't evaluate field __struct__ in type
      # main.PromptData` there, so it has to be a miss here too.
      assert {:error, %RenderError{reason: :missing_field} = error} =
               Template.render("{{.__struct__}}", @full)

      assert error.detail ==
               "can't evaluate field __struct__ in type Tend.Template.RendererTest.PromptData"
    end

    test "a parse error comes back from render/2 too, as its own struct" do
      assert {:error, %Tend.Template.ParseError{}} = Template.render("{{.Task.Title", @full)
    end
  end

  describe "Go truthiness" do
    @falsy [false, 0, nil, "", [], %{}]
    @truthy [true, 1, -1, "x", [0], %{"a" => 1}]

    for value <- @falsy do
      test "#{inspect(value)} takes the {{else}} arm" do
        assert Template.render("{{if .V}}t{{else}}f{{end}}", %{
                 "V" => unquote(Macro.escape(value))
               }) ==
                 {:ok, "f"}
      end
    end

    for value <- @truthy do
      test "#{inspect(value)} takes the {{if}} arm" do
        assert Template.render("{{if .V}}t{{else}}f{{end}}", %{
                 "V" => unquote(Macro.escape(value))
               }) ==
                 {:ok, "t"}
      end
    end

    test "zero is falsy even though it is a number -- the one that bites" do
      # This is the rule behind {{if $i}} on the first index of a
      # {{range $i, $o := ...}}: index 0 takes the {{else}} arm, which is how
      # the Go tree renders "approve, reject" with one comma.
      assert Template.render("{{if .Iteration}}t{{else}}f{{end}}", @zero) == {:ok, "f"}
      assert Template.render("{{if .Iteration}}t{{else}}f{{end}}", @full) == {:ok, "t"}
    end

    test "an empty string and an empty list are falsy on real data" do
      assert Template.render("{{if .Cwd}}t{{else}}f{{end}}", @zero) == {:ok, "f"}
      assert Template.render("{{if .Subtasks}}t{{else}}f{{end}}", @zero) == {:ok, "f"}
    end

    test "a struct is truthy however empty it is, as it is in Go" do
      assert Template.render("{{if .Task}}t{{else}}f{{end}}", @zero) == {:ok, "t"}
    end
  end

  describe "range" do
    test "the cursor is rebound to the element, for {{.}} and for {{.Field}}" do
      assert Template.render("{{range .Outcomes}}[{{.}}]{{end}}", @full) ==
               {:ok, "[approve][reject]"}

      assert Template.render("{{range .Subtasks}}{{.Title}};{{end}}", @full) ==
               {:ok, "write the migration;wire the store;expose over MCP;"}
    end

    test "the cursor is restored after the loop" do
      assert Template.render("{{range .Outcomes}}{{.}}{{end}}{{.Cwd}}", @full) ==
               {:ok, "approvereject/home/me/proj"}
    end

    test "an empty list renders the {{else}} arm" do
      assert Template.render("{{range .Subtasks}}x{{else}}empty{{end}}", @zero) == {:ok, "empty"}
      assert Template.render("{{range .Subtasks}}x{{else}}empty{{end}}", @full) == {:ok, "xxx"}
    end

    test "an empty list with no {{else}} renders nothing, without raising" do
      assert Template.render("{{range .Subtasks}}x{{end}}", @zero) == {:ok, ""}
    end

    test "$ still reaches the root from inside the loop" do
      assert Template.render("{{range .Outcomes}}{{$.Cwd}}:{{.}} {{end}}", @full) ==
               {:ok, "/home/me/proj:approve /home/me/proj:reject "}
    end

    test "an integer ranges over its indices, the way Go has since 1.22" do
      # .Iteration is an int64 in internal/workflow/prompt.go, so this is an
      # ordinary prompt rather than a curiosity. Go 1.26.4 against fullData:
      #
      #     {{range .Iteration}}x{{end}}     => "xxx"
      #     {{range .Iteration}}{{.}}{{end}} => "012"
      assert Template.render("{{range .Iteration}}x{{end}}", @full) == {:ok, "xxx"}
      assert Template.render("{{range .Iteration}}{{.}}{{end}}", @full) == {:ok, "012"}
    end

    test "a zero or negative integer iterates no times, as Go's <= 0 break does" do
      # Go 1.26.4: "{{range .Iteration}}x{{else}}none{{end}}" is "none" for
      # both 0 and -2, and "" with no {{else}} arm.
      assert Template.render("{{range .Iteration}}x{{else}}none{{end}}", @zero) == {:ok, "none"}
      assert Template.render("{{range .Iteration}}x{{end}}", @zero) == {:ok, ""}
      assert Template.render("{{range .V}}x{{else}}none{{end}}", %{"V" => -2}) == {:ok, "none"}
    end

    test "ranging over something that is not a list is an error naming it" do
      # Go refuses a string too -- `range can't iterate over /home/me/proj` --
      # so this is parity, not a gap.
      assert {:error, %RenderError{reason: :not_iterable} = error} =
               Template.render("{{range .Cwd}}x{{end}}", @full)

      assert error.detail == "range can't iterate over /home/me/proj"
      assert error.context == ".Cwd"
    end

    test "ranging over a struct is refused, and names it the way Go's %v does" do
      # Go 1.26.4, "{{range .Task}}x{{end}}" against fullData:
      # range can't iterate over {42 Fix the flaky test It fails on CI only.}
      assert {:error, %RenderError{reason: :not_iterable} = error} =
               Template.render("{{range .Task}}x{{end}}", @full)

      assert error.detail ==
               "range can't iterate over {42 Fix the flaky test It fails on CI only.}"
    end
  end

  describe "Go's %v" do
    test "integers, booleans and strings" do
      assert render_value(42) == "42"
      assert render_value(-1) == "-1"
      assert render_value(0) == "0"
      assert render_value(true) == "true"
      assert render_value(false) == "false"
      assert render_value("") == ""
      assert render_value("a b") == "a b"
    end

    test "[]int64 is space-separated inside square brackets" do
      assert render_value([]) == "[]"
      assert render_value([1]) == "[1]"
      assert render_value([1, 2, 3]) == "[1 2 3]"
      assert render_value([-1, 0]) == "[-1 0]"
    end

    test "[]string too -- {{.Outcomes}} is the one the Go tests assert on" do
      assert render_value(["approve", "reject"]) == "[approve reject]"
      assert Template.render("{{.Outcomes}}", @full) == {:ok, "[approve reject]"}
    end

    test "nil prints Go's <no value>, not an empty string" do
      # Go prints this for a nil interface. A field that is *absent* is an
      # error instead; a field that is present and nil is this.
      assert render_value(nil) == "<no value>"
    end

    test "a float loses the trailing .0, the way Go's %v does" do
      assert render_value(1.0) == "1"
      assert render_value(1.5) == "1.5"
    end

    defp render_value(value) do
      assert {:ok, rendered} = Template.render("{{.V}}", %{"V" => value})
      rendered
    end
  end

  describe "resolving a Go field name against Elixir data" do
    test "a string key wins first" do
      assert Template.render("{{.Title}}", %{"Title" => "a", title: "b"}) == {:ok, "a"}
    end

    test "an atom key of the same name comes next" do
      assert Template.render("{{.Title}}", %{Title: "a", title: "b"}) == {:ok, "a"}
    end

    test "an underscored atom key is how a struct resolves" do
      assert Template.render("{{.IsBlocked}}{{.DependsOn}}{{.ID}}", %{
               is_blocked: true,
               depends_on: [1],
               id: 7
             }) == {:ok, "true[1]7"}
    end

    test "an unknown name is a miss and never creates an atom" do
      # A prompt is user input and the atom table is not garbage collected,
      # so lookup only ever asks for atoms that already exist. Rendering the
      # name below must not make `:no_such_field_in_this_system` appear.
      assert {:error, %RenderError{}} =
               Template.render("{{.NoSuchFieldInThisSystem}}", %{"other" => 1})

      assert_raise ArgumentError, fn ->
        String.to_existing_atom("no_such_field_in_this_system")
      end
    end
  end

  describe "an argument given to something that is not a function" do
    # Go has three messages for this and picks by what the head of the
    # command turned out to be. Every `detail` below is Go 1.26.4's, captured
    # against a struct with these fields and against a plain map.
    test "a struct field cannot be invoked as a function" do
      assert {:error, %RenderError{reason: :bad_command} = error} =
               Template.render("{{.Cwd .Input}}", @full)

      assert error.detail == "Cwd has arguments but cannot be invoked as function"
      assert error.context == ".Cwd"
    end

    test "so does a field reached through $" do
      assert {:error, %RenderError{} = error} = Template.render("{{$.Cwd .Input}}", @full)

      assert error.detail == "Cwd has arguments but cannot be invoked as function"
      assert error.context == "$.Cwd"
    end

    test "a map key is not a method, which is Go's other wording" do
      assert {:error, %RenderError{} = error} =
               Template.render("{{.k .k}}", %{"k" => "v"})

      assert error.detail == "k is not a method but has arguments"
      assert error.context == ".k"
    end

    test "only a head that is no field at all can't be given an argument" do
      for {source, detail, context} <- [
            {"{{$ .Cwd}}", "can't give argument to non-function $", "$"},
            {"{{. .Cwd}}", "can't give argument to non-function .", "."},
            {"{{1 2}}", "can't give argument to non-function 1", "1"},
            {~S({{"a" "b"}}), ~S(can't give argument to non-function "a"), ~S("a")},
            {"{{true .Cwd}}", "can't give argument to non-function true", "true"},
            {"{{(.Cwd) .Input}}", "can't give argument to non-function .Cwd", "(.Cwd) .Input"}
          ] do
        assert {:error, %RenderError{reason: :bad_command} = error} =
                 Template.render(source, @full)

        assert error.detail == detail
        assert error.context == context
      end
    end

    test "the head is resolved first, so a missing field still wins" do
      # Go: `{{.Bogus .Cwd}}` is `can't evaluate field Bogus in type main.D`,
      # not an argument error.
      assert {:error, %RenderError{reason: :missing_field} = error} =
               Template.render("{{.Bogus .Cwd}}", @full)

      assert error.detail =~ "can't evaluate field Bogus"
    end
  end

  describe "the constructs the builtins sub-task added" do
    # These used to be the renderer's "not supported yet" list. They render
    # now; `Tend.Template.FuncsTest` and `Tend.Template.VariablesTest` pin
    # each one against Go, and this is the summary that says the gap is
    # closed.
    @once_deferred [
      {"{{len .Subtasks}} sub-tasks", "3 sub-tasks"},
      {"{{if and (ne .Cwd \"x\") (not .Iteration)}}y{{else}}n{{end}}", "n"},
      {"{{.Outcomes | len}}", "2"},
      {"{{range $i, $o := .Outcomes}}{{$o}}{{end}}", "approvereject"},
      {"{{$x := .Cwd}}{{$x}}", "/home/me/proj"}
    ]

    for {source, want} <- @once_deferred do
      test "#{inspect(source)} renders" do
        assert Template.render(unquote(source), @full) == {:ok, unquote(want)}
      end
    end
  end

  describe "render!/2" do
    test "raises the render error instead of returning it" do
      assert_raise RenderError, fn -> Template.render!("{{.Nope}}", @full) end
    end

    test "raises the parse error instead of returning it" do
      assert_raise Tend.Template.ParseError, fn -> Template.render!("{{.Nope", @full) end
    end
  end
end
