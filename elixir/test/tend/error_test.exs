defmodule Tend.ErrorTest do
  use ExUnit.Case, async: true

  alias Tend.Error

  # The one-to-one mapping with the Go sentinels is checked against the Go
  # sources themselves, in Tend.GoParityTest. These are the module's own rules.

  describe "sentinels/0" do
    test "lists the sentinels of the three ported files, sorted" do
      assert Error.sentinels() == [
               :dependency_cycle,
               :empty_project_name,
               :empty_title,
               :project_not_found,
               :protected_project,
               :self_dependency
             ]
    end

    test "has no duplicates" do
      assert Enum.uniq(Error.sentinels()) == Error.sentinels()
    end
  end

  describe "sentinel?/1" do
    test "is true for every listed sentinel" do
      for reason <- Error.sentinels(), do: assert(Error.sentinel?(reason))
    end

    test "is false for anything else, of any shape" do
      refute Error.sentinel?(:nope)
      refute Error.sentinel?({:invalid_date, "x"})
      refute Error.sentinel?("empty_title")
      refute Error.sentinel?(nil)
    end
  end

  describe "message/1" do
    test "renders every sentinel" do
      for reason <- Error.sentinels() do
        message = Error.message(reason)
        assert is_binary(message) and message != ""
      end
    end

    test "renders the interpolating date error the way Go's fmt.Errorf does" do
      assert Error.message({:invalid_date, "06/09/2026"}) ==
               ~s|invalid date "06/09/2026" (want YYYY-MM-DD)|

      assert Error.message({:invalid_date, ""}) == ~s|invalid date "" (want YYYY-MM-DD)|
    end

    test "refuses a reason nobody registered, and says how to fix it" do
      assert_raise ArgumentError, ~r/add it to Tend.Error/, fn -> Error.message(:invented) end
      assert_raise ArgumentError, fn -> Error.message({:invented, 1}) end
    end
  end

  describe "the convention" do
    test "the modules ported so far return sentinels, not strings or exceptions" do
      assert Tend.Task.normalize_title("") == {:error, :empty_title}
      assert Tend.Task.Project.normalize_name(" ") == {:error, :empty_project_name}
    end

    test "every sentinel a ported function can return is one this module lists" do
      {:error, title} = Tend.Task.normalize_title("")
      {:error, name} = Tend.Task.Project.normalize_name(" ")

      for reason <- [title, name], do: assert(Error.sentinel?(reason))
    end
  end
end
