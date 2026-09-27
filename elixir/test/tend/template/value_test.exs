defmodule Tend.Template.ValueTest.PromptSubtask do
  @moduledoc false
  # internal/workflow/prompt.go's PromptSubtask, field for field and in
  # declaration order, because declaration order is what `%v` prints.
  defstruct id: 0, title: "", state: "", is_blocked: false, depends_on: []
end

defmodule Tend.Template.ValueTest do
  @moduledoc """
  `Tend.Template.Value` at its own boundary.

  The three questions it answers -- is this true, how does it print, what is
  it called -- used to be private to `Tend.Template.Renderer`, where they
  could only be reached through a whole template. `Tend.Template.RendererTest`
  still pins them end to end, against Go; this pins the function contract the
  extraction created, and the one place where the contract is deliberately
  not Go's.

  Every Go value quoted here was captured from `text/template` with
  `Option("missingkey=error")` over a structurally identical `PromptData`.
  """

  use ExUnit.Case, async: true

  doctest Tend.Template.Value

  alias Tend.Template
  alias Tend.Template.Value
  alias Tend.Template.ValueTest.PromptSubtask

  describe "truthy?/1 is Go's isTrue, not Elixir's" do
    test "the falsey set is Go's: nil, false, zero, empty string, empty list, empty map" do
      for value <- [nil, false, 0, 0.0, "", [], %{}] do
        refute Value.truthy?(value), "expected #{inspect(value)} to be false, as in Go"
      end
    end

    test "and everything else is true, including a zero-valued struct" do
      # A Go struct is always true, however empty it is -- there is no
      # "empty struct" for isTrue to look at.
      for value <- [true, 1, -1, 0.5, "a", [0], %{a: 1}, %PromptSubtask{}] do
        assert Value.truthy?(value), "expected #{inspect(value)} to be true, as in Go"
      end
    end

    test "the one that bites: index 0 takes the {{else}} arm" do
      refute Value.truthy?(0)
    end
  end

  describe "format/1 is Go's %v" do
    test "scalars" do
      assert Value.format(42) == "42"
      assert Value.format(-1) == "-1"
      assert Value.format(true) == "true"
      assert Value.format("a b") == "a b"
      assert Value.format("") == ""
    end

    test "nil is Go's <no value>, which is what fmt prints for a nil interface" do
      assert Value.format(nil) == "<no value>"
    end

    test "a list is space-separated inside square brackets, and nests" do
      assert Value.format([]) == "[]"
      assert Value.format(["approve", "reject"]) == "[approve reject]"
      assert Value.format([[1, 2], [3]]) == "[[1 2] [3]]"
    end

    test "a struct prints its fields in declaration order, as Go prints a struct" do
      subtask = %PromptSubtask{id: 44, title: "wire the store", state: "todo", depends_on: [43]}

      assert Value.format(subtask) == "{44 wire the store todo false [43]}"
    end

    test "a map is Go's map[...], sorted by key the way Go sorts it" do
      assert Value.format(%{}) == "map[]"
      assert Value.format(%{"b" => 2, "a" => 1}) == "map[a:1 b:2]"
    end
  end

  describe "format/1 on a float is Go's %v only up to strconv's threshold" do
    # `%v` on a float is `strconv.FormatFloat(v, 'g', -1, 64)`, and for the
    # shortest precision `ftoa.go` pins the exponent-form cutoff at 6 --
    # not at the digit count. So Go goes exponential at a *million*.
    test "below the threshold the two agree, byte for byte" do
      assert Value.format(1.0) == "1"
      assert Value.format(1.5) == "1.5"
      assert Value.format(-1.5) == "-1.5"
      assert Value.format(999_999.0) == "999999"
      assert Value.format(0.0001) == "0.0001"

      assert Template.render("{{999999.0}}", %{}) == {:ok, "999999"}
      assert Template.render("{{0.0001}}", %{}) == {:ok, "0.0001"}
    end

    # Left as a divergence on purpose: no field of the prompt data is a
    # float, so only a float *literal* written into a prompt can reach it,
    # and porting strconv's ftoa to match is more machinery than the case
    # is worth. `Tend.Template.Value`'s format_float/1 says so; this is the
    # evidence for the thresholds it quotes.
    @divergences [
      {"{{1e6}}", "1000000", "1e+06"},
      {"{{1000000.0}}", "1000000", "1e+06"},
      {"{{2.5e10}}", "25000000000", "2.5e+10"},
      {"{{1234567890123.0}}", "1234567890123", "1.234567890123e+12"},
      {"{{1e20}}", "100000000000000000000", "1e+20"},
      {"{{0.00001}}", "1.0e-5", "1e-05"},
      {"{{1e-7}}", "1.0e-7", "1e-07"}
    ]

    for {source, here, go} <- @divergences do
      test "#{source} is #{inspect(here)} here and #{inspect(go)} in Go" do
        assert Template.render(unquote(source), %{}) == {:ok, unquote(here)}
      end
    end

    test "negative zero is the third case: Go keeps the sign and this does not" do
      # Go: {{-0.0}} -> -0. trunc(-0.0) is 0, so the sign is gone by the
      # time the integer form is chosen.
      assert Value.format(-0.0) == "0"
      assert Template.render("{{-0.0}}", %{}) == {:ok, "0"}
    end
  end

  describe "type_name/1 names an Elixir type where Go names a Go one" do
    test "a struct is named by its module" do
      assert Value.type_name(%PromptSubtask{}) == "Tend.Template.ValueTest.PromptSubtask"
    end

    test "everything else is its Elixir type" do
      assert Value.type_name("s") == "binary"
      assert Value.type_name(1) == "integer"
      assert Value.type_name(1.5) == "float"
      assert Value.type_name(true) == "boolean"
      assert Value.type_name([]) == "list"
      assert Value.type_name(%{}) == "map"
      assert Value.type_name(nil) == "nil"
      assert Value.type_name(:atom) == "atom"
    end

    test "nil and a boolean are named before the atom clause can claim them" do
      # Both are atoms in Elixir and neither is an atom in Go, so the order
      # of the clauses is load-bearing.
      refute Value.type_name(nil) == "atom"
      refute Value.type_name(false) == "atom"
    end
  end
end
