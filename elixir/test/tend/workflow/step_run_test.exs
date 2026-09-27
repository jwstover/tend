defmodule Tend.Workflow.StepRunTest do
  use ExUnit.Case, async: true

  alias Tend.Workflow.StepRun

  describe "finished?/1" do
    test "a step run with no end time has not handed off" do
      refute StepRun.finished?(%StepRun{})
    end

    test "an end time is the hand-off, whatever the outcome says" do
      assert StepRun.finished?(%StepRun{ended_at: ~U[2026-09-13 12:00:00Z]})

      # An outcome without an end time is not finished, and an end time with an
      # empty deliverable is -- Go reads EndedAt and nothing else.
      refute StepRun.finished?(%StepRun{outcome: "done"})

      assert StepRun.finished?(%StepRun{
               ended_at: ~U[2026-09-13 12:00:00Z],
               outcome: "",
               deliverable: ""
             })
    end
  end

  describe "the struct" do
    test "defaults to the Go zero values, with nil where Go has time.Time{}" do
      run = %StepRun{}

      assert run.id == 0
      assert run.run_id == 0
      assert run.step_id == 0
      assert run.iteration == 0
      assert run.started_at == nil
      assert run.ended_at == nil

      for field <- [
            :session_external_id,
            :prompt_rendered,
            :system_prompt,
            :model,
            :permission_mode,
            :input,
            :feedback,
            :outcome,
            :deliverable,
            :log_path
          ] do
        assert Map.fetch!(run, field) == "", "#{field} should default to Go's \"\""
      end
    end
  end
end
