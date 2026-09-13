defmodule Tend.TaskTest do
  use ExUnit.Case, async: true

  alias Tend.Task

  # A port of TestNormalizeDate in internal/task/task_test.go, case for case.
  describe "normalize_date/1" do
    test "canonicalizes an ISO 8601 date" do
      assert Task.normalize_date("2026-06-09") == {:ok, "2026-06-09"}
    end

    test "trims whitespace" do
      assert Task.normalize_date(" 2026-06-09 ") == {:ok, "2026-06-09"}
    end

    test "rejects the wrong format" do
      assert Task.normalize_date("06/09/2026") == {:error, {:invalid_date, "06/09/2026"}}
    end

    test "rejects something that is not a date" do
      assert Task.normalize_date("tomorrow") == {:error, {:invalid_date, "tomorrow"}}
    end

    test "rejects an impossible date" do
      assert Task.normalize_date("2026-02-30") == {:error, {:invalid_date, "2026-02-30"}}
    end

    test "rejects the empty string" do
      assert Task.normalize_date("") == {:error, {:invalid_date, ""}}
    end

    test "reports the string as typed, not as trimmed" do
      assert Task.normalize_date("  nope  ") == {:error, {:invalid_date, "  nope  "}}
    end

    test "a rejection renders the way the Go error does" do
      {:error, reason} = Task.normalize_date("06/09/2026")
      assert Tend.Error.message(reason) == ~s|invalid date "06/09/2026" (want YYYY-MM-DD)|
    end

    test "rejects a date carrying a time, the way the Go layout does" do
      assert {:error, _} = Task.normalize_date("2026-06-09T10:00:00Z")
    end

    test "rejects unpadded components" do
      assert {:error, _} = Task.normalize_date("2026-6-9")
    end

    # Go reads the year as exactly four digits, so it rejects ISO 8601's
    # extended-form sign; Date.from_iso8601/1 on its own accepts both. The
    # negative one is the dangerous half: it would reach tasks.due, which the
    # database compares lexically, and sort before every real date.
    test "rejects a positively signed year, as the Go layout does" do
      assert Task.normalize_date("+2026-09-13") == {:error, {:invalid_date, "+2026-09-13"}}
    end

    test "rejects a negatively signed year, as the Go layout does" do
      assert Task.normalize_date("-2026-09-13") == {:error, {:invalid_date, "-2026-09-13"}}
    end

    test "rejects a five-digit year" do
      assert Task.normalize_date("10000-01-01") == {:error, {:invalid_date, "10000-01-01"}}
    end

    test "keeps the four-digit years Go accepts" do
      assert Task.normalize_date("0000-01-01") == {:ok, "0000-01-01"}
      assert Task.normalize_date("9999-12-31") == {:ok, "9999-12-31"}
    end
  end

  # A port of TestNormalizeTitle in internal/task/task_test.go, case for case.
  describe "normalize_title/1" do
    test "keeps a plain title" do
      assert Task.normalize_title("buy milk") == {:ok, "buy milk"}
    end

    test "trims surrounding whitespace" do
      assert Task.normalize_title("  buy milk\n") == {:ok, "buy milk"}
    end

    test "keeps interior whitespace" do
      assert Task.normalize_title("a  b") == {:ok, "a  b"}
    end

    test "rejects the empty string" do
      assert Task.normalize_title("") == {:error, :empty_title}
    end

    test "rejects a title that is only whitespace" do
      assert Task.normalize_title(" \t\n") == {:error, :empty_title}
    end
  end

  describe "open_blockers/1" do
    test "keeps every blocker that is not done, in order" do
      blockers = [
        %Task{id: 1, state: :todo},
        %Task{id: 2, state: :done},
        %Task{id: 3, state: :blocked},
        %Task{id: 4, state: :done}
      ]

      assert Enum.map(Task.open_blockers(blockers), & &1.id) == [1, 3]
    end

    test "is empty when everything is done" do
      assert Task.open_blockers([%Task{state: :done}]) == []
    end

    test "is empty for no blockers at all" do
      assert Task.open_blockers([]) == []
    end
  end

  describe "the struct" do
    test "a bare task defaults to Go's zero values" do
      assert %Task{} == %Task{
               id: 0,
               title: "",
               body_md: "",
               state: nil,
               parent_id: nil,
               project_id: 0,
               priority: nil,
               due: nil,
               snooze_until: nil,
               created_at: nil,
               updated_at: nil,
               completed_at: nil
             }
    end
  end
end
