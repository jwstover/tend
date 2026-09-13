defmodule Tend.Task.StateTest do
  use ExUnit.Case, async: true

  alias Tend.Task.State

  # A port of TestStateValid in internal/task/task_test.go.
  describe "valid?/1" do
    test "every seeded state is valid" do
      for state <- [:inbox, :todo, :doing, :review, :blocked, :done, :someday] do
        assert State.valid?(state), "State.valid?(#{inspect(state)}) should be true"
      end
    end

    test "all/0 is exactly the seeded states" do
      assert State.all() == [:inbox, :todo, :doing, :review, :blocked, :done, :someday]
    end

    test "nothing else is" do
      # The Go test's rejects, as atoms, plus the shapes only the Elixir port
      # can be handed.
      for state <- [:"", :DONE, :archived, :"in review", :in_review, nil, "done", 1] do
        refute State.valid?(state), "State.valid?(#{inspect(state)}) should be false"
      end
    end
  end

  describe "parse/1 and format/1" do
    test "every state round trips through its stored name" do
      for state <- State.all() do
        assert State.parse(State.format(state)) == {:ok, state}
      end
    end

    test "the stored names are the Go constants' strings" do
      assert Enum.map(State.all(), &State.format/1) ==
               ~w(inbox todo doing review blocked done someday)
    end

    test "parse/1 rejects everything the Go validity check rejects" do
      for name <- ["", "DONE", "archived", "in review", "in_review"] do
        assert State.parse(name) == :error, "State.parse(#{inspect(name)}) should be :error"
      end
    end

    test "parse/1 rejects a label, since labels are for reading" do
      assert State.parse(State.label(:review)) == :error
    end

    test "format/1 refuses a state that does not exist" do
      assert_raise FunctionClauseError, fn -> State.format(:archived) end
    end
  end

  # A port of TestStateLabel in internal/task/task_test.go.
  describe "label/1" do
    test "review reads as a heading" do
      assert State.label(:review) == "in review"
    end

    test "every other state labels as its own name" do
      for state <- State.all(), state != :review do
        assert State.label(state) == State.format(state)
      end
    end
  end
end
