defmodule Tend.Task.EventTest do
  use ExUnit.Case, async: true

  alias Tend.Task.Event
  alias Tend.Task.Summary

  defp state_event(task_id, title, old, new) do
    %Event{task_id: task_id, task_title: title, kind: :state, old: old, new: new}
  end

  defp project_event(task_id, title, from, to) do
    %Event{task_id: task_id, task_title: title, kind: :project, old: from, new: to}
  end

  # Go's time.Date(..., time.FixedZone("test", -7*3600)): an instant that knows
  # its own offset and nothing else.
  defp at(year, month, day, hour) do
    %DateTime{
      year: year,
      month: month,
      day: day,
      hour: hour,
      minute: 0,
      second: 0,
      microsecond: {0, 0},
      time_zone: "test",
      zone_abbr: "test",
      utc_offset: -7 * 3600,
      std_offset: 0
    }
  end

  describe "the struct" do
    test "a bare event defaults to Go's zero values" do
      assert %Event{} == %Event{
               id: 0,
               task_id: 0,
               task_title: "",
               kind: "",
               old: nil,
               new: nil,
               created_at: nil
             }
    end
  end

  describe "kinds" do
    test "kinds/0 is the kinds the Go constants declare, in order" do
      assert Event.kinds() == [:created, :state, :deleted, :project, :parent]
    end

    test "every kind round trips through its stored name" do
      for kind <- Event.kinds() do
        assert Event.valid_kind?(kind)
        assert kind |> Event.format_kind() |> Event.parse_kind() == kind
      end
    end

    test "the stored names are the ones the schema holds" do
      assert Enum.map(Event.kinds(), &Event.format_kind/1) ==
               ~w(created state deleted project parent)
    end

    test "a kind tend does not know parses to itself rather than failing" do
      # Go converts the column with EventKind(row.Kind), which accepts any
      # string and matches no switch. A row from a future migration must not
      # take the process down.
      assert Event.parse_kind("archived") == "archived"
      assert Event.parse_kind("") == ""
      refute Event.valid_kind?("archived")
      assert Event.format_kind("archived") == "archived"
    end

    test "an unknown kind is ignored by summarize/1, the way Go ignores it" do
      old = "todo"
      new = "done"

      events = [
        %Event{task_id: 1, task_title: "future", kind: "archived", old: old, new: new}
      ]

      assert Summary.empty?(Event.summarize(events))
    end

    test "top_level_label/0 is what a parent event records for no parent" do
      assert Event.top_level_label() == "(top level)"
    end
  end

  describe "summarize/1" do
    test "completed beats blocked beats started" do
      sum =
        Event.summarize([
          state_event(1, "ship it", "todo", "doing"),
          state_event(1, "ship it", "doing", "done"),
          state_event(2, "stuck", "todo", "doing"),
          state_event(2, "stuck", "doing", "blocked"),
          state_event(3, "underway", "todo", "doing")
        ])

      assert Enum.map(sum.completed, & &1.task_id) == [1]
      assert Enum.map(sum.blocked, & &1.task_id) == [2]
      assert Enum.map(sum.started, & &1.task_id) == [3]
    end

    test "replays bounces to where the task ended up" do
      sum =
        Event.summarize([
          # done then reopened: not completed, but doing was touched.
          state_event(1, "reopened", "doing", "done"),
          state_event(1, "reopened", "done", "doing"),
          # blocked then unblocked back to todo: nothing to report.
          state_event(2, "unblocked", "todo", "blocked"),
          state_event(2, "unblocked", "blocked", "todo")
        ])

      assert sum.completed == []
      assert sum.blocked == []
      assert Enum.map(sum.started, & &1.task_id) == [1]
    end

    test "counts each task that left the inbox once" do
      sum =
        Event.summarize([
          state_event(1, "a", "inbox", "todo"),
          state_event(2, "b", "inbox", "someday"),
          state_event(3, "c", "inbox", "doing"),
          # second transition, still one triage
          state_event(1, "a", "todo", "doing")
        ])

      assert sum.triaged == 3
      assert Enum.map(sum.started, & &1.task_id) == [1, 3]
    end

    test "ignores created and deleted events" do
      sum =
        Event.summarize([
          %Event{task_id: 1, task_title: "captured", kind: :created, new: "inbox"},
          %Event{task_id: 2, task_title: "removed", kind: :deleted, old: "inbox"}
        ])

      assert Summary.empty?(sum)
    end

    # A re-parent is bookkeeping, not standup news: it must not show up as a
    # move (that is for projects) or touch anything else.
    test "ignores parent events" do
      sum =
        Event.summarize([
          %Event{
            task_id: 1,
            task_title: "nested",
            kind: :parent,
            old: Event.top_level_label(),
            new: "host"
          }
        ])

      assert Summary.empty?(sum)
    end

    test "a state event missing either end says nothing" do
      sum =
        Event.summarize([
          %Event{task_id: 1, task_title: "half", kind: :state, old: "todo", new: nil},
          %Event{task_id: 2, task_title: "half", kind: :state, old: nil, new: "done"}
        ])

      assert Summary.empty?(sum)
    end

    test "the last title wins, so a renamed task reads by its latest name" do
      sum =
        Event.summarize([
          state_event(1, "old name", "todo", "doing"),
          state_event(1, "new name", "doing", "done")
        ])

      assert Enum.map(sum.completed, & &1.title) == ["new name"]
    end

    test "reports project moves" do
      sum = Event.summarize([project_event(1, "move me", "Unsorted", "tend")])

      assert [moved] = sum.moved
      assert moved.task_id == 1
      assert moved.title == "move me"
      assert moved.to == "tend"
    end

    # A task moved twice in the window reports where it ended up, not its
    # route -- the same "replay to the destination" rule state events follow.
    test "reports the last move" do
      sum =
        Event.summarize([
          project_event(1, "wanderer", "Unsorted", "tend"),
          project_event(1, "wanderer", "tend", "hapi")
        ])

      assert [%{to: "hapi"}] = sum.moved
    end

    test "a project event with no destination is not a move" do
      sum = Event.summarize([%Event{task_id: 1, task_title: "nowhere", kind: :project, new: nil}])

      assert Summary.empty?(sum)
    end

    # Moving and completing in one window are both worth reporting, so moved
    # is independent of the completed/blocked/started precedence.
    test "moved is independent of state" do
      sum =
        Event.summarize([
          project_event(1, "both", "Unsorted", "tend"),
          state_event(1, "both", "doing", "done")
        ])

      assert length(sum.completed) == 1
      assert length(sum.moved) == 1
    end

    test "an empty window summarizes to nothing" do
      assert Event.summarize([]) == %Summary{}
    end
  end

  describe "Summary.empty?/1" do
    test "a blank summary is empty" do
      assert Summary.empty?(%Summary{})
    end

    # A window holding only a move is not empty: it has something to report.
    test "a summary holding a move is not" do
      refute Summary.empty?(Event.summarize([project_event(1, "moved", "Unsorted", "tend")]))
    end

    test "a summary holding only a triage is not" do
      refute Summary.empty?(%Summary{triaged: 1})
    end
  end

  describe "last_workday_start/1" do
    test "steps back over the weekend to the previous weekday's midnight" do
      cases = [
        # Monday morning reports Friday.
        {at(2026, 7, 6, 9), at(2026, 7, 3, 0)},
        # Tuesday reports Monday.
        {at(2026, 7, 7, 9), at(2026, 7, 6, 0)},
        # Sunday reports Friday too.
        {at(2026, 7, 5, 9), at(2026, 7, 3, 0)}
      ]

      for {now, want} <- cases do
        got = Event.last_workday_start(now)
        assert DateTime.compare(got, want) == :eq
        assert got == want
      end
    end

    test "keeps the offset it was given" do
      got = Event.last_workday_start(at(2026, 7, 7, 9))

      assert got.utc_offset == -7 * 3600
      assert {got.hour, got.minute, got.second, got.microsecond} == {0, 0, 0, {0, 0}}
    end
  end

  describe "window_label/2" do
    test "a window starting yesterday, or today, reads Yesterday" do
      now = ~U[2026-07-06 09:00:00Z]

      assert Event.window_label(~U[2026-07-05 00:00:00Z], now) == "Yesterday"
      assert Event.window_label(~U[2026-07-06 00:00:00Z], now) == "Yesterday"
    end

    test "a window starting within the past week reads as its weekday" do
      assert Event.window_label(~U[2026-07-03 00:00:00Z], ~U[2026-07-06 09:00:00Z]) ==
               "Since Friday"
    end

    test "a window a week old or more reads as its date" do
      # Exactly seven days is already the date form.
      assert Event.window_label(~U[2026-06-29 00:00:00Z], ~U[2026-07-06 00:00:00Z]) ==
               "Since 2026-06-29"

      assert Event.window_label(~U[2026-06-26 00:00:00Z], ~U[2026-07-06 09:00:00Z]) ==
               "Since 2026-06-26"
    end
  end
end
