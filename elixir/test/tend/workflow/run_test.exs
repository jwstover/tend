defmodule Tend.Workflow.RunTest do
  use ExUnit.Case, async: true

  alias Tend.Workflow.Run

  describe "the struct" do
    # CurrentStepRunID is *int64, so "no step run yet" is nil and never 0 -- 0
    # is a row id that cannot exist. Cwd is a plain string, so its zero is ""
    # and never nil. State has no atom for Go's RunState("") zero.
    test "defaults to the Go zero values, with nil for the pointers" do
      assert %Run{} == %Run{
               id: 0,
               workflow_id: 0,
               task_id: 0,
               cwd: "",
               state: nil,
               current_step_run_id: nil,
               tmux_session: "",
               error: "",
               started_at: nil,
               ended_at: nil
             }
    end
  end
end
