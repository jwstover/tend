defmodule Tend.Workflow.HandoffTest do
  @moduledoc """
  A port of `internal/workflow/handoff_test.go`.

  The Go cases are here case for case. What the block actually *says* is pinned
  against Go's own functions in `Tend.Workflow.GoDriverTest`; this file checks
  the rules -- the fallback predicate, the `done` fill-in for an edgeless step,
  and the sentences the Go test names because a real run went wrong without
  them.
  """

  use ExUnit.Case, async: true

  alias Tend.Workflow.Handoff
  alias Tend.Workflow.Handoff.Context

  doctest Tend.Workflow.Handoff

  describe "fallback_allowed?/1" do
    # TestFallbackAllowed, case for case. Go's `nil` and `[]string{}` are the
    # same empty list here, so the first two rows collapse into one.
    @cases [
      {[], true},
      {["done"], true},
      {["approve"], false},
      {["approve", "reject"], false},
      {["done", "retry"], false}
    ]

    for {outcomes, want} <- @cases do
      test "#{inspect(outcomes)} -> #{want}" do
        assert Handoff.fallback_allowed?(unquote(outcomes)) == unquote(want)
      end
    end

    test "a single done edge is the only single outcome it allows" do
      # The rule, stated as a rule: anything but done has to hand off, because
      # a guessed done would take an edge the agent never chose.
      assert Handoff.fallback_allowed?([Tend.Workflow.outcome_done()])
      refute Handoff.fallback_allowed?([Tend.Workflow.outcome_approve()])
      refute Handoff.fallback_allowed?([Tend.Workflow.outcome_reject()])
    end
  end

  describe "step_system_prompt/1" do
    setup do
      %{
        block:
          Handoff.step_system_prompt(%Context{
            workflow: "ship it",
            step: "review",
            iteration: 2,
            outcomes: ["approve", "reject"]
          })
      }
    end

    # TestStepSystemPrompt's list: the injected block names where the agent is,
    # the exact tool ids, every allowed outcome, and that the tool does not end
    # the session.
    for want <- [
          ~s(step "review"),
          ~s(workflow "ship it"),
          "iteration 2",
          "mcp__tend__finish_step",
          "mcp__tend__get_workflow_step",
          ~s("approve", "reject"),
          "does not end your session",
          # A bypassPermissions step once edited tend.db directly when no tool
          # could express its change (task #252); the block forbids it.
          "Never open, copy or write tend's SQLite database"
        ] do
      test "says #{inspect(want)}", %{block: block} do
        assert block =~ unquote(want)
      end
    end

    test "has no unrendered verb", %{block: block} do
      # Go's guard against an fmt.Fprintf that lost an argument. Here the
      # equivalent slip is an unsubstituted interpolation, which would leave a
      # literal #{ rather than a %; both are caught by the same rule, since
      # neither belongs in the finished text.
      refute block =~ "%"
      refute block =~ "\#{"
    end

    test "an edgeless step is offered done, the set get_workflow_step reports" do
      last = Handoff.step_system_prompt(%Context{workflow: "w", step: "ship", iteration: 1})
      assert last =~ ~s(one of: "done".)
    end

    test "quotes the names Go's %q way, escapes and all" do
      block =
        Handoff.step_system_prompt(%Context{
          workflow: "say \"hi\"",
          step: "tab\there",
          iteration: 1
        })

      assert block =~ ~S(step "tab\there")
      assert block =~ ~S(workflow "say \"hi\"")
    end
  end

  describe "nudge_prompt/2" do
    # TestNudgePrompt's list.
    for want <- [
          ~s(step "review"),
          "mcp__tend__finish_step",
          ~s("approve", "reject"),
          "Do not redo the work"
        ] do
      test "says #{inspect(want)}" do
        assert Handoff.nudge_prompt("review", ["approve", "reject"]) =~ unquote(want)
      end
    end

    test "an edgeless step is offered done" do
      assert Handoff.nudge_prompt("ship", []) =~ ~s("done")
    end
  end

  describe "retry_prompt/3" do
    test "names the step, the recorded failure and the outcomes" do
      turn = Handoff.retry_prompt("review", "  step exited without finish_step\n", ["approve"])

      assert turn =~ ~s(step "review")
      # The reason is trimmed, as Go's strings.TrimSpace trims it, so the
      # blank line after it is the prompt's own and not the reason's.
      assert turn =~ "recorded the failure as: step exited without finish_step\n\n"
      assert turn =~ ~s("approve")
      assert turn =~ "do not end your turn without it"
    end

    test "an edgeless step is offered done" do
      assert Handoff.retry_prompt("ship", "boom", []) =~ ~s("done")
    end
  end

  describe "the deferred-tool hint" do
    test "is in every turn that asks for a tool call" do
      # Task #197: tend's tools are deferred behind a tool search, the model
      # loads them, then searches again and again instead of calling what it
      # loaded. Every prompt that asks for a call carries the hint.
      hint = "Never search for the same tool twice"

      assert Handoff.step_system_prompt(%Context{workflow: "w", step: "s"}) =~ hint
      assert Handoff.nudge_prompt("s", []) =~ hint
      assert Handoff.retry_prompt("s", "boom", []) =~ hint
    end

    test "names both tool ids in one loadable search" do
      assert Handoff.nudge_prompt("s", []) =~
               "`select:#{Handoff.finish_step_tool()},#{Handoff.get_workflow_step_tool()}`"
    end
  end
end
