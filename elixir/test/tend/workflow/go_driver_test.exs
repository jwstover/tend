defmodule Tend.Workflow.GoDriverTest do
  @moduledoc """
  Byte parity with `internal/workflow` itself.

  `Tend.Workflow.GoParityTest` reads the Go *source* and checks shape; this
  runs the Go *functions* and compares their output to the port's, byte for
  byte. Both are needed: a field renamed on one side is a shape failure, and a
  sentence reworded in `handoff.go` is only ever a byte failure.

  Tagged `:go` and skipped where there is no Go toolchain, the same rule
  `Tend.Template.ParityTest` follows. `Tend.Workflow.GoDriver` says how the
  driver reaches an `internal/` package.
  """

  use ExUnit.Case, async: true

  alias Tend.Error
  alias Tend.Workflow.GoDriver
  alias Tend.Workflow.Handoff
  alias Tend.Workflow.Handoff.Context
  alias Tend.Workflow.Prompt
  alias Tend.Workflow.PromptCases

  @moduletag :go

  if not GoDriver.available?() do
    @moduletag skip: "the Go toolchain is not installed"
  end

  # Every case, in one driver run: `go run` costs a second or two and there is
  # nothing to learn from paying it six times.
  setup_all do
    render =
      for template <- PromptCases.templates(),
          data <- PromptCases.both_data_sets(),
          do: %{kind: "render", template: template, data: data}

    named =
      for %{template: template, data: data} <- PromptCases.render_cases(),
          do: %{kind: "render", template: template, data: data}

    validate =
      for template <- PromptCases.templates(), do: %{kind: "validate", template: template}

    handoff = handoff_cases()

    cases = render ++ named ++ validate ++ handoff
    {:ok, results: cases |> GoDriver.run() |> Enum.zip(cases) |> Map.new(fn {r, c} -> {c, r} end)}
  end

  # The hand-off cases: handoff_test.go's own, plus the shapes its table does
  # not reach -- an empty outcome list on each of the three prompts, a name
  # needing Go's %q escaping, and a reason with whitespace to trim.
  defp handoff_cases do
    contexts = [
      %{workflow: "ship it", step: "review", iteration: 2, outcomes: ["approve", "reject"]},
      %{workflow: "w", step: "ship", iteration: 1, outcomes: []},
      %{workflow: "w", step: "s", iteration: 0, outcomes: ["done"]},
      %{workflow: "say \"hi\"", step: "tab\there", iteration: 9, outcomes: ["a\\b"]},
      %{workflow: "w", step: "s", iteration: 1_000_000, outcomes: ["a", "b", "c"]}
    ]

    outcome_sets = [[], ["done"], ["approve"], ["approve", "reject"], ["done", "retry"]]
    reasons = ["boom", "  step exited without finish_step\n", "", "   "]

    Enum.map(contexts, &Map.put(&1, :kind, "system_prompt")) ++
      Enum.map(outcome_sets, &%{kind: "nudge", step: "review", outcomes: &1}) ++
      Enum.map(outcome_sets, &%{kind: "fallback", outcomes: &1}) ++
      for(
        reason <- reasons,
        set <- outcome_sets,
        do: %{kind: "retry", step: "review", reason: reason, outcomes: set}
      )
  end

  defp go(%{results: results}, one), do: Map.fetch!(results, one)

  defp data(name), do: Map.fetch!(PromptCases.data_sets(), name)

  describe "render/2" do
    test "renders every prompt_test.go case to Go's bytes, empty data and populated", context do
      # The acceptance criterion, stated as a test: every template the Go
      # prompt test writes, against both an empty PromptData and a populated
      # one, through Go's own RenderPrompt and through the port.
      for template <- PromptCases.templates(), set <- PromptCases.both_data_sets() do
        one = %{kind: "render", template: template, data: set}
        assert_same_render(go(context, one), Prompt.render(template, data(set)), one)
      end
    end

    test "renders each case against the data set the Go table names for it", context do
      # The other half: three of the render cases name a partial PromptData of
      # their own, and those are where {{.Input}} rendering as nothing rather
      # than <nil> is actually decided.
      for %{template: template, data: set} <- PromptCases.render_cases() do
        one = %{kind: "render", template: template, data: set}
        assert_same_render(go(context, one), Prompt.render(template, data(set)), one)
      end
    end
  end

  describe "validate/1" do
    test "agrees with Go's ValidatePrompt on every template", context do
      # This is also what pins sample_data/0: Go's samplePromptData is
      # unexported, so ValidatePrompt is the only way to ask what it holds, and
      # the truthy-branch cases in the table are decided by it.
      for template <- PromptCases.templates() do
        one = %{kind: "validate", template: template}
        result = go(context, one)

        case Prompt.validate(template) do
          :ok ->
            assert result["ok"], "Go refuses #{inspect(template)}: #{result["err"]}"

          {:error, reason} ->
            refute result["ok"], "Go accepts #{inspect(template)}, the port does not"
            assert_same_refusal(result["err"], Error.message(reason), template)
        end
      end
    end
  end

  describe "the hand-off text" do
    test "step_system_prompt/1 is StepSystemPrompt, byte for byte", context do
      for %{kind: "system_prompt"} = one <- handoff_cases() do
        assert go(context, one)["out"] ==
                 Handoff.step_system_prompt(%Context{
                   workflow: one.workflow,
                   step: one.step,
                   iteration: one.iteration,
                   outcomes: one.outcomes
                 }),
               "StepSystemPrompt differs for #{inspect(one)}"
      end
    end

    test "nudge_prompt/2 is NudgePrompt, byte for byte", context do
      for %{kind: "nudge"} = one <- handoff_cases() do
        assert go(context, one)["out"] == Handoff.nudge_prompt(one.step, one.outcomes),
               "NudgePrompt differs for #{inspect(one)}"
      end
    end

    test "retry_prompt/3 is RetryPrompt, byte for byte", context do
      for %{kind: "retry"} = one <- handoff_cases() do
        assert go(context, one)["out"] ==
                 Handoff.retry_prompt(one.step, one.reason, one.outcomes),
               "RetryPrompt differs for #{inspect(one)}"
      end
    end

    test "fallback_allowed?/1 is FallbackAllowed on every case", context do
      for %{kind: "fallback"} = one <- handoff_cases() do
        assert go(context, one)["bool"] == Handoff.fallback_allowed?(one.outcomes),
               "FallbackAllowed differs for #{inspect(one.outcomes)}"
      end
    end
  end

  # Go's bytes against the port's, with the refusals compared the way
  # Tend.Template.Parity compares them: the two engines cannot name the same
  # types or report the same column, so what has to match is the verdict, the
  # ErrInvalidPrompt prefix, and the identifier the message blames.
  defp assert_same_render(result, rendered, one) do
    case {result["ok"], rendered} do
      {true, {:ok, output}} ->
        assert output == result["out"], "rendered bytes differ for #{inspect(one)}"

      {false, {:error, reason}} ->
        assert_same_refusal(result["err"], Error.message(reason), one.template)

      {true, {:error, reason}} ->
        flunk("Go renders #{inspect(one)}; the port refuses it: #{Error.message(reason)}")

      {false, {:ok, output}} ->
        flunk(
          "Go refuses #{inspect(one)} (#{result["err"]}); the port rendered #{inspect(output)}"
        )
    end
  end

  defp assert_same_refusal(go_message, port_message, template) do
    # Go's wrapping, which the TUI's validate action shows as-is.
    assert String.starts_with?(go_message, "invalid prompt template: ")
    assert String.starts_with?(port_message, "invalid prompt template: ")

    # And the load-bearing part: whatever identifier Go blames, the port blames
    # too. Go words it `field Nope in type main.PromptData` or
    # `unexpected ...`; the identifier is what a user reads the message for.
    for name <- blamed_identifiers(go_message) do
      assert port_message =~ name,
             "Go blames #{inspect(name)} in #{inspect(template)}; the port says #{port_message}"
    end
  end

  # The identifiers Go's message names: the field in `can't evaluate field X`
  # and the key in `map has no entry for key "X"`. A parse error names no
  # identifier, and this returns none for it.
  defp blamed_identifiers(message) do
    ~r/can't evaluate field (\w+)|no entry for key "(\w+)"/
    |> Regex.scan(message)
    |> Enum.map(&List.last/1)
  end
end
