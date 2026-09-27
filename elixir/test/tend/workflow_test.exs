defmodule Tend.WorkflowTest do
  use ExUnit.Case, async: true

  alias Tend.Workflow

  # A port of TestNormalizeName in internal/workflow/workflow_test.go.
  describe "normalize_name/1" do
    test "trims surrounding whitespace and preserves case" do
      assert Workflow.normalize_name("  Ship it ") == {:ok, "Ship it"}
    end

    test "rejects a name that is only whitespace" do
      assert Workflow.normalize_name(" \t") == {:error, :empty_name}
      assert Workflow.normalize_name("") == {:error, :empty_name}
    end

    test "leaves inner whitespace alone" do
      assert Workflow.normalize_name(" a  b ") == {:ok, "a  b"}
    end
  end

  # A port of TestNormalizeOutcome in internal/workflow/workflow_test.go: the
  # same five cases, plus the ones only the port can get wrong.
  describe "normalize_outcome/1" do
    test "the Go table" do
      assert Workflow.normalize_outcome("approve") == {:ok, "approve"}
      assert Workflow.normalize_outcome("  Approve  ") == {:ok, "approve"}
      assert Workflow.normalize_outcome("REJECT") == {:ok, "reject"}
      assert Workflow.normalize_outcome("") == {:error, :empty_outcome}
      assert Workflow.normalize_outcome("   ") == {:error, :empty_outcome}
    end

    test "the conventional outcomes normalize to themselves" do
      for outcome <- [
            Workflow.outcome_done(),
            Workflow.outcome_approve(),
            Workflow.outcome_reject()
          ] do
        assert Workflow.normalize_outcome(outcome) == {:ok, outcome}
        assert Workflow.normalize_outcome(String.upcase(outcome)) == {:ok, outcome}
      end
    end

    test "lower-cases the way strings.ToLower does" do
      # U+0130 is the one character where String.downcase/1's full mapping and
      # Go's simple per-rune mapping disagree: Go yields "i", String.downcase/1
      # yields "i" + U+0307. Left alone the port would store an outcome no edge
      # authored in Go could match. Same fold as Tend.Task.Project's.
      assert Workflow.normalize_outcome("İ") == {:ok, "i"}
      assert Workflow.normalize_outcome("FİX") == {:ok, "fix"}
    end

    test "a non-blank outcome of any shape survives" do
      assert Workflow.normalize_outcome("needs work") == {:ok, "needs work"}
      assert Workflow.normalize_outcome("\tDone\n") == {:ok, "done"}
    end
  end

  # A port of TestInUseErrorMatchesSentinel in
  # internal/workflow/workflow_test.go. Go asserts errors.Is(err, ErrInUse) and
  # the message; here the sentinel is the tuple's tag, so matching it is what
  # errors.Is does, and Tend.Error renders the message.
  describe "in_use_error/2" do
    test "carries the in-use sentinel as its tag" do
      assert {:in_use, "workflow 3", 7} = Workflow.in_use_error("workflow 3", 7)
    end

    test "renders the message Go's InUseError renders" do
      assert Tend.Error.message(Workflow.in_use_error("workflow 3", 7)) ==
               "workflow 3 is referenced by an active run 7"
    end

    test "the tag it carries is a listed sentinel" do
      {:in_use, _what, _run_id} = Workflow.in_use_error("step 1", 2)
      assert Tend.Error.sentinel?(:in_use)
    end
  end

  describe "the conventional outcomes" do
    test "are the Go constants' strings" do
      assert Workflow.outcome_done() == "done"
      assert Workflow.outcome_approve() == "approve"
      assert Workflow.outcome_reject() == "reject"
    end
  end

  describe "the struct" do
    test "defaults to the Go zero values, with nil where Go has time.Time{}" do
      assert %Workflow{} == %Workflow{
               id: 0,
               name: "",
               description: "",
               created_at: nil,
               updated_at: nil,
               step_count: 0
             }
    end
  end
end
