defmodule Tend.Workflow.StepTest do
  use ExUnit.Case, async: true

  alias Tend.Workflow.Step

  describe "the struct" do
    # Go's zero kind is StepKind(""), which Valid rejects, so the port has no
    # atom for it and the default is nil. Defaulting to :agent would make an
    # unpopulated struct claim to be a runnable agent step.
    test "defaults to the Go zero values, with nil where Go has time.Time{}" do
      assert %Step{} == %Step{
               id: 0,
               workflow_id: 0,
               name: "",
               kind: nil,
               prompt_md: "",
               model: "",
               permission_mode: "",
               sort_order: 0,
               created_at: nil,
               updated_at: nil
             }
    end
  end
end
