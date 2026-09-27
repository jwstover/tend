defmodule Tend.ErrorTest do
  use ExUnit.Case, async: true

  alias Tend.Error

  # The one-to-one mapping with the Go sentinels is checked against the Go
  # sources themselves, in Tend.GoParityTest. These are the module's own rules.

  describe "sentinels/0" do
    test "lists the sentinels of the ported files, sorted" do
      assert Error.sentinels() == [
               :dependency_cycle,
               :empty_note,
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

  # The value message/1 interpolated, with the fixed text stripped off.
  defp quoted(s) do
    "invalid date " <> rest = Error.message({:invalid_date, s})
    String.replace_suffix(rest, " (want YYYY-MM-DD)", "")
  end

  # Every expectation below is literal output of
  # fmt.Errorf("invalid date %q (want YYYY-MM-DD)", s) on the same input, run
  # against go1.26. They are the cases where inspect/1 -- the obvious but
  # wrong stand-in for %q -- disagrees with it.
  describe "message/1 quotes an interpolated value the way Go's %q does" do
    test "leaves an interpolation marker alone" do
      assert quoted("a" <> <<?#, ?{>> <> "b}") == "\"a" <> <<?#, ?{>> <> "b}\""
    end

    test "renders a control character as a quoted string, not a binary literal" do
      assert quoted(<<0>>) == ~S("\x00")
      assert quoted(<<0x7F>>) == ~S("\x7f")
    end

    test "renders a non-ASCII non-printable as a quoted escape" do
      assert quoted(<<0x85::utf8>>) == ~S("\u0085")
      assert quoted(<<0xA0::utf8>>) == ~S("\u00a0")
      assert quoted(<<0x2028::utf8>>) == ~S("\u2028")
      assert quoted(<<0xE0001::utf8>>) == ~S("\U000e0001")
    end

    test "uses Go's escape spelling, not Elixir's" do
      assert quoted(<<0x1B>>) == ~S("\x1b")
    end

    test "uses the seven escapes Go names" do
      assert quoted(<<7, 8, 9, 10, 11, 12, 13>>) == ~S("\a\b\t\n\v\f\r")
    end

    test "backslashes the quote and the backslash" do
      assert quoted(~S(a"b\c)) == ~S("a\"b\\c")
    end

    test "passes printable text through, ASCII or not" do
      assert quoted("café") == ~s("café")
      assert quoted("😀") == ~s("😀")
    end

    test "renders a byte that is not valid UTF-8 one \\xNN at a time" do
      assert quoted(<<0xFF, 0xFE>>) == ~S("\xff\xfe")
      assert quoted(<<0xC3, ?(>>) == ~S("\xc3(")
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
