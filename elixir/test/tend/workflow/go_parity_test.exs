defmodule Tend.Workflow.GoParityTest do
  @moduledoc """
  `Tend.GoParityTest`'s counterpart for `internal/workflow`: reads the Go
  sources the workflow modules were ported from and fails when the two drift
  apart.

  A separate file rather than more cases in the task one, because the two
  packages are ported in separate parts and each should be able to say what it
  covers.
  """

  use ExUnit.Case, async: true

  alias Tend.Workflow
  alias Tend.Workflow.Edge
  alias Tend.Workflow.Run
  alias Tend.Workflow.RunState
  alias Tend.Workflow.Step
  alias Tend.Workflow.StepKind
  alias Tend.Workflow.StepRun

  # The two files this part of the port covers. Their siblings (prompt.go,
  # graph.go, handoff.go) belong to later parts and are deliberately not read.
  @go_dir Path.expand("../../../../internal/workflow", __DIR__)
  @go_files ["workflow.go", "status.go"]

  # For the one check that has to look wider than a single part: every domain
  # package whose sentinels could have landed in Tend.Error.
  @domain_dirs [
    Path.expand("../../../../internal/task", __DIR__),
    Path.expand("../../../../internal/workflow", __DIR__)
  ]

  @sentinel_re ~r/(Err\w+)\s*=\s*(?:errors\.New|fmt\.Errorf)\("([^"]*)"\)/

  defp go_source(file) do
    path = Path.join(@go_dir, file)
    assert File.exists?(path), "expected the Go source at #{path}"
    File.read!(path)
  end

  defp go_sources, do: Enum.map_join(@go_files, "\n", &go_source/1)

  # Every `ErrX = errors.New("...")` or `ErrX = fmt.Errorf("...")` in the files
  # this part covers, as {"ErrX", "message"}. Same rule as the task parity
  # test: which constructor built it is the author's choice, not a distinction
  # this port cares about.
  defp go_sentinels, do: scan_sentinels(go_sources())

  defp scan_sentinels(source) do
    @sentinel_re
    |> Regex.scan(source)
    |> Enum.map(fn [_whole, name, message] -> {name, message} end)
  end

  defp atom_for(name) do
    name |> String.replace_prefix("Err", "") |> Macro.underscore() |> String.to_atom()
  end

  # The field names of a Go struct, in declaration order. The five structs read
  # here have no nested braces, so matching to the first unindented "}" is
  # enough.
  defp go_struct_fields(file, struct_name) do
    source = go_source(file)

    [_whole, body] =
      Regex.run(~r/\ntype #{struct_name} struct \{\n(.*?)\n\}\n/s, source) ||
        flunk("no `type #{struct_name} struct` in #{file}")

    body
    |> String.split("\n")
    |> Enum.map(&String.trim/1)
    |> Enum.reject(&(&1 == "" or String.starts_with?(&1, "//")))
    |> Enum.map(&(&1 |> String.split(~r/\s+/) |> hd()))
  end

  defp elixir_fields(struct), do: struct |> Map.from_struct() |> Map.keys() |> Enum.sort()

  defp expected_fields(file, struct_name) do
    file
    |> go_struct_fields(struct_name)
    |> Enum.map(&(&1 |> Macro.underscore() |> String.to_atom()))
    |> Enum.sort()
  end

  describe "sentinel errors" do
    test "the Go sources really do define the sentinels we think they do" do
      # Guards the regex itself: if it silently stopped matching, every other
      # assertion in this block would pass vacuously. All ten are in
      # workflow.go; status.go defines none.
      assert length(go_sentinels()) == 10
      assert scan_sentinels(go_source("status.go")) == []
    end

    test "each Go sentinel maps to exactly one atom, by the documented rule" do
      expected = go_sentinels() |> Enum.map(fn {name, _msg} -> atom_for(name) end) |> Enum.sort()

      assert length(Enum.uniq(expected)) == length(expected)
      assert expected -- Tend.Error.sentinels() == []
    end

    test "each atom's message is its Go counterpart's, verbatim" do
      for {name, message} <- go_sentinels() do
        atom = atom_for(name)

        assert Tend.Error.message(atom) == message,
               "#{name} says #{inspect(message)}; :#{atom} says #{inspect(Tend.Error.message(atom))}"
      end
    end

    test "no atom in Tend.Error was invented rather than ported" do
      # The direction the per-package tests cannot check on their own: every
      # sentinel Tend.Error lists comes from a Go sentinel in one of the domain
      # packages. Sentinels the Go tree has and the port has not are fine here
      # -- they are just later parts -- and each part's own test above is what
      # catches a missing one.
      ported =
        @domain_dirs
        |> Enum.flat_map(&Path.wildcard(Path.join(&1, "*.go")))
        |> Enum.reject(&String.ends_with?(&1, "_test.go"))
        |> Enum.flat_map(&(&1 |> File.read!() |> scan_sentinels()))
        |> Enum.map(fn {name, _msg} -> atom_for(name) end)
        |> MapSet.new()

      assert Enum.reject(Tend.Error.sentinels(), &MapSet.member?(ported, &1)) == []
    end
  end

  describe "struct fields" do
    test "Tend.Workflow has the fields of Go's workflow.Workflow" do
      assert elixir_fields(%Workflow{}) == expected_fields("workflow.go", "Workflow")
    end

    test "Tend.Workflow.Step has the fields of Go's workflow.Step" do
      assert elixir_fields(%Step{}) == expected_fields("workflow.go", "Step")
    end

    test "Tend.Workflow.Edge has the fields of Go's workflow.Edge" do
      assert elixir_fields(%Edge{}) == expected_fields("workflow.go", "Edge")
    end

    test "Tend.Workflow.Run has the fields of Go's workflow.Run" do
      assert elixir_fields(%Run{}) == expected_fields("workflow.go", "Run")
    end

    test "Tend.Workflow.StepRun has the fields of Go's workflow.StepRun" do
      assert elixir_fields(%StepRun{}) == expected_fields("workflow.go", "StepRun")
    end
  end

  describe "enumerations" do
    test "the step kinds are the Go StepKind constants" do
      names =
        ~r/Step(\w+)\s+StepKind = "(\w+)"/
        |> Regex.scan(go_source("workflow.go"))
        |> Enum.map(fn [_whole, _const, name] -> name end)

      assert names != []
      assert Enum.map(StepKind.all(), &StepKind.format/1) == names
    end

    test "the run states are the Go RunState constants" do
      names =
        ~r/Run(\w+)\s+RunState = "(\w+)"/
        |> Regex.scan(go_source("workflow.go"))
        |> Enum.map(fn [_whole, _const, name] -> name end)

      assert names != []
      assert Enum.map(RunState.all(), &RunState.format/1) == names
    end

    test "the terminal states are the ones Go's Terminal names" do
      [_whole, body] =
        Regex.run(
          ~r/func \(s RunState\) Terminal\(\) bool \{\n(.*?)\n\}\n/s,
          go_source("workflow.go")
        ) ||
          flunk("no `func (s RunState) Terminal()` in workflow.go")

      names =
        ~r/Run(\w+)/
        |> Regex.scan(body)
        |> Enum.map(fn [_whole, suffix] -> suffix end)
        |> Enum.map(&(&1 |> Macro.underscore() |> String.to_atom()))
        |> Enum.sort()

      assert names != []
      assert names == RunState.all() |> Enum.filter(&RunState.terminal?/1) |> Enum.sort()
    end
  end

  describe "the conventional outcomes" do
    test "are the Go Outcome constants" do
      names =
        ~r/^\s*(?:const\s+)?Outcome(\w+)\s*=\s*"(\w+)"$/m
        |> Regex.scan(go_source("workflow.go"))
        |> Enum.map(fn [_whole, _const, value] -> value end)

      assert names != []

      assert Enum.sort(names) ==
               Enum.sort([
                 Workflow.outcome_done(),
                 Workflow.outcome_approve(),
                 Workflow.outcome_reject()
               ])
    end
  end

  describe "the run-to-session-status map" do
    test "is the one internal/workflow/status.go writes" do
      pairs =
        ~r/case Run(\w+):\n\s*return task\.Session(\w+), true/
        |> Regex.scan(go_source("status.go"))
        |> Enum.map(fn [_whole, state, status] ->
          {state |> Macro.underscore() |> String.to_atom(),
           status |> Macro.underscore() |> String.to_atom()}
        end)

      assert length(pairs) == 4

      for {state, status} <- pairs do
        assert RunState.session_status(state) == {:ok, status}
      end

      # And nothing beyond them: the map is total over the state set, so every
      # state Go does not name reports nothing.
      mapped = MapSet.new(pairs, &elem(&1, 0))

      for state <- RunState.all(), not MapSet.member?(mapped, state) do
        assert RunState.session_status(state) == :error
      end
    end
  end
end
