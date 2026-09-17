defmodule Tend.Template.FuncsTest.PromptTask do
  @moduledoc false
  # internal/workflow/prompt.go's PromptTask, field for field and in
  # declaration order, because declaration order is what `%v` prints -- and
  # what `eq`'s refusal quotes back. Same convention as
  # Tend.Template.RendererTest.
  defstruct id: 0, title: "", body: ""
end

defmodule Tend.Template.FuncsTest.PromptSubtask do
  @moduledoc false
  defstruct id: 0, title: "", state: "", is_blocked: false, depends_on: []
end

defmodule Tend.Template.FuncsTest do
  @moduledoc """
  `Tend.Template.Funcs` at its own boundary.

  The built-ins used to be reachable only through a whole template, which made
  every question about them -- how many arguments does Go accept, which pairs
  does `eq` refuse, what does `len` count -- a question about the renderer too.
  This pins the function contract: `call/2`, `check_arity/3`, `defined?/1` and
  `short_circuit?/1`, each called directly.

  `Tend.Template.FuncsRenderTest` holds the cases that genuinely need a
  renderer to drive them -- `and` and `or`, which are not in `call/2` at all,
  and the wrapping that turns a `{:error, detail}` here into a
  `Tend.Template.RenderError` there.

  Every value pinned here was produced by running the same call through Go's
  `text/template` with `Option("missingkey=error")` against structurally
  identical data. These are the cases worth keeping close at hand, because
  they are the ones where the Elixir reflex and Go's answer differ.
  """

  use ExUnit.Case, async: true

  doctest Tend.Template.Funcs

  alias Tend.Template.Funcs
  alias Tend.Template.FuncsTest.PromptSubtask
  alias Tend.Template.FuncsTest.PromptTask

  # The fields of internal/workflow/prompt_test.go's fullData that the
  # built-ins are asked about, as the renderer would hand them over.
  @cwd "/home/me/proj"
  @iteration 3
  @outcomes ["approve", "reject"]

  @task %PromptTask{id: 42, title: "Fix the flaky test", body: "It fails on CI only."}

  @subtasks [
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

  describe "defined?/1 is the table of built-ins this port implements" do
    test "the six the prompts call are defined" do
      for name <- ["and", "or", "not", "eq", "ne", "len"] do
        assert Funcs.defined?(name), "expected #{name} to be a built-in"
      end
    end

    test "the built-ins Go has and this port does not are refused by name" do
      # Go renders every one of these. This port deliberately does not
      # implement them: no prompt in the repo's fixtures and no prompt_md row
      # in a real tend.db calls one, so they are out until one does. Adding
      # one is a clause in `Tend.Template.Funcs` plus an entry in @builtins.
      go_builtins = [
        "lt",
        "le",
        "gt",
        "ge",
        "index",
        "slice",
        "printf",
        "print",
        "println",
        "html",
        "js",
        "urlquery",
        "call"
      ]

      for name <- go_builtins do
        refute Funcs.defined?(name), "expected #{name} to be a gap, not a built-in"
      end
    end

    test "a name Go does not have either is refused the same way" do
      refute Funcs.defined?("bogus")
    end
  end

  describe "short_circuit?/1 marks exactly and and or" do
    test "and and or are the two that evaluate their arguments lazily" do
      assert Funcs.short_circuit?("and")
      assert Funcs.short_circuit?("or")
    end

    test "every other built-in has its arguments evaluated before the call" do
      for name <- ["not", "eq", "ne", "len"] do
        refute Funcs.short_circuit?(name), "expected #{name} to be called, not short-circuited"
      end
    end
  end

  describe "check_arity/3 counts arguments the way Go's evalCall does" do
    # and, or and eq are variadic with one fixed parameter, so one argument is
    # the minimum and there is no maximum; not and len take exactly one and ne
    # exactly two. Zero arguments is an error for all of them, and Go words it
    # differently for the variadic ones.
    @zero_args [
      {"and", "wrong number of args for and: want at least 1 got 0"},
      {"or", "wrong number of args for or: want at least 1 got 0"},
      {"eq", "wrong number of args for eq: want at least 1 got 0"},
      {"not", "wrong number of args for not: want 1 got 0"},
      {"ne", "wrong number of args for ne: want 2 got 0"},
      {"len", "wrong number of args for len: want 1 got 0"}
    ]

    for {name, detail} <- @zero_args do
      test "#{name} with no arguments is #{inspect(detail)}" do
        assert Funcs.check_arity(unquote(name), 0, false) == {:error, unquote(detail)}
      end
    end

    @too_many [
      {"not", 2, "wrong number of args for not: want 1 got 2"},
      {"ne", 1, "wrong number of args for ne: want 2 got 1"},
      {"ne", 3, "wrong number of args for ne: want 2 got 3"},
      {"len", 2, "wrong number of args for len: want 1 got 2"}
    ]

    for {name, count, detail} <- @too_many do
      test "#{name} with #{count} arguments is #{inspect(detail)}" do
        assert Funcs.check_arity(unquote(name), unquote(count), false) ==
                 {:error, unquote(detail)}
      end
    end

    test "a variadic built-in accepts any number of arguments above the minimum" do
      for name <- ["and", "or", "eq"], count <- 1..5 do
        assert Funcs.check_arity(name, count, false) == :ok,
               "expected #{name} to accept #{count} arguments"
      end
    end

    test "a fixed built-in accepts exactly its arity" do
      assert Funcs.check_arity("not", 1, false) == :ok
      assert Funcs.check_arity("ne", 2, false) == :ok
      assert Funcs.check_arity("len", 1, false) == :ok
    end

    test "want N got M counts the piped value, so a pipeline needs one less" do
      # `final?` says a value is arriving from the previous stage of a
      # pipeline. For a fixed built-in it counts in both halves of the check:
      # `{{.Cwd | len}}` writes no argument and is right, `{{.Cwd | len 1}}`
      # writes one and is one too many -- and `got` says 2, not 1.
      assert Funcs.check_arity("len", 0, true) == :ok
      assert Funcs.check_arity("not", 0, true) == :ok
      assert Funcs.check_arity("ne", 1, true) == :ok

      assert Funcs.check_arity("len", 1, true) ==
               {:error, "wrong number of args for len: want 1 got 2"}

      assert Funcs.check_arity("not", 1, true) ==
               {:error, "wrong number of args for not: want 1 got 2"}

      assert Funcs.check_arity("ne", 2, true) ==
               {:error, "wrong number of args for ne: want 2 got 3"}
    end

    test "want at least N got M counts only the written arguments" do
      # Go's quirk, reproduced: the piped value satisfies the minimum but is
      # not in `got`. The count checked is count + 1 and the count *printed*
      # is count, which is why `{{eq}}` says `got 0` -- and why a piped value
      # alone is enough for the one fixed parameter of a variadic built-in.
      for name <- ["and", "or", "eq"] do
        assert Funcs.check_arity(name, 0, true) == :ok,
               "expected a piped value to be #{name}'s one fixed argument"
      end

      assert Funcs.check_arity("eq", 0, false) ==
               {:error, "wrong number of args for eq: want at least 1 got 0"}
    end
  end

  describe "not" do
    # call/2 returns the value, not a rendered string: `not` is the one
    # built-in whose answer really is a boolean.
    @negations [
      {@cwd, false},
      {"", true},
      {0, true},
      {@iteration, false},
      {nil, true}
    ]

    for {value, want} <- @negations do
      test "not(#{inspect(value)}) is #{want}" do
        assert Funcs.call("not", [unquote(value)]) == {:ok, unquote(want)}
      end
    end

    test "a non-empty list is true, so not is false" do
      assert Funcs.call("not", [@subtasks]) == {:ok, false}
    end

    test "a struct is always true, however empty it is, so not is false" do
      assert Funcs.call("not", [@task]) == {:ok, false}
      assert Funcs.call("not", [%PromptTask{}]) == {:ok, false}
    end

    test "twice over is Go's truthiness as a boolean" do
      # `{{.Cwd | not | not}}`: each stage is its own call, on the previous
      # stage's value.
      assert {:ok, false} = Funcs.call("not", [@cwd])
      assert Funcs.call("not", [false]) == {:ok, true}
    end
  end

  describe "eq and ne compare within a kind, the way Go does" do
    @comparisons [
      {"eq", [1, 1], true},
      {"eq", [1, 2], false},
      {"eq", [-1, -1], true},
      {"eq", [1.5, 1.5], true},
      {"eq", ["a", "a"], true},
      {"eq", [true, true], true},
      # int64 against an int literal: the same kind, so Go compares them.
      {"eq", [@iteration, 3], true},
      {"eq", [42, 42], true},
      # Variadic: == b || == c || ...
      {"eq", [@cwd, "x", @cwd], true},
      {"eq", [@cwd, "x", "y"], false},
      {"ne", [1, 2], true},
      {"ne", [@cwd, "x"], true},
      {"ne", ["Fix the flaky test", "Fix the flaky test"], false},
      # nil is not equal to a string, but it is not an error either.
      {"eq", [nil, nil], true},
      {"eq", [nil, "x"], false},
      {"eq", [nil, @cwd], false}
    ]

    for {name, args, want} <- @comparisons do
      test "#{name}#{inspect(args)} is #{want}" do
        assert Funcs.call(unquote(name), unquote(args)) == {:ok, unquote(want)}
      end
    end

    test "a struct whose fields are all comparable compares by value" do
      assert Funcs.call("eq", [@task, @task]) == {:ok, true}
    end

    # Go's wordings, with Go's type names replaced by Elixir ones --
    # `integer` for `int`, `float` for `float64`, `list` for `[]string` --
    # which Tend.Template.RenderError documents as the one substitution every
    # message here makes.
    @refusals [
      {"eq", [1, 1.0], "incompatible types for comparison: integer and float"},
      {"ne", [1, 1.0], "incompatible types for comparison: integer and float"},
      {"eq", [true, 1], "incompatible types for comparison: boolean and integer"},
      {"eq", [1], "missing argument for comparison"},
      {"eq", [@outcomes, @outcomes], "non-comparable type [approve reject]: list"}
    ]

    for {name, args, detail} <- @refusals do
      test "#{name}#{inspect(args)} fails with #{inspect(detail)}" do
        assert Funcs.call(unquote(name), unquote(args)) == {:error, unquote(detail)}
      end
    end

    test "an empty list is refused too, which is the one answer Go splits in two" do
      # Go's zero PromptData holds a *nil* Outcomes, and `{{eq nil-slice
      # nil-slice}}` is `true` there; an empty-but-not-nil `[]string{}` is
      # `non-comparable type`. Elixir has one `[]` for both and the data
      # contract says it is `[]`, so this takes the error. `Tend.Template.Funcs`
      # says why.
      assert Funcs.call("eq", [[], []]) == {:error, "non-comparable type []: list"}
    end

    test "two different kinds that are neither of them basic name both" do
      # Go's four-part form, and its ordering is not the symmetric one an
      # Elixir reflex would write: value, type, then type, value.
      assert Funcs.call("eq", [@outcomes, @task]) ==
               {:error,
                "non-comparable types [approve reject]: list, " <>
                  "Tend.Template.FuncsTest.PromptTask: " <>
                  "{42 Fix the flaky test It fails on CI only.}"}
    end
  end

  describe "len" do
    test "a list is counted by element" do
      assert Funcs.call("len", [@subtasks]) == {:ok, 3}
      assert Funcs.call("len", [@outcomes]) == {:ok, 2}
    end

    test "a string is counted in bytes, not characters, as Go counts it" do
      assert Funcs.call("len", [@cwd]) == {:ok, 13}
      assert Funcs.call("len", ["héllo"]) == {:ok, 6}
    end

    test "a piped value is the same single argument" do
      # `{{.Outcomes | len}}`: the pipeline decides where the argument comes
      # from, not what len does with it.
      assert Funcs.call("len", [@outcomes]) == {:ok, 2}
    end

    test "a length is an integer, so it compares against an int literal" do
      # `{{len .Subtasks | eq 3}}`.
      assert {:ok, count} = Funcs.call("len", [@subtasks])
      assert Funcs.call("eq", [3, count]) == {:ok, true}
    end

    test "a value with no length is Go's len of type T" do
      assert Funcs.call("len", [3]) == {:error, "len of type integer"}
    end

    test "a struct has no length either" do
      assert Funcs.call("len", [@task]) ==
               {:error, "len of type Tend.Template.FuncsTest.PromptTask"}
    end

    test "nil is Go's len of nil pointer" do
      assert Funcs.call("len", [nil]) == {:error, "len of nil pointer"}
    end
  end
end
