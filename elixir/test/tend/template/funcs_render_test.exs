defmodule Tend.Template.FuncsRenderTest.PromptTask do
  @moduledoc false
  # internal/workflow/prompt.go's PromptTask, field for field and in
  # declaration order, so `{{.Task}}` prints what Go prints. Same convention
  # as Tend.Template.RendererTest.
  defstruct id: 0, title: "", body: ""
end

defmodule Tend.Template.FuncsRenderTest.PromptSubtask do
  @moduledoc false
  defstruct id: 0, title: "", state: "", is_blocked: false, depends_on: []
end

defmodule Tend.Template.FuncsRenderTest.PromptData do
  @moduledoc false
  defstruct task: %Tend.Template.FuncsRenderTest.PromptTask{},
            cwd: "",
            input: "",
            feedback: "",
            iteration: 0,
            outcomes: [],
            subtasks: []
end

defmodule Tend.Template.FuncsRenderTest do
  @moduledoc """
  The built-ins that can only be pinned through a whole template.

  `Tend.Template.FuncsTest` covers the built-ins at the `Tend.Template.Funcs`
  boundary, by calling `call/2`, `check_arity/3`, `defined?/1` and
  `short_circuit?/1` directly. Three things cannot be reached that way, and
  they live here:

    * `and` and `or` are not in `Funcs.call/2` at all. They are variadic,
      they short-circuit, and they return the *argument that decided*, so
      their arguments have to be evaluated one at a time and stopped at --
      which is the renderer's job, marked by `Funcs.short_circuit?/1`. There
      is no function to call; the template is the only interface they have.

    * A pipeline is the renderer's, too. `{{"x" | and true}}` appends the
      piped value as the last argument, and whether that is what happened is
      only visible in the rendered output.

    * `Funcs` reports a failure as `{:error, detail}` and never raises,
      because only the renderer knows the node to hang the message on. The
      `error calling eq: ...` wrapping, the `Tend.Template.RenderError`
      reason and its context are all added on the way out.

  Every `want` in this file was produced by rendering the same template
  through Go's `text/template` with `Option("missingkey=error")` against the
  same data.
  """

  use ExUnit.Case, async: true

  alias Tend.Template
  alias Tend.Template.FuncsRenderTest.PromptData
  alias Tend.Template.FuncsRenderTest.PromptSubtask
  alias Tend.Template.FuncsRenderTest.PromptTask
  alias Tend.Template.RenderError

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

  describe "and and or return the decisive argument, not a boolean" do
    # Go's and: the first false argument, or the last argument. Go's or: the
    # first true argument, or the last argument. Neither ever returns a
    # boolean of its own making.
    @values [
      {"{{and 1 2}}", "2"},
      {"{{and 0 2}}", "0"},
      {"{{and 1 0 2}}", "0"},
      {"{{and true}}", "true"},
      {"{{or 1 2}}", "1"},
      {"{{or 0 \"x\"}}", "x"},
      {"{{or \"\" 0}}", "0"},
      {"{{and .Outcomes .Cwd}}", "/home/me/proj"},
      {"[{{and \"\" .Cwd}}]", "[]"},
      {"{{and (ne .Cwd \"x\") (not .Iteration)}}", "false"}
    ]

    for {source, want} <- @values do
      test "#{inspect(source)} is #{inspect(want)}, as in Go" do
        assert Template.render(unquote(source), @full) == {:ok, unquote(want)}
      end
    end

    test "a slice comes back as the slice, printed the way Go prints it" do
      assert Template.render("{{or .Subtasks 1}}", @full) ==
               {:ok,
                "[{43 write the migration done false []} " <>
                  "{44 wire the store todo false [43]} " <>
                  "{45 expose over MCP todo true [44]}]"}
    end
  end

  describe "and and or short-circuit, so the rest is never evaluated" do
    test "a false argument stops and before it reaches an unknown field" do
      # Go renders this without complaining: `.Bogus` is never evaluated.
      assert Template.render("{{and 0 .Bogus}}", @full) == {:ok, "0"}
    end

    test "a true argument stops or the same way" do
      assert Template.render("{{or 1 .Bogus}}", @full) == {:ok, "1"}
    end

    test "an argument that is reached is still evaluated, and still fails" do
      assert {:error, %RenderError{reason: :missing_field} = error} =
               Template.render("{{and .Cwd .Bogus}}", @full)

      assert error.detail =~ "can't evaluate field Bogus"
    end
  end

  describe "a piped value is and's and or's last argument" do
    @piped [
      {"{{true | and false}}", "false"},
      {"{{\"x\" | and true}}", "x"},
      {"{{\"x\" | or false}}", "x"},
      {"{{0 | and 1}}", "0"}
    ]

    for {source, want} <- @piped do
      test "#{inspect(source)} is #{inspect(want)}" do
        assert Template.render(unquote(source), @full) == {:ok, unquote(want)}
      end
    end
  end

  describe "the argument counts Go accepts and refuses, as the render surfaces them" do
    # The counting itself is `Funcs.check_arity/3`'s, and
    # `Tend.Template.FuncsTest` pins every wording against it. What is only
    # visible here is that the check runs at all, before any argument is
    # evaluated, and that its detail reaches the caller as a :wrong_args
    # RenderError.
    @arity [
      {"{{and}}", "wrong number of args for and: want at least 1 got 0"},
      {"{{or}}", "wrong number of args for or: want at least 1 got 0"},
      {"{{eq}}", "wrong number of args for eq: want at least 1 got 0"},
      {"{{not}}", "wrong number of args for not: want 1 got 0"},
      {"{{not 1 2}}", "wrong number of args for not: want 1 got 2"},
      {"{{ne 1}}", "wrong number of args for ne: want 2 got 1"},
      {"{{ne 1 2 3}}", "wrong number of args for ne: want 2 got 3"},
      {"{{len .Subtasks 1}}", "wrong number of args for len: want 1 got 2"}
    ]

    for {source, detail} <- @arity do
      test "#{inspect(source)} is #{inspect(detail)}" do
        assert {:error, %RenderError{reason: :wrong_args} = error} =
                 Template.render(unquote(source), @full)

        assert error.detail == unquote(detail)
      end
    end

    test "the error is reported against the function name, as Go reports it" do
      assert {:error, error} = Template.render("{{and}}", @full)
      assert error.context == "and"
    end
  end

  describe "a built-in's error is wrapped with the name Go wraps it with" do
    # `Funcs.call/2` returns a bare detail and never raises; `error calling
    # <name>: ` is the renderer's, and so is the :call_error reason.
    test "eq's refusal of a list becomes error calling eq" do
      assert {:error, %RenderError{reason: :call_error} = error} =
               Template.render("{{eq .Outcomes .Outcomes}}", @zero)

      assert error.detail == "error calling eq: non-comparable type []: list"
    end

    test "len's refusal of a value with no length becomes error calling len" do
      assert {:error, %RenderError{reason: :call_error} = error} =
               Template.render("{{len 3}}", @full)

      assert error.detail == "error calling len: len of type integer"
    end
  end

  describe "a function this port does not have" do
    # Two different situations, one message.
    #
    # `bogus` is a name Go does not have either -- Go refuses it while
    # *parsing*, with `template: prompt:1: function "bogus" not defined`, and
    # this port has no function table in the parser so it refuses it at render
    # time with the same wording. Neither renders it.
    #
    # `printf`, `index` and `slice` are built-ins Go *does* have and this port
    # deliberately does not: no prompt in the repo's fixtures and no prompt_md
    # row in a real tend.db calls one, so they are out until one does. This is
    # a real divergence -- Go renders these -- and `Tend.Template` lists it.
    # Adding one is a clause in `Tend.Template.Funcs`.
    @undefined [
      "{{bogus}}",
      "{{printf \"%s\" .Cwd}}",
      "{{index .Outcomes 0}}",
      "{{slice .Cwd 1}}"
    ]

    for source <- @undefined do
      test "#{inspect(source)} is refused by name" do
        assert {:error, %RenderError{reason: :undefined_function} = error} =
                 Template.render(unquote(source), @full)

        assert error.detail =~ "not defined"
      end
    end

    test "and the parser still accepts it, so this is a Funcs gap, not a syntax one" do
      for source <- @undefined, do: assert({:ok, _nodes} = Template.parse(source))
    end
  end
end
