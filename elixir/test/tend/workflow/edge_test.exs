defmodule Tend.Workflow.EdgeTest do
  use ExUnit.Case, async: true

  alias Tend.Workflow.Edge

  describe "the struct" do
    # MaxIterations is *int64: nil is "no bound", and 0 would be a bound of
    # zero -- the opposite meaning. The parity test compares field names only,
    # so this is what pins the pointer's semantics.
    test "defaults to the Go zero values, with nil for the *int64" do
      assert %Edge{} == %Edge{
               id: 0,
               from_step_id: 0,
               outcome: "",
               to_step_id: 0,
               max_iterations: nil
             }
    end
  end
end
