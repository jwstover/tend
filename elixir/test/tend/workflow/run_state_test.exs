defmodule Tend.Workflow.RunStateTest do
  use ExUnit.Case, async: true

  alias Tend.Workflow.RunState

  # A port of TestRunStateTerminalAndValid in
  # internal/workflow/workflow_test.go.
  describe "valid?/1 and terminal?/1" do
    test "the Go table: every state is valid, and only the last three end a run" do
      terminal = %{
        pending: false,
        running: false,
        waiting_review: false,
        paused: false,
        done: true,
        failed: true,
        cancelled: true
      }

      for {state, want} <- terminal do
        assert RunState.valid?(state), "#{state} should be a valid state"

        assert RunState.terminal?(state) == want,
               "#{state}.terminal? = #{RunState.terminal?(state)}, want #{want}"
      end

      assert Enum.sort(Map.keys(terminal)) == Enum.sort(RunState.all())
    end

    test "an unknown state is neither valid nor terminal" do
      for state <- [:bogus, :"", nil, "done", 1] do
        refute RunState.valid?(state), "RunState.valid?(#{inspect(state)}) should be false"
        refute RunState.terminal?(state), "RunState.terminal?(#{inspect(state)}) should be false"
      end
    end
  end

  describe "parse/1 and format/1" do
    test "every state round trips through its stored name" do
      for state <- RunState.all() do
        assert RunState.parse(RunState.format(state)) == {:ok, state}
      end
    end

    test "the stored names are the Go constants' strings" do
      assert Enum.map(RunState.all(), &RunState.format/1) ==
               ~w(pending running waiting_review paused done failed cancelled)
    end

    test "parse/1 rejects everything Go's Valid rejects" do
      for name <- ["", "bogus", "DONE", "waiting review", "Paused"] do
        assert RunState.parse(name) == :error, "RunState.parse(#{inspect(name)}) should be :error"
      end
    end

    test "format/1 refuses a state that does not exist" do
      assert_raise FunctionClauseError, fn -> RunState.format(:bogus) end
    end
  end

  # A port of TestRunStateSessionStatus in internal/workflow/status_test.go.
  # Go's (SessionStatus(""), false) is :error here -- see the moduledoc.
  describe "session_status/1" do
    test "the Go table" do
      cases = [
        {:pending, {:ok, :starting}},
        {:running, {:ok, :working}},
        {:waiting_review, {:ok, :blocked}},
        {:paused, {:ok, :idle}},
        {:done, :error},
        {:failed, :error},
        {:cancelled, :error},
        {:bogus, :error}
      ]

      for {state, want} <- cases do
        assert RunState.session_status(state) == want,
               "#{inspect(state)}.session_status = #{inspect(RunState.session_status(state))}, " <>
                 "want #{inspect(want)}"
      end
    end

    test "every live state maps, and every terminal one does not" do
      for state <- RunState.all() do
        if RunState.terminal?(state) do
          assert RunState.session_status(state) == :error
        else
          assert {:ok, status} = RunState.session_status(state)
          assert Tend.Task.SessionStatus.valid?(status)
        end
      end
    end
  end
end
