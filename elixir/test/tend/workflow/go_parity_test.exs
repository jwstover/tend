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
  alias Tend.Workflow.Graph
  alias Tend.Workflow.Handoff
  alias Tend.Workflow.Handoff.Context
  alias Tend.Workflow.Prompt
  alias Tend.Workflow.PromptData
  alias Tend.Workflow.PromptSubtask
  alias Tend.Workflow.PromptTask
  alias Tend.Workflow.Run
  alias Tend.Workflow.RunState
  alias Tend.Workflow.Step
  alias Tend.Workflow.StepKind
  alias Tend.Workflow.StepRun

  # Every file of internal/workflow the port covers -- which, with prompt.go
  # and handoff.go landed, is the whole package.
  @go_dir Path.expand("../../../../internal/workflow", __DIR__)
  @go_files ["workflow.go", "status.go", "graph.go", "prompt.go", "handoff.go"]

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

  # The port's own source, for the checks that have to pin a literal on both
  # sides rather than trusting that a Go-side grep also constrains Elixir.
  @elixir_dir Path.expand("../../../lib/tend", __DIR__)

  defp elixir_source(file) do
    path = Path.join(@elixir_dir, file)
    assert File.exists?(path), "expected the Elixir source at #{path}"
    File.read!(path)
  end

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
      # assertion in this block would pass vacuously. Eleven are in
      # workflow.go and one -- ErrInvalidPrompt -- in prompt.go; status.go,
      # graph.go and handoff.go define none.
      assert length(go_sentinels()) == 12
      assert length(scan_sentinels(go_source("prompt.go"))) == 1
      assert scan_sentinels(go_source("status.go")) == []
      assert scan_sentinels(go_source("graph.go")) == []
      assert scan_sentinels(go_source("handoff.go")) == []
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

    test "Tend.Workflow.PromptTask has the fields of Go's workflow.PromptTask" do
      assert elixir_fields(%PromptTask{}) == expected_fields("prompt.go", "PromptTask")
    end

    test "Tend.Workflow.PromptSubtask has the fields of Go's workflow.PromptSubtask" do
      assert elixir_fields(%PromptSubtask{}) == expected_fields("prompt.go", "PromptSubtask")
    end

    test "Tend.Workflow.PromptData has the fields of Go's workflow.PromptData" do
      assert elixir_fields(%PromptData{}) == expected_fields("prompt.go", "PromptData")
    end

    test "Tend.Workflow.Handoff.Context has the fields of Go's workflow.HandoffContext" do
      assert elixir_fields(%Context{}) == expected_fields("handoff.go", "HandoffContext")
    end

    test "the three prompt structs keep Go's declaration order, not just its names" do
      # Load-bearing rather than tidy: Tend.Template.Value prints a struct's
      # fields in declaration order to match Go's %v, so a bare {{.Task}} or
      # {{.Subtasks}} in a stored prompt renders Go's bytes only while the two
      # agree. elixir_fields/1 sorts, so it cannot see this.
      for {struct, name} <- [
            {%PromptTask{}, "PromptTask"},
            {%PromptSubtask{}, "PromptSubtask"},
            {%PromptData{}, "PromptData"}
          ] do
        declared =
          struct.__struct__.__info__(:struct) |> Enum.map(& &1.field) |> Enum.map(&to_string/1)

        assert declared ==
                 Enum.map(go_struct_fields("prompt.go", name), &Macro.underscore/1),
               "#{name}'s field order drifted from Go's"
      end
    end
  end

  describe "the prompt port" do
    test "sample_data/0 is Go's samplePromptData, value for value" do
      # samplePromptData is unexported, so the Go driver cannot render against
      # it; this reads the literal instead. Tend.Workflow.GoDriverTest pins its
      # *effect* -- ValidatePrompt runs against it -- and this pins the values.
      [_whole, body] =
        Regex.run(~r/samplePromptData = PromptData\{\n(.*?)\n\}\n/s, go_source("prompt.go")) ||
          flunk("no `samplePromptData = PromptData{` in prompt.go")

      sample = Prompt.sample_data()

      assert body =~
               ~s|ID: #{sample.task.id}, Title: "#{sample.task.title}", Body: "#{sample.task.body}"|

      assert body =~ ~s|Cwd:       "#{sample.cwd}"|
      assert body =~ ~s|Input:     "#{sample.input}"|
      assert body =~ ~s|Feedback:  "#{sample.feedback}"|
      assert body =~ "Iteration: #{sample.iteration}"
      assert body =~ "Outcomes:  []string{OutcomeDone}"
      assert sample.outcomes == [Workflow.outcome_done()]

      for subtask <- sample.subtasks do
        assert body =~ ~s|ID: #{subtask.id}, Title: "#{subtask.title}", State: "#{subtask.state}"|
      end

      assert [
               %PromptSubtask{is_blocked: false, depends_on: []},
               %PromptSubtask{is_blocked: true, depends_on: [2]}
             ] =
               sample.subtasks
    end
  end

  describe "the step tool ids" do
    test "are the Go constants, verbatim" do
      # A session loads these by name from the hand-off block, so a typo on
      # either side is a step that cannot hand off at all.
      ids =
        ~r/^\s*(\w+Tool)\s*=\s*"([\w_]+)"$/m
        |> Regex.scan(go_source("handoff.go"))
        |> Map.new(fn [_whole, name, value] -> {name, value} end)

      assert ids == %{
               "FinishStepTool" => Handoff.finish_step_tool(),
               "GetWorkflowStepTool" => Handoff.get_workflow_step_tool()
             }
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

  describe "the graph problems" do
    # The TUI's validate action renders a problem verbatim, so the message text
    # is the port's contract. These read the format strings out of Go's
    # Validate rather than trusting a copy of them here: a rule added on the Go
    # side fails this test, which is how the port hears about it.
    #
    # One such rule is already known and owed. PR #75,
    # feat/validate-permission-mode, was OPEN against main when this landed; it
    # adds a seventh add(st, ...) to Validate, before the "%v" below:
    #
    #   "no permission mode: a headless step denies every tool call that would
    #    need approval; set one on the step (acceptEdits, or bypassPermissions
    #    for a step that runs commands)"
    #
    # This branch ports the committed graph.go only, so Tend.Workflow.Graph
    # does not have that rule and this list does not carry that format.
    # Whichever of #75 and this branch merges second turns this test red on
    # every PR, by design -- a loud gap rather than a silent one. Closing it in
    # the rebase: add the format to the list below, in Go's source order (fifth,
    # ahead of "%v"), and add the clause it comes from at the head of
    # Tend.Workflow.Graph's prompt_problems/4 agent body, ahead of the injected
    # prompt check. Tend.Workflow.Step already carries permission_mode.
    test "Validate's message formats are the ones the port reproduces" do
      assert go_validate_formats() == [
               "unreachable: no edge leads here",
               "no edge leaves it; the run would end here, before %s",
               "edge on %q leads to a step that no longer exists",
               "%q loops back to %s with no max iterations",
               # The prompt error, which the port takes from its injected
               # prompt validator. See Tend.Workflow.Graph's module doc.
               "%v",
               "prompt mentions %q but no edge routes it"
             ]
    end

    test "the workflow-level problem is the one Go returns for no steps" do
      [_whole, message] =
        Regex.run(~r/return \[\]Problem\{\{Msg: "([^"]*)"\}\}/, go_source("graph.go")) ||
          flunk("no workflow-level Problem literal in graph.go")

      assert [%{msg: ^message}] = Graph.validate([], [])
    end

    test "every message the port produces is one of Go's formats, and each format is used" do
      patterns = Enum.map(go_validate_formats(), &format_to_regex/1)

      steps = [
        %Step{id: 1, name: "implement", kind: :agent, prompt_md: "finish with approve."},
        %Step{id: 2, name: "review", kind: :agent, prompt_md: "Review it."},
        %Step{id: 3, name: "ship", kind: :agent, prompt_md: "Open the PR."}
      ]

      edges = [
        %Edge{from_step_id: 1, outcome: "reject", to_step_id: 1},
        %Edge{from_step_id: 1, outcome: "done", to_step_id: 99}
      ]

      broken = fn _prompt -> {:error, "invalid prompt template: nope"} end
      problems = Graph.validate(steps, edges, prompt_validator: broken)

      for %{msg: msg} <- problems do
        assert Enum.any?(patterns, &Regex.match?(&1, msg)),
               "#{inspect(msg)} matches no format in graph.go"
      end

      for pattern <- patterns do
        assert Enum.any?(problems, &Regex.match?(pattern, &1.msg)),
               "no problem exercises #{inspect(Regex.source(pattern))}"
      end
    end

    test "the vocabulary Validate seeds itself with is Go's conventionalOutcomes" do
      [_whole, body] =
        Regex.run(~r/conventionalOutcomes = \[\]string\{([^}]*)\}/, go_source("graph.go")) ||
          flunk("no conventionalOutcomes in graph.go")

      names =
        body
        |> String.split(",")
        |> Enum.map(&String.trim/1)
        |> Enum.reject(&(&1 == ""))

      assert names == ["OutcomeApprove", "OutcomeReject"]

      # Both are looked for in a prompt whether or not any edge names them,
      # which is what makes them a seeded vocabulary rather than edge data.
      step = %Step{
        id: 1,
        name: "review",
        kind: :agent,
        prompt_md: "finish with #{Workflow.outcome_approve()} or #{Workflow.outcome_reject()}."
      }

      assert [
               %{msg: ~s(prompt mentions "approve" but no edge routes it)},
               %{msg: ~s(prompt mentions "reject" but no edge routes it)}
             ] = Graph.validate([step], [])
    end

    test "the hand-off cue is the Go regexp, verbatim" do
      # Asserted on BOTH sides, so a change to either has to be made on both.
      # Grepping only Go would leave @handoff_cue free to drift -- dropping
      # the plural alternative, or the finish_step one, or the semicolon from
      # the sentence terminators all pass every behavioural test the Go table
      # ports, which is why graph_test.exs's "the hand-off cue" cases exist
      # alongside this one.
      pattern = ~S<(?i)\b(?:finish(?:_step)?|outcomes?)\b[^.;\n]*>

      assert go_source("graph.go") =~ "`" <> pattern <> "`"
      assert elixir_source("workflow/graph.ex") =~ "~r/" <> pattern <> "/"
    end
  end

  # Every format string Validate hands to its `add` helper, in source order.
  defp go_validate_formats do
    ~r/add\(st, "((?:[^"\\]|\\.)*)"/
    |> Regex.scan(go_source("graph.go"))
    |> Enum.map(fn [_whole, format] -> format end)
  end

  # A Go format string as a regex over the message it produces. %q is a quoted
  # string, %s and %v anything.
  defp format_to_regex(format) do
    pattern =
      format
      |> String.split(~r/%[sqv]/, include_captures: true)
      |> Enum.map_join(fn
        "%q" -> ~S{"(?:[^"\\]|\\.)*"}
        verb when verb in ["%s", "%v"] -> ".+"
        literal -> Regex.escape(literal)
      end)

    Regex.compile!("\\A" <> pattern <> "\\z")
  end
end
