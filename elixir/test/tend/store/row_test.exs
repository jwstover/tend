defmodule Tend.Store.RowTest do
  @moduledoc """
  The row mappers on their own, driven with literal column values rather than
  a database.

  `Tend.Store.TasksTest` proves the mapping round-trips what SQLite actually
  writes; this proves the edges SQLite will not produce on demand -- a
  timestamp in the wrong shape, a state nothing seeded, the millisecond layout
  only one column uses.
  """

  use ExUnit.Case, async: true

  alias Tend.Store.Row

  # The column order every task query selects, which is the order to_task/1
  # reads positionally. Spelled out rather than derived from the defaults map,
  # because the order is the thing under test.
  @columns [
    :id,
    :title,
    :body_md,
    :state,
    :parent_id,
    :priority,
    :due,
    :snooze_until,
    :created_at,
    :updated_at,
    :completed_at,
    :project_id
  ]

  @defaults %{
    id: 7,
    title: "buy milk",
    body_md: "",
    state: "inbox",
    parent_id: nil,
    priority: nil,
    due: nil,
    snooze_until: nil,
    created_at: "2026-09-14 08:30:00",
    updated_at: "2026-09-14 08:30:00",
    completed_at: nil,
    project_id: 1
  }

  defp row(overrides \\ []) do
    values = Map.merge(@defaults, Map.new(overrides))
    Enum.map(@columns, &Map.fetch!(values, &1))
  end

  describe "to_task/1" do
    test "maps every column into its field" do
      assert {:ok, task} =
               Row.to_task(
                 row(
                   id: 42,
                   title: "ship it",
                   body_md: "## context",
                   state: "doing",
                   parent_id: 7,
                   priority: 2,
                   due: "2026-12-01",
                   snooze_until: "2026-11-01",
                   created_at: "2026-09-14 08:30:00",
                   updated_at: "2026-09-14 09:00:00",
                   completed_at: "2026-09-14 09:30:00",
                   project_id: 3
                 )
               )

      assert task.id == 42
      assert task.title == "ship it"
      assert task.body_md == "## context"
      assert task.state == :doing
      assert task.parent_id == 7
      assert task.priority == 2
      assert task.due == "2026-12-01"
      assert task.snooze_until == "2026-11-01"
      assert task.project_id == 3
      assert task.created_at == ~U[2026-09-14 08:30:00Z]
      assert task.updated_at == ~U[2026-09-14 09:00:00Z]
      assert task.completed_at == ~U[2026-09-14 09:30:00Z]
    end

    test "a NULL column arrives as nil, with no wrapper in between" do
      assert {:ok, task} = Row.to_task(row())

      assert task.parent_id == nil
      assert task.priority == nil
      assert task.due == nil
      assert task.snooze_until == nil
      assert task.completed_at == nil
    end

    test "an unparseable timestamp names the column it came from" do
      assert {:error, {:invalid_timestamp, "task 7 created_at", "yesterday"}} =
               Row.to_task(row(created_at: "yesterday"))

      assert {:error, {:invalid_timestamp, "task 7 updated_at", ""}} =
               Row.to_task(row(updated_at: ""))

      assert {:error, {:invalid_timestamp, "task 7 completed_at", "2026-09-14"}} =
               Row.to_task(row(completed_at: "2026-09-14"))
    end

    test "a state outside the seeded set is refused rather than carried" do
      assert {:error, {:unknown_state, "archived"}} = Row.to_task(row(state: "archived"))
    end
  end

  describe "to_tasks/1" do
    test "maps in order and stops at the first row that will not map" do
      assert {:ok, [first, second]} = Row.to_tasks([row(id: 1), row(id: 2)])
      assert [first.id, second.id] == [1, 2]

      assert {:error, {:unknown_state, "archived"}} =
               Row.to_tasks([row(id: 1), row(id: 2, state: "archived"), row(id: 3)])
    end

    test "no rows is an empty list, not an error" do
      assert Row.to_tasks([]) == {:ok, []}
    end
  end

  # The five columns every workflow query selects, in the order to_workflow/2
  # reads them. The sixth, ListWorkflows' joined count, is appended by the
  # to_workflows/1 cases below.
  defp workflow_row(overrides \\ []) do
    values =
      Map.merge(
        %{
          id: 3,
          name: "fix a bug",
          description: "",
          created_at: "2026-09-14 08:30:00",
          updated_at: "2026-09-14 08:30:00"
        },
        Map.new(overrides)
      )

    Enum.map([:id, :name, :description, :created_at, :updated_at], &Map.fetch!(values, &1))
  end

  describe "to_workflow/2" do
    test "maps every column into its field, with the count supplied apart" do
      assert {:ok, workflow} =
               Row.to_workflow(
                 workflow_row(
                   id: 42,
                   name: "ship it",
                   description: "the usual",
                   created_at: "2026-09-14 08:30:00",
                   updated_at: "2026-09-14 09:00:00"
                 ),
                 4
               )

      assert workflow.id == 42
      assert workflow.name == "ship it"
      assert workflow.description == "the usual"
      assert workflow.step_count == 4
      assert workflow.created_at == ~U[2026-09-14 08:30:00Z]
      assert workflow.updated_at == ~U[2026-09-14 09:00:00Z]
    end

    test "a zero count is what every caller but the listing passes" do
      assert {:ok, workflow} = Row.to_workflow(workflow_row(), 0)
      assert workflow.step_count == 0
    end

    test "an unparseable timestamp names the column it came from" do
      assert {:error, {:invalid_timestamp, "workflow 3 created_at", "yesterday"}} =
               Row.to_workflow(workflow_row(created_at: "yesterday"), 0)

      assert {:error, {:invalid_timestamp, "workflow 3 updated_at", ""}} =
               Row.to_workflow(workflow_row(updated_at: ""), 0)
    end
  end

  describe "to_workflows/1" do
    test "splits the joined count off the row and keeps the query's order" do
      rows = [
        workflow_row(id: 1, name: "empty") ++ [0],
        workflow_row(id: 2, name: "two steps") ++ [2]
      ]

      assert {:ok, [first, second]} = Row.to_workflows(rows)
      assert {first.name, first.step_count} == {"empty", 0}
      assert {second.name, second.step_count} == {"two steps", 2}
    end

    test "stops at the first row that will not map, and takes no rows in stride" do
      rows = [workflow_row(id: 1) ++ [0], workflow_row(id: 2, updated_at: "nope") ++ [0]]

      assert {:error, {:invalid_timestamp, "workflow 2 updated_at", "nope"}} =
               Row.to_workflows(rows)

      assert Row.to_workflows([]) == {:ok, []}
    end
  end

  describe "parse_time/1 (Go's sqliteTimeLayout)" do
    test "parses what datetime('now') writes, as UTC" do
      assert Row.parse_time("2026-09-14 08:30:00") == {:ok, ~U[2026-09-14 08:30:00Z]}
      assert Row.parse_time("1999-12-31 23:59:59") == {:ok, ~U[1999-12-31 23:59:59Z]}
    end

    test "accepts a fractional second the layout does not name, the way Go does" do
      # "the input may contain a fractional second field immediately after the
      # seconds field, even if the layout does not signify its presence".
      assert Row.parse_time("2026-09-14 08:30:00.123") ==
               {:ok, ~U[2026-09-14 08:30:00.123Z]}

      assert Row.parse_time("2026-09-14 08:30:00.1") == {:ok, ~U[2026-09-14 08:30:00.1Z]}

      assert Row.parse_time("2026-09-14 08:30:00.123456") ==
               {:ok, ~U[2026-09-14 08:30:00.123456Z]}
    end

    test "is as strict as Go's time.Parse, and stricter than ISO 8601" do
      for bad <- [
            # NaiveDateTime.from_iso8601/1 takes all of these; Go does not.
            "2026-09-14T08:30:00",
            "2026-09-14 08:30:00Z",
            "2026-09-14 08:30:00+01:00",
            # Unpadded fields, a missing one, junk either side.
            "2026-9-14 08:30:00",
            "2026-09-14 8:30:00",
            "2026-09-14 08:30",
            " 2026-09-14 08:30:00",
            "2026-09-14 08:30:00 ",
            "2026-09-14 08:30:00.",
            # Fields out of range, and a day the month does not have.
            "2026-02-30 08:30:00",
            "2026-09-14 24:30:00",
            "2026-09-14 08:60:00",
            "2026-09-14 08:30:60",
            "",
            "yesterday"
          ] do
        assert Row.parse_time(bad) == :error, "expected #{inspect(bad)} to be refused"
      end
    end

    test "a value that is not a string is refused rather than raising" do
      assert Row.parse_time(nil) == :error
      assert Row.parse_time(20_260_914) == :error
    end
  end

  describe "parse_status_time/1 (Go's statusTimeLayout)" do
    test "parses what strftime('%Y-%m-%d %H:%M:%f') writes into status_updated_at" do
      assert Row.parse_status_time("2026-09-14 08:30:00.123") ==
               {:ok, ~U[2026-09-14 08:30:00.123Z]}

      assert Row.parse_status_time("2026-09-14 08:30:59.000") ==
               {:ok, ~U[2026-09-14 08:30:59.000Z]}
    end

    test "the milliseconds are kept, because the poller's CAS compares them" do
      # A parser that rounded to the second would make two writes a few
      # hundred milliseconds apart compare equal, which is the exact race the
      # millisecond column exists to catch.
      assert {:ok, earlier} = Row.parse_status_time("2026-09-14 08:30:00.100")
      assert {:ok, later} = Row.parse_status_time("2026-09-14 08:30:00.900")

      assert DateTime.compare(earlier, later) == :lt
      assert earlier.microsecond == {100_000, 3}
      assert later.microsecond == {900_000, 3}
    end

    test "requires exactly three fractional digits, the way Go's .000 chunk does" do
      for bad <- [
            "2026-09-14 08:30:00",
            "2026-09-14 08:30:00.",
            "2026-09-14 08:30:00.1",
            "2026-09-14 08:30:00.12",
            "2026-09-14 08:30:00.1234"
          ] do
        assert Row.parse_status_time(bad) == :error, "expected #{inspect(bad)} to be refused"
      end
    end

    test "a value that is not a string is refused rather than raising" do
      assert Row.parse_status_time(nil) == :error
    end

    test "the two layouts are not interchangeable" do
      whole_second = "2026-09-14 08:30:00"
      millisecond = "2026-09-14 08:30:00.500"

      assert Row.parse_time(whole_second) == {:ok, ~U[2026-09-14 08:30:00Z]}
      assert Row.parse_status_time(whole_second) == :error

      # parse_time's leniency is Go's: it takes the millisecond form too, so
      # only the status parser can tell the two columns apart.
      assert Row.parse_time(millisecond) == {:ok, ~U[2026-09-14 08:30:00.500Z]}
      assert Row.parse_status_time(millisecond) == {:ok, ~U[2026-09-14 08:30:00.500Z]}
    end
  end
end
