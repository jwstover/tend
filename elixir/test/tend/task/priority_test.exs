defmodule Tend.Task.PriorityTest do
  use ExUnit.Case, async: true

  alias Tend.Task.Priority

  describe "the bounds" do
    test "run 1 (highest) through 4 (lowest)" do
      assert Priority.highest() == 1
      assert Priority.lowest() == 4
      assert Priority.all() == [1, 2, 3, 4]
    end
  end

  describe "valid?/1" do
    test "every stored priority is valid" do
      for priority <- Priority.all(), do: assert(Priority.valid?(priority))
    end

    test "nothing outside the bounds is" do
      for priority <- [0, 5, -1, nil, "A", :a, 1.0] do
        refute Priority.valid?(priority), "Priority.valid?(#{inspect(priority)}) should be false"
      end
    end
  end

  describe "letter/1 and parse_letter/1" do
    test "every stored priority round trips through its letter" do
      for priority <- Priority.all() do
        assert Priority.parse_letter(Priority.letter(priority)) == {:ok, priority}
      end
    end

    test "the letters are A through D, in order" do
      assert Enum.map(Priority.all(), &Priority.letter/1) == ["A", "B", "C", "D"]
    end

    test "unprioritized and out of range both render as an empty cell" do
      for priority <- [nil, 0, 5, -1, "A"] do
        assert Priority.letter(priority) == "",
               "Priority.letter(#{inspect(priority)}) should be empty"
      end
    end

    test "parse_letter/1 rejects anything that is not A through D" do
      for letter <- ["", "a", "E", "AA", "1", " A"] do
        assert Priority.parse_letter(letter) == :error,
               "Priority.parse_letter(#{inspect(letter)}) should be :error"
      end
    end
  end
end
