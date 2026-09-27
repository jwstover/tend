defmodule Tend.Workflow.StepKindTest do
  use ExUnit.Case, async: true

  alias Tend.Workflow.StepKind

  # A port of TestStepKindValid in internal/workflow/workflow_test.go.
  describe "valid?/1" do
    test "agent and gate are the two kinds" do
      assert StepKind.valid?(:agent)
      assert StepKind.valid?(:gate)
      assert StepKind.all() == [:agent, :gate]
    end

    test "nothing else is" do
      for kind <- [:human, :"", nil, "agent", 1] do
        refute StepKind.valid?(kind), "StepKind.valid?(#{inspect(kind)}) should be false"
      end
    end
  end

  describe "parse/1 and format/1" do
    test "every kind round trips through its stored name" do
      for kind <- StepKind.all() do
        assert StepKind.parse(StepKind.format(kind)) == {:ok, kind}
      end
    end

    test "the stored names are the Go constants' strings" do
      assert Enum.map(StepKind.all(), &StepKind.format/1) == ~w(agent gate)
    end

    test "parse/1 rejects everything Go's Valid rejects" do
      for name <- ["", "human", "AGENT", "Gate", " agent"] do
        assert StepKind.parse(name) == :error, "StepKind.parse(#{inspect(name)}) should be :error"
      end
    end

    test "format/1 refuses a kind that does not exist" do
      assert_raise FunctionClauseError, fn -> StepKind.format(:human) end
    end
  end
end
