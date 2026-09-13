defmodule Tend.Task.CountsTest do
  use ExUnit.Case, async: true

  alias Tend.Task.BlockerCount
  alias Tend.Task.ChildCount

  describe "ChildCount" do
    test "counts default to zero, the way a task with no sub-tasks reads" do
      assert %ChildCount{} == %ChildCount{done: 0, total: 0}
    end

    test "carries the N/M the progress indicator shows" do
      assert %ChildCount{done: 2, total: 5}.done == 2
      assert %ChildCount{done: 2, total: 5}.total == 5
    end
  end

  describe "BlockerCount.blocked?/1" do
    test "a task with open blockers is blocked" do
      assert BlockerCount.blocked?(%BlockerCount{open: 1, total: 3})
    end

    test "a task whose blockers are all done is not" do
      refute BlockerCount.blocked?(%BlockerCount{open: 0, total: 3})
    end

    test "a task with no blockers at all is not" do
      refute BlockerCount.blocked?(%BlockerCount{})
    end
  end
end
